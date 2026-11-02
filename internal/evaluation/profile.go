package evaluation

import (
	_ "embed"
	"errors"
	"os"
	"path/filepath"

	"finkoper-morning-briefing/internal/briefing"
	"finkoper-morning-briefing/internal/diagnostic"
)

const Profile8B = "qwen3-8b"
const Profile14B = "qwen3-14b-q4-k-m-v1"
const Model14B = "qwen3:14b-q4_K_M"

//go:embed profiles/qwen3-14b-q4-k-m-v1.json
var profile14Manifest []byte

// ConfigureProfile is diagnostic only. API configuration never calls it.
func ConfigureProfile(counter *briefing.PythonCounter, profile, resources, mode, split string) error {
	switch profile {
	case Profile8B:
		if resources != "" {
			return errors.New("profile_resources_requires_14b_profile")
		}
		if counter.Model != "qwen3:8b" {
			return errors.New("batch_model_requires_separate_tokenizer_template_budget_validation")
		}
	case Profile14B:
		// First experiment is deliberately development + non-thinking only.
		if mode != "think-false" || split != "development" {
			return errors.New("14b_initial_profile_requires_development_think_false")
		}
		return configure14BResources(counter, resources)
	default:
		return errors.New("unknown_diagnostic_profile")
	}
	return nil
}

// ConfigureFileProfile selects the existing pin without assigning files to a dataset split.
func ConfigureFileProfile(counter *briefing.PythonCounter, profile, resources string) error {
	if profile != Profile14B {
		return errors.New("standalone_files_require_explicit_14b_profile")
	}
	return configure14BResources(counter, resources)
}

func configure14BResources(counter *briefing.PythonCounter, resources string) error {
	if value := os.Getenv("OLLAMA_MODEL"); value != "" && value != Model14B {
		return errors.New("OLLAMA_MODEL_conflicts_with_diagnostic_profile")
	}
	if resources == "" {
		resources = "resources/tokenizer/qwen3-14b"
	}
	absolute, err := filepath.Abs(resources)
	if err != nil {
		return errors.New("invalid_profile_resources")
	}
	raw, err := os.ReadFile(filepath.Join(absolute, "manifest.json"))
	if err != nil || diagnostic.Hash(string(raw)) != diagnostic.Hash(string(profile14Manifest)) {
		return errors.New("diagnostic_profile_manifest_mismatch")
	}
	counter.Model = Model14B
	counter.Resources = absolute
	counter.DiagnosticProfile = Profile14B
	return nil
}
