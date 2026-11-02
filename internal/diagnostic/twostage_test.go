package diagnostic

import (
	"bytes"
	"context"
	"encoding/json"
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

// Fixtures assert wiring and reference validation; they are not semantic model outputs.
func groupFixture(in briefing.Input) string {
	group := Grouping{Issues: []Issue{{MessageIDs: []string{in.Messages[0].ID}, ContextIDs: []string{}, Request: "Проверить вопрос", StaffIDs: []string{}, ConfirmationIDs: []string{}, Missing: []string{}}}}
	raw, _ := json.Marshal(group)
	return string(raw)
}
func topicFixture(in briefing.Input) string {
	raw, _ := json.Marshal(briefing.Result{Topics: []briefing.Topic{{Text: "Запрошена проверка вопроса", Status: "awaiting_staff", Sources: []string{in.Messages[0].ID}}}})
	return string(raw)
}

func TestGroupingValidation(t *testing.T) {
	if !json.Valid([]byte(GroupingSchema)) {
		t.Fatal("invalid grouping schema")
	}
	in := example(t)
	valid := groupFixture(in)
	for _, tc := range []struct {
		name, raw string
		ok        bool
	}{
		{"valid", valid, true}, {"empty", `{"issues":[]}`, true}, {"missing", `{"issues":[{}]}`, false}, {"null", `{"issues":null}`, false},
		{"extra", strings.Replace(valid, `"request":`, `"extra":1,"request":`, 1), false},
		{"wrong source", strings.ReplaceAll(valid, in.Messages[0].ID, "unknown"), false},
		{"wrong type", strings.Replace(valid, `"missing_information":[]`, `"missing_information":false`, 1), false},
		{"missing list", strings.Replace(valid, `"staff_reply_ids":[]`, `"staff_reply_ids":null`, 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateGrouping([]byte(tc.raw), in); (err == nil) != tc.ok {
				t.Fatal(err)
			}
		})
	}
	badRole := Grouping{Issues: []Issue{{MessageIDs: []string{in.Messages[0].ID}, ContextIDs: []string{}, Request: "x", StaffIDs: []string{in.Messages[0].ID}, ConfirmationIDs: []string{}, Missing: []string{}}}}
	raw, _ := json.Marshal(badRole)
	if err := ValidateGrouping(raw, in); err == nil || err.Error() != "invalid_grouping_roles" {
		t.Fatal(err)
	}
	badRole.Issues[0].StaffIDs = []string{}
	id := "unknown"
	badRole.Issues[0].DeadlineID = &id
	raw, _ = json.Marshal(badRole)
	if err := ValidateGrouping(raw, in); err == nil || err.Error() != "invalid_grouping_sources" {
		t.Fatal(err)
	}
	// Context must stay in its own source scope; it cannot be the only period source.
	badRole.Issues[0].DeadlineID = nil
	badRole.Issues[0].MessageIDs = []string{in.Context[0].ID}
	raw, _ = json.Marshal(badRole)
	if ValidateGrouping(raw, in) == nil {
		t.Fatal("context masqueraded as period message")
	}
}

func TestStageSchemaMarshalFailureDoesNotReachNetwork(t *testing.T) {
	counter := &spyCounter{}
	v := Variant{"GROUP", GroupingPrompt, "test", json.RawMessage(`{"broken"`)}
	record := NewRunner("http://127.0.0.1:1", "qwen3:8b", time.Second, counter).execute(context.Background(), "input", v, "user", true, func([]byte) error { return nil })
	if record.Error == nil || *record.Error != "diagnostic_format_invalid" || counter.counts != 0 || record.GenerationAttempts != 0 {
		t.Fatal(record)
	}
}

func TestTwoFixedGenerationsAndOriginalsInFinal(t *testing.T) {
	for _, file := range []string{"04-exact-20.json", "02.json"} {
		t.Run(file, func(t *testing.T) {
			raw, err := os.ReadFile("../../examples/" + file)
			if err != nil {
				t.Fatal(err)
			}
			var in briefing.Input
			_ = json.Unmarshal(raw, &in)
			if in.Normalize() != nil {
				t.Fatal("invalid input")
			}
			// Two groups, including a repeated source; still only one call per whole stage.
			var grouping Grouping
			_ = json.Unmarshal([]byte(groupFixture(in)), &grouping)
			grouping.Issues = append(grouping.Issues, grouping.Issues[0])
			groupRaw, _ := json.Marshal(grouping)
			_, original := briefing.PromptInput(in)
			counter := &spyCounter{}
			var generations, renders atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var p struct {
					Debug                  bool                `json:"_debug_render_only"`
					Messages               []map[string]string `json:"messages"`
					Format                 json.RawMessage     `json:"format"`
					Think, Shift, Truncate bool
					Options                map[string]int `json:"options"`
				}
				if json.NewDecoder(r.Body).Decode(&p) != nil || len(p.Messages) != 2 || p.Think || p.Shift || p.Truncate || p.Options["num_ctx"] != 8192 || p.Options["num_predict"] != 1500 {
					t.Error("wrong parameters")
					w.WriteHeader(400)
					return
				}
				s, u := p.Messages[0]["content"], p.Messages[1]["content"]
				if counter.system != s || counter.user != u {
					t.Error("helper prompt differs")
				}
				if p.Debug {
					renders.Add(1)
					_ = json.NewEncoder(w).Encode(map[string]any{"_debug_info": map[string]string{"rendered_template": ExpectedRender(s, u)}})
					return
				}
				n := generations.Add(1)
				content := string(groupRaw)
				if n == 1 {
					if s != GroupingPrompt || u != original || string(p.Format) != GroupingSchema {
						t.Error("bad grouping request")
					}
				} else if n == 2 {
					if s != FinalPrompt || string(p.Format) != briefing.OutputSchema {
						t.Error("bad final contract")
					}
					var final struct {
						Original  json.RawMessage   `json:"original_input"`
						Candidate GroupingCandidate `json:"grouping_candidate"`
					}
					if json.Unmarshal([]byte(u), &final) != nil || !bytes.Equal(final.Original, []byte(original)) || !bytes.Equal(final.Candidate.Output, groupRaw) || final.Candidate.Trusted {
						t.Error("originals/grouping lost or elevated to truth")
					}
					content = topicFixture(in)
				} else {
					t.Error("extra generation")
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"done": true, "done_reason": "stop", "message": map[string]string{"content": content}, "prompt_eval_count": 100, "eval_count": 20, "total_duration": 1000000})
			}))
			defer srv.Close()
			report := NewRunner(srv.URL, "qwen3:8b", time.Second, counter).TwoStage(context.Background(), file, in, true)
			if report.Error != nil || report.Final == nil || report.GenerationAttempts != 2 || generations.Load() != 2 || renders.Load() != 2 || counter.counts != 2 || counter.verifies != 2 {
				t.Fatalf("bad fixed pipeline: %+v", report)
			}
			if report.TotalInputTokens == nil || *report.TotalInputTokens != 200 || report.TotalOutputTokens == nil || *report.TotalOutputTokens != 40 || report.TotalOllamaMS == nil || *report.TotalOllamaMS != 2 {
				t.Fatal("incorrect totals")
			}
		})
	}
}

func TestBadGroupingIsUntrustedInputNotARepairLoop(t *testing.T) {
	for _, candidate := range []string{`{"issues":[{}]}`, `not JSON`, strings.ReplaceAll(groupFixture(example(t)), example(t).Messages[0].ID, "unknown"), `{"issues":[]}`} {
		t.Run(candidate, func(t *testing.T) {
			in := example(t)
			counter := &spyCounter{}
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var p struct {
					Debug    bool                `json:"_debug_render_only"`
					Messages []map[string]string `json:"messages"`
				}
				_ = json.NewDecoder(r.Body).Decode(&p)
				if p.Debug {
					_ = json.NewEncoder(w).Encode(map[string]any{"_debug_info": map[string]string{"rendered_template": ExpectedRender(counter.system, counter.user)}})
					return
				}
				n := calls.Add(1)
				content := candidate
				if n == 2 {
					var input struct {
						Original  json.RawMessage   `json:"original_input"`
						Candidate GroupingCandidate `json:"grouping_candidate"`
					}
					_ = json.Unmarshal([]byte(p.Messages[1]["content"]), &input)
					expectedErrors := 1
					if candidate == `{"issues":[]}` {
						expectedErrors = 0
					}
					if input.Candidate.Trusted || len(input.Candidate.StructuralErrors) != expectedErrors || len(input.Original) == 0 {
						t.Error("bad grouping was trusted or hidden")
					}
					if json.Valid([]byte(candidate)) {
						if string(input.Candidate.Output) != candidate {
							t.Error("grouping silently repaired")
						}
					} else if input.Candidate.InvalidContent == nil || *input.Candidate.InvalidContent != candidate {
						t.Error("invalid grouping hidden")
					}
					content = topicFixture(in)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"done": true, "done_reason": "stop", "message": map[string]string{"content": content}})
			}))
			defer srv.Close()
			report := NewRunner(srv.URL, "qwen3:8b", time.Second, counter).TwoStage(context.Background(), "input", in, true)
			badStructure := candidate != `{"issues":[]}`
			if calls.Load() != 2 || report.GenerationAttempts != 2 || (report.Grouping.StructuralError != nil) != badStructure || report.Final == nil || Failed(*report.Final) || (report.Error != nil) != badStructure {
				t.Fatal("expected visible bad grouping and independent final stage", report)
			}
		})
	}
}

type finalOverflowCounter struct{ spyCounter }

func (c *finalOverflowCounter) Count(ctx context.Context, s, u string) (briefing.Budget, error) {
	b, err := c.spyCounter.Count(ctx, s, u)
	if s == FinalPrompt {
		b.InputTokens = 7000
		b.RequiredTokens = 8500
		b.RemainingTokens = -308
	}
	return b, err
}
func TestSecondStageBudgetFailureAndIncompleteGroupingStop(t *testing.T) {
	for _, mode := range []string{"overflow", "incomplete", "transport"} {
		t.Run(mode, func(t *testing.T) {
			in := example(t)
			counter := &finalOverflowCounter{}
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var p struct {
					Debug bool `json:"_debug_render_only"`
				}
				_ = json.NewDecoder(r.Body).Decode(&p)
				if p.Debug {
					_ = json.NewEncoder(w).Encode(map[string]any{"_debug_info": map[string]string{"rendered_template": ExpectedRender(counter.system, counter.user)}})
					return
				}
				calls.Add(1)
				if mode == "transport" {
					w.WriteHeader(503)
					return
				}
				reason := "stop"
				if mode == "incomplete" {
					reason = "length"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"done": true, "done_reason": reason, "message": map[string]string{"content": groupFixture(in)}})
			}))
			defer srv.Close()
			report := NewRunner(srv.URL, "qwen3:8b", time.Second, counter).TwoStage(context.Background(), "input", in, true)
			if report.Error == nil || report.FinalSkipped == nil || calls.Load() != 1 || report.GenerationAttempts != 1 {
				t.Fatal("should stop without retry", report)
			}
			if mode == "overflow" && (report.Final == nil || report.Final.Error == nil || *report.Final.Error != "input_budget_exceeded") {
				t.Fatal(report)
			}
			if mode != "overflow" && report.Final != nil {
				t.Fatal("incomplete/failed stage 1 must not trigger final")
			}
		})
	}
}

func TestTwoStagePlanIsOffline(t *testing.T) {
	counter := &spyCounter{}
	r := NewRunner("http://127.0.0.1:1", "qwen3:8b", time.Second, counter)
	report := r.TwoStage(context.Background(), "input", example(t), false)
	if report.Error != nil || report.Final != nil || report.FinalSkipped == nil || report.GenerationAttempts != 0 || counter.verifies != 0 || counter.counts != 1 {
		t.Fatal(report)
	}
}

type groupingOverflowCounter struct{ spyCounter }

func (c *groupingOverflowCounter) Count(context.Context, string, string) (briefing.Budget, error) {
	return briefing.Budget{InputTokens: 7000, OutputReserve: 1500, ContextTokens: 8192, RequiredTokens: 8500, RemainingTokens: -308, Method: briefing.BudgetMethod}, nil
}
func TestFirstStageBudgetFailureNeverGenerates(t *testing.T) {
	counter := &groupingOverflowCounter{}
	report := NewRunner("http://127.0.0.1:1", "qwen3:8b", time.Second, counter).TwoStage(context.Background(), "input", example(t), true)
	if report.GenerationAttempts != 0 || report.Final != nil || report.Grouping.Error == nil || *report.Grouping.Error != "input_budget_exceeded" {
		t.Fatal(report)
	}
}

func TestTwoStageActualHelperLocalSimulation(t *testing.T) {
	python, base := os.Getenv("BRIEFING_TEST_BUDGET_PYTHON"), os.Getenv("BRIEFING_TEST_SIMULATION_URL")
	if python == "" || base == "" {
		t.Skip("opt-in local Python + mock")
	}
	if !strings.HasPrefix(base, "http://127.0.0.1:") {
		t.Fatal("local mock only")
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
	for _, file := range []string{"04-exact-20.json", "02.json"} {
		raw, err := os.ReadFile("../../examples/" + file)
		if err != nil {
			t.Fatal(err)
		}
		var in briefing.Input
		_ = json.Unmarshal(raw, &in)
		if err = in.Normalize(); err != nil {
			t.Fatal(err)
		}
		report := NewRunner(base, "qwen3:8b", 5*time.Second, counter).TwoStage(context.Background(), file, in, true)
		if report.Error != nil || report.Final == nil || report.GenerationAttempts != 2 {
			t.Fatal(report)
		}
		t.Logf("%s: grouping=%d final=%d generations=%d", file, report.Grouping.Budget.InputTokens, report.Final.Budget.InputTokens, report.GenerationAttempts)
	}
}

func TestFinalBudgetWithActualTokenizerRejectsWholeAugmentedInput(t *testing.T) {
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
	in := example(t)
	var candidate Grouping
	_ = json.Unmarshal([]byte(groupFixture(in)), &candidate)
	candidate.Issues[0].Request = strings.Repeat("a ", 8000)
	raw, _ := json.Marshal(candidate)
	augmented := FinalUser(in, Record{Result: raw})
	record := NewRunner("http://127.0.0.1:1", "qwen3:8b", time.Second, counter).execute(context.Background(), "input", Variant{"FINAL", FinalPrompt, "diagnostic-final-v1", json.RawMessage(briefing.OutputSchema)}, augmented, true, func(raw []byte) error { _, err := briefing.ValidateResult(raw, in); return err })
	if record.Error == nil || *record.Error != "input_budget_exceeded" || record.GenerationAttempts != 0 || record.Budget.InputTokens+1500 <= 8192 || record.Metrics.CompatibilityCheck != nil {
		t.Fatal("final input must be rejected before any network request", record)
	}
}
