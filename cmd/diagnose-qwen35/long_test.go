package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"finkoper-morning-briefing/internal/briefing"
	"finkoper-morning-briefing/internal/diagnostic"
)

type longCounter struct{ tokens, verifies int }

func (c *longCounter) Count(context.Context, string, string) (briefing.Budget, error) {
	return briefing.Budget{InputTokens: c.tokens, OutputReserve: 6144, ContextTokens: 8192, RequiredTokens: c.tokens + 6144, RemainingTokens: 2048 - c.tokens, Method: diagnostic.Qwen35BudgetMethod}, nil
}
func (c *longCounter) Verify(context.Context, string, string) error { c.verifies++; return nil }

func TestThinkingLongExplicitScope(t *testing.T) {
	factory := func(string, string) (*diagnostic.Runner, error) {
		t.Fatal("invalid scope reached resource factory")
		return nil, nil
	}
	for _, selected := range []string{"sr-09", "hol-11", "development", "holdout", "six"} {
		args := []string{"--profile", diagnostic.Qwen35Profile, "--mode", "thinking-long"}
		if selected != "" {
			args = append(args, "--case", selected)
		}
		if err := runWithRunner(args, &bytes.Buffer{}, factory); err == nil {
			t.Fatal("invalid scope accepted", selected)
		}
	}
	// The pre-existing all test pins exactly 18 records / three modes; default is baseline.
	root, _ := filepath.Abs("../..")
	t.Chdir(root)
	var output bytes.Buffer
	factory = func(_ string, mode string) (*diagnostic.Runner, error) {
		if mode != "baseline" {
			t.Fatal("long became default")
		}
		return diagnostic.NewQwen35RunnerMode("http://127.0.0.1:1", modeCounter{1000}, mode)
	}
	if err := runWithRunner([]string{"--profile", diagnostic.Qwen35Profile, "--case", "sr-06"}, &output, factory); err != nil {
		t.Fatal(err)
	}
	var entry report
	json.Unmarshal(output.Bytes(), &entry)
	if entry.GenerationMode != "baseline" || entry.GenerationAttempts != 0 {
		t.Fatal("default changed")
	}
	old, _ := diagnostic.Qwen35Configuration("thinking")
	long, _ := diagnostic.Qwen35Configuration("thinking-long")
	old.Options["num_predict"] = 6144
	if !reflect.DeepEqual(old.Options, long.Options) || !long.Thinking || long.Reserve != 6144 || long.Timeout != 600*time.Second {
		t.Fatal("sampling/reserve/timeout")
	}
}

func TestThinkingLongSingleHTTP(t *testing.T) {
	for _, scenario := range []string{"plan", "check", "ok", "length", "empty", "overflow", "boundary"} {
		t.Run(scenario, func(t *testing.T) {
			root, _ := filepath.Abs("../..")
			t.Chdir(root)
			counter := &longCounter{tokens: 100}
			if scenario == "overflow" {
				counter.tokens = 2049
			}
			if scenario == "boundary" {
				counter.tokens = 2048
			}
			generations, requests := 0, 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				var p struct {
					Think    bool
					Debug    bool `json:"_debug_render_only"`
					Options  map[string]float64
					Messages []struct{ Content string }
					Format   json.RawMessage
				}
				if json.NewDecoder(r.Body).Decode(&p) != nil {
					t.Error("decode")
					return
				}
				expected := map[string]float64{"temperature": 1, "top_p": .95, "top_k": 20, "min_p": 0, "presence_penalty": 1.5, "repeat_penalty": 1, "num_ctx": 8192, "num_predict": 6144}
				if !p.Think || !reflect.DeepEqual(p.Options, expected) || p.Messages[0].Content != diagnostic.SummaryPromptV2 || string(p.Format) != diagnostic.SummarySchema {
					t.Error("long request mismatch")
				}
				if p.Debug {
					json.NewEncoder(w).Encode(map[string]any{"_debug_info": map[string]string{"rendered_template": diagnostic.Qwen35Render(p.Messages[0].Content, p.Messages[1].Content, true)}})
					return
				}
				generations++
				var user struct{ Messages []struct{ ID string } }
				json.Unmarshal([]byte(p.Messages[1].Content), &user)
				content, _ := json.Marshal(map[string]any{"summary": "Ответ mock.", "source_message_ids": []string{user.Messages[0].ID}})
				reason := "stop"
				if scenario == "length" {
					reason = "length"
					content = nil
				}
				if scenario == "empty" {
					content = nil
				}
				json.NewEncoder(w).Encode(map[string]any{"done": true, "done_reason": reason, "prompt_eval_count": counter.tokens, "eval_count": 6144, "message": map[string]string{"content": string(content), "thinking": "LONG_PRIVATE_THINKING"}})
			}))
			defer srv.Close()
			factory := func(_ string, mode string) (*diagnostic.Runner, error) {
				runner, err := diagnostic.NewQwen35RunnerMode(srv.URL, counter, mode)
				if err == nil && runner.Client.Timeout != 600*time.Second {
					t.Fatal("actual timeout")
				}
				return runner, err
			}
			args := []string{"--profile", diagnostic.Qwen35Profile, "--mode", "thinking-long", "--case", "sr-06"}
			if scenario == "check" {
				args = append(args, "--check-profile")
			} else if scenario != "plan" && scenario != "boundary" {
				args = append(args, "--run")
			}
			var output bytes.Buffer
			err := runWithRunner(args, &output, factory)
			shouldFail := scenario == "overflow" || scenario == "length" || scenario == "empty"
			if (err != nil) != shouldFail {
				t.Fatalf("status %v", err)
			}
			var entry report
			if json.Unmarshal(output.Bytes(), &entry) != nil || strings.Contains(strings.TrimSpace(output.String()), "\n") || strings.Contains(output.String(), "LONG_PRIVATE_THINKING") {
				t.Fatal("report/privacy")
			}
			if entry.GenerationMode != "thinking-long" || entry.CaseID != "sr-06" || entry.CallTimeoutMS != 600000 || entry.Budget.OutputReserve != 6144 || entry.Budget.RequiredTokens != counter.tokens+6144 {
				t.Fatal("report budget")
			}
			if scenario == "overflow" && (requests != 0 || counter.verifies != 0 || entry.Error == nil || *entry.Error != "input_budget_exceeded") {
				t.Fatal("overflow reached HTTP")
			}
			if scenario == "length" || scenario == "empty" {
				if entry.Error == nil || string(entry.Result) != "null" || entry.RuntimeTokenCountVerified == nil || !*entry.RuntimeTokenCountVerified {
					t.Fatal("matching count made incomplete response successful")
				}
			}
			expectedGens := 1
			if scenario == "plan" || scenario == "check" || scenario == "overflow" || scenario == "boundary" {
				expectedGens = 0
			}
			if generations != expectedGens || entry.GenerationAttempts != expectedGens {
				t.Fatal("retry/unexpected generation")
			}
		})
	}
}
