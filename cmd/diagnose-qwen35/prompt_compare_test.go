package main

import (
	"bytes"
	"encoding/json"
	"finkoper-morning-briefing/internal/diagnostic"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func Test27V2V5Comparison(t *testing.T) {
	if diagnostic.Hash(diagnostic.SummaryPromptV2) != "8cc6dc4d3fad8eba8da327ecf9837aaa0213c6f978678e65c4ad105464d8ff31" {
		t.Fatal("v2 changed")
	}
	if !strings.HasPrefix(diagnostic.SummaryPromptV5, diagnostic.SummaryPromptV2) || diagnostic.SummaryPromptV5 == diagnostic.SummaryPromptV2 {
		t.Fatal("not a separate v2 derivative")
	}
	root, _ := filepath.Abs("../..")
	raw, err := os.ReadFile(filepath.Join(root, casePaths["sr-01"]))
	if err != nil {
		t.Fatal(err)
	}
	var base map[string]any
	json.Unmarshal(raw, &base)
	dir := t.TempDir()
	for n := 1; n <= 6; n++ {
		id := fmt.Sprintf("syn-%02d", n)
		base["client_id"] = id
		data, _ := json.Marshal(base)
		os.WriteFile(filepath.Join(dir, id+".json"), data, 0600)
	}
	// The unselected rest must not even be parsed.
	os.WriteFile(filepath.Join(dir, "syn-07.json"), []byte("INVALID_UNSELECTED"), 0600)
	os.WriteFile(filepath.Join(dir, "review.md"), []byte("PRIVATE_REVIEW"), 0600)
	calls := 0
	order := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			Model    string
			Think    bool
			Debug    bool `json:"_debug_render_only"`
			Options  map[string]float64
			Messages []struct{ Content string }
			Format   json.RawMessage
		}
		json.NewDecoder(r.Body).Decode(&p)
		system, user := p.Messages[0].Content, p.Messages[1].Content
		if p.Model != diagnostic.Qwen27Model || p.Think || p.Options["num_predict"] != 1000 || p.Options["temperature"] != .7 || string(p.Format) != diagnostic.SummarySchema || strings.Contains(user, "PRIVATE_REVIEW") {
			t.Error("transport/config")
		}
		if system != diagnostic.SummaryPromptV2 && system != diagnostic.SummaryPromptV5 {
			t.Error("unexpected prompt")
		}
		if p.Debug {
			json.NewEncoder(w).Encode(map[string]any{"_debug_info": map[string]string{"rendered_template": diagnostic.Qwen35Render(system, user)}})
			return
		}
		calls++
		version := "v2"
		if system == diagnostic.SummaryPromptV5 {
			version = "v5"
		}
		var input struct {
			Client   string `json:"client_id"`
			Messages []struct{ ID string }
		}
		json.Unmarshal([]byte(user), &input)
		order = append(order, input.Client+":"+version)
		content, _ := json.Marshal(map[string]any{"summary": "Ответ mock.", "source_message_ids": []string{input.Messages[0].ID}})
		json.NewEncoder(w).Encode(map[string]any{"done": true, "done_reason": "stop", "prompt_eval_count": 100, "message": map[string]string{"content": string(content), "thinking": "PRIVATE_REASONING"}})
	}))
	defer srv.Close()
	factory := func(string, string) (*diagnostic.Runner, error) {
		return diagnostic.NewQwen27Runner(srv.URL, modeCounter{1000}), nil
	}
	args := []string{"--profile", diagnostic.Qwen27Profile, "--input-dir", dir, "--case", "first-six", "--prompt", "compare-v2-v5"}
	var output bytes.Buffer
	if err := runWithRunner(args, &output, factory, factory); err != nil || calls != 0 || len(strings.Split(strings.TrimSpace(output.String()), "\n")) != 12 {
		t.Fatal("offline comparison", err)
	}
	output.Reset()
	if err := runWithRunner(append(args, "--run"), &output, factory, factory); err != nil || calls != 12 {
		t.Fatal("comparison", err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 12 || strings.Contains(output.String(), "PRIVATE_REASONING") {
		t.Fatal("count/privacy")
	}
	for i := 0; i < 6; i++ {
		var a, b report
		json.Unmarshal([]byte(lines[2*i]), &a)
		json.Unmarshal([]byte(lines[2*i+1]), &b)
		id := fmt.Sprintf("syn-%02d", i+1)
		if order[2*i] != id+":v2" || order[2*i+1] != id+":v5" || a.PromptVersion != diagnostic.SummaryPromptVersionV2 || b.PromptVersion != diagnostic.SummaryPromptVersionV5 || a.PromptHash == b.PromptHash || a.UserHash != b.UserHash || a.FormatHash != b.FormatHash || a.ModeConfigHash != b.ModeConfigHash || a.InputFileHash != b.InputFileHash {
			t.Fatal("comparison identity/order")
		}
	}
	bad := append(append([]string{}, args...), "--case", "remaining", "--reviewed-first", "--run")
	if runWithRunner(bad, &bytes.Buffer{}, factory, factory) == nil || calls != 12 {
		t.Fatal("remaining comparison allowed")
	}
}
