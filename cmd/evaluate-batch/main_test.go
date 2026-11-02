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
	for _, args := range [][]string{{"--mode", "anything"}, {"--split", "all"}, {"extra"}, {"--out", "report.txt"}} {
		if run(args, &bytes.Buffer{}) == nil {
			t.Fatal("invalid selection accepted")
		}
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
