package briefing

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This deterministic stand-in tests HTTP/worker behavior, never tokenizer/model quality.
type testCounter struct{}

func (testCounter) Count(_ context.Context, s, u string) (Budget, error) {
	n := len(s) + len(u) + 512
	return Budget{n, OutputReserve, ContextTokens, n + OutputReserve, ContextTokens - n - OutputReserve, BudgetMethod, true}, nil
}
func (testCounter) Verify(context.Context, string, string) error { return nil }

type failingCounter struct{}

func (failingCounter) Count(context.Context, string, string) (Budget, error) {
	return Budget{}, errors.New("budget_helper_failed")
}
func (failingCounter) Verify(context.Context, string, string) error {
	return errors.New("context_chat_render_mismatch")
}

type renderFailureCounter struct{ testCounter }

func (renderFailureCounter) Verify(context.Context, string, string) error {
	return errors.New("context_chat_render_mismatch")
}
func TestRenderFailurePreventsGeneration(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(500) }))
	defer srv.Close()
	_, _, err := NewOllama(srv.URL, "qwen3:8b", time.Second, renderFailureCounter{}).Generate(context.Background(), sample())
	if err == nil || err.Error() != "context_chat_render_mismatch" || calls != 0 {
		t.Fatal(err, calls)
	}
}

func TestBudgetDiagnosticsAndRetry(t *testing.T) {
	s := store(t)
	in := sample()
	_ = in.Normalize()
	raw, _ := json.Marshal(in)
	h := Handler(s, "secret", testCounter{})
	w := request(h, "POST", "/v1/briefings", "accepted", raw, true)
	if w.Code != 202 {
		t.Fatal(w.Code)
	}
	broken := Handler(s, "secret", failingCounter{})
	if w = request(broken, "POST", "/v1/briefings", "accepted", raw, true); w.Code != 202 {
		t.Fatal("retry needs helper", w.Code)
	}
	if w = request(broken, "POST", "/v1/briefings", "new", raw, true); w.Code != 503 {
		t.Fatal(w.Code)
	}
	in.Messages[0].Text = strings.Repeat("private-input-", 650)
	raw, _ = json.Marshal(in)
	w = request(h, "POST", "/v1/briefings", "long", raw, true)
	if w.Code != 422 {
		t.Fatal(w.Code, w.Body.String())
	}
	var out struct {
		Error string `json:"error"`
		Budget
	}
	if json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Error != "input_budget_exceeded" || out.InputTokens <= 0 || out.OutputReserve != 1500 || out.ContextTokens != 8192 || out.Method != BudgetMethod {
		t.Fatal(w.Body.String())
	}
	if strings.Contains(w.Body.String(), "private-input") || strings.Contains(w.Body.String(), "secret") {
		t.Fatal("leaked input")
	}
}

func localPythonCounter(t *testing.T) *PythonCounter {
	t.Helper()
	python := os.Getenv("BRIEFING_TEST_BUDGET_PYTHON")
	if python == "" {
		t.Skip("opt-in local Python/tokenizer test")
	}
	t.Setenv("BRIEFING_BUDGET_PYTHON", python)
	script, _ := filepath.Abs("../../tools/context_budget/helper.py")
	resources, _ := filepath.Abs("../../resources/tokenizer/qwen3-8b")
	t.Setenv("BRIEFING_BUDGET_HELPER", script)
	t.Setenv("BRIEFING_TOKENIZER_DIR", resources)
	counter, err := NewPythonCounter()
	if err != nil {
		t.Fatal(err)
	}
	return counter
}
func TestRealLocalTokenizer(t *testing.T) {
	counter := localPythonCounter(t)
	for _, name := range []string{"01.json", "02.json", "04-exact-20.json"} {
		raw, err := os.ReadFile(filepath.Join("../../examples", name))
		if err != nil {
			t.Fatal(err)
		}
		var in Input
		if err = Strict(raw, &in); err != nil {
			t.Fatal(err)
		}
		if err = in.Normalize(); err != nil {
			t.Fatal(err)
		}
		b, err := AnalyzeBudget(context.Background(), counter, in)
		if err != nil {
			t.Fatal(name, err)
		}
		if b.Verified {
			t.Fatal("offline is not runtime verification")
		}
		t.Logf("%s: input=%d reserve=%d context=%d", name, b.InputTokens, b.OutputReserve, b.ContextTokens)
	}
	in := sample()
	in.Messages[0].Text = strings.Repeat("a ", 7000)
	b, err := AnalyzeBudget(context.Background(), counter, in)
	var exceeded *BudgetExceeded
	if !errors.As(err, &exceeded) || b.RequiredTokens <= 8192 {
		t.Fatal(b, err)
	}
	raw, _ := json.Marshal(in)
	w := request(Handler(store(t), "secret", counter), "POST", "/v1/briefings", "overflow", raw, true)
	if w.Code != 422 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestHelperTimeoutAndBoundedOutput(t *testing.T) {
	counter := localPythonCounter(t)
	dir := t.TempDir()
	for _, tc := range []struct {
		name, script string
		timeout      time.Duration
		want         string
	}{
		{"timeout", "import time; time.sleep(5)", 100 * time.Millisecond, "budget_helper_timeout"},
		{"output", "print('x'*17000)", 3 * time.Second, "budget_helper_invalid_response"},
		{"leak", "print('{\"error\":\"secret-private-text\"}'); raise SystemExit(1)", 3 * time.Second, "budget_helper_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, tc.name+".py")
			if err := os.WriteFile(path, []byte(tc.script), 0600); err != nil {
				t.Fatal(err)
			}
			counter.Script = path
			counter.Timeout = tc.timeout
			_, err := counter.Count(context.Background(), "s", "u")
			if err == nil || err.Error() != tc.want {
				t.Fatal(err)
			}
		})
	}
}

func TestVerifiedPromptMatchesGeneration(t *testing.T) {
	// Real subprocess/urllib/tokenizers against a local HTTP stand-in, not a claim about real Ollama.
	counter := localPythonCounter(t)
	manifestRaw, err := os.ReadFile(filepath.Join(counter.Resources, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Digest string `json:"model_digest"`
	}
	_ = json.Unmarshal(manifestRaw, &manifest)
	showRaw, err := os.ReadFile("../../qwen3-show.json")
	if err != nil {
		t.Fatal(err)
	}
	var show struct {
		Template string `json:"template"`
	}
	_ = json.Unmarshal(showRaw, &show)
	var verifiedSystem, verifiedUser string
	generations := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/version":
			_ = json.NewEncoder(w).Encode(map[string]string{"version": "0.40.5"})
		case "/api/tags":
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]string{{"name": "qwen3:8b", "digest": manifest.Digest}}})
		case "/api/show":
			_ = json.NewEncoder(w).Encode(map[string]string{"template": show.Template})
		case "/api/chat":
			var p struct {
				Debug    bool                             `json:"_debug_render_only"`
				Think    bool                             `json:"think"`
				Truncate bool                             `json:"truncate"`
				Shift    bool                             `json:"shift"`
				Messages []struct{ Role, Content string } `json:"messages"`
			}
			if json.NewDecoder(r.Body).Decode(&p) != nil || len(p.Messages) != 2 || p.Think || p.Truncate || p.Shift {
				t.Error("invalid request")
				w.WriteHeader(400)
				return
			}
			if p.Debug {
				verifiedSystem = p.Messages[0].Content
				verifiedUser = p.Messages[1].Content
				render := "<|im_start|>system\n\n" + verifiedSystem + "<|im_end|>\n<|im_start|>user\n" + verifiedUser + " /no_think<|im_end|>\n<|im_start|>assistant\n<think>\n\n</think>\n\n"
				_ = json.NewEncoder(w).Encode(map[string]any{"_debug_info": map[string]string{"rendered_template": render}})
			} else {
				generations++
				if p.Messages[0].Content != verifiedSystem || p.Messages[1].Content != verifiedUser {
					t.Error("counted/verified prompt differs")
				}
				_ = json.NewEncoder(w).Encode(envelope(goodResult, "stop", true))
			}
		default:
			w.WriteHeader(501)
		}
	}))
	defer srv.Close()
	counter.BaseURL = srv.URL
	counter.Model = "qwen3:8b"
	probeSystem, probeUser := SyntheticProbe()
	if err := counter.Verify(context.Background(), probeSystem, probeUser); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"01.json", "02.json", "04-exact-20.json"} {
		raw, err := os.ReadFile(filepath.Join("../../examples", name))
		if err != nil {
			t.Fatal(err)
		}
		w := request(Handler(store(t), "secret", counter), "POST", "/v1/briefings", name, raw, true)
		if w.Code != 202 {
			t.Fatal(name, w.Code, w.Body.String())
		}
	}
	in := sample()
	in.Messages[0].Text = "Текст и\u0306 е\u0308 <>& 😀 ` $()"
	if _, _, err := NewOllama(srv.URL, "qwen3:8b", time.Second, counter).Generate(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if generations != 1 {
		t.Fatal(generations)
	}
}
