package evaluation

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
	"finkoper-morning-briefing/internal/diagnostic"
)

func TestDatasetAndAnnotations(t *testing.T) {
	for _, split := range []string{"development", "holdout"} {
		dataset, err := Load("../../evaluation", split)
		if err != nil {
			t.Fatal(err)
		}
		if len(dataset.Cases) != 10 {
			t.Fatal("split size")
		}
		for _, entry := range dataset.Cases {
			raw, err := os.ReadFile(filepath.Join("../../evaluation/annotations", split, entry.ID+".json"))
			if err != nil {
				t.Fatal(err)
			}
			var annotation struct {
				ID        string
				Synthetic bool
				Scenario  string
				Expected  []struct {
					Question  string
					Sources   []string `json:"required_source_ids"`
					Facts     []string `json:"essential_facts"`
					Statuses  []string `json:"allowed_statuses"`
					Deadline  *string  `json:"deadline_text"`
					Needs     *bool    `json:"needs_context"`
					Forbidden []string `json:"forbidden_conclusions"`
				} `json:"expected_topics"`
				Forbidden []string `json:"forbidden_conclusions"`
				Matching  string
			}
			if json.Unmarshal(raw, &annotation) != nil || annotation.ID != entry.ID || !annotation.Synthetic || annotation.Scenario == "" || annotation.Matching == "" {
				t.Fatal("annotation metadata", entry.ID)
			}
			all := map[string]bool{}
			current := map[string]bool{}
			for _, m := range entry.Input.Context {
				all[m.ID] = true
			}
			for _, m := range entry.Input.Messages {
				all[m.ID] = true
				current[m.ID] = true
			}
			for _, topic := range annotation.Expected {
				if topic.Question == "" || len(topic.Facts) == 0 || topic.Needs == nil || len(topic.Statuses) == 0 {
					t.Fatal("incomplete rubric", entry.ID)
				}
				periodSource := false
				for _, id := range topic.Sources {
					if !all[id] {
						t.Fatal("invalid annotation source", entry.ID, id)
					}
					periodSource = periodSource || current[id]
				}
				if !periodSource {
					t.Fatal("annotation without period source", entry.ID)
				}
				for _, status := range topic.Statuses {
					if !strings.Contains("|awaiting_staff|awaiting_client|resolved|unclear|", "|"+status+"|") {
						t.Fatal("status")
					}
					if status == "resolved" && topic.Deadline != nil {
						t.Fatal("resolved deadline")
					}
				}
				if len(topic.Forbidden)+len(annotation.Forbidden) == 0 {
					t.Fatal("missing forbidden conclusions", entry.ID)
				}
			}
			if len(annotation.Expected) == 0 && len(annotation.Forbidden) == 0 {
				t.Fatal("empty-case rubric")
			}
		}
	}
	if _, err := Load("../../evaluation", "all"); err == nil {
		t.Fatal("implicit holdout allowed")
	}
}

type counter struct {
	reserve, tokens, verifications int
	fail                           bool
}

func (c *counter) Count(_ context.Context, system, user string) (briefing.Budget, error) {
	if system != briefing.SystemPrompt || strings.Contains(user, "essential_facts") {
		return briefing.Budget{}, errors.New("rubric_or_prompt_changed")
	}
	return briefing.Budget{InputTokens: c.tokens, OutputReserve: c.reserve, ContextTokens: 8192, RequiredTokens: c.tokens + c.reserve, RemainingTokens: 8192 - c.tokens - c.reserve, Method: briefing.BudgetMethod}, nil
}
func (c *counter) Verify(context.Context, string, string) error {
	c.verifications++
	if c.fail {
		return errors.New("context_chat_render_mismatch")
	}
	return nil
}

func TestBatchSequentialBothModesNoReasoningOrLabels(t *testing.T) {
	dataset, err := Load("../../evaluation", "development")
	if err != nil {
		t.Fatal(err)
	}
	for _, thinking := range []bool{false, true} {
		t.Run(map[bool]string{false: "false", true: "true"}[thinking], func(t *testing.T) {
			reserve := 1500
			if thinking {
				reserve = 4096
			}
			count := &counter{reserve: reserve, tokens: 100}
			var calls, active, maxActive atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var p struct {
					Think    bool
					Debug    bool `json:"_debug_render_only"`
					Messages []struct{ Role, Content string }
					Options  map[string]float64
					Format   json.RawMessage
				}
				if json.NewDecoder(r.Body).Decode(&p) != nil || p.Think != thinking || p.Options["num_predict"] != float64(reserve) || p.Options["num_ctx"] != 8192 {
					t.Error("contract")
				}
				if p.Messages[0].Content != briefing.SystemPrompt || strings.Contains(p.Messages[1].Content, "expected_topics") {
					t.Error("label leaked")
				}
				if p.Debug {
					json.NewEncoder(w).Encode(map[string]any{"_debug_info": map[string]string{"rendered_template": diagnostic.ExpectedRender(p.Messages[0].Content, p.Messages[1].Content, thinking)}})
					return
				}
				n := active.Add(1)
				if n > maxActive.Load() {
					maxActive.Store(n)
				}
				defer active.Add(-1)
				calls.Add(1)
				time.Sleep(time.Millisecond)
				json.NewEncoder(w).Encode(map[string]any{"done": true, "done_reason": "stop", "message": map[string]string{"content": `{"topics":[]}`, "thinking": "PRIVATE_REASONING"}, "prompt_eval_count": 100, "eval_count": 10})
			}))
			defer srv.Close()
			runner := diagnostic.NewRunner(srv.URL, "qwen3:8b", time.Second, count)
			runner.Thinking = thinking
			var output bytes.Buffer
			if Run(context.Background(), dataset, runner, strings.Repeat("a", 64), "manifest-hash", true, &output) != nil {
				t.Fatal("batch failed")
			}
			if calls.Load() != 10 || maxActive.Load() != 1 || count.verifications != 10 {
				t.Fatal("not exactly ten sequential calls")
			}
			if strings.Contains(output.String(), "PRIVATE_REASONING") {
				t.Fatal("thinking leaked")
			}
			decoder := json.NewDecoder(&output)
			for i := 0; i < 10; i++ {
				var report Report
				if decoder.Decode(&report) != nil || report.ModelDigest == nil || report.SchemaVersion != "topics-v1" || report.ExampleID != dataset.Cases[i].ID || report.DatasetHash == "" || report.InputFileHash == "" || report.GenerationAttempts != 1 {
					t.Fatal("invalid report")
				}
			}
		})
	}
}

func TestPlanAndPreflightDoNotGenerate(t *testing.T) {
	dataset, _ := Load("../../evaluation", "development")
	for _, scenario := range []string{"plan", "budget", "verify"} {
		count := &counter{reserve: 1500, tokens: 100, fail: scenario == "verify"}
		if scenario == "budget" {
			count.tokens = 8000
		}
		runner := diagnostic.NewRunner("http://127.0.0.1:1", "qwen3:8b", time.Second, count)
		var output bytes.Buffer
		err := Run(context.Background(), dataset, runner, "digest", "hash", scenario != "plan", &output)
		if (err != nil) != (scenario != "plan") {
			t.Fatal("unexpected outcome")
		}
		if strings.Contains(output.String(), `"generation_attempts":1`) {
			t.Fatal("generation before preflight")
		}
		if scenario == "plan" && count.verifications != 0 {
			t.Fatal("network verification in plan")
		}
	}
}

func TestNoRetryOnIncompleteFinal(t *testing.T) {
	dataset, _ := Load("../../evaluation", "development")
	dataset.Cases = dataset.Cases[:1]
	count := &counter{reserve: 1500, tokens: 100}
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			Debug    bool `json:"_debug_render_only"`
			Messages []struct{ Content string }
		}
		json.NewDecoder(r.Body).Decode(&p)
		if p.Debug {
			json.NewEncoder(w).Encode(map[string]any{"_debug_info": map[string]string{"rendered_template": diagnostic.ExpectedRender(p.Messages[0].Content, p.Messages[1].Content)}})
			return
		}
		calls++
		json.NewEncoder(w).Encode(map[string]any{"done": true, "done_reason": "length", "message": map[string]string{"content": `{"topics":[]}`}})
	}))
	defer srv.Close()
	var output bytes.Buffer
	err := Run(context.Background(), dataset, diagnostic.NewRunner(srv.URL, "qwen3:8b", time.Second, count), "digest", "hash", true, &output)
	if err == nil || calls != 1 {
		t.Fatal("incomplete accepted or retried")
	}
	var report Report
	json.Unmarshal(output.Bytes(), &report)
	if string(report.Result) != "null" || report.Error == nil {
		t.Fatal("incomplete result published")
	}
}
