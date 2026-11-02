package briefing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func sample() Input {
	from := time.Date(2026, 11, 1, 18, 0, 0, 0, time.UTC)
	return Input{CompanyID: "c", ClientID: "u", Period: Period{From: from, To: from.Add(15 * time.Hour)}, Messages: []Message{{ID: "m1", ConversationID: "chat", Channel: "max", SenderRole: "client", SentAt: from.Add(time.Hour), Text: "Проверьте документы"}}}
}
func store(t *testing.T) *Store {
	t.Helper()
	s, e := Open(filepath.Join(t.TempDir(), "test.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(s.Close)
	return s
}
func request(h http.Handler, method, path, key string, b []byte, auth bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, bytes.NewReader(b))
	if auth {
		r.Header.Set("Authorization", "Bearer secret")
	}
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestHTTPIdempotencyAndTenancy(t *testing.T) {
	s := store(t)
	s.limit = 1
	h := Handler(s, "secret", testCounter{})
	in := sample()
	b, _ := json.Marshal(in)
	for _, p := range []string{"/v1/briefings", "/v1/briefings/x?company_id=c"} {
		if w := request(h, "GET", p, "", nil, false); w.Code != 401 {
			t.Fatal(w.Code)
		}
	}
	if w := request(h, "GET", "/healthz", "", nil, false); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := request(h, "POST", "/v1/briefings", "", b, true); w.Code != 400 {
		t.Fatal(w.Code)
	}
	var wg sync.WaitGroup
	ids := make(chan string, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := request(h, "POST", "/v1/briefings", "same", b, true)
			if w.Code != 202 {
				t.Errorf("status %d %s", w.Code, w.Body.String())
				return
			}
			var j Job
			_ = json.Unmarshal(w.Body.Bytes(), &j)
			ids <- j.ID
		}()
	}
	wg.Wait()
	close(ids)
	id := ""
	for v := range ids {
		if id != "" && v != id {
			t.Fatal("different jobs")
		}
		id = v
	}
	if w := request(h, "POST", "/v1/briefings", "new", b, true); w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatal(w.Code)
	}
	in.Messages[0].Text = "Другое"
	changed, _ := json.Marshal(in)
	if w := request(h, "POST", "/v1/briefings", "same", changed, true); w.Code != 409 {
		t.Fatal(w.Code)
	}
	for _, tc := range []struct {
		company string
		code    int
	}{{"c", 200}, {"other", 404}} {
		w := request(h, "GET", "/v1/briefings/"+id+"?company_id="+tc.company, "", nil, true)
		if w.Code != tc.code {
			t.Fatal(w.Code)
		}
	}
}
func TestOptionalContext(t *testing.T) {
	s := store(t)
	h := Handler(s, "secret", testCounter{})
	b, _ := json.Marshal(sample())
	var obj map[string]any
	_ = json.Unmarshal(b, &obj)
	var id string
	for _, mode := range []string{"absent", "null", "empty"} {
		switch mode {
		case "absent":
			delete(obj, "context_messages")
		case "null":
			obj["context_messages"] = nil
		case "empty":
			obj["context_messages"] = []any{}
		}
		b, _ = json.Marshal(obj)
		w := request(h, "POST", "/v1/briefings", "key", b, true)
		if w.Code != 202 {
			t.Fatal(w.Code, w.Body.String())
		}
		var j Job
		_ = json.Unmarshal(w.Body.Bytes(), &j)
		if id != "" && id != j.ID {
			t.Fatal("context normalization")
		}
		id = j.ID
	}
}
func TestRecoveryAndLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "q.db")
	s, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	in := sample()
	_ = in.Normalize()
	j, e := s.Enqueue(context.Background(), in, "k")
	if e != nil {
		t.Fatal(e)
	}
	if second, e := Open(path); e == nil {
		second.Close()
		t.Fatal("second owner allowed")
	}
	if _, _, e = s.claim(context.Background()); e != nil {
		t.Fatal(e)
	}
	s.Close()
	s, e = Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	got, e := s.Get(context.Background(), "c", j.ID)
	if e != nil || got.Status != "queued" {
		t.Fatal(got, e)
	}
}

const goodResult = `{"topics":[{"text":"Проверить документы","status":"awaiting_staff","deadline_text":null,"needs_context":false,"source_message_ids":["m1"]}]}`

func envelope(content, reason string, done bool) map[string]any {
	return map[string]any{"message": map[string]string{"content": content}, "done": done, "done_reason": reason, "total_duration": 1000000, "load_duration": 0, "prompt_eval_count": 42}
}
func TestOllamaValidation(t *testing.T) {
	for _, tc := range []struct {
		name, content, reason string
		done, ok              bool
	}{
		{"valid", goodResult, "stop", true, true}, {"invalid source", strings.ReplaceAll(goodResult, "m1", "unknown"), "stop", true, false},
		{"length", goodResult, "length", true, false}, {"not done", goodResult, "stop", false, false},
		{"missing fields", `{"topics":[{"text":"x"}]}`, "stop", true, false},
		{"null bool", strings.ReplaceAll(goodResult, `"needs_context":false`, `"needs_context":null`), "stop", true, false},
		{"unknown field", strings.ReplaceAll(goodResult, `"text":`, `"extra":1,"text":`), "stop", true, false},
		{"invalid enum", strings.ReplaceAll(goodResult, "awaiting_staff", "waiting"), "stop", true, false},
		{"null topics", `{"topics":null}`, "stop", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var p map[string]any
				_ = json.NewDecoder(r.Body).Decode(&p)
				if r.URL.Path != "/api/chat" || p["stream"] != false || p["think"] != false || p["truncate"] != false || p["format"] == nil {
					t.Error("request contract")
				}
				_ = json.NewEncoder(w).Encode(envelope(tc.content, tc.reason, tc.done))
			}))
			defer srv.Close()
			_, m, e := NewOllama(srv.URL, "qwen3:8b", time.Second, testCounter{}).Generate(context.Background(), sample())
			if (e == nil) != tc.ok {
				t.Fatal(e)
			}
			if m.Load == nil || *m.Load != 0 || m.Generation != nil {
				t.Fatal("absent vs zero metrics")
			}
		})
	}
}
func TestWorkerSerial(t *testing.T) {
	var active, maxActive, calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		if n > maxActive.Load() {
			maxActive.Store(n)
		}
		calls.Add(1)
		time.Sleep(25 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(envelope(goodResult, "stop", true))
	}))
	defer srv.Close()
	s := store(t)
	in := sample()
	_ = in.Normalize()
	for i := 0; i < 5; i++ {
		if _, e := s.Enqueue(context.Background(), in, fmt.Sprint(i)); e != nil {
			t.Fatal(e)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, s, NewOllama(srv.URL, "qwen3:8b", time.Second, testCounter{})) }()
	defer func() {
		cancel()
		if e := <-done; e != nil {
			t.Error(e)
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		_ = s.db.QueryRow("SELECT count(*) FROM jobs WHERE status='succeeded'").Scan(&n)
		if n == 5 {
			if maxActive.Load() != 1 || calls.Load() != 5 {
				t.Fatal("concurrency or retries")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("worker timeout")
}
func TestInputLimitsAndChronology(t *testing.T) {
	for _, mutate := range []func(*Input){func(in *Input) { in.Messages = nil }, func(in *Input) { in.Messages = append(in.Messages, in.Messages[0]) }, func(in *Input) { in.Messages[0].SentAt = in.Period.To }, func(in *Input) { in.Context = append(in.Context, in.Messages[0]); in.Context[0].ID = "ctx" }, func(in *Input) { in.Period.To = in.Period.From }, func(in *Input) { in.Messages[0].Channel = "unknown" }} {
		in := sample()
		mutate(&in)
		if in.Normalize() == nil {
			t.Fatal("accepted invalid input")
		}
	}
	in := sample()
	in.Messages[0].Text = strings.Repeat("x", 7000)
	if _, e := AnalyzeBudget(context.Background(), testCounter{}, in); e == nil {
		t.Fatal("accepted over budget")
	}
	s := store(t)
	h := Handler(s, "secret", testCounter{})
	b, _ := json.Marshal(in)
	if w := request(h, "POST", "/v1/briefings", "large", b, true); w.Code != 422 {
		t.Fatal(w.Code)
	}
	if w := request(h, "POST", "/v1/briefings", "huge", []byte(strings.Repeat("x", MaxBody+1)), true); w.Code != 413 {
		t.Fatal(w.Code)
	}
	in = sample()
	m := in.Messages[0]
	m.ID = "m2"
	in.Messages = append(in.Messages, m)
	m.ID = "early"
	m.SentAt = in.Period.From
	in.Messages = append(in.Messages, m)
	var sorted Input
	_ = json.Unmarshal(userData(in), &sorted)
	if sorted.Messages[0].ID != "early" || sorted.Messages[1].ID != "m1" || sorted.Messages[2].ID != "m2" {
		t.Fatal("unstable order")
	}
}
func TestExamplesValid(t *testing.T) {
	for _, name := range []string{"01.json", "02.json", "03.json", "04-exact-20.json"} {
		b, e := os.ReadFile(filepath.Join("..", "..", "examples", name))
		if e != nil {
			t.Fatal(e)
		}
		var in Input
		if e = Strict(b, &in); e != nil {
			t.Fatal(e)
		}
		if e = in.Normalize(); e != nil {
			t.Fatal(e)
		}
	}
}

func TestWorkerFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer srv.Close()
	s := store(t)
	in := sample()
	_ = in.Normalize()
	j, e := s.Enqueue(context.Background(), in, "k")
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, s, NewOllama(srv.URL, "q", time.Second, testCounter{})) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got, e := s.Get(context.Background(), "c", j.ID)
		if e == nil && got.Status == "failed" {
			if got.Result != nil || got.Error == nil || got.Metrics.Processing == nil {
				t.Fatal(got)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("not failed")
}

// Opt-in only; normal go test never calls external endpoints.
func TestExternalSimulation(t *testing.T) {
	base := os.Getenv("BRIEFING_TEST_SIMULATION_URL")
	if base == "" {
		t.Skip("manual local Postman simulation only")
	}
	if !strings.HasPrefix(base, "http://127.0.0.1:") {
		t.Fatal("simulation must be local")
	}
	var counter BudgetCounter = testCounter{}
	if os.Getenv("BRIEFING_TEST_BUDGET_PYTHON") != "" {
		python := localPythonCounter(t)
		python.BaseURL = base
		python.Model = "qwen3:8b"
		s, u := SyntheticProbe()
		if err := python.Verify(context.Background(), s, u); err != nil {
			t.Fatal(err)
		}
		counter = python
	}
	s := store(t)
	h := Handler(s, "secret", counter)
	b, _ := json.Marshal(sample())
	w := request(h, "POST", "/v1/briefings", "simulation", b, true)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	var job Job
	_ = json.Unmarshal(w.Body.Bytes(), &job)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, s, NewOllama(base, "qwen3:8b", 3*time.Second, counter)) }()
	defer func() {
		cancel()
		if e := <-done; e != nil {
			t.Error(e)
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		w = request(h, "GET", "/v1/briefings/"+job.ID+"?company_id=c", "", nil, true)
		var got Job
		_ = json.Unmarshal(w.Body.Bytes(), &got)
		if got.Status == "failed" {
			t.Fatal(got.Error)
		}
		if got.Status == "succeeded" {
			if got.Result == nil || len(got.Result.Topics) != 1 || got.Metrics.Load == nil || *got.Metrics.Load != 0 || got.Metrics.BudgetHelper == nil || got.Metrics.CompatibilityCheck == nil {
				t.Fatal("unexpected simulation result")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("simulation timeout")
}

func TestContextOnlySourceRejected(t *testing.T) {
	in := sample()
	m := in.Messages[0]
	m.ID = "ctx"
	m.SentAt = in.Period.From.Add(-time.Hour)
	in.Context = []Message{m}
	if _, e := ValidateResult([]byte(strings.ReplaceAll(goodResult, "m1", "ctx")), in); e == nil {
		t.Fatal("context-only topic accepted")
	}
}

func TestOllamaErrorsAndTimeout(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		delay  time.Duration
	}{{"http", 500, `server error`, 0}, {"json", 200, `not json`, 0}, {"error field", 200, `{"done":true,"done_reason":"stop","error":"secret upstream text"}`, 0}, {"timeout", 200, `{}`, 150 * time.Millisecond}} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				time.Sleep(tc.delay)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			_, _, e := NewOllama(srv.URL, "q", 30*time.Millisecond, testCounter{}).Generate(context.Background(), sample())
			if e == nil || strings.Contains(e.Error(), "secret") {
				t.Fatal(e)
			}
			if calls.Load() != 1 {
				t.Fatal("unexpected retries")
			}
		})
	}
}

func TestShutdownRequeuesInterrupted(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)
	path := filepath.Join(t.TempDir(), "shutdown.db")
	s, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	in := sample()
	_ = in.Normalize()
	j, e := s.Enqueue(context.Background(), in, "k")
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, s, NewOllama(srv.URL, "q", time.Second, testCounter{})) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("not started")
	}
	cancel()
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	s.Close()
	s, e = Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	got, e := s.Get(context.Background(), "c", j.ID)
	if e != nil || got.Status != "queued" {
		t.Fatal(got, e)
	}
}
