package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"finkoper-morning-briefing/internal/briefing"
	"finkoper-morning-briefing/internal/diagnostic"
	"finkoper-morning-briefing/internal/evaluation"
)

func TestExplicitProfileAndAction(t *testing.T) {
	for _, args := range [][]string{nil, {"--profile", "qwen3-8b"}, {"--profile", diagnostic.Qwen35Profile, "--check-profile", "--run"}, {"--profile", diagnostic.Qwen35Profile, "--case", "unknown"}} {
		if run(args, &bytes.Buffer{}) == nil {
			t.Fatal("unsafe arguments accepted")
		}
	}
	for _, name := range []string{"", "invalid"} {
		if run([]string{"--profile", diagnostic.Qwen35Profile, "--mode", name}, &bytes.Buffer{}) == nil {
			t.Fatal("invalid mode")
		}
	}
}

func TestActualQwen35CPUHelperLocalRuntimeMock(t *testing.T) {
	python := os.Getenv("BRIEFING_TEST_BUDGET_PYTHON")
	if python == "" {
		t.Skip("opt-in separate local Qwen3.5 resources")
	}
	root, _ := filepath.Abs("../..")
	if _, err := os.Stat(filepath.Join(root, "resources/tokenizer/qwen35-9b/manifest.json")); err != nil {
		t.Skip("prepare Qwen3.5 resources")
	}
	t.Chdir(root)
	t.Setenv("BRIEFING_BUDGET_PYTHON", python)
	t.Setenv("OLLAMA_MODEL", diagnostic.Qwen35Model)
	t.Setenv("OLLAMA_BASE_URL", "http://127.0.0.1:1")
	// Existing 8B configuration is deliberately invalid: the separate factory must not use it.
	t.Setenv("BRIEFING_TOKENIZER_DIR", "not-8b-resources")
	t.Setenv("BRIEFING_BUDGET_HELPER", "not-8b-helper")
	var output bytes.Buffer
	args := []string{"--profile", diagnostic.Qwen35Profile, "--case", "six"}
	if err := run(args, &output); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 6 {
		t.Fatal("six cases")
	}
	var plan report
	if json.Unmarshal([]byte(lines[0]), &plan) != nil || plan.GenerationAttempts != 0 || plan.Budget.Verified || plan.Budget.Method != diagnostic.Qwen35BudgetMethod || plan.PromptVersion != diagnostic.SummaryPromptVersionV2 {
		t.Fatal("plan")
	}
	raw, err := os.ReadFile("qwen35-9b-show.json")
	if err != nil {
		t.Fatal(err)
	}
	var show map[string]any
	json.Unmarshal(raw, &show)
	info := show["model_info"].(map[string]any)
	for key := range info {
		if strings.HasPrefix(key, "tokenizer.ggml.") && (strings.HasSuffix(key, "tokens") || strings.HasSuffix(key, "merges") || strings.HasSuffix(key, "token_type")) {
			delete(info, key)
		}
	}
	show["modelfile"] = strings.Split(show["modelfile"].(string), "LICENSE")[0]
	manifest, _ := diagnostic.Qwen35PinnedManifest()
	generations := 0
	mode := "ok"
	generationMode := "baseline"
	var eventLock sync.Mutex
	var events []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		eventLock.Lock()
		events = append(events, mode+":"+r.URL.Path)
		eventLock.Unlock()
		switch r.URL.Path {
		case "/api/version":
			json.NewEncoder(w).Encode(map[string]string{"version": "0.40.5"})
		case "/api/tags":
			digest := manifest.Digest
			if mode == "digest" {
				digest = "wrong"
			}
			json.NewEncoder(w).Encode(map[string]any{"models": []map[string]string{{"name": diagnostic.Qwen35Model, "digest": digest}}})
		case "/api/show":
			// Consume the POST body before replying, as Ollama's JSON binding does.
			// Otherwise Windows can reset urllib's connection on an early response.
			if _, err := io.Copy(io.Discard, r.Body); err != nil {
				t.Error(err)
			}
			json.NewEncoder(w).Encode(show)
		case "/api/chat":
			var p struct {
				Debug    bool `json:"_debug_render_only"`
				Think    bool
				Options  map[string]float64
				Messages []struct{ Content string }
			}
			json.NewDecoder(r.Body).Decode(&p)
			config, _ := diagnostic.Qwen35Configuration(generationMode)
			if p.Think != config.Thinking || p.Options["num_ctx"] != 8192 || p.Options["num_predict"] != float64(config.Reserve) || p.Options["temperature"] != config.Options["temperature"].(float64) || p.Options["top_p"] != config.Options["top_p"].(float64) || p.Options["presence_penalty"] != config.Options["presence_penalty"].(float64) || p.Messages[0].Content != diagnostic.SummaryPromptV2 {
				t.Error("request mismatch")
			}
			if p.Debug {
				if mode == "missing" {
					json.NewEncoder(w).Encode(map[string]any{"_debug_info": nil})
					return
				}
				rendered := diagnostic.Qwen35Render(p.Messages[0].Content, p.Messages[1].Content, config.Thinking)
				if mode == "passthrough" {
					rendered = "{{ .Prompt }}"
				}
				if mode == "wrong_thinking" {
					rendered = diagnostic.Qwen35Render(p.Messages[0].Content, p.Messages[1].Content, false)
				}
				json.NewEncoder(w).Encode(map[string]any{"_debug_info": map[string]string{"rendered_template": rendered}})
				return
			}
			generations++
			var user struct{ Messages []struct{ ID string } }
			json.Unmarshal([]byte(p.Messages[1].Content), &user)
			content, _ := json.Marshal(map[string]any{"summary": "Контролируемый ответ mock, не эталон.", "source_message_ids": []string{user.Messages[0].ID}})
			count := plan.Budget.InputTokens
			if mode == "count_mismatch" {
				count++
			}
			json.NewEncoder(w).Encode(map[string]any{"done": true, "done_reason": "stop", "message": map[string]string{"content": string(content), "thinking": "PRIVATE_REASONING"}, "prompt_eval_count": count, "eval_count": 10})
		default:
			w.WriteHeader(501)
		}
	}))
	defer srv.Close()
	t.Setenv("OLLAMA_BASE_URL", srv.URL)
	expectedFailures := map[string]string{"missing": "qwen35_render_only_missing_rendered_template", "passthrough": "qwen35_runtime_renderer_mismatch", "digest": "qwen35_model_digest_mismatch"}
	for _, failure := range []string{"missing", "passthrough", "digest"} {
		mode = failure
		output.Reset()
		if err := run([]string{"--profile", diagnostic.Qwen35Profile, "--check-profile"}, &output); err == nil || generations != 0 {
			t.Fatal("compatibility bypass", failure)
		}
		var rejected report
		if json.Unmarshal(output.Bytes(), &rejected) != nil || rejected.Error == nil || *rejected.Error != expectedFailures[failure] {
			t.Fatalf("wrong rejection for %s: %s", failure, output.String())
		}
	}
	mode = "ok"
	output.Reset()
	if err := run([]string{"--profile", diagnostic.Qwen35Profile, "--check-profile"}, &output); err != nil {
		t.Fatalf("check: %v output=%s requests=%v", err, output.String(), events)
	}
	var checked report
	json.Unmarshal(output.Bytes(), &checked)
	if generations != 0 || checked.RuntimeTokenCountVerified != nil || !checked.Budget.Verified {
		t.Fatal("check-only overclaimed")
	}
	output.Reset()
	if err := run([]string{"--profile", diagnostic.Qwen35Profile, "--run"}, &output); err != nil {
		t.Fatalf("probe: %v output=%s requests=%v", err, output.String(), events)
	}
	var result report
	json.Unmarshal(output.Bytes(), &result)
	if generations != 1 || result.RuntimeTokenCountVerified == nil || !*result.RuntimeTokenCountVerified || strings.Contains(output.String(), "PRIVATE_REASONING") {
		t.Fatal("probe result")
	}
	mode = "count_mismatch"
	output.Reset()
	if err := run([]string{"--profile", diagnostic.Qwen35Profile, "--case", "six", "--run"}, &output); err == nil || generations != 2 || strings.Count(strings.TrimSpace(output.String()), "\n") != 0 {
		var failed report
		_ = json.Unmarshal(output.Bytes(), &failed)
		code := "none"
		if failed.Error != nil {
			code = *failed.Error
		}
		eventLock.Lock()
		defer eventLock.Unlock()
		t.Fatalf("count mismatch: err=%v generations=%d records=%d code=%s requests=%v", err, generations, strings.Count(strings.TrimSpace(output.String()), "\n")+1, code, events)
	}
	for _, name := range []string{"greedy", "thinking", "thinking-long"} {
		selected := "sr-01"
		if name == "thinking-long" {
			selected = "sr-06"
		}
		inputs, err := evaluation.LoadFiles([]string{casePaths[selected]})
		if err != nil {
			t.Fatal(err)
		}
		_, user := briefing.PromptInput(inputs[0].Input)
		generationMode, mode = name, "ok"
		counter, err := diagnostic.NewQwen35CounterMode("", name)
		if err != nil {
			t.Fatal(err)
		}
		plan.Budget, err = counter.Count(context.Background(), diagnostic.SummaryPromptV2, user)
		if err != nil {
			t.Fatal(err)
		}
		before := generations
		if name == "thinking" {
			mode = "wrong_thinking"
			output.Reset()
			err := run([]string{"--profile", diagnostic.Qwen35Profile, "--mode", name, "--case", selected, "--run"}, &output)
			var rejected report
			json.Unmarshal(output.Bytes(), &rejected)
			if err == nil || generations != before || rejected.Error == nil || *rejected.Error != "qwen35_runtime_renderer_mismatch" {
				t.Fatal("thinking helper accepted non-thinking renderer", output.String())
			}
			mode = "ok"
		}
		for _, action := range []string{"--check-profile", "--run"} {
			output.Reset()
			if err := run([]string{"--profile", diagnostic.Qwen35Profile, "--mode", name, "--case", selected, action}, &output); err != nil {
				t.Fatalf("%s %s: %v %s", name, action, err, output.String())
			}
			var result report
			if json.Unmarshal(output.Bytes(), &result) != nil || result.GenerationMode != name || strings.Contains(output.String(), "PRIVATE_REASONING") {
				t.Fatal("mode report")
			}
			if action == "--check-profile" && generations != before {
				t.Fatal("check generated")
			}
			if action == "--run" && (generations != before+1 || result.RuntimeTokenCountVerified == nil || !*result.RuntimeTokenCountVerified) {
				t.Fatal("mode count/retries")
			}
		}
	}
}
