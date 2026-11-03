package evaluation

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"finkoper-morning-briefing/internal/briefing"
	"finkoper-morning-briefing/internal/diagnostic"
)

type comparisonCounter struct {
	system, user     string
	verifies, tokens int
}

func (c *comparisonCounter) Count(_ context.Context, s, u string) (briefing.Budget, error) {
	c.system = s
	c.user = u
	return briefing.Budget{InputTokens: c.tokens, OutputReserve: 1000, ContextTokens: 8192, RequiredTokens: c.tokens + 1000, RemainingTokens: 7192 - c.tokens, Method: briefing.BudgetMethod}, nil
}
func (c *comparisonCounter) Verify(_ context.Context, s, u string) error {
	c.verifies++
	if c.system != s || c.user != u {
		panic("counter input changed")
	}
	return nil
}

func TestSummarySelectionsAndFrozenV2(t *testing.T) {
	if diagnostic.Hash(diagnostic.SummaryPromptV2) != "8cc6dc4d3fad8eba8da327ecf9837aaa0213c6f978678e65c4ad105464d8ff31" {
		t.Fatal("v2 baseline changed")
	}
	if diagnostic.Hash(diagnostic.SummaryPrompt) == diagnostic.Hash(diagnostic.SummaryPromptV2) {
		t.Fatal("v3 identical to v2")
	}
	for set, count := range map[string]int{"regressions": 18, "new-regressions": 8, "previous-holdout": 10, "development": 10, "examples": 2} {
		selected, err := LoadSummaryComparison("../..", set)
		if err != nil || len(selected.Inputs) != count || selected.Hash == "" {
			t.Fatal(set, err)
		}
	}
	if _, err := LoadSummaryComparison("../..", "holdout"); err == nil {
		t.Fatal("independent holdout label must not be accepted by comparison")
	}
}

func TestSummaryPairTransportSameInputsAndParameters(t *testing.T) {
	selection, err := LoadSummaryComparison("../..", "new-regressions")
	if err != nil {
		t.Fatal(err)
	}
	selection.Inputs = selection.Inputs[:2]
	counter := &comparisonCounter{tokens: 100}
	var calls, active, maxActive atomic.Int32
	users := []string{}
	parameters := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			Model                          string
			Think, Stream, Shift, Truncate bool
			Keep                           string `json:"keep_alive"`
			Debug                          bool   `json:"_debug_render_only"`
			Options                        map[string]float64
			Format                         json.RawMessage
			Messages                       []struct{ Role, Content string }
		}
		json.NewDecoder(r.Body).Decode(&p)
		if p.Model != "qwen3:8b" || p.Think || p.Stream || p.Shift || p.Truncate || p.Keep != "30m" || p.Options["num_ctx"] != 8192 || p.Options["num_predict"] != 1000 || p.Options["temperature"] != 0 || string(p.Format) != diagnostic.SummarySchema {
			t.Error("parameters changed")
		}
		if p.Messages[0].Content != counter.system || p.Messages[1].Content != counter.user || strings.Contains(counter.user, "manual_checks") {
			t.Error("different counter inputs or manual labels leaked")
		}
		if p.Debug {
			json.NewEncoder(w).Encode(map[string]any{"_debug_info": map[string]string{"rendered_template": diagnostic.ExpectedRender(counter.system, counter.user)}})
			return
		}
		n := int(calls.Add(1))
		expected := diagnostic.SummaryVariants()[(n-1)%2]
		if p.Messages[0].Content != expected.Prompt {
			t.Error("wrong prompt order")
		}
		users = append(users, p.Messages[1].Content)
		raw, _ := json.Marshal(p.Options)
		parameters = append(parameters, string(raw))
		a := active.Add(1)
		if a > maxActive.Load() {
			maxActive.Store(a)
		}
		defer active.Add(-1)
		time.Sleep(time.Millisecond)
		content, _ := json.Marshal(map[string]any{"summary": "Контролируемый ответ mock, не оценка смысла.", "source_message_ids": []string{selection.Inputs[(n-1)/2].Input.Messages[0].ID}})
		json.NewEncoder(w).Encode(map[string]any{"done": true, "done_reason": "stop", "message": map[string]string{"content": string(content), "thinking": "PRIVATE_REASONING"}, "prompt_eval_count": 100, "eval_count": 10})
	}))
	defer srv.Close()
	var output bytes.Buffer
	if err := CompareSummaries(context.Background(), selection, diagnostic.NewSummaryRunner(srv.URL, counter), "digest", "manifest", true, &output); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 4 || counter.verifies != 4 || maxActive.Load() != 1 {
		t.Fatal("not one sequential generation per version and input")
	}
	if users[0] != users[1] || users[2] != users[3] || parameters[0] != parameters[1] || strings.Contains(output.String(), "PRIVATE_REASONING") {
		t.Fatal("pair inputs/parameters/privacy")
	}
	decoder := json.NewDecoder(&output)
	for i := 0; i < 4; i++ {
		var record SummaryComparisonRecord
		if decoder.Decode(&record) != nil || record.PromptVersion != diagnostic.SummaryVariants()[i%2].PromptVersion || record.PairIndex != i/2+1 || record.GenerationAttempts != 1 || record.Result == nil || record.Metrics.InputTokens == nil || record.AssessmentRole != "regression" {
			t.Fatal("comparison report")
		}
	}
}

func TestSummaryComparisonOfflineAndBudget(t *testing.T) {
	selection, _ := LoadSummaryComparison("../..", "new-regressions")
	for _, tokens := range []int{100, 7500} {
		counter := &comparisonCounter{tokens: tokens}
		var output bytes.Buffer
		err := CompareSummaries(context.Background(), selection, diagnostic.NewSummaryRunner("http://127.0.0.1:1", counter), "digest", "manifest", false, &output)
		if (err != nil) != (tokens == 7500) || counter.verifies != 0 || strings.Contains(output.String(), `"generation_attempts":1`) {
			t.Fatal("offline/budget guard")
		}
	}
}
