package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"finkoper-morning-briefing/internal/briefing"
	"finkoper-morning-briefing/internal/diagnostic"
	"finkoper-morning-briefing/internal/evaluation"
)

func TestNoImplicitCombinedSetOrArbitraryInputs(t *testing.T) {
	for _, args := range [][]string{{"--set", "all"}, {"extra.json"}, {"--out", "report.txt"}} {
		if run(args, &bytes.Buffer{}) == nil {
			t.Fatal("invalid summary selection")
		}
	}
}

// All holdout data/annotations here are generated test fixtures, never real rubric contents.
func TestDefaultOfflineAndExplicitHoldoutHTTPWithoutAnnotations(t *testing.T) {
	python := os.Getenv("BRIEFING_TEST_BUDGET_PYTHON")
	if python == "" {
		t.Skip("opt-in local CPU helper and HTTP mock")
	}
	root, _ := filepath.Abs("../..")
	script := filepath.Join(root, "tools/context_budget/helper.py")
	resources := filepath.Join(root, "resources/tokenizer/qwen3-8b")
	raw, err := os.ReadFile(filepath.Join(root, "qwen3-show.json"))
	if err != nil {
		t.Fatal(err)
	}
	var show struct{ Template string }
	if json.Unmarshal(raw, &show) != nil {
		t.Fatal("export")
	}
	raw, err = os.ReadFile(filepath.Join(resources, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var metadata struct {
		Digest string `json:"model_digest"`
	}
	json.Unmarshal(raw, &metadata)
	fixture := t.TempDir()
	manifest := evaluation.Manifest{Version: "isolation-fixture", Synthetic: true}
	expectedUsers := []string{}
	for _, split := range []string{"development", "holdout"} {
		os.MkdirAll(filepath.Join(fixture, "evaluation/inputs", split), 0700)
		os.MkdirAll(filepath.Join(fixture, "evaluation/annotations", split), 0700)
		for n := 1; n <= 10; n++ {
			id := fmt.Sprintf("%s-%02d", split, n)
			in := briefing.Input{CompanyID: "fixture", ClientID: id, Period: briefing.Period{From: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)}, Messages: []briefing.Message{{ID: fmt.Sprintf("m%d", n), ConversationID: "fixture", Channel: "max", SentAt: time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC), SenderRole: "client", Text: "Сообщение для проверки транспорта."}}}
			raw, _ := json.Marshal(in)
			file := fmt.Sprintf("inputs/%s/%s.json", split, id)
			if os.WriteFile(filepath.Join(fixture, "evaluation", filepath.FromSlash(file)), raw, 0600) != nil {
				t.Fatal("input write")
			}
			// Deliberately invalid annotation JSON: runtime must neither parse nor transmit it.
			os.WriteFile(filepath.Join(fixture, "evaluation/annotations", split, id+".json"), []byte(`{"PRIVATE_ANNOTATION_SENTINEL":`), 0600)
			manifest.Cases = append(manifest.Cases, evaluation.Entry{ID: id, Split: split, Input: file})
			if split == "holdout" {
				_, user := briefing.PromptInput(in)
				expectedUsers = append(expectedUsers, user)
			}
		}
	}
	raw, _ = json.Marshal(manifest)
	os.WriteFile(filepath.Join(fixture, "evaluation/manifest.json"), raw, 0600)
	var requests, generations, active, maxActive atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.URL.Path {
		case "/api/version":
			json.NewEncoder(w).Encode(map[string]string{"version": "0.40.5"})
		case "/api/tags":
			json.NewEncoder(w).Encode(map[string]any{"models": []map[string]string{{"name": "qwen3:8b", "digest": metadata.Digest}}})
		case "/api/show":
			json.NewEncoder(w).Encode(map[string]string{"template": show.Template})
		case "/api/chat":
			var p struct {
				Debug    bool `json:"_debug_render_only"`
				Think    bool
				Model    string
				Options  map[string]float64
				Messages []struct{ Role, Content string }
			}
			json.NewDecoder(r.Body).Decode(&p)
			if p.Model != "qwen3:8b" || p.Think || p.Options["num_predict"] != 1000 || p.Options["num_ctx"] != 8192 || len(p.Messages) != 2 || p.Messages[0].Content != diagnostic.SummaryPrompt {
				t.Error("summary profile changed")
			}
			if strings.Contains(p.Messages[1].Content, "PRIVATE_ANNOTATION_SENTINEL") || r.Header.Get("Authorization") != "" {
				t.Error("annotation or token leaked")
			}
			if p.Debug {
				json.NewEncoder(w).Encode(map[string]any{"_debug_info": map[string]string{"rendered_template": diagnostic.ExpectedRender(p.Messages[0].Content, p.Messages[1].Content)}})
				return
			}
			n := int(generations.Add(1))
			if n > 10 {
				t.Error("extra generation")
				w.WriteHeader(500)
				return
			}
			if p.Messages[1].Content != expectedUsers[n-1] {
				t.Error("user payload contains something other than input")
			}
			a := active.Add(1)
			if a > maxActive.Load() {
				maxActive.Store(a)
			}
			defer active.Add(-1)
			time.Sleep(time.Millisecond)
			content, _ := json.Marshal(map[string]any{"summary": "Контролируемый ответ mock.", "source_message_ids": []string{fmt.Sprintf("m%d", n)}})
			json.NewEncoder(w).Encode(map[string]any{"done": true, "done_reason": "stop", "message": map[string]string{"content": string(content)}})
		default:
			w.WriteHeader(501)
		}
	}))
	defer srv.Close()
	t.Chdir(fixture)
	t.Setenv("BRIEFING_BUDGET_PYTHON", python)
	t.Setenv("BRIEFING_BUDGET_HELPER", script)
	t.Setenv("BRIEFING_TOKENIZER_DIR", resources)
	t.Setenv("OLLAMA_BASE_URL", srv.URL)
	t.Setenv("OLLAMA_MODEL", "qwen3:8b")
	var output bytes.Buffer
	if err := run(nil, &output); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 0 || strings.Count(output.String(), `"input_scope":"development"`) != 10 {
		t.Fatal("default must be development/offline")
	}
	output.Reset()
	if err := run([]string{"--set", "holdout", "--run"}, &output); err != nil {
		var last report
		for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
			_ = json.Unmarshal([]byte(line), &last)
		}
		if last.Error != nil {
			t.Fatalf("%v: %s", err, *last.Error)
		}
		t.Fatal(err)
	}
	if generations.Load() != 10 || maxActive.Load() != 1 || strings.Contains(output.String(), "PRIVATE_ANNOTATION_SENTINEL") {
		t.Fatal("isolation/sequential guarantee failed")
	}
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		var entry report
		if json.Unmarshal([]byte(line), &entry) != nil || entry.InputScope != "regression" || entry.DatasetVersion != "isolation-fixture" || entry.PromptVersion != diagnostic.SummaryPromptVersion || entry.GenerationAttempts != 1 || entry.Error != nil {
			t.Fatal("holdout report")
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
	for _, set := range []string{"examples", "development", "holdout"} {
		var output bytes.Buffer
		if err := run([]string{"--set", set}, &output); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(output.String(), "PRIVATE_TOKEN") || (set != "holdout" && strings.Contains(output.String(), "hol-")) || strings.Contains(output.String(), "expected_topics") {
			t.Fatal("token, unintended holdout or annotation leaked")
		}
		lines := strings.Split(strings.TrimSpace(output.String()), "\n")
		count := 2
		if set != "examples" {
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
