package diagnostic

import (
	_ "embed"
	"encoding/json"
	"errors"
	"finkoper-morning-briefing/internal/briefing"
	"os"
	"path/filepath"
)

const Qwen27Profile = "qwen35-27b-summary-v2-v1"
const Qwen27Model = "qwen3.5:27b-q4_K_M"

//go:embed profiles/qwen35-27b-v1.json
var qwen27Pin []byte

func Qwen27PinnedManifest() (Qwen35Manifest, string) {
	var m Qwen35Manifest
	_ = json.Unmarshal(qwen27Pin, &m)
	return m, Hash(string(qwen27Pin))
}
func NewQwen27Counter(resources string) (*briefing.PythonCounter, error) {
	if model := os.Getenv("OLLAMA_MODEL"); model != "" && model != Qwen27Model {
		return nil, errors.New("OLLAMA_MODEL_conflicts_with_qwen27_profile")
	}
	p, err := briefing.NewPythonCounter()
	if err != nil {
		return nil, err
	}
	if resources == "" {
		resources = "resources/tokenizer/qwen35-27b"
	}
	p.Resources, err = filepath.Abs(resources)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(p.Resources, "manifest.json"))
	if err != nil || Hash(string(raw)) != Hash(string(qwen27Pin)) {
		return nil, errors.New("qwen35_manifest_pin_mismatch")
	}
	p.Model = Qwen27Model
	p.Script, err = filepath.Abs("tools/context_budget/qwen35_27_helper.py")
	if err != nil {
		return nil, err
	}
	p.DiagnosticBudgetMethod = Qwen35BudgetMethod
	p.DiagnosticQwen35Mode = "baseline"
	p.DiagnosticOllamaVersion = "0.40.15"
	return p, nil
}
func NewQwen27Runner(base string, c briefing.BudgetCounter) *Runner {
	r := NewQwen35Runner(base, c)
	r.Model = Qwen27Model
	r.Qwen27Profile = true
	return r
}
