package main

import (
	"bytes"
	"encoding/json"
	"finkoper-morning-briefing/internal/diagnostic"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func Test27ActualHelperMock(t *testing.T) {
	python := os.Getenv("BRIEFING_TEST_BUDGET_PYTHON")
	if python == "" {
		t.Skip("opt-in local CPU resources")
	}
	root, _ := filepath.Abs("../..")
	t.Chdir(root)
	t.Setenv("BRIEFING_BUDGET_PYTHON", python)
	t.Setenv("OLLAMA_MODEL", diagnostic.Qwen27Model)
	t.Setenv("OLLAMA_BASE_URL", "http://127.0.0.1:1")
	var out bytes.Buffer
	args := []string{"--profile", diagnostic.Qwen27Profile, "--case", "six"}
	if err := run(args, &out); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 6 {
		t.Fatal("plan")
	}
	for _, line := range lines {
		var r report
		json.Unmarshal([]byte(line), &r)
		counts[r.UserHash] = r.Budget.InputTokens
		if r.GenerationAttempts != 0 || r.Model != diagnostic.Qwen27Model {
			t.Fatal("offline plan")
		}
	}
	for _, a := range [][]string{{"--mode", "thinking"}, {"--run"}} {
		bad := append(append([]string{}, args...), a...)
		if run(bad, &bytes.Buffer{}) == nil {
			t.Fatal("unsafe selection")
		}
	}
	raw, err := os.ReadFile("qwen27-show.json")
	if err != nil {
		t.Fatal(err)
	}
	var show map[string]any
	json.Unmarshal(raw, &show)
	info := show["model_info"].(map[string]any)
	for _, k := range []string{"tokenizer.ggml.tokens", "tokenizer.ggml.token_type", "tokenizer.ggml.merges"} {
		delete(info, k)
	}
	m, _ := diagnostic.Qwen27PinnedManifest()
	generations := 0
	scenario := "ok"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/version":
			json.NewEncoder(w).Encode(map[string]string{"version": "0.40.15"})
		case "/api/tags":
			json.NewEncoder(w).Encode(map[string]any{"models": []map[string]string{{"name": m.Model, "digest": m.Digest}}})
		case "/api/show":
			io.Copy(io.Discard, r.Body)
			json.NewEncoder(w).Encode(show)
		case "/api/chat":
			var p struct {
				Model    string
				Think    bool
				Debug    bool `json:"_debug_render_only"`
				Options  map[string]float64
				Messages []struct{ Content string }
				Format   json.RawMessage
			}
			json.NewDecoder(r.Body).Decode(&p)
			if p.Model != m.Model || p.Think || p.Options["temperature"] != .7 || p.Options["num_predict"] != 1000 || p.Options["num_ctx"] != 8192 || p.Options["presence_penalty"] != 1.5 || p.Messages[0].Content != diagnostic.SummaryPromptV2 || string(p.Format) != "" && string(p.Format) != diagnostic.SummarySchema {
				t.Error("request changed")
			}
			if p.Debug {
				if scenario == "missing" {
					json.NewEncoder(w).Encode(map[string]any{})
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"_debug_info": map[string]string{"rendered_template": diagnostic.Qwen35Render(p.Messages[0].Content, p.Messages[1].Content)}})
				return
			}
			generations++
			var user struct{ Messages []struct{ ID string } }
			json.Unmarshal([]byte(p.Messages[1].Content), &user)
			content, _ := json.Marshal(map[string]any{"summary": "Ответ mock.", "source_message_ids": []string{user.Messages[0].ID}})
			count := counts[diagnostic.Hash(p.Messages[1].Content)]
			if scenario == "mismatch" {
				count++
			}
			json.NewEncoder(w).Encode(map[string]any{"done": true, "done_reason": "stop", "prompt_eval_count": count, "message": map[string]string{"content": string(content), "thinking": "PRIVATE27"}})
		default:
			w.WriteHeader(501)
		}
	}))
	defer srv.Close()
	t.Setenv("OLLAMA_BASE_URL", srv.URL)
	base := []string{"--profile", diagnostic.Qwen27Profile, "--case", "sr-06"}
	out.Reset()
	if err := run(append(base, "--check-profile"), &out); err != nil || generations != 0 {
		t.Fatal("check", err, out.String())
	}
	out.Reset()
	if err := run(append(base, "--run"), &out); err != nil || generations != 1 || strings.Contains(out.String(), "PRIVATE27") {
		t.Fatal("probe", err, out.String())
	}
	probe := filepath.Join(t.TempDir(), "probe.jsonl")
	os.WriteFile(probe, out.Bytes(), 0600)
	var good report
	json.Unmarshal(out.Bytes(), &good)
	badPath := filepath.Join(t.TempDir(), "bad.jsonl")
	for _, kind := range []string{"error", "digest", "parameters"} {
		bad := good
		if kind == "error" {
			code := "ollama_incomplete_response"
			bad.Error = &code
		}
		if kind == "digest" {
			wrong := "wrong"
			bad.ModelDigest = &wrong
		}
		if kind == "parameters" {
			bad.Budget.OutputReserve = 4096
		}
		raw, _ := json.Marshal(bad)
		os.WriteFile(badPath, raw, 0600)
		if validate27Probe(badPath) == nil {
			t.Fatal("bad probe accepted", kind)
		}
	}
	rest := []string{"--profile", diagnostic.Qwen27Profile, "--case", "remaining", "--probe-report", probe, "--run"}
	out.Reset()
	if err := run(rest, &out); err != nil || generations != 6 || len(strings.Split(strings.TrimSpace(out.String()), "\n")) != 5 {
		t.Fatal("remaining", err, out.String())
	}
	scenario = "missing"
	out.Reset()
	if err := run(rest, &out); err == nil || generations != 6 {
		t.Fatal("render bypass")
	}
	scenario = "mismatch"
	out.Reset()
	if err := run(rest, &out); err == nil || generations != 7 || !strings.Contains(out.String(), "qwen35_runtime_token_count_mismatch") {
		t.Fatal("count mismatch bypass", out.String())
	}
}
