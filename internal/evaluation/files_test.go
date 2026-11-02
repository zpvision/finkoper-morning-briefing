package evaluation

import (
	"bytes"
	"context"
	"encoding/json"
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
)

func fileInputs(t *testing.T) []FileInput {
	t.Helper()
	inputs, err := LoadFiles([]string{"../../examples/04-exact-20.json", "../../examples/02.json"})
	if err != nil {
		t.Fatal(err)
	}
	return inputs
}

func TestLoadStandaloneFiles(t *testing.T) {
	inputs := fileInputs(t)
	if inputs[0].File != "../../examples/04-exact-20.json" || len(inputs[0].Input.Messages) != 20 || inputs[0].Hash == "" || len(inputs) != 2 {
		t.Fatal("wrong files")
	}
	if _, err := LoadFiles([]string{"../../examples/02.json", "../../examples/../examples/02.json"}); err == nil {
		t.Fatal("duplicate file accepted")
	}
	dir := t.TempDir()
	bad := filepath.Join(dir, "invalid.json")
	os.WriteFile(bad, []byte(`{"PRIVATE_TEXT":`), 0600)
	huge := filepath.Join(dir, "huge.json")
	os.WriteFile(huge, bytes.Repeat([]byte(" "), briefing.MaxBody+1), 0600)
	for _, paths := range [][]string{nil, {bad}, {huge}, {dir}, {"missing.json"}} {
		if _, err := LoadFiles(paths); err == nil || strings.Contains(err.Error(), "PRIVATE_TEXT") {
			t.Fatal("unsafe or missing validation")
		}
	}
}

func TestStandaloneTransportSequentialAndNoSplit(t *testing.T) {
	inputs := fileInputs(t)
	count := &counter{reserve: 1500, tokens: 100}
	var active, maxActive, calls atomic.Int32
	order := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Model           string
			Think, Stream   bool
			Keep            string `json:"keep_alive"`
			Debug           bool   `json:"_debug_render_only"`
			Messages        []struct{ Role, Content string }
			Options         map[string]float64
			Format          json.RawMessage
			Shift, Truncate bool
		}
		if json.NewDecoder(r.Body).Decode(&payload) != nil {
			t.Error("payload")
		}
		if payload.Model != Model14B || payload.Think || payload.Stream || payload.Shift || payload.Truncate || payload.Keep != "30m" || payload.Options["temperature"] != 0 || payload.Options["num_ctx"] != 8192 || payload.Options["num_predict"] != 1500 || string(payload.Format) != briefing.OutputSchema || r.Header.Get("Authorization") != "" {
			t.Error("parameters changed")
		}
		if len(payload.Messages) != 2 || payload.Messages[0].Role != "system" || payload.Messages[0].Content != briefing.SystemPrompt || payload.Messages[1].Role != "user" {
			t.Error("prompt changed")
		}
		if payload.Debug {
			json.NewEncoder(w).Encode(map[string]any{"_debug_info": map[string]string{"rendered_template": diagnostic.ExpectedRender(payload.Messages[0].Content, payload.Messages[1].Content)}})
			return
		}
		n := active.Add(1)
		if n > maxActive.Load() {
			maxActive.Store(n)
		}
		defer active.Add(-1)
		calls.Add(1)
		order = append(order, diagnostic.Hash(payload.Messages[1].Content))
		time.Sleep(time.Millisecond)
		json.NewEncoder(w).Encode(map[string]any{"done": true, "done_reason": "stop", "message": map[string]string{"content": `{"topics":[]}`, "thinking": "PRIVATE_REASONING"}, "prompt_eval_count": 100, "eval_count": 15})
	}))
	defer srv.Close()
	var output bytes.Buffer
	if err := RunFiles(context.Background(), inputs, diagnostic.NewRunner(srv.URL, Model14B, 120*time.Second, count), "digest", "manifest-hash", true, &output); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || maxActive.Load() != 1 || count.verifications != 2 {
		t.Fatal("not one sequential generation per file")
	}
	if strings.Contains(output.String(), "PRIVATE_REASONING") || strings.Contains(output.String(), `"split"`) || strings.Contains(output.String(), `"dataset_version"`) {
		t.Fatal("reasoning or false dataset assignment")
	}
	decoder := json.NewDecoder(&output)
	for i, input := range inputs {
		var report FileReport
		if decoder.Decode(&report) != nil {
			t.Fatal("decode")
		}
		_, user := briefing.PromptInput(input.Input)
		if order[i] != diagnostic.Hash(user) || report.File != input.File || report.InputFileHash != input.Hash || report.ModelDigest == nil || report.InputScope != "standalone_files" || report.SchemaVersion != "topics-v1" || report.PromptVersion != "morning-v3" || report.GenerationAttempts != 1 || report.Metrics.InputTokens == nil {
			t.Fatal("incorrect report")
		}
	}
}

func TestStandaloneOfflineAndFailClosed(t *testing.T) {
	for _, mode := range []string{"plan", "budget", "verify"} {
		count := &counter{reserve: 1500, tokens: 100, fail: mode == "verify"}
		if mode == "budget" {
			count.tokens = 8000
		}
		runner := diagnostic.NewRunner("http://127.0.0.1:1", Model14B, time.Second, count)
		var output bytes.Buffer
		err := RunFiles(context.Background(), fileInputs(t), runner, "digest", "hash", mode != "plan", &output)
		if (err != nil) != (mode != "plan") {
			t.Fatal("preflight result")
		}
		if strings.Contains(output.String(), `"generation_attempts":1`) {
			t.Fatal("unexpected generation")
		}
		if mode == "plan" && count.verifications != 0 {
			t.Fatal("offline mode verified remotely")
		}
	}
}

func TestStandaloneIncompleteOrInvalidSourcesNeverRetried(t *testing.T) {
	for _, mode := range []string{"length", "sources"} {
		t.Run(mode, func(t *testing.T) {
			count := &counter{reserve: 1500, tokens: 100}
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var p struct {
					Debug    bool `json:"_debug_render_only"`
					Messages []struct{ Content string }
				}
				json.NewDecoder(r.Body).Decode(&p)
				if p.Debug {
					json.NewEncoder(w).Encode(map[string]any{"_debug_info": map[string]string{"rendered_template": diagnostic.ExpectedRender(p.Messages[0].Content, p.Messages[1].Content)}})
					return
				}
				calls++
				reason := "stop"
				content := `{"topics":[{"text":"request","status":"unclear","deadline_text":null,"needs_context":false,"source_message_ids":["missing"]}]}`
				if mode == "length" {
					reason = "length"
					content = `{"topics":[]}`
				}
				json.NewEncoder(w).Encode(map[string]any{"done": true, "done_reason": reason, "message": map[string]string{"content": content}})
			}))
			defer srv.Close()
			var output bytes.Buffer
			err := RunFiles(context.Background(), fileInputs(t), diagnostic.NewRunner(srv.URL, Model14B, time.Second, count), "digest", "hash", true, &output)
			if err == nil || calls != 2 {
				t.Fatal("accepted failure or retried")
			}
			for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
				var report FileReport
				json.Unmarshal([]byte(line), &report)
				if string(report.Result) != "null" || !diagnostic.Failed(report.Record) {
					t.Fatal("invalid output accepted")
				}
			}
		})
	}
}
