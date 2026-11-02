package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultCommandIsOfflinePlanAndOmitsAPIToken(t *testing.T) {
	python := os.Getenv("BRIEFING_TEST_BUDGET_PYTHON")
	if python == "" {
		t.Skip("opt-in local Python helper")
	}
	t.Setenv("BRIEFING_BUDGET_PYTHON", python)
	script, _ := filepath.Abs("../../tools/context_budget/helper.py")
	resources, _ := filepath.Abs("../../resources/tokenizer/qwen3-8b")
	t.Setenv("BRIEFING_BUDGET_HELPER", script)
	t.Setenv("BRIEFING_TOKENIZER_DIR", resources)
	t.Setenv("OLLAMA_BASE_URL", "http://127.0.0.1:1") // No listener: any network call would fail.
	t.Setenv("BRIEFING_API_TOKEN", "must-never-appear-in-diagnostic-output")
	var out bytes.Buffer
	if err := run([]string{"../../examples/04-exact-20.json", "../../examples/02.json"}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "must-never-appear-in-diagnostic-output") {
		t.Fatal("token leaked")
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 6 {
		t.Fatal("expected six planned variants")
	}
	for _, line := range lines {
		var record struct {
			Mode        string `json:"mode"`
			Generations int    `json:"generation_attempts"`
			Hash        string `json:"prompt_sha256"`
		}
		if json.Unmarshal([]byte(line), &record) != nil || record.Mode != "plan" || record.Generations != 0 || len(record.Hash) != 64 {
			t.Fatal("invalid plan record")
		}
	}
}
