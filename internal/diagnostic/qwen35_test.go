package diagnostic

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"finkoper-morning-briefing/internal/briefing"
)

type qwen35Counter struct {
	spyCounter
	tokens  int
	reserve int
}

func (c *qwen35Counter) Count(ctx context.Context, s, u string) (briefing.Budget, error) {
	c.spyCounter.Count(ctx, s, u)
	reserve := c.reserve
	if reserve == 0 {
		reserve = 1000
	}
	return briefing.Budget{InputTokens: c.tokens, OutputReserve: reserve, ContextTokens: 8192, RequiredTokens: c.tokens + reserve, RemainingTokens: 8192 - c.tokens - reserve, Method: Qwen35BudgetMethod}, nil
}

func TestQwen35ModeConfigurations(t *testing.T) {
	baseline := map[string]any{"num_ctx": 8192, "num_predict": 1000, "temperature": float64(.7), "top_p": float64(.8), "top_k": float64(20), "min_p": float64(0), "presence_penalty": float64(1.5), "repeat_penalty": float64(1)}
	config, _ := Qwen35Configuration("baseline")
	if !reflect.DeepEqual(config.Options, baseline) || config.Thinking || config.Reserve != 1000 || config.Timeout != 120*time.Second {
		t.Fatal("baseline changed", config)
	}
	greedy, _ := Qwen35Configuration("greedy")
	baseline["temperature"], baseline["presence_penalty"] = float64(0), float64(0)
	if !reflect.DeepEqual(greedy.Options, baseline) || greedy.Thinking || greedy.Reserve != 1000 || greedy.Timeout != config.Timeout {
		t.Fatal("greedy changes more than two parameters")
	}
	thinking, _ := Qwen35Configuration("thinking")
	baseline["temperature"], baseline["presence_penalty"], baseline["top_p"], baseline["num_predict"] = float64(1), float64(1.5), float64(.95), 4096
	if !reflect.DeepEqual(thinking.Options, baseline) || !thinking.Thinking || thinking.Reserve != 4096 || thinking.Timeout != 300*time.Second {
		t.Fatal("thinking parameters")
	}
	if _, err := Qwen35Configuration("other"); err == nil {
		t.Fatal("unknown mode")
	}
	if Hash(SummaryPromptV2) != "8cc6dc4d3fad8eba8da327ecf9837aaa0213c6f978678e65c4ad105464d8ff31" || Hash(SummarySchema) != "64c9ba78c46b22b88423deae1f961a89e3d93a00d1197eb4acb1bfc9c236e275" {
		t.Fatal("frozen system/schema changed")
	}
	if got := Qwen35Render("s", "u", true); got != "<|im_start|>system\ns<|im_end|>\n<|im_start|>user\nu<|im_end|>\n<|im_start|>assistant\n<think>\n" {
		t.Fatal("thinking renderer", got)
	}
	// A full 4096 reserve is required before HTTP, including reasoning and final content.
	for _, tokens := range []int{4096, 4097} {
		counter := &qwen35Counter{tokens: tokens, reserve: 4096}
		runner, _ := NewQwen35RunnerMode("http://127.0.0.1:1", counter, "thinking")
		record := runner.SummarizeVersion(context.Background(), "input", example(t), SummaryPromptVersionV2, false)
		if (record.Error != nil) != (tokens == 4097) || record.Budget.RequiredTokens != tokens+4096 || counter.verifies != 0 {
			t.Fatal("thinking budget", record)
		}
	}
}

func TestQwen35ThinkingFailuresAndPrivacy(t *testing.T) {
	for _, scenario := range []string{"ok", "length", "empty", "invalid_json", "sources", "wrong_renderer", "token_mismatch", "missing_count", "timeout"} {
		t.Run(scenario, func(t *testing.T) {
			counter := &qwen35Counter{tokens: 100, reserve: 4096}
			generations := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload struct {
					Think    bool
					Debug    bool `json:"_debug_render_only"`
					Options  map[string]float64
					Format   json.RawMessage
					Messages []struct{ Role, Content string }
				}
				if json.NewDecoder(r.Body).Decode(&payload) != nil {
					t.Error("decode")
					return
				}
				if !payload.Think || payload.Options["num_predict"] != 4096 || payload.Options["temperature"] != 1 || payload.Options["top_p"] != .95 || payload.Options["presence_penalty"] != 1.5 || string(payload.Format) != SummarySchema || payload.Messages[0].Content != SummaryPromptV2 {
					t.Error("thinking request")
				}
				if payload.Debug {
					rendered := Qwen35Render(counter.system, counter.user, true)
					if scenario == "wrong_renderer" {
						rendered = Qwen35Render(counter.system, counter.user, false)
					}
					json.NewEncoder(w).Encode(map[string]any{"_debug_info": map[string]string{"rendered_template": rendered}})
					return
				}
				generations++
				if scenario == "timeout" {
					<-r.Context().Done()
					return
				}
				id := example(t).Messages[0].ID
				if scenario == "sources" {
					id = "missing"
				}
				content, _ := json.Marshal(map[string]any{"summary": "Ответ mock.", "source_message_ids": []string{id}})
				if scenario == "empty" {
					content = nil
				}
				if scenario == "invalid_json" {
					content = []byte("not JSON")
				}
				reason := "stop"
				if scenario == "length" {
					reason = "length"
				}
				response := map[string]any{"done": true, "done_reason": reason, "message": map[string]string{"content": string(content), "thinking": "PRIVATE_THINKING_ONLY"}, "prompt_eval_count": 100, "eval_count": 50}
				if scenario == "missing_count" {
					delete(response, "prompt_eval_count")
				}
				if scenario == "token_mismatch" {
					response["prompt_eval_count"] = 101
				}
				json.NewEncoder(w).Encode(response)
			}))
			defer srv.Close()
			runner, _ := NewQwen35RunnerMode(srv.URL, counter, "thinking")
			if scenario == "timeout" {
				runner.Client.Timeout = 100 * time.Millisecond
			}
			record := runner.SummarizeVersion(context.Background(), "input", example(t), SummaryPromptVersionV2, true)
			raw, _ := json.Marshal(record)
			if strings.Contains(string(raw), "PRIVATE_THINKING_ONLY") {
				t.Fatal("thinking leaked")
			}
			if scenario == "ok" {
				if Failed(record) || record.ThinkingPresent == nil || !*record.ThinkingPresent {
					t.Fatal("valid thinking content", record)
				}
			} else if !Failed(record) || record.Result != nil {
				t.Fatal("failure accepted", record)
			}
			if scenario == "timeout" && (record.Error == nil || *record.Error != "ollama_timeout") {
				t.Fatal("timeout not classified")
			}
			if scenario == "wrong_renderer" {
				if generations != 0 {
					t.Fatal("render fallback")
				}
			} else if generations != 1 {
				t.Fatal("retry")
			}
		})
	}
}

func TestQwen35TransportFailsClosed(t *testing.T) {
	for _, mode := range []string{"check", "ok", "missing_render", "passthrough", "wrong_renderer", "token_mismatch", "missing_count", "length", "sources"} {
		t.Run(mode, func(t *testing.T) {
			counter := &qwen35Counter{tokens: 100}
			gens := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var p struct {
					Model                          string
					Think, Stream, Shift, Truncate bool
					Debug                          bool `json:"_debug_render_only"`
					Options                        map[string]float64
					Format                         json.RawMessage
					Messages                       []struct{ Role, Content string }
				}
				if json.NewDecoder(r.Body).Decode(&p) != nil {
					t.Error("payload")
				}
				for k, v := range Qwen35Options() {
					if k == "num_ctx" || k == "num_predict" {
						continue
					}
					if p.Options[k] != v.(float64) {
						t.Errorf("option %s", k)
					}
				}
				if p.Options["num_ctx"] != 8192 || p.Options["num_predict"] != 1000 || p.Model != Qwen35Model || p.Think || p.Stream || p.Shift || p.Truncate || string(p.Format) != SummarySchema || p.Messages[0].Content != SummaryPromptV2 || p.Messages[1].Content != counter.user {
					t.Error("profile/prompt mismatch")
				}
				if p.Debug {
					if mode == "missing_render" {
						json.NewEncoder(w).Encode(map[string]any{})
						return
					}
					rendered := Qwen35Render(counter.system, counter.user)
					if mode == "passthrough" {
						rendered = "{{ .Prompt }}"
					}
					if mode == "wrong_renderer" {
						rendered = ExpectedRender(counter.system, counter.user)
					}
					json.NewEncoder(w).Encode(map[string]any{"_debug_info": map[string]string{"rendered_template": rendered}})
					return
				}
				gens++
				id := example(t).Messages[0].ID
				if mode == "sources" {
					id = "missing"
				}
				content, _ := json.Marshal(map[string]any{"summary": "Контролируемый ответ mock.", "source_message_ids": []string{id}})
				reason := "stop"
				if mode == "length" {
					reason = "length"
				}
				out := map[string]any{"done": true, "done_reason": reason, "message": map[string]string{"content": string(content), "thinking": "PRIVATE_REASONING"}, "prompt_eval_count": 100, "eval_count": 10}
				if mode == "token_mismatch" {
					out["prompt_eval_count"] = 101
				}
				if mode == "missing_count" {
					delete(out, "prompt_eval_count")
				}
				json.NewEncoder(w).Encode(out)
			}))
			defer srv.Close()
			runner := NewQwen35Runner(srv.URL, counter)
			runner.VerifyOnly = mode == "check"
			record := runner.SummarizeVersion(context.Background(), "input", example(t), SummaryPromptVersionV2, mode != "check")
			raw, _ := json.Marshal(record)
			if strings.Contains(string(raw), "PRIVATE_REASONING") {
				t.Fatal("thinking leaked")
			}
			if mode == "check" {
				if gens != 0 || Failed(record) || !record.Budget.Verified || record.RuntimeTokenCountVerified != nil {
					t.Fatal("check claim")
				}
				return
			}
			if mode == "missing_render" || mode == "passthrough" || mode == "wrong_renderer" {
				if gens != 0 || record.Error == nil {
					t.Fatal("render bypass")
				}
				return
			}
			if gens != 1 {
				t.Fatal("retries")
			}
			if mode == "ok" {
				if Failed(record) || record.RuntimeTokenCountVerified == nil || !*record.RuntimeTokenCountVerified {
					t.Fatal("count not verified")
				}
			} else if !Failed(record) || record.Result != nil {
				t.Fatal("invalid result accepted")
			}
		})
	}
}

func TestQwen35PlanBudgetAndPromptRestriction(t *testing.T) {
	counter := &qwen35Counter{tokens: 7500}
	runner := NewQwen35Runner("http://127.0.0.1:1", counter)
	record := runner.SummarizeVersion(context.Background(), "input", example(t), SummaryPromptVersionV2, false)
	if record.Error == nil || *record.Error != "input_budget_exceeded" || counter.verifies != 0 || record.GenerationAttempts != 0 {
		t.Fatal("budget guard")
	}
	if record := runner.Summarize(context.Background(), "input", example(t), true); record.Error == nil || record.GenerationAttempts != 0 {
		t.Fatal("v4 allowed")
	}
	if Qwen35Render("s", "u") == ExpectedRender("s", "u") {
		t.Fatal("8B renderer reused")
	}
}

func TestQwen35FactoryPinsSeparateResources(t *testing.T) {
	python, err := os.Executable() // Factory-only: no process is started by this test.
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("BRIEFING_BUDGET_PYTHON", python)
	t.Setenv("OLLAMA_MODEL", Qwen35Model)
	t.Setenv("BRIEFING_TOKENIZER_DIR", "must-not-be-used")
	t.Setenv("BRIEFING_BUDGET_HELPER", "must-not-be-used")
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	os.WriteFile(path, []byte("{}"), 0600)
	if _, err := NewQwen35Counter(dir); err == nil {
		t.Fatal("untrusted manifest accepted")
	}
	os.WriteFile(path, qwen35Pin, 0600)
	counter, err := NewQwen35Counter(dir)
	if err != nil || counter.Model != Qwen35Model || counter.Resources != dir || filepath.Base(counter.Script) != "qwen35_helper.py" {
		t.Fatal("profile isolation", err)
	}
	t.Setenv("OLLAMA_MODEL", "qwen3:8b")
	if _, err := NewQwen35Counter(dir); err == nil {
		t.Fatal("conflicting model accepted")
	}
}
