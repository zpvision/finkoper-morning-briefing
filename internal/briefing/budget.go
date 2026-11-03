package briefing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const ContextTokens = 8192
const OutputReserve = 1500
const BudgetMethod = "qwen3_tokenizers_ollama_template_no_normalization"

type Budget struct {
	InputTokens     int    `json:"input_tokens"`
	OutputReserve   int    `json:"output_reserve"`
	ContextTokens   int    `json:"context_tokens"`
	RequiredTokens  int    `json:"required_tokens"`
	RemainingTokens int    `json:"remaining_tokens"`
	Method          string `json:"method"`
	Verified        bool   `json:"compatibility_verified"`
}
type BudgetExceeded struct{ Budget Budget }

func (*BudgetExceeded) Error() string { return "input_budget_exceeded" }

type BudgetCounter interface {
	Count(context.Context, string, string) (Budget, error)
	Verify(context.Context, string, string) error
}

func checkBudget(ctx context.Context, c BudgetCounter, system, user string) (Budget, error) {
	if c == nil {
		return Budget{}, errors.New("budget_counter_not_configured")
	}
	b, err := c.Count(ctx, system, user)
	if err != nil {
		return b, err
	}
	if b.InputTokens < 0 || b.OutputReserve != OutputReserve || b.ContextTokens != ContextTokens || b.RequiredTokens != b.InputTokens+OutputReserve || b.RemainingTokens != ContextTokens-b.RequiredTokens || b.Method != BudgetMethod {
		return Budget{}, errors.New("budget_helper_invalid_response")
	}
	if b.RequiredTokens > ContextTokens {
		return b, &BudgetExceeded{b}
	}
	return b, nil
}

type PythonCounter struct {
	Python, Script, Resources, BaseURL, Model string
	Timeout                                   time.Duration
	// Explicit diagnostic-only opt-in; never set by service configuration.
	DiagnosticThinking     bool
	DiagnosticSummary      bool   // CLI-only summary reserve=1000; production default is unchanged.
	DiagnosticProfile      string // Explicit CLI-only profile, never set by service configuration.
	DiagnosticBudgetMethod string // Explicit diagnostic factory only; default remains the 8B method.
	DiagnosticQwen35Mode   string // CLI-only mode for the independent Qwen3.5 helper.
	slot                   chan struct{}
	verified               atomic.Bool
}

func NewPythonCounter() (*PythonCounter, error) {
	python := os.Getenv("BRIEFING_BUDGET_PYTHON")
	if python == "" {
		return nil, errors.New("BRIEFING_BUDGET_PYTHON is required (dedicated tokenizers venv)")
	}
	resolved, err := exec.LookPath(python)
	if err != nil {
		return nil, errors.New("budget_python_not_found")
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return nil, errors.New("budget_python_not_found")
	}
	timeout, err := time.ParseDuration(env("BRIEFING_BUDGET_TIMEOUT", "30s"))
	if err != nil || timeout <= 0 || timeout > 2*time.Minute {
		return nil, errors.New("invalid BRIEFING_BUDGET_TIMEOUT (maximum 2m)")
	}
	script, err := filepath.Abs(env("BRIEFING_BUDGET_HELPER", "tools/context_budget/helper.py"))
	if err != nil {
		return nil, err
	}
	resources, err := filepath.Abs(env("BRIEFING_TOKENIZER_DIR", "resources/tokenizer/qwen3-8b"))
	if err != nil {
		return nil, err
	}
	return &PythonCounter{Python: resolved, Script: script, Resources: resources, BaseURL: env("OLLAMA_BASE_URL", "http://127.0.0.1:11434"), Model: env("OLLAMA_MODEL", "qwen3:8b"), Timeout: timeout, slot: make(chan struct{}, 1)}, nil
}

type limitedOutput struct {
	bytes.Buffer
	tooLarge bool
}

func (b *limitedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 16384 {
		b.tooLarge = true
		return 0, errors.New("helper_output_too_large")
	}
	return b.Buffer.Write(p)
}

var helperCodes = map[string]bool{
	"qwen35_manifest_pin_mismatch": true, "qwen35_resource_checksum_mismatch": true, "qwen35_requires_tokenizers_0_22_1_venv": true,
	"qwen35_unsupported_outer_whitespace": true, "qwen35_model_metadata_mismatch": true, "qwen35_model_digest_mismatch": true,
	"qwen35_ollama_version_mismatch": true, "qwen35_invalid_base_url": true, "qwen35_verification_request_failed": true,
	"qwen35_render_only_missing_rendered_template": true, "qwen35_runtime_renderer_mismatch": true, "qwen35_helper_failed": true,
	"qwen35_unexpected_media":                      true,
	"qwen35_verification_request_failed_HTTPError": true, "qwen35_verification_request_failed_URLError": true,
	"qwen35_verification_request_failed_TimeoutError": true, "qwen35_verification_request_failed_ConnectionResetError": true,
	"qwen35_verification_request_failed_IncompleteRead": true, "qwen35_verification_request_failed_JSONDecodeError": true,
	"qwen35_verification_request_failed_UnicodeDecodeError": true, "qwen35_verification_request_failed_ValueError": true,
	"qwen35_verification_request_failed_other": true,
	"budget_python_requires_venv":              true,
	"tokenizers_version_mismatch":              true, "invalid_tokenizer_manifest": true, "tokenizer_checksum_mismatch": true,
	"invalid_ollama_url": true, "ollama_verification_response_too_large": true, "ollama_verification_invalid_response": true,
	"ollama_verification_request_failed": true, "context_renderer_ollama_version_not_validated": true,
	"context_tokenizer_model_not_validated": true, "context_chat_template_changed": true,
	"context_render_only_unavailable": true, "context_chat_render_mismatch": true, "budget_helper_failed": true,
}

func (p *PythonCounter) invoke(ctx context.Context, mode string, request any, out any) error {
	ctx, cancel := context.WithTimeout(ctx, p.Timeout)
	defer cancel()
	select {
	case p.slot <- struct{}{}:
		defer func() { <-p.slot }()
	case <-ctx.Done():
		return errors.New("budget_helper_timeout")
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return errors.New("budget_helper_request_failed")
	}
	args := []string{"-I", p.Script, "--resources", p.Resources, "--mode", mode, "--http-timeout", strconv.FormatFloat(p.Timeout.Seconds(), 'f', 3, 64)}
	if p.DiagnosticThinking {
		args = append(args, "--thinking")
	}
	if p.DiagnosticSummary {
		args = append(args, "--summary")
	}
	if p.DiagnosticProfile != "" {
		args = append(args, "--diagnostic-profile", p.DiagnosticProfile)
	}
	if p.DiagnosticQwen35Mode != "" {
		args = append(args, "--qwen35-mode", p.DiagnosticQwen35Mode)
	}
	cmd := exec.CommandContext(ctx, p.Python, args...)
	cmd.WaitDelay = time.Second
	cmd.Stdin = bytes.NewReader(raw)
	var output limitedOutput
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	// Keep backend tokens and Python/HF overrides out of the isolated helper process.
	cmd.Env = []string{}
	for _, key := range []string{"PATH", "SystemRoot", "WINDIR", "TEMP", "TMP", "LANG", "LC_ALL"} {
		if value := os.Getenv(key); value != "" {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	err = cmd.Run()
	if ctx.Err() != nil {
		return errors.New("budget_helper_timeout")
	}
	if output.tooLarge {
		return errors.New("budget_helper_invalid_response")
	}
	var failure struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(output.Bytes(), &failure) == nil && failure.Error != "" {
		if helperCodes[failure.Error] {
			return errors.New(failure.Error)
		}
		return errors.New("budget_helper_failed")
	}
	if err != nil {
		return errors.New("budget_helper_failed")
	}
	if Strict(output.Bytes(), out) != nil {
		return errors.New("budget_helper_invalid_response")
	}
	return nil
}
func (p *PythonCounter) Count(ctx context.Context, system, user string) (Budget, error) {
	var b Budget
	err := p.invoke(ctx, "count", map[string]string{"system": system, "user": user}, &b)
	b.Verified = p.verified.Load()
	return b, err
}
func (p *PythonCounter) Verify(ctx context.Context, system, user string) error {
	var result struct {
		Verified bool   `json:"verified"`
		Digest   string `json:"model_digest"`
		Version  string `json:"ollama_version"`
		Template string `json:"template_sha256"`
		Method   string `json:"method"`
	}
	request := map[string]any{"system": system, "user": user, "base_url": p.BaseURL, "model": p.Model}
	if p.DiagnosticThinking {
		request["format"] = json.RawMessage(OutputSchema)
	}
	err := p.invoke(ctx, "verify", request, &result)
	if err != nil {
		p.verified.Store(false)
		return err
	}
	expectedMethod := BudgetMethod
	if p.DiagnosticBudgetMethod != "" {
		expectedMethod = p.DiagnosticBudgetMethod
	}
	if !result.Verified || result.Version != "0.40.5" || len(result.Digest) != 64 || len(result.Template) != 64 || result.Method != expectedMethod {
		p.verified.Store(false)
		return errors.New("budget_helper_invalid_response")
	}
	p.verified.Store(true)
	return nil
}
func SyntheticProbe() (string, string) {
	return "Проверка\n {\"label\":\"neutral\"}\n ", "Текст\n {скобки} 😀 и\u0306 е\u0308\n "
}
func PromptInput(in Input) (string, string) { return SystemPrompt, string(userData(in)) }
func AnalyzeBudget(ctx context.Context, c BudgetCounter, in Input) (Budget, error) {
	system, user := PromptInput(in)
	return checkBudget(ctx, c, system, user)
}

// Helpful for log/CLI codes only; never returns subprocess stderr or payloads.
func budgetFailure(err error) string {
	var exceeded *BudgetExceeded
	if errors.As(err, &exceeded) {
		return exceeded.Error()
	}
	if strings.HasPrefix(err.Error(), "context_") || strings.HasPrefix(err.Error(), "budget_") || helperCodes[err.Error()] {
		return err.Error()
	}
	return "budget_unavailable"
}
