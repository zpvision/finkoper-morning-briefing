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

func TestComparisonArguments(t *testing.T) {
	for _, args := range [][]string{{"--set", "holdout"}, {"--out", "file.txt"}, {"extra"}} {
		if run(args, &bytes.Buffer{}) == nil {
			t.Fatal("invalid comparison args")
		}
	}
}
func TestComparisonPlanActualCPUHelper(t *testing.T) {
	python := os.Getenv("BRIEFING_TEST_BUDGET_PYTHON")
	if python == "" {
		t.Skip("opt-in local tokenizers, no network")
	}
	root, _ := filepath.Abs("../..")
	t.Chdir(root)
	t.Setenv("BRIEFING_BUDGET_PYTHON", python)
	t.Setenv("OLLAMA_MODEL", "qwen3:8b")
	t.Setenv("OLLAMA_BASE_URL", "http://127.0.0.1:1")
	var output bytes.Buffer
	if err := run([]string{"--set", "examples"}, &output); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 4 {
		t.Fatal("four planned records")
	}
	for _, line := range lines {
		var record evaluation.SummaryComparisonRecord
		if json.Unmarshal([]byte(line), &record) != nil || record.GenerationAttempts != 0 || record.Budget.OutputReserve != 1000 || record.Budget.Verified || record.ModelDigest != nil || record.Error != nil {
			t.Fatal("plan")
		}
	}
}
