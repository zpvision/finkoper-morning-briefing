package diagnostic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"finkoper-morning-briefing/internal/briefing"
)

type spyCounter struct {
	system, user     string
	fail             bool
	counts, verifies int
}

func TestVerifyOnly14BDoesNotGenerate(t *testing.T) {
	for _, mode := range []string{"ok", "missing", "mismatch", "verify_failure"} {
		t.Run(mode, func(t *testing.T) {
			counter := &spyCounter{fail: mode == "verify_failure"}
			renders := 0
			generations := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var p struct {
					Debug    bool `json:"_debug_render_only"`
					Model    string
					Think    bool
					Messages []struct{ Content string }
					Format   json.RawMessage
				}
				json.NewDecoder(r.Body).Decode(&p)
				if p.Model != "qwen3:14b-q4_K_M" || p.Think || string(p.Format) != briefing.OutputSchema {
					t.Error("profile payload")
				}
				if !p.Debug {
					generations++
					t.Error("check generated")
					return
				}
				renders++
				if mode == "missing" {
					json.NewEncoder(w).Encode(map[string]any{})
					return
				}
				rendered := ExpectedRender(p.Messages[0].Content, p.Messages[1].Content)
				if mode == "mismatch" {
					rendered += " "
				}
				json.NewEncoder(w).Encode(map[string]any{"_debug_info": map[string]string{"rendered_template": rendered}})
			}))
			defer server.Close()
			runner := NewRunner(server.URL, "qwen3:14b-q4_K_M", time.Second, counter)
			runner.VerifyOnly = true
			record := runner.Compare(context.Background(), "input", example(t), Variants()[0], false)
			if record.Mode != "check" || record.GenerationAttempts != 0 || generations != 0 {
				t.Fatal("generation in check")
			}
			if mode == "ok" {
				if Failed(record) || !record.Budget.Verified || renders != 1 {
					t.Fatal("check failed")
				}
			} else if !Failed(record) || record.Budget.Verified {
				t.Fatal("check did not fail closed")
			}
		})
	}
}

func (c *spyCounter) Count(_ context.Context, s, u string) (briefing.Budget, error) {
	c.system, c.user = s, u
	c.counts++
	return briefing.Budget{InputTokens: 100, OutputReserve: 1500, ContextTokens: 8192, RequiredTokens: 1600, RemainingTokens: 6592, Method: briefing.BudgetMethod}, nil
}
func (c *spyCounter) Verify(_ context.Context, s, u string) error {
	c.verifies++
	if c.fail {
		return errors.New("context_chat_render_mismatch")
	}
	if c.system != s || c.user != u {
		return errors.New("wrong_input")
	}
	return nil
}
func example(t *testing.T) briefing.Input {
	t.Helper()
	raw, err := os.ReadFile("../../examples/02.json")
	if err != nil {
		t.Fatal(err)
	}
	var in briefing.Input
	if briefing.Strict(raw, &in) != nil || in.Normalize() != nil {
		t.Fatal("invalid example")
	}
	return in
}

func TestVariantsSameInputAndParametersOneGenerationEach(t *testing.T) {
	in := example(t)
	_, user := briefing.PromptInput(in)
	counter := &spyCounter{}
	var generations, renders atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" || r.Header.Get("Authorization") != "" {
			t.Error("unexpected request")
		}
		var payload struct {
			Model    string              `json:"model"`
			Stream   bool                `json:"stream"`
			Think    bool                `json:"think"`
			Truncate bool                `json:"truncate"`
			Shift    bool                `json:"shift"`
			Keep     string              `json:"keep_alive"`
			Debug    bool                `json:"_debug_render_only"`
			Format   json.RawMessage     `json:"format"`
			Messages []map[string]string `json:"messages"`
			Options  map[string]int      `json:"options"`
		}
		if json.NewDecoder(r.Body).Decode(&payload) != nil {
			t.Error("invalid payload")
			w.WriteHeader(400)
			return
		}
		if payload.Model != "qwen3:8b" || payload.Stream || payload.Think || payload.Truncate || payload.Shift || payload.Keep != "30m" || payload.Options["num_ctx"] != 8192 || payload.Options["num_predict"] != 1500 || payload.Options["temperature"] != 0 {
			t.Error("parameters changed")
		}
		if len(payload.Messages) != 2 || payload.Messages[0]["role"] != "system" || payload.Messages[1]["role"] != "user" || payload.Messages[0]["content"] != counter.system || payload.Messages[1]["content"] != counter.user || counter.user != user {
			t.Error("prompt lost or replaced")
		}
		if payload.Debug {
			renders.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"_debug_info": map[string]string{"rendered_template": ExpectedRender(counter.system, counter.user)}})
			return
		}
		n := generations.Add(1)
		if n == 1 {
			if string(payload.Format) != briefing.OutputSchema {
				t.Error("A schema altered")
			}
		} else if string(payload.Format) != `"json"` {
			t.Error("B/C format differs")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"done": true, "done_reason": "stop", "message": map[string]string{"content": `{"topics":[]}`}, "prompt_eval_count": 100, "eval_count": 5, "total_duration": 1000000, "load_duration": 0})
	}))
	defer srv.Close()
	runner := NewRunner(srv.URL, "qwen3:8b", time.Second, counter)
	records := []Record{}
	for _, v := range Variants() {
		record := runner.Compare(context.Background(), "input", in, v, true)
		if Failed(record) || record.GenerationAttempts != 1 || record.SystemPreserved == nil || !*record.SystemPreserved || record.TokenDifference == nil || *record.TokenDifference != 0 {
			t.Fatal(record)
		}
		records = append(records, record)
	}
	if generations.Load() != 3 || renders.Load() != 3 || counter.counts != 3 || counter.verifies != 3 {
		t.Fatal("unexpected attempts")
	}
	if records[0].PromptHash != records[1].PromptHash || records[0].MessagesHash != records[1].MessagesHash || records[0].PromptHash == records[2].PromptHash {
		t.Fatal("invalid A/B/C comparison")
	}
	if records[0].RenderedHash == nil || records[1].RenderedHash == nil || *records[0].RenderedHash != *records[1].RenderedHash {
		t.Fatal("A/B render differs despite same messages")
	}
	if records[0].UserHash != records[2].UserHash || records[0].Parameters["keep_alive"] != records[2].Parameters["keep_alive"] {
		t.Fatal("input changed")
	}
}

func TestPlanAndPreflightFailureNeverGenerate(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(500) }))
	defer srv.Close()
	in := example(t)
	counter := &spyCounter{}
	runner := NewRunner(srv.URL, "qwen3:8b", time.Second, counter)
	record := runner.Compare(context.Background(), "input", in, Variants()[0], false)
	if calls != 0 || counter.verifies != 0 || Failed(record) || record.GenerationAttempts != 0 {
		t.Fatal(record)
	}
	counter.fail = true
	record = runner.Compare(context.Background(), "input", in, Variants()[0], true)
	if calls != 0 || record.Error == nil || *record.Error != "context_chat_render_mismatch" || record.GenerationAttempts != 0 {
		t.Fatal(record)
	}
}

func TestMissingOrWrongVariantRenderStopsWithoutGeneration(t *testing.T) {
	for _, mode := range []string{"missing", "wrong"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			counter := &spyCounter{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if mode == "missing" {
					_, _ = w.Write([]byte(`{"done":true,"message":{"content":"not a render"}}`))
				} else {
					_ = json.NewEncoder(w).Encode(map[string]any{"_debug_info": map[string]string{"rendered_template": ExpectedRender("replaced", counter.user)}})
				}
			}))
			defer srv.Close()
			record := NewRunner(srv.URL, "qwen3:8b", time.Second, counter).Compare(context.Background(), "input", example(t), Variants()[0], true)
			if calls.Load() != 1 || record.Error == nil || record.GenerationAttempts != 0 {
				t.Fatal(record)
			}
		})
	}
}

func TestInvalidAndIncompleteOutputsRemainVisibleWithoutRetries(t *testing.T) {
	for _, tc := range []struct{ content, reason string }{{`{"unexpected":true}`, "stop"}, {`not JSON`, "stop"}, {`{"topics":[]}`, "length"}} {
		t.Run(tc.content+tc.reason, func(t *testing.T) {
			counter := &spyCounter{}
			var gens atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var p struct {
					Debug bool `json:"_debug_render_only"`
				}
				_ = json.NewDecoder(r.Body).Decode(&p)
				if p.Debug {
					_ = json.NewEncoder(w).Encode(map[string]any{"_debug_info": map[string]string{"rendered_template": ExpectedRender(counter.system, counter.user)}})
					return
				}
				gens.Add(1)
				_ = json.NewEncoder(w).Encode(map[string]any{"done": true, "done_reason": tc.reason, "message": map[string]string{"content": tc.content}})
			}))
			defer srv.Close()
			record := NewRunner(srv.URL, "qwen3:8b", time.Second, counter).Compare(context.Background(), "input", example(t), Variants()[1], true)
			if !Failed(record) || gens.Load() != 1 || record.GenerationAttempts != 1 {
				t.Fatal(record)
			}
			if !json.Valid([]byte(tc.content)) && record.InvalidContent == nil {
				t.Fatal("invalid output hidden")
			}
			if json.Valid([]byte(tc.content)) && !bytes.Equal(record.Result, []byte(tc.content)) {
				t.Fatal("model output replaced")
			}
		})
	}
}

func TestDiagnosticWithActualHelperLocalSimulation(t *testing.T) {
	python := os.Getenv("BRIEFING_TEST_BUDGET_PYTHON")
	base := os.Getenv("BRIEFING_TEST_SIMULATION_URL")
	if python == "" || base == "" {
		t.Skip("opt-in local helper + Postman simulation")
	}
	if !strings.HasPrefix(base, "http://127.0.0.1:") {
		t.Fatal("local simulation only")
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
	counter.BaseURL = base
	runner := NewRunner(base, "qwen3:8b", 5*time.Second, counter)
	for _, file := range []string{"04-exact-20.json", "02.json"} {
		raw, err := os.ReadFile("../../examples/" + file)
		if err != nil {
			t.Fatal(err)
		}
		var in briefing.Input
		_ = json.Unmarshal(raw, &in)
		if in.Normalize() != nil {
			t.Fatal("invalid example")
		}
		for _, variant := range Variants() {
			record := runner.Compare(context.Background(), file, in, variant, true)
			if Failed(record) || record.GenerationAttempts != 1 {
				t.Fatal(record)
			}
			t.Logf("%s %s: prompt=%s tokens=%d preserved=%v", file, variant.Name, record.PromptHash, record.Budget.InputTokens, *record.SystemPreserved)
		}
	}
}
