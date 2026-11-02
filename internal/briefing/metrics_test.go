package briefing

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type stageCounter struct{ failCount, failVerify bool }

func (c stageCounter) Count(ctx context.Context, s, u string) (Budget, error) {
	time.Sleep(20 * time.Millisecond)
	if c.failCount {
		return Budget{}, errors.New("budget_helper_failed")
	}
	return testCounter{}.Count(ctx, s, u)
}
func (c stageCounter) Verify(context.Context, string, string) error {
	time.Sleep(40 * time.Millisecond)
	if c.failVerify {
		return errors.New("context_chat_render_mismatch")
	}
	return nil
}

func TestSeparateStageMetrics(t *testing.T) {
	for _, tc := range []struct {
		name           string
		counter        stageCounter
		wantGeneration bool
	}{
		{"success", stageCounter{}, true},
		{"count failure", stageCounter{failCount: true}, false},
		{"verification failure", stageCounter{failVerify: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				_ = json.NewEncoder(w).Encode(envelope(goodResult, "stop", true))
			}))
			defer srv.Close()
			_, m, err := NewOllama(srv.URL, "qwen3:8b", time.Second, tc.counter).Generate(context.Background(), sample())
			if m.BudgetHelper == nil || *m.BudgetHelper < 20 {
				t.Fatalf("helper duration: %v", m.BudgetHelper)
			}
			if tc.counter.failCount {
				if m.CompatibilityCheck != nil {
					t.Fatal("unexecuted verification must be null")
				}
			} else if m.CompatibilityCheck == nil || *m.CompatibilityCheck < 40 {
				t.Fatalf("verification duration: %v", m.CompatibilityCheck)
			}
			if tc.wantGeneration {
				if err != nil || calls.Load() != 1 || m.Total == nil || *m.Total != 1 {
					t.Fatal(err, calls.Load(), m)
				}
			} else if err == nil || calls.Load() != 0 || m.Total != nil {
				t.Fatal("failure must retain stage metrics without generating", err, calls.Load())
			}
		})
	}
}

func TestStageMetricsStoredWithJob(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(envelope(goodResult, "stop", true))
	}))
	defer srv.Close()
	s := store(t)
	in := sample()
	_ = in.Normalize()
	j, err := s.Enqueue(context.Background(), in, "metrics")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, s, NewOllama(srv.URL, "qwen3:8b", time.Second, stageCounter{})) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got, err := s.Get(context.Background(), in.CompanyID, j.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status == "succeeded" {
			m := got.Metrics
			if m.BudgetHelper == nil || m.CompatibilityCheck == nil || m.Processing == nil || m.Total == nil {
				t.Fatal("missing persisted stage metrics")
			}
			if *m.Processing < *m.BudgetHelper+*m.CompatibilityCheck {
				t.Fatal("processing must include measured stages")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("worker did not finish")
}

func TestStageMetricsAbsentVersusZero(t *testing.T) {
	var legacy Metrics
	if err := json.Unmarshal([]byte(`{"processing_ms":100}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.BudgetHelper != nil || legacy.CompatibilityCheck != nil {
		t.Fatal("older records must keep missing metrics null")
	}
	zero := int64(0)
	raw, err := json.Marshal(Metrics{BudgetHelper: &zero, CompatibilityCheck: &zero})
	if err != nil {
		t.Fatal(err)
	}
	var restored Metrics
	if err = json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.BudgetHelper == nil || *restored.BudgetHelper != 0 || restored.CompatibilityCheck == nil || *restored.CompatibilityCheck != 0 {
		t.Fatal("zero must remain a measured zero")
	}
}
