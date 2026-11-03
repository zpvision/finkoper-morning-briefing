package diagnostic

import (
	_ "embed"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

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

// Sampling source: pinned Qwen/Qwen3.5-9B README, "Thinking mode for general tasks".
const Qwen35SamplingSource = "https://huggingface.co/Qwen/Qwen3.5-9B/blob/c202236235762e1c871ad0ccb60c8ee5ba337b9a/README.md"

type Qwen35Config struct {
	Mode     string
	Thinking bool
	Reserve  int
	Timeout  time.Duration
	Options  map[string]any
	Note     string
}

func Qwen35Configuration(mode string) (Qwen35Config, error) {
	if mode == "" {
		mode = "baseline"
	}
	c := Qwen35Config{Mode: mode, Reserve: 1000, Timeout: 120 * time.Second, Options: Qwen35Options()}
	switch mode {
	case "baseline":
		c.Note = "Frozen baseline from 6bb6aaf: non-thinking general sampling. Compared with 8B, model, tokenizer, renderer and sampling differ."
	case "greedy":
		c.Options["temperature"] = float64(0)
		c.Options["presence_penalty"] = float64(0)
		c.Note = "Compared with baseline: temperature and presence_penalty change together; this does not isolate either parameter."
	case "thinking", "thinking-long":
		c.Thinking, c.Reserve, c.Timeout = true, 4096, 300*time.Second
		c.Options["temperature"] = float64(1)
		c.Options["top_p"] = float64(0.95)
		c.Options["num_predict"] = c.Reserve
		c.Note = "Compared with baseline: think, sampling, full generation reserve and HTTP timeout change; this does not isolate thinking. Official Qwen3.5-9B thinking/general sampling."
		if mode == "thinking-long" {
			c.Reserve, c.Timeout = 6144, 600*time.Second
			c.Options["num_predict"] = c.Reserve
			c.Note = "Explicit regression probe. Same sampling and renderer as thinking; full reserve increases to 6144 and HTTP timeout to 600s. No retries or fallback."
		}
	default:
		return Qwen35Config{}, errors.New("invalid_qwen35_mode")
	}
	return c, nil
}

// Restricted, pinned Qwen35Renderer, not the 8B Go template: distinct thinking prefill,
// no double system newline or /no_think suffix. Runtime render-only must match.
func Qwen35Render(system, user string, thinking ...bool) string {
	prefix := "<|im_start|>system\n" + system + "<|im_end|>\n<|im_start|>user\n" + user + "<|im_end|>\n<|im_start|>assistant\n"
	if len(thinking) > 0 && thinking[0] {
		return prefix + "<think>\n"
	}
	return prefix + "<think>\n\n</think>\n\n"
}

func NewQwen35Counter(resources string) (*briefing.PythonCounter, error) {
	return NewQwen35CounterMode(resources, "baseline")
}

func NewQwen35CounterMode(resources, mode string) (*briefing.PythonCounter, error) {
	config, err := Qwen35Configuration(mode)
	if err != nil {
		return nil, err
	}
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
	counter.DiagnosticQwen35Mode = config.Mode
	if strings.TrimSpace(counter.BaseURL) == "" {
		return nil, errors.New("qwen35_invalid_base_url")
	}
	return counter, nil
}

func NewQwen35Runner(base string, counter briefing.BudgetCounter) *Runner {
	runner, _ := NewQwen35RunnerMode(base, counter, "baseline")
	return runner
}

func NewQwen35RunnerMode(base string, counter briefing.BudgetCounter, mode string) (*Runner, error) {
	config, err := Qwen35Configuration(mode)
	if err != nil {
		return nil, err
	}
	runner := NewSummaryRunner(base, counter)
	runner.Model = Qwen35Model
	runner.Qwen35Profile = true
	runner.Qwen35Mode = config.Mode
	runner.Thinking = config.Thinking
	runner.Client.Timeout = config.Timeout
	return runner, nil
}
