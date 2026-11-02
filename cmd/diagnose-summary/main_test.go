package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNoHoldoutOrArbitraryInputs(t *testing.T) {
	for _, args := range [][]string{{"--set", "holdout"}, {"--set", "all"}, {"extra.json"}, {"--out", "report.txt"}} {
		if run(args, &bytes.Buffer{}) == nil {
			t.Fatal("invalid summary selection")
		}
	}
}

func TestSummaryOfflineBothSelections(t *testing.T) {
	python := os.Getenv("BRIEFING_TEST_BUDGET_PYTHON")
	if python == "" {
		t.Skip("opt-in CPU tokenizer, no network")
	}
	root, _ := filepath.Abs("../..")
	t.Chdir(root)
	t.Setenv("BRIEFING_BUDGET_PYTHON", python)
	t.Setenv("OLLAMA_BASE_URL", "http://127.0.0.1:1")
	t.Setenv("OLLAMA_MODEL", "qwen3:8b")
	t.Setenv("BRIEFING_API_TOKEN", "PRIVATE_TOKEN")
	for _, set := range []string{"examples", "development"} {
		var output bytes.Buffer
		if err := run([]string{"--set", set}, &output); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(output.String(), "PRIVATE_TOKEN") || strings.Contains(output.String(), "hol-") || strings.Contains(output.String(), "expected_topics") {
			t.Fatal("token, holdout or annotation leaked")
		}
		lines := strings.Split(strings.TrimSpace(output.String()), "\n")
		count := 2
		if set == "development" {
			count = 10
		}
		if len(lines) != count {
			t.Fatal("wrong input count")
		}
		for _, line := range lines {
			var entry report
			if json.Unmarshal([]byte(line), &entry) != nil || entry.Mode != "plan" || entry.Budget.OutputReserve != 1000 || entry.Budget.ContextTokens != 8192 || entry.GenerationAttempts != 0 || entry.Error != nil || entry.SchemaVersion != "summary-v1" || entry.ModelDigest != nil {
				t.Fatal("invalid offline plan")
			}
		}
	}
	t.Setenv("OLLAMA_MODEL", "qwen3:14b-q4_K_M")
	if err := run(nil, &bytes.Buffer{}); err == nil {
		t.Fatal("unsupported model accepted")
	}
}
