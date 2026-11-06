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

func TestExternalInputDirectoryHTTP(t *testing.T) {
	root, _ := filepath.Abs("../..")
	raw, err := os.ReadFile(filepath.Join(root, casePaths["sr-01"]))
	if err != nil {
		t.Fatal(err)
	}
	var base map[string]any
	json.Unmarshal(raw, &base)
	dir := t.TempDir()
	for n := 1; n <= 24; n++ {
		id := fmt.Sprintf("syn-%02d", n)
		base["client_id"] = id
		data, _ := json.Marshal(base)
		if err := os.WriteFile(filepath.Join(dir, id+".json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(dir, "review.md"), []byte("MANUAL_REVIEW_ONLY"), 0600)
	generations := []string{}
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var p struct {
			Model    string
			Think    bool
			Debug    bool `json:"_debug_render_only"`
			Messages []struct{ Content string }
			Options  map[string]float64
			Format   json.RawMessage
		}
		json.NewDecoder(r.Body).Decode(&p)
		if p.Model != diagnostic.Qwen27Model || p.Think || p.Messages[0].Content != diagnostic.SummaryPromptV2 || string(p.Format) != diagnostic.SummarySchema || strings.Contains(p.Messages[1].Content, "MANUAL_REVIEW_ONLY") || p.Options["num_predict"] != 1000 || p.Options["temperature"] != .7 {
			t.Error("request changed/annotations leaked")
		}
		if p.Debug {
			json.NewEncoder(w).Encode(map[string]any{"_debug_info": map[string]string{"rendered_template": diagnostic.Qwen35Render(p.Messages[0].Content, p.Messages[1].Content)}})
			return
		}
		var input struct {
			Client   string `json:"client_id"`
			Messages []struct{ ID string }
		}
		json.Unmarshal([]byte(p.Messages[1].Content), &input)
		generations = append(generations, input.Client)
		content, _ := json.Marshal(map[string]any{"summary": "Ответ mock.", "source_message_ids": []string{input.Messages[0].ID}})
		json.NewEncoder(w).Encode(map[string]any{"done": true, "done_reason": "stop", "prompt_eval_count": 100, "message": map[string]string{"content": string(content), "thinking": "PRIVATE_EXTERNAL"}})
	}))
	defer srv.Close()
	factory := func(string, string) (*diagnostic.Runner, error) {
		return diagnostic.NewQwen27Runner(srv.URL, modeCounter{1000}), nil
	}
	args := []string{"--profile", diagnostic.Qwen27Profile, "--input-dir", dir}
	var out bytes.Buffer
	if err := runWithRunner(args, &out, factory, factory); err != nil || requests != 0 || len(strings.Split(strings.TrimSpace(out.String()), "\n")) != 6 {
		t.Fatal("default offline first six", err)
	}
	out.Reset()
	if err := runWithRunner(append(args, "--run"), &out, factory, factory); err != nil || len(generations) != 6 {
		t.Fatal("first six", err)
	}
	if generations[0] != "syn-01" || generations[5] != "syn-06" || strings.Contains(out.String(), "PRIVATE_EXTERNAL") {
		t.Fatal("order/privacy")
	}
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var entry report
		json.Unmarshal([]byte(line), &entry)
		if entry.AssessmentRole != "synthetic_external" || entry.InputFileHash == "" || !filepath.IsAbs(entry.File) {
			t.Fatal("file metadata")
		}
	}
	rest := append(append([]string{}, args...), "--case", "remaining", "--run")
	if runWithRunner(rest, &bytes.Buffer{}, factory, factory) == nil || len(generations) != 6 {
		t.Fatal("review gate bypass")
	}
	if err := runWithRunner(append(rest, "--reviewed-first"), &bytes.Buffer{}, factory, factory); err != nil || len(generations) != 24 || generations[6] != "syn-07" || generations[23] != "syn-24" {
		t.Fatal("remaining", err)
	}
	os.WriteFile(filepath.Join(dir, "syn-03.json"), []byte("invalid"), 0600)
	before := requests
	if runWithRunner(append(args, "--run"), &bytes.Buffer{}, factory, factory) == nil || requests != before {
		t.Fatal("invalid input reached HTTP")
	}
}
