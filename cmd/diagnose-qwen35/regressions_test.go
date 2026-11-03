package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"finkoper-morning-briefing/internal/briefing"
	"finkoper-morning-briefing/internal/diagnostic"
	"finkoper-morning-briefing/internal/evaluation"
)

var expectedRegressionIDs = []string{"sr-01", "sr-02", "sr-03", "sr-04", "sr-05", "sr-06", "sr-07", "sr-08", "hol-01", "hol-02", "hol-03", "hol-04", "hol-05", "hol-06", "hol-07", "hol-08", "hol-09", "hol-10"}

func TestRegressionSelection(t *testing.T) {
	ids, err := selectCaseIDs("thinking-long", "regressions")
	if err != nil || !reflect.DeepEqual(ids, expectedRegressionIDs) {
		t.Fatal("selection", ids, err)
	}
	seen := map[string]bool{}
	for _, id := range ids {
		path := casePaths[id]
		if seen[path] || !strings.Contains(path, "/inputs/") || !strings.HasSuffix(path, id+".json") {
			t.Fatal("duplicate or non-input file", path)
		}
		seen[path] = true
		single, err := selectCaseIDs("thinking-long", id)
		if err != nil || !reflect.DeepEqual(single, []string{id}) {
			t.Fatal("single case", id)
		}
	}
	for _, mode := range []string{"baseline", "greedy", "thinking", "all"} {
		if _, err := selectCaseIDs(mode, "regressions"); err == nil {
			t.Fatal("54-generation selection accepted")
		}
		factory := func(string, string) (*diagnostic.Runner, error) {
			t.Fatal("invalid selection reached factory")
			return nil, nil
		}
		if err := runWithRunner([]string{"--profile", diagnostic.Qwen35Profile, "--mode", mode, "--case", "regressions", "--run"}, &bytes.Buffer{}, factory); err == nil {
			t.Fatal("CLI accepted expanded legacy scope")
		}
		if _, err := selectCaseIDs(mode, "sr-02"); err == nil {
			t.Fatal("legacy scope expanded")
		}
		old, err := selectCaseIDs(mode, "six")
		if err != nil || !reflect.DeepEqual(old, []string{"sr-01", "sr-04", "sr-06", "sr-07", "hol-05", "hol-09"}) {
			t.Fatal("six changed")
		}
	}
}

type regressionCounter struct {
	scenario, affected, current string
	counts, verifications       map[string]int
}

func (c *regressionCounter) Count(_ context.Context, system, user string) (briefing.Budget, error) {
	c.current = user
	c.counts[user]++
	if user == c.affected && c.scenario == "checksum" {
		return briefing.Budget{}, errors.New("qwen35_resource_checksum_mismatch")
	}
	tokens := 100
	if user == c.affected && c.scenario == "budget" {
		tokens = 2049
	}
	return briefing.Budget{InputTokens: tokens, OutputReserve: 6144, ContextTokens: 8192, RequiredTokens: tokens + 6144, RemainingTokens: 2048 - tokens, Method: diagnostic.Qwen35BudgetMethod}, nil
}
func (c *regressionCounter) Verify(context.Context, string, string) error {
	c.verifications[c.current]++
	if c.current == c.affected && c.scenario == "digest" {
		return errors.New("qwen35_model_digest_mismatch")
	}
	return nil
}

func TestThinkingLongRegressionHTTP(t *testing.T) {
	for _, scenario := range []string{"plan", "ok", "budget", "generation_error", "length", "empty", "renderer", "runtime_count", "checksum", "digest"} {
		t.Run(scenario, func(t *testing.T) {
			root, _ := filepath.Abs("../..")
			// A checkout containing only input JSONs: no annotations, dataset manifest or labels.
			inputOnly := t.TempDir()
			paths := []string{}
			for _, id := range expectedRegressionIDs {
				path := casePaths[id]
				raw, err := os.ReadFile(filepath.Join(root, path))
				if err != nil {
					t.Fatal(err)
				}
				target := filepath.Join(inputOnly, path)
				if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(target, raw, 0600); err != nil {
					t.Fatal(err)
				}
				paths = append(paths, path)
			}
			t.Chdir(inputOnly)
			inputs, err := evaluation.LoadFiles(paths)
			if err != nil {
				t.Fatal(err)
			}
			expectedUsers := map[string]string{}
			users := []string{}
			for i, input := range inputs {
				_, user := briefing.PromptInput(input.Input)
				expectedUsers[user] = expectedRegressionIDs[i]
				users = append(users, user)
			}
			if len(expectedUsers) != 18 {
				t.Fatal("nonunique inputs")
			}
			counter := &regressionCounter{scenario: scenario, affected: users[1], counts: map[string]int{}, verifications: map[string]int{}}
			var active, maximum atomic.Int32
			order := []string{}
			httpRequests := map[string]int{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/chat" {
					w.WriteHeader(501)
					return
				}
				var p struct {
					Model                          string
					Think, Stream, Shift, Truncate bool
					Debug                          bool `json:"_debug_render_only"`
					Options                        map[string]float64
					Format                         json.RawMessage
					Messages                       []struct{ Role, Content string }
				}
				if json.NewDecoder(r.Body).Decode(&p) != nil || len(p.Messages) != 2 {
					t.Error("decode")
					return
				}
				user := p.Messages[1].Content
				id, ok := expectedUsers[user]
				if !ok {
					t.Error("input changed or annotated")
					w.WriteHeader(400)
					return
				}
				httpRequests[user]++
				if p.Messages[0].Content != diagnostic.SummaryPromptV2 || string(p.Format) != diagnostic.SummarySchema || p.Model != diagnostic.Qwen35Model || !p.Think || p.Stream || p.Shift || p.Truncate || !reflect.DeepEqual(p.Options, map[string]float64{"num_ctx": 8192, "num_predict": 6144, "temperature": 1, "top_p": .95, "top_k": 20, "min_p": 0, "presence_penalty": 1.5, "repeat_penalty": 1}) {
					t.Error("config changed")
				}
				if p.Debug {
					thinking := !(scenario == "renderer" && user == counter.affected)
					json.NewEncoder(w).Encode(map[string]any{"_debug_info": map[string]string{"rendered_template": diagnostic.Qwen35Render(p.Messages[0].Content, user, thinking)}})
					return
				}
				n := active.Add(1)
				defer active.Add(-1)
				if n > maximum.Load() {
					maximum.Store(n)
				}
				order = append(order, id)
				if scenario == "generation_error" && user == counter.affected {
					w.WriteHeader(500)
					return
				}
				var input struct{ Messages []struct{ ID string } }
				json.Unmarshal([]byte(user), &input)
				content, _ := json.Marshal(map[string]any{"summary": "Контролируемый ответ mock.", "source_message_ids": []string{input.Messages[0].ID}})
				reason, count := "stop", 100
				if user == counter.affected {
					if scenario == "length" {
						reason = "length"
						content = nil
					}
					if scenario == "empty" {
						content = nil
					}
					if scenario == "runtime_count" {
						count++
					}
				}
				json.NewEncoder(w).Encode(map[string]any{"done": true, "done_reason": reason, "prompt_eval_count": count, "eval_count": 6144, "message": map[string]string{"content": string(content), "thinking": "REGRESSION_PRIVATE_THINKING"}})
			}))
			defer srv.Close()
			factory := func(_ string, mode string) (*diagnostic.Runner, error) {
				return diagnostic.NewQwen35RunnerMode(srv.URL, counter, mode)
			}
			args := []string{"--profile", diagnostic.Qwen35Profile, "--mode", "thinking-long", "--case", "regressions"}
			if scenario != "plan" {
				args = append(args, "--run")
			}
			var output bytes.Buffer
			err = runWithRunner(args, &output, factory)
			if (err == nil) != (scenario == "plan" || scenario == "ok") {
				t.Fatal("exit status", err)
			}
			lines := strings.Split(strings.TrimSpace(output.String()), "\n")
			stops := scenario == "renderer" || scenario == "runtime_count" || scenario == "checksum" || scenario == "digest"
			expectedRecords := 18
			if stops {
				expectedRecords = 2
			}
			if len(lines) != expectedRecords || maximum.Load() > 1 || strings.Contains(output.String(), "REGRESSION_PRIVATE_THINKING") {
				t.Fatal("records/concurrency/privacy", len(lines), maximum.Load())
			}
			expectedOrder := append([]string(nil), expectedRegressionIDs...)
			if scenario == "plan" {
				expectedOrder = []string{}
			}
			if scenario == "budget" {
				expectedOrder = append(expectedOrder[:1], expectedOrder[2:]...)
			}
			if stops {
				expectedOrder = expectedRegressionIDs[:1]
				if scenario == "runtime_count" {
					expectedOrder = expectedRegressionIDs[:2]
				}
			}
			if !reflect.DeepEqual(order, expectedOrder) {
				t.Fatal("sequence/retry", order)
			}
			for i, line := range lines {
				var entry report
				if json.Unmarshal([]byte(line), &entry) != nil {
					t.Fatal("report")
				}
				if entry.CaseID != expectedRegressionIDs[i] || entry.GenerationMode != "thinking-long" || entry.AssessmentRole != "regression" || entry.CallTimeoutMS != 600000 || entry.Budget.OutputReserve != 6144 && scenario != "checksum" {
					t.Fatal("record metadata")
				}
				if counter.counts[users[i]] != 1 {
					t.Fatal("input counted more than once")
				}
				if scenario == "budget" && i == 1 && (entry.Error == nil || *entry.Error != "input_budget_exceeded" || entry.GenerationAttempts != 0 || httpRequests[users[i]] != 0 || counter.verifications[users[i]] != 0) {
					t.Fatal("budget refusal reached HTTP")
				}
				if i == 1 && scenario != "ok" && scenario != "plan" && (!diagnostic.Failed(entry.Record) || string(entry.Result) != "null") {
					t.Fatal("failure accepted")
				}
			}
			if stops && counter.counts[users[2]] != 0 {
				t.Fatal("continued after incompatibility")
			}
		})
	}
}
