package diagnostic

import (
	"bytes"
	"context"
	"encoding/json"
	"finkoper-morning-briefing/internal/briefing"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"text/template"
	"time"
)

func TestThinkingActualHelperAndExportedTemplate(t *testing.T) {
	python := os.Getenv("BRIEFING_TEST_BUDGET_PYTHON")
	if python == "" {
		t.Skip("opt-in local tokenizers")
	}
	t.Setenv("BRIEFING_BUDGET_PYTHON", python)
	script, _ := filepath.Abs("../../tools/context_budget/helper.py")
	resources, _ := filepath.Abs("../../resources/tokenizer/qwen3-8b")
	t.Setenv("BRIEFING_BUDGET_HELPER", script)
	t.Setenv("BRIEFING_TOKENIZER_DIR", resources)
	counter, err := briefing.NewPythonCounter()
	if err != nil {
		t.Fatal(err)
	}
	counter.DiagnosticThinking = true
	raw, err := os.ReadFile("../../qwen3-show.json")
	if err != nil {
		t.Fatal(err)
	}
	var show struct{ Template string }
	if json.Unmarshal(raw, &show) != nil {
		t.Fatal("export")
	}
	tmpl, err := template.New("export").Parse(show.Template)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(filepath.Join(resources, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Digest string `json:"model_digest"`
	}
	json.Unmarshal(raw, &manifest)
	generations := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/version":
			json.NewEncoder(w).Encode(map[string]string{"version": "0.40.5"})
		case "/api/tags":
			json.NewEncoder(w).Encode(map[string]any{"models": []map[string]string{{"name": "qwen3:8b", "digest": manifest.Digest}}})
		case "/api/show":
			json.NewEncoder(w).Encode(map[string]string{"template": show.Template})
		case "/api/chat":
			var p struct {
				Think    bool
				Debug    bool `json:"_debug_render_only"`
				Format   json.RawMessage
				Messages []struct{ Role, Content string }
			}
			json.NewDecoder(r.Body).Decode(&p)
			var actualSchema, expectedSchema any
			json.Unmarshal(p.Format, &actualSchema)
			json.Unmarshal([]byte(briefing.OutputSchema), &expectedSchema)
			if !p.Think || len(p.Messages) != 2 || !reflect.DeepEqual(actualSchema, expectedSchema) {
				t.Error("thinking schema contract")
			}
			if p.Debug {
				var rendered bytes.Buffer
				root := map[string]any{"System": p.Messages[0].Content, "Tools": nil, "Think": true, "IsThinkSet": true, "Messages": []map[string]string{{"Role": "system", "Content": p.Messages[0].Content}, {"Role": "user", "Content": p.Messages[1].Content}}}
				if tmpl.Execute(&rendered, root) != nil {
					t.Error("render")
				}
				json.NewEncoder(w).Encode(map[string]any{"_debug_info": map[string]string{"rendered_template": rendered.String()}})
				return
			}
			generations++
			json.NewEncoder(w).Encode(map[string]any{"done": true, "done_reason": "stop", "message": map[string]string{"content": `{"topics":[]}`, "thinking": "PRIVATE_REASONING_SENTINEL"}})
		default:
			w.WriteHeader(501)
		}
	}))
	defer srv.Close()
	counter.BaseURL = srv.URL
	for _, file := range []string{"04-exact-20.json", "02.json"} {
		raw, err := os.ReadFile("../../examples/" + file)
		if err != nil {
			t.Fatal(err)
		}
		var in briefing.Input
		if briefing.Strict(raw, &in) != nil || in.Normalize() != nil {
			t.Fatal("input")
		}
		out := NewThinkingRunner(srv.URL, counter).Compare(context.Background(), file, in, ThinkingVariant(), true)
		if Failed(out) || !out.Budget.Verified || out.Budget.OutputReserve != 4096 {
			t.Fatalf("report %+v", out)
		}
		t.Logf("%s thinking tokens=%d, reserve=%d", file, out.Budget.InputTokens, out.Budget.OutputReserve)
	}
	if generations != 2 {
		t.Fatal("generation count")
	}
	counterOutput, err := counter.Count(context.Background(), briefing.SystemPrompt, strings.Repeat("long ", 10000))
	if err != nil || counterOutput.RequiredTokens <= 8192 {
		t.Fatal("overflow")
	}
}

type thinkingCounter struct {
	spyCounter
	tokens int
}

func (c *thinkingCounter) Count(ctx context.Context, s, u string) (briefing.Budget, error) {
	c.spyCounter.Count(ctx, s, u)
	return briefing.Budget{InputTokens: c.tokens, OutputReserve: 4096, ContextTokens: 8192, RequiredTokens: c.tokens + 4096, RemainingTokens: 4096 - c.tokens, Method: briefing.BudgetMethod}, nil
}

func TestThinkingTransportAndPrivacy(t *testing.T) {
	for _, mode := range []string{"ok", "length", "empty", "sources", "missing_render", "wrong_render"} {
		t.Run(mode, func(t *testing.T) {
			counter := &thinkingCounter{tokens: 100}
			generations := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var p struct {
					Think    bool                `json:"think"`
					Stream   bool                `json:"stream"`
					Debug    bool                `json:"_debug_render_only"`
					Keep     string              `json:"keep_alive"`
					Format   json.RawMessage     `json:"format"`
					Messages []map[string]string `json:"messages"`
					Options  map[string]float64  `json:"options"`
				}
				if json.NewDecoder(r.Body).Decode(&p) != nil {
					t.Error("decode")
				}
				if !p.Think || p.Stream || p.Keep != "30m" || Hash(string(p.Format)) != Hash(briefing.OutputSchema) {
					t.Error("thinking contract")
				}
				expected := map[string]float64{"temperature": 0.6, "top_p": 0.95, "top_k": 20, "min_p": 0, "num_ctx": 8192, "num_predict": 4096}
				for k, v := range expected {
					if p.Options[k] != v {
						t.Errorf("option %s", k)
					}
				}
				if len(p.Messages) != 2 || p.Messages[0]["content"] != briefing.SystemPrompt || p.Messages[1]["content"] != counter.user {
					t.Error("prompt changed")
				}
				if p.Debug {
					if mode == "missing_render" {
						json.NewEncoder(w).Encode(map[string]any{})
						return
					}
					rendered := ExpectedRender(counter.system, counter.user, true)
					if mode == "wrong_render" {
						rendered += "x"
					}
					json.NewEncoder(w).Encode(map[string]any{"_debug_info": map[string]string{"rendered_template": rendered}})
					return
				}
				generations++
				content := `{"topics":[]}`
				reason := "stop"
				if mode == "length" {
					reason = "length"
				}
				if mode == "empty" {
					content = ""
				}
				if mode == "sources" {
					content = `{"topics":[{"text":"request","status":"unclear","deadline_text":null,"needs_context":true,"source_message_ids":["unknown"]}]}`
				}
				json.NewEncoder(w).Encode(map[string]any{"done": true, "done_reason": reason, "message": map[string]string{"content": content, "thinking": "PRIVATE_REASONING_SENTINEL"}, "prompt_eval_count": 100, "eval_count": 4096})
			}))
			defer server.Close()
			runner := NewThinkingRunner(server.URL, counter)
			if runner.Client.Timeout != 180*time.Second {
				t.Fatal("timeout")
			}
			out := runner.Compare(context.Background(), "example", example(t), ThinkingVariant(), true)
			raw, _ := json.Marshal(out)
			if strings.Contains(string(raw), "PRIVATE_REASONING_SENTINEL") {
				t.Fatal("reasoning leaked")
			}
			if mode == "missing_render" || mode == "wrong_render" {
				if generations != 0 || out.Error == nil {
					t.Fatal("render must fail closed")
				}
				return
			}
			if generations != 1 || counter.verifies != 1 {
				t.Fatal("generation count")
			}
			if out.ThinkingBytes == nil || *out.ThinkingBytes != len("PRIVATE_REASONING_SENTINEL") {
				t.Fatal("thinking metadata")
			}
			if mode == "ok" {
				if Failed(out) || out.Result == nil || out.TokenDifference == nil || *out.TokenDifference != 0 {
					t.Fatalf("unexpected report %+v", out)
				}
			} else if !Failed(out) || out.Result != nil {
				t.Fatal("invalid final accepted")
			}
		})
	}
}

func TestThinkingOfflineAndBudget(t *testing.T) {
	for _, tokens := range []int{100, 5000} {
		counter := &thinkingCounter{tokens: tokens}
		runner := NewThinkingRunner("http://127.0.0.1:1", counter)
		out := runner.Compare(context.Background(), "example", example(t), ThinkingVariant(), false)
		if counter.verifies != 0 || out.GenerationAttempts != 0 || out.Budget.Verified {
			t.Fatal("offline mode used network")
		}
		if (out.Error != nil) != (tokens == 5000) {
			t.Fatal("budget guard")
		}
	}
}
