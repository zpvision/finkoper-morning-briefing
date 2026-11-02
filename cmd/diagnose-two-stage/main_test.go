package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOfflineTwoStagePlanNeverContactsOllama(t *testing.T) {
	python := os.Getenv("BRIEFING_TEST_BUDGET_PYTHON")
	if python == "" {
		t.Skip("opt-in local tokenizers")
	}
	t.Setenv("BRIEFING_BUDGET_PYTHON", python)
	script, _ := filepath.Abs("../../tools/context_budget/helper.py")
	resources, _ := filepath.Abs("../../resources/tokenizer/qwen3-8b")
	t.Setenv("BRIEFING_BUDGET_HELPER", script)
	t.Setenv("BRIEFING_TOKENIZER_DIR", resources)
	t.Setenv("OLLAMA_BASE_URL", "http://127.0.0.1:1")
	t.Setenv("BRIEFING_API_TOKEN", "do-not-output-backend-token")
	var out bytes.Buffer
	if err := run([]string{"../../examples/04-exact-20.json", "../../examples/02.json"}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "do-not-output-backend-token") {
		t.Fatal("token leaked")
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatal("expected two reports")
	}
	for _, line := range lines {
		var report struct {
			Mode     string  `json:"mode"`
			Attempts int     `json:"generation_attempts"`
			Final    any     `json:"final"`
			Skipped  *string `json:"final_skipped_reason"`
		}
		if json.Unmarshal([]byte(line), &report) != nil || report.Mode != "plan" || report.Attempts != 0 || report.Final != nil || report.Skipped == nil {
			t.Fatal("invalid offline plan")
		}
	}
}
