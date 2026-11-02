package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"finkoper-morning-briefing/internal/evaluation"
)

func TestExplicitProfileAndAllFilesValidatedFirst(t *testing.T) {
	for _, args := range [][]string{nil, {"--profile", "qwen3-8b"}, {"--profile", evaluation.Profile14B}, {"--profile", evaluation.Profile14B, "--out", "report.txt", "../../examples/02.json"}} {
		if run(args, &bytes.Buffer{}) == nil {
			t.Fatal("invalid arguments accepted")
		}
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	t.Setenv("OLLAMA_BASE_URL", server.URL)
	bad := filepath.Join(t.TempDir(), "bad.json")
	os.WriteFile(bad, []byte(`{"secret_message":`), 0600)
	err := run([]string{"--profile", evaluation.Profile14B, "--run", "../../examples/02.json", bad}, &bytes.Buffer{})
	if err == nil || err.Error() != "diagnostic_input_file_invalid" || requests.Load() != 0 {
		t.Fatal("network or setup before full validation")
	}
}

func TestOfflineStandalone14BWithLocalHelper(t *testing.T) {
	python := os.Getenv("BRIEFING_TEST_BUDGET_PYTHON")
	if python == "" {
		t.Skip("opt-in local tokenizers, no network")
	}
	script, _ := filepath.Abs("../../tools/context_budget/helper.py")
	resources, _ := filepath.Abs("../../resources/tokenizer/qwen3-14b")
	if _, err := os.Stat(filepath.Join(resources, "manifest.json")); err != nil {
		t.Skip("prepare 14B resources first")
	}
	t.Setenv("BRIEFING_BUDGET_PYTHON", python)
	t.Setenv("BRIEFING_BUDGET_HELPER", script)
	t.Setenv("BRIEFING_TOKENIZER_DIR", "do-not-use-8b-resources")
	t.Setenv("OLLAMA_MODEL", evaluation.Model14B)
	t.Setenv("OLLAMA_BASE_URL", "http://127.0.0.1:1")
	t.Setenv("BRIEFING_API_TOKEN", "PRIVATE_BACKEND_TOKEN")
	var output bytes.Buffer
	args := []string{"--profile", evaluation.Profile14B, "--profile-resources", resources, "../../examples/04-exact-20.json", "../../examples/02.json"}
	if err := run(args, &output); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "PRIVATE_BACKEND_TOKEN") {
		t.Fatal("token leaked")
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 {
		t.Fatal("expected two reports")
	}
	for i, line := range lines {
		var report evaluation.FileReport
		if json.Unmarshal([]byte(line), &report) != nil || report.Mode != "plan" || report.GenerationAttempts != 0 || report.Model != evaluation.Model14B || report.ModelDigest != nil || report.Budget.Verified || report.Budget.OutputReserve != 1500 || report.InputScope != "standalone_files" || report.Error != nil {
			t.Fatal("incorrect plan")
		}
		expected := []int{2370, 1665}
		if report.Budget.InputTokens != expected[i] {
			t.Fatal("input/template changed", report.Budget.InputTokens)
		}
	}
	// Reports are new files, never overwrite existing results.
	out := filepath.Join(t.TempDir(), "plan.jsonl")
	withOut := append([]string{"--out", out}, args...)
	if err := run(withOut, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := run(withOut, &bytes.Buffer{}); err == nil || err.Error() != "diagnostic_report_create_failed" {
		t.Fatal("report overwritten")
	}
}
