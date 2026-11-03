package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"finkoper-morning-briefing/internal/briefing"
	"finkoper-morning-briefing/internal/diagnostic"
)

type modeCounter struct{ reserve int }

func (c modeCounter) Count(context.Context, string, string) (briefing.Budget, error) {
	return briefing.Budget{InputTokens: 100, OutputReserve: c.reserve, ContextTokens: 8192, RequiredTokens: 100 + c.reserve, RemainingTokens: 8192 - 100 - c.reserve, Method: diagnostic.Qwen35BudgetMethod}, nil
}
func (modeCounter) Verify(context.Context, string, string) error { return nil }

func TestThreeModeComparisonHTTP(t *testing.T) {
	for _, scenario := range []string{"plan", "ok", "generation_error", "length", "length_no_count", "render_mismatch", "token_mismatch"} {
		t.Run(scenario, func(t *testing.T) {
			root, _ := filepath.Abs("../..")
			t.Chdir(root)
			generations := 0
			var active, maximum atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/chat" {
					w.WriteHeader(501)
					return
				}
				var p struct {
					Debug                          bool `json:"_debug_render_only"`
					Think, Stream, Shift, Truncate bool
					KeepAlive                      string `json:"keep_alive"`
					Model                          string
					Options                        map[string]float64
					Format                         json.RawMessage
					Messages                       []struct{ Role, Content string }
				}
				if json.NewDecoder(r.Body).Decode(&p) != nil {
					t.Error("request")
					return
				}
				if len(p.Messages) != 2 || p.Messages[0].Content != diagnostic.SummaryPromptV2 || string(p.Format) != diagnostic.SummarySchema || p.Model != diagnostic.Qwen35Model || p.Stream || p.Shift || p.Truncate || p.KeepAlive != "30m" {
					t.Error("frozen input/schema/transport changed")
				}
				if p.Debug {
					rendered := "<|im_start|>system\n" + p.Messages[0].Content + "<|im_end|>\n<|im_start|>user\n" + p.Messages[1].Content + "<|im_end|>\n<|im_start|>assistant\n<think>\n"
					if !p.Think || scenario == "render_mismatch" {
						rendered += "\n</think>\n\n"
					}
					json.NewEncoder(w).Encode(map[string]any{"_debug_info": map[string]string{"rendered_template": rendered}})
					return
				}
				n := active.Add(1)
				defer active.Add(-1)
				if n > maximum.Load() {
					maximum.Store(n)
				}
				expectedMode := generations % 3
				expectedThink := expectedMode == 2
				temperatures := []float64{.7, 0, 1}
				penalties := []float64{1.5, 0, 1.5}
				topP, reserve := .8, float64(1000)
				if expectedThink {
					topP, reserve = .95, 4096
				}
				if p.Think != expectedThink || p.Options["temperature"] != temperatures[expectedMode] || p.Options["presence_penalty"] != penalties[expectedMode] || p.Options["top_p"] != topP || p.Options["num_predict"] != reserve || p.Options["num_ctx"] != 8192 || p.Options["top_k"] != 20 || p.Options["min_p"] != 0 || p.Options["repeat_penalty"] != 1 {
					t.Error("wrong mode or sequence")
				}
				generations++
				if scenario == "generation_error" && generations == 1 {
					w.WriteHeader(500)
					return
				}
				var user struct{ Messages []struct{ ID string } }
				json.Unmarshal([]byte(p.Messages[1].Content), &user)
				if len(user.Messages) == 0 {
					t.Error("original messages missing")
					return
				}
				content, _ := json.Marshal(map[string]any{"summary": "Контролируемый ответ mock.", "source_message_ids": []string{user.Messages[0].ID}})
				count, reason := 100, "stop"
				if scenario == "token_mismatch" && generations == 2 {
					count++
				}
				if (scenario == "length" || scenario == "length_no_count") && generations == 1 {
					reason = "length"
				}
				response := map[string]any{"done": true, "done_reason": reason, "message": map[string]string{"content": string(content), "thinking": "SECRET_THINKING"}, "prompt_eval_count": count, "eval_count": 12}
				if scenario == "length_no_count" && generations == 1 {
					delete(response, "prompt_eval_count")
				}
				json.NewEncoder(w).Encode(response)
			}))
			defer srv.Close()
			factory := func(_ string, mode string) (*diagnostic.Runner, error) {
				config, err := diagnostic.Qwen35Configuration(mode)
				if err != nil {
					return nil, err
				}
				return diagnostic.NewQwen35RunnerMode(srv.URL, modeCounter{config.Reserve}, mode)
			}
			args := []string{"--profile", diagnostic.Qwen35Profile, "--case", "six", "--mode", "all"}
			if scenario != "plan" {
				args = append(args, "--run")
			}
			var output bytes.Buffer
			err := runWithRunner(args, &output, factory)
			if (err == nil) != (scenario == "ok" || scenario == "plan") {
				t.Fatal("exit status", err)
			}
			lines := strings.Split(strings.TrimSpace(output.String()), "\n")
			expectedRecords, expectedGens := 18, 18
			if scenario == "plan" {
				expectedGens = 0
			}
			if scenario == "render_mismatch" {
				expectedRecords, expectedGens = 3, 2
			}
			if scenario == "token_mismatch" {
				expectedRecords, expectedGens = 2, 2
			}
			if len(lines) != expectedRecords || generations != expectedGens || maximum.Load() > 1 || strings.Contains(output.String(), "SECRET_THINKING") {
				t.Fatalf("records=%d gens=%d active=%d", len(lines), generations, maximum.Load())
			}
			first := map[string]report{}
			for i, line := range lines {
				var entry report
				if json.Unmarshal([]byte(line), &entry) != nil {
					t.Fatal("report")
				}
				expectedModes := []string{"baseline", "greedy", "thinking"}
				if entry.GenerationMode != expectedModes[i%3] || entry.AssessmentRole != "regression" {
					t.Fatal("mode metadata")
				}
				if previous, ok := first[entry.CaseID]; ok {
					if entry.UserHash != previous.UserHash || entry.MessagesHash != previous.MessagesHash || entry.InputFileHash != previous.InputFileHash || entry.PromptHash != previous.PromptHash || entry.FormatHash != previous.FormatHash {
						t.Fatal("inputs differ between modes")
					}
				} else {
					first[entry.CaseID] = entry
				}
				if entry.GenerationMode == "thinking" && (entry.CallTimeoutMS != 300000 || entry.Budget.OutputReserve != 4096) {
					t.Fatal("thinking budget/timeout")
				}
				if entry.GenerationMode == "baseline" && entry.CaseID == "sr-01" && entry.RequestHash != "f9c873f3678dd6d227cd85e5b2f04d391cd9e6fd50051bb374d1e0c7ade01cb4" {
					t.Fatal("baseline wire request changed", entry.RequestHash)
				}
				if (scenario == "length" || scenario == "length_no_count" || scenario == "generation_error") && i == 0 && (!diagnostic.Failed(entry.Record) || string(entry.Result) != "null") {
					t.Fatal("failed generation became success")
				}
			}
		})
	}
}
