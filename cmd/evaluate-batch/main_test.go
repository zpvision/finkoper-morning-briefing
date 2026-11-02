package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"finkoper-morning-briefing/internal/evaluation"
)

func TestInvalidSelection(t *testing.T) {
	for _, args := range [][]string{{"--mode", "anything"}, {"--split", "all"}, {"extra"}, {"--out", "report.txt"}, {"--profile", "unknown"}, {"--check-profile", "--run"}, {"--split", "holdout", "--probe"}} {
		if run(args, &bytes.Buffer{}) == nil {
			t.Fatal("invalid selection accepted")
		}
	}
}

func Test14BOfflineProbeAndExplicitLocalCheck(t *testing.T) {
	python := os.Getenv("BRIEFING_TEST_BUDGET_PYTHON")
	if python == "" {
		t.Skip("opt-in local CPU tokenizer")
	}
	t.Setenv("BRIEFING_BUDGET_PYTHON", python)
	script, _ := filepath.Abs("../../tools/context_budget/helper.py")
	resources, _ := filepath.Abs("../../resources/tokenizer/qwen3-14b")
	if _, err := os.Stat(filepath.Join(resources, "manifest.json")); err != nil {
		t.Skip("prepare 14B resources")
	}
	t.Setenv("BRIEFING_BUDGET_HELPER", script)
	// Deliberately retain 8B's normal resource configuration: explicit profile uses its own directory.
	t.Setenv("BRIEFING_TOKENIZER_DIR", "not-the-diagnostic-resources")
	t.Setenv("OLLAMA_MODEL", "")
	t.Setenv("OLLAMA_BASE_URL", "http://127.0.0.1:1")
	args := []string{"--dataset", "../../evaluation", "--profile", evaluation.Profile14B, "--profile-resources", resources, "--probe"}
	var output bytes.Buffer
	if err := run(args, &output); err != nil {
		t.Fatal(err)
	}
	var report evaluation.Report
	if json.Unmarshal(output.Bytes(), &report) != nil || report.Model != evaluation.Model14B || report.DiagnosticProfile != evaluation.Profile14B || report.SelectedExamples != 1 || report.ExampleID != "dev-01" || report.GenerationAttempts != 0 || report.Budget.Verified || report.Budget.OutputReserve != 1500 {
		t.Fatal("invalid plan")
	}
	base := os.Getenv("BRIEFING_TEST_SIMULATION_URL")
	if base == "" {
		return
	}
	if !strings.HasPrefix(base, "http://127.0.0.1:") {
		t.Fatal("local simulation only")
	}
	t.Setenv("OLLAMA_BASE_URL", base)
	output.Reset()
	if err := run(append(args, "--check-profile"), &output); err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(output.Bytes(), &report) != nil || report.Mode != "check" || !report.Budget.Verified || report.GenerationAttempts != 0 || report.ModelDigest == nil {
		t.Fatal("invalid runtime check")
	}
}

func TestOfflineBatchWithLocalTokenizers(t *testing.T) {
	python := os.Getenv("BRIEFING_TEST_BUDGET_PYTHON")
	if python == "" {
		t.Skip("opt-in local CPU tokenizer; no network")
	}
	t.Setenv("BRIEFING_BUDGET_PYTHON", python)
	script, _ := filepath.Abs("../../tools/context_budget/helper.py")
	resources, _ := filepath.Abs("../../resources/tokenizer/qwen3-8b")
	t.Setenv("BRIEFING_BUDGET_HELPER", script)
	t.Setenv("BRIEFING_TOKENIZER_DIR", resources)
	t.Setenv("OLLAMA_BASE_URL", "http://127.0.0.1:1")
	t.Setenv("OLLAMA_MODEL", "qwen3:8b")
	t.Setenv("BRIEFING_API_TOKEN", "PRIVATE_API_TOKEN")
	for _, mode := range []string{"think-false", "think-true"} {
		var output bytes.Buffer
		if err := run([]string{"--dataset", "../../evaluation", "--mode", mode}, &output); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(output.String(), "PRIVATE_API_TOKEN") {
			t.Fatal("token leaked")
		}
		lines := strings.Split(strings.TrimSpace(output.String()), "\n")
		if len(lines) != 10 {
			t.Fatal("default must be ten development cases")
		}
		for _, line := range lines {
			var report evaluation.Report
			if json.Unmarshal([]byte(line), &report) != nil || report.Split != "development" || report.GenerationAttempts != 0 || report.Budget.Verified || report.ModelDigest != nil || report.ExpectedModelDigest == "" || report.Error != nil {
				t.Fatal("invalid offline report")
			}
			reserve := 1500
			if mode == "think-true" {
				reserve = 4096
			}
			if report.Budget.OutputReserve != reserve {
				t.Fatal("wrong reserve")
			}
		}
	}
	t.Setenv("OLLAMA_MODEL", "unverified-model")
	if err := run([]string{"--dataset", "../../evaluation"}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "separate_tokenizer") {
		t.Fatal("unverified model accepted")
	}
}
