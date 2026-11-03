package diagnostic

import (
	_ "embed"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"finkoper-morning-briefing/internal/briefing"
)

const Qwen35Profile = "qwen35-9b-summary-v2-v1"
const Qwen35Model = "qwen3.5:9b"
const Qwen35BudgetMethod = "qwen35_tokenizers_pinned_renderer_no_normalization"

//go:embed profiles/qwen35-9b-v1.json
var qwen35Pin []byte

type Qwen35Manifest struct {
	Profile      string             `json:"profile"`
	Model        string             `json:"model"`
	Digest       string             `json:"model_digest"`
	OllamaCommit string             `json:"ollama_commit"`
	HFRevision   string             `json:"hf_revision"`
	Sampling     map[string]float64 `json:"sampling"`
}

func Qwen35PinnedManifest() (Qwen35Manifest, string) {
	var m Qwen35Manifest
	_ = json.Unmarshal(qwen35Pin, &m)
	return m, Hash(string(qwen35Pin))
}
func Qwen35Options() map[string]any {
	m, _ := Qwen35PinnedManifest()
	options := map[string]any{"num_ctx": 8192, "num_predict": 1000}
	for key, value := range m.Sampling {
		options[key] = value
	}
	return options
}

// Restricted, pinned Qwen35Renderer, not the 8B Go template: no double system newline or /no_think suffix.
func Qwen35Render(system, user string) string {
	return "<|im_start|>system\n" + system + "<|im_end|>\n<|im_start|>user\n" + user + "<|im_end|>\n<|im_start|>assistant\n<think>\n\n</think>\n\n"
}

func NewQwen35Counter(resources string) (*briefing.PythonCounter, error) {
	if model := os.Getenv("OLLAMA_MODEL"); model != "" && model != Qwen35Model {
		return nil, errors.New("OLLAMA_MODEL_conflicts_with_qwen35_profile")
	}
	counter, err := briefing.NewPythonCounter()
	if err != nil {
		return nil, err
	}
	if resources == "" {
		resources = "resources/tokenizer/qwen35-9b"
	}
	absolute, err := filepath.Abs(resources)
	if err != nil {
		return nil, errors.New("invalid_qwen35_resources")
	}
	raw, err := os.ReadFile(filepath.Join(absolute, "manifest.json"))
	if err != nil || Hash(string(raw)) != Hash(string(qwen35Pin)) {
		return nil, errors.New("qwen35_manifest_pin_mismatch")
	}
	counter.Model = Qwen35Model
	counter.Resources = absolute
	counter.Script, err = filepath.Abs("tools/context_budget/qwen35_helper.py")
	if err != nil {
		return nil, err
	}
	counter.DiagnosticBudgetMethod = Qwen35BudgetMethod
	if strings.TrimSpace(counter.BaseURL) == "" {
		return nil, errors.New("qwen35_invalid_base_url")
	}
	return counter, nil
}

func NewQwen35Runner(base string, counter briefing.BudgetCounter) *Runner {
	runner := NewSummaryRunner(base, counter)
	runner.Model = Qwen35Model
	runner.Qwen35Profile = true
	return runner
}
