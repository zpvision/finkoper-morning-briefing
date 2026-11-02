package briefing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

type Ollama struct {
	BaseURL, Model string
	Client         *http.Client
	Counter        BudgetCounter
}

func NewOllama(base, model string, timeout time.Duration, counters ...BudgetCounter) *Ollama {
	var counter BudgetCounter
	if len(counters) > 0 {
		counter = counters[0]
	}
	return &Ollama{BaseURL: strings.TrimRight(base, "/"), Model: model, Client: &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, Counter: counter}
}
func millis(p *int64) *float64 {
	if p == nil {
		return nil
	}
	v := float64(*p) / 1e6
	return &v
}
func (o *Ollama) Generate(ctx context.Context, in Input) (*Result, Metrics, error) {
	var m Metrics
	system, user := PromptInput(in)
	countStart := time.Now()
	_, countErr := checkBudget(ctx, o.Counter, system, user)
	countMS := time.Since(countStart).Milliseconds()
	m.BudgetHelper = &countMS
	if countErr != nil {
		return nil, m, countErr
	}
	verifyStart := time.Now()
	verifyErr := o.Counter.Verify(ctx, system, user)
	verifyMS := time.Since(verifyStart).Milliseconds()
	m.CompatibilityCheck = &verifyMS
	if verifyErr != nil {
		return nil, m, verifyErr
	}
	payload := map[string]any{"model": o.Model, "stream": false, "think": false, "truncate": false, "shift": false, "keep_alive": "30m", "format": json.RawMessage(OutputSchema), "options": map[string]any{"temperature": 0, "num_ctx": ContextTokens, "num_predict": OutputReserve}, "messages": []map[string]string{{"role": "system", "content": system}, {"role": "user", "content": user}}}
	b, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, "POST", o.BaseURL+"/api/chat", bytes.NewReader(b))
	if err != nil {
		return nil, m, errors.New("ollama_request_error")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.Client.Do(req)
	if err != nil {
		return nil, m, errors.New("ollama_transport_error")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, m, errors.New("ollama_http_error")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxBody+1))
	if err != nil || len(raw) > MaxBody {
		return nil, m, errors.New("ollama_response_size_or_read_error")
	}
	var out struct {
		Done    bool   `json:"done"`
		Reason  string `json:"done_reason"`
		Error   string `json:"error"`
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		Total  *int64 `json:"total_duration"`
		Load   *int64 `json:"load_duration"`
		Prompt *int64 `json:"prompt_eval_duration"`
		Eval   *int64 `json:"eval_duration"`
		Input  *int64 `json:"prompt_eval_count"`
		Output *int64 `json:"eval_count"`
	}
	if json.Unmarshal(raw, &out) != nil {
		return nil, m, errors.New("ollama_invalid_envelope")
	}
	m.Total = millis(out.Total)
	m.Load = millis(out.Load)
	m.Prompt = millis(out.Prompt)
	m.Generation = millis(out.Eval)
	m.InputTokens = out.Input
	m.OutputTokens = out.Output
	if out.Error != "" {
		return nil, m, errors.New("ollama_error")
	}
	if !out.Done || out.Reason != "stop" {
		return nil, m, errors.New("ollama_incomplete_response")
	}
	result, err := ValidateResult([]byte(out.Message.Content), in)
	return result, m, err
}
