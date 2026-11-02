package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOfflineThinkingPlan(t *testing.T) {
	python := os.Getenv("BRIEFING_TEST_BUDGET_PYTHON")
	if python == "" {
		t.Skip("opt-in local tokenizers")
	}
	root, _ := filepath.Abs("../..")
	t.Chdir(root)
	t.Setenv("BRIEFING_BUDGET_PYTHON", python)
	t.Setenv("OLLAMA_BASE_URL", "http://127.0.0.1:1")
	t.Setenv("OLLAMA_MODEL", "qwen3:8b")
	t.Setenv("BRIEFING_API_TOKEN", "PRIVATE_BACKEND_TOKEN")
	var output bytes.Buffer
	if err := run(nil, &output); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "PRIVATE_BACKEND_TOKEN") {
		t.Fatal("secret leaked")
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 {
		t.Fatal("expected two fixed inputs")
	}
	for _, line := range lines {
		var report struct {
			Mode     string
			Attempts int `json:"generation_attempts"`
			Budget   struct {
				Reserve int `json:"output_reserve"`
			}
			Error *string
		}
		if json.Unmarshal([]byte(line), &report) != nil || report.Mode != "plan" || report.Attempts != 0 || report.Budget.Reserve != 4096 || report.Error != nil {
			t.Fatal("invalid plan")
		}
	}
	if run([]string{"--run", "extra.json"}, &bytes.Buffer{}) == nil {
		t.Fatal("additional inputs accepted")
	}
}
