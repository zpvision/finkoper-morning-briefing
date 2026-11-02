package briefing

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
	"time"
)

const PromptVersion = "morning-v3"
const SchemaVersion = "topics-v1"
const MaxBody = 1 << 20
const QueueLimit = 100

type Message struct {
	ID             string    `json:"id"`
	ConversationID string    `json:"conversation_id"`
	Channel        string    `json:"channel"`
	SentAt         time.Time `json:"sent_at"`
	SenderRole     string    `json:"sender_role"`
	Text           string    `json:"text"`
}
type Period struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}
type Input struct {
	CompanyID string    `json:"company_id"`
	ClientID  string    `json:"client_id"`
	Period    Period    `json:"period"`
	Context   []Message `json:"context_messages"`
	Messages  []Message `json:"messages"`
}
type Topic struct {
	Text         string   `json:"text"`
	Status       string   `json:"status"`
	Deadline     *string  `json:"deadline_text"`
	NeedsContext bool     `json:"needs_context"`
	Sources      []string `json:"source_message_ids"`
}
type Result struct {
	Topics []Topic `json:"topics"`
}
type Metrics struct {
	QueueWait          *int64   `json:"queue_wait_ms"`
	Processing         *int64   `json:"processing_ms"`
	BudgetHelper       *int64   `json:"budget_helper_ms"`
	CompatibilityCheck *int64   `json:"compatibility_check_ms"`
	Total              *float64 `json:"ollama_total_ms"`
	Load               *float64 `json:"ollama_load_ms"`
	Prompt             *float64 `json:"prompt_eval_ms"`
	Generation         *float64 `json:"generation_ms"`
	InputTokens        *int64   `json:"input_tokens"`
	OutputTokens       *int64   `json:"output_tokens"`
}
type Job struct {
	ID            string    `json:"job_id"`
	Status        string    `json:"status"`
	Created       time.Time `json:"created_at"`
	PromptVersion string    `json:"prompt_version"`
	SchemaVersion string    `json:"schema_version"`
	Result        *Result   `json:"result"`
	Error         *string   `json:"error"`
	Metrics       Metrics   `json:"metrics"`
}

func Strict(data []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return errors.New("invalid JSON structure or types")
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("expected one JSON value")
	}
	return nil
}
func validID(s string) bool { return strings.TrimSpace(s) != "" && len(s) <= 128 }
func (in *Input) Normalize() error {
	if !validID(in.CompanyID) || !validID(in.ClientID) {
		return errors.New("invalid company_id or client_id")
	}
	if in.Period.From.IsZero() || in.Period.To.IsZero() || !in.Period.From.Before(in.Period.To) {
		return errors.New("invalid period")
	}
	if len(in.Messages) == 0 || len(in.Messages)+len(in.Context) > 200 {
		return errors.New("require messages; maximum 200 including context")
	}
	ids := map[string]bool{}
	for group, list := range [][]Message{in.Context, in.Messages} {
		for _, m := range list {
			if !validID(m.ID) || !validID(m.ConversationID) || ids[m.ID] {
				return errors.New("invalid or duplicate message ID")
			}
			ids[m.ID] = true
			if m.Channel != "email" && m.Channel != "max" && m.Channel != "telegram" && m.Channel != "finchat" {
				return errors.New("invalid channel")
			}
			if m.SenderRole != "client" && m.SenderRole != "staff" {
				return errors.New("invalid sender_role")
			}
			if strings.TrimSpace(m.Text) == "" || len(m.Text) > 16000 {
				return errors.New("invalid message text (maximum 16000 UTF-8 bytes)")
			}
			if m.SentAt.IsZero() || (group == 0 && !m.SentAt.Before(in.Period.From)) || (group == 1 && (m.SentAt.Before(in.Period.From) || !m.SentAt.Before(in.Period.To))) {
				return errors.New("message timestamp outside allowed interval")
			}
		}
	}
	if in.Context == nil {
		in.Context = []Message{}
	}
	return nil
}

// Canonical identity preserves input order (also the tie breaker at equal timestamps).
// Missing, null and empty context are equivalent; whitespace and JSON key order are irrelevant.
func userData(in Input) []byte {
	in.Context = append([]Message{}, in.Context...)
	in.Messages = append([]Message{}, in.Messages...)
	sort.SliceStable(in.Context, func(i, j int) bool { return in.Context[i].SentAt.Before(in.Context[j].SentAt) })
	sort.SliceStable(in.Messages, func(i, j int) bool { return in.Messages[i].SentAt.Before(in.Messages[j].SentAt) })
	b, _ := json.Marshal(in)
	return b
}
func ValidateResult(raw []byte, in Input) (*Result, error) {
	var root map[string]json.RawMessage
	if Strict(raw, &root) != nil || len(root) != 1 || root["topics"] == nil || bytes.Equal(bytes.TrimSpace(root["topics"]), []byte("null")) {
		return nil, errors.New("invalid_model_result")
	}
	var items []map[string]json.RawMessage
	if json.Unmarshal(root["topics"], &items) != nil {
		return nil, errors.New("invalid_model_result")
	}
	for _, item := range items {
		if len(item) != 5 {
			return nil, errors.New("invalid_model_result")
		}
		for _, key := range []string{"text", "status", "deadline_text", "needs_context", "source_message_ids"} {
			if item[key] == nil || (key != "deadline_text" && bytes.Equal(bytes.TrimSpace(item[key]), []byte("null"))) {
				return nil, errors.New("invalid_model_result")
			}
		}
	}
	var out Result
	if Strict(raw, &out) != nil {
		return nil, errors.New("invalid_model_result")
	}
	all := map[string]bool{}
	current := map[string]bool{}
	for _, m := range in.Context {
		all[m.ID] = true
	}
	for _, m := range in.Messages {
		all[m.ID] = true
		current[m.ID] = true
	}
	for _, t := range out.Topics {
		if strings.TrimSpace(t.Text) == "" || len(t.Text) > 8000 {
			return nil, errors.New("invalid_model_result")
		}
		switch t.Status {
		case "awaiting_staff", "awaiting_client", "resolved", "unclear":
		default:
			return nil, errors.New("invalid_model_result")
		}
		if t.Deadline != nil && (strings.TrimSpace(*t.Deadline) == "" || t.Status == "resolved") {
			return nil, errors.New("invalid_model_result")
		}
		hasCurrent := false
		seen := map[string]bool{}
		for _, id := range t.Sources {
			if !all[id] || seen[id] {
				return nil, errors.New("invalid_model_sources")
			}
			seen[id] = true
			hasCurrent = hasCurrent || current[id]
		}
		if !hasCurrent {
			return nil, errors.New("invalid_model_sources")
		}
	}
	return &out, nil
}
