package diagnostic

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"finkoper-morning-briefing/internal/briefing"
)

type summaryCounter struct {
	spyCounter
	tokens int
}

func (c *summaryCounter) Count(ctx context.Context, s, u string) (briefing.Budget, error) {
	c.spyCounter.Count(ctx, s, u)
	return briefing.Budget{InputTokens: c.tokens, OutputReserve: 1000, ContextTokens: 8192, RequiredTokens: c.tokens + 1000, RemainingTokens: 7192 - c.tokens, Method: briefing.BudgetMethod}, nil
}

func TestSummaryValidation(t *testing.T) {
	in := example(t)
	id := in.Messages[0].ID
	contextID := in.Context[0].ID
	good := func(ids []string) []byte {
		raw, _ := json.Marshal(map[string]any{"summary": "Сводка обращения.", "source_message_ids": ids})
		return raw
	}
	for _, raw := range [][]byte{[]byte(`{}`), []byte(`{"summary":"x"}`), []byte(`{"summary":null,"source_message_ids":[]}`), []byte(`{"summary":" ","source_message_ids":[]}`), []byte(`{"summary":1,"source_message_ids":[]}`), []byte(`{"summary":"x","source_message_ids":null}`), []byte(`{"summary":"x","source_message_ids":[1]}`), []byte(`{"summary":"x","source_message_ids":[],"topics":[]}`), []byte(`{"topics":[]}`), good([]string{}), good([]string{"missing"}), good([]string{id, id}), good([]string{contextID}), append(good([]string{id}), []byte(` {}`)...)} {
		if _, err := ValidateSummary(raw, in); err == nil {
			t.Fatalf("accepted invalid structure/source: %s", raw)
		}
	}
	if _, err := ValidateSummary(good([]string{id, contextID}), in); err != nil {
		t.Fatal(err)
	}
	wrongCase, _ := json.Marshal(map[string]any{"Summary": "text", "source_message_ids": []string{id}})
	if _, err := ValidateSummary(wrongCase, in); err == nil {
		t.Fatal("wrong field case accepted")
	}
	// No topic/count classifier: a no-requests summary can cite a greeting as evidence.
	in.Messages = in.Messages[:1]
	in.Messages[0].Text = "Добрый день!"
	in.Context = nil
	raw, _ := json.Marshal(map[string]any{"summary": "Новых просьб нет.", "source_message_ids": []string{id}})
	if _, err := ValidateSummary(raw, in); err != nil {
		t.Fatal(err)
	}
}

func TestSummaryTransportAndIncompleteResponse(t *testing.T) {
	in := example(t)
	for _, mode := range []string{"ok", "length", "not_done", "invalid", "missing_render", "wrong_render"} {
		t.Run(mode, func(t *testing.T) {
			counter := &summaryCounter{tokens: 100}
			calls := 0
			renders := 0
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
				if p.Model != "qwen3:8b" || p.Think || p.Stream || p.Shift || p.Truncate || p.Keep != "30m" || p.Options["temperature"] != 0 || p.Options["num_predict"] != 1000 || p.Options["num_ctx"] != 8192 || string(p.Format) != SummarySchema {
					t.Error("summary contract")
				}
				if len(p.Messages) != 2 || p.Messages[0].Content != SummaryPrompt || p.Messages[1].Content != counter.user || counter.system != SummaryPrompt {
					t.Error("different counting/generation prompt")
				}
				if p.Debug {
					renders++
					if mode == "missing_render" {
						json.NewEncoder(w).Encode(map[string]any{})
						return
					}
					rendered := ExpectedRender(counter.system, counter.user)
					if mode == "wrong_render" {
						rendered += "x"
					}
					json.NewEncoder(w).Encode(map[string]any{"_debug_info": map[string]string{"rendered_template": rendered}})
					return
				}
				calls++
				done := mode != "not_done"
				reason := "stop"
				if mode == "length" {
					reason = "length"
				}
				content, _ := json.Marshal(map[string]any{"summary": "Клиент обратился с просьбой.", "source_message_ids": []string{in.Messages[0].ID}})
				if mode == "invalid" {
					content = []byte(`{"topics":[]}`)
				}
				json.NewEncoder(w).Encode(map[string]any{"done": done, "done_reason": reason, "message": map[string]string{"content": string(content), "thinking": "PRIVATE_REASONING"}, "prompt_eval_count": 100, "eval_count": 30})
			}))
			defer srv.Close()
			record := NewSummaryRunner(srv.URL, counter).Summarize(context.Background(), "input", in, true)
			raw, _ := json.Marshal(record)
			if strings.Contains(string(raw), "PRIVATE_REASONING") {
				t.Fatal("reasoning leaked")
			}
			if mode == "missing_render" || mode == "wrong_render" {
				if calls != 0 || record.Error == nil {
					t.Fatal("render bypass")
				}
				return
			}
			if calls != 1 || renders != 1 || counter.verifies != 1 {
				t.Fatal("unexpected generation count")
			}
			if mode == "ok" {
				if Failed(record) || record.Result == nil || record.PromptVersion != SummaryPromptVersion {
					t.Fatal(record)
				}
			} else if !Failed(record) || record.Result != nil {
				t.Fatal("invalid summary accepted")
			}
		})
	}
}

func TestSummaryOfflineAndBudget(t *testing.T) {
	for _, tokens := range []int{100, 7500} {
		counter := &summaryCounter{tokens: tokens}
		out := NewSummaryRunner("http://127.0.0.1:1", counter).Summarize(context.Background(), "input", example(t), false)
		if counter.verifies != 0 || out.GenerationAttempts != 0 || out.Budget.Verified {
			t.Fatal("network in plan")
		}
		if (out.Error != nil) != (tokens == 7500) {
			t.Fatal("budget not enforced")
		}
	}
}
