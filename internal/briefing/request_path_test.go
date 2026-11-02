package briefing

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"text/template"
	"time"
)

type pathCounter struct {
	delegate                                         BudgetCounter
	countSystem, countUser, verifySystem, verifyUser string
}

func (c *pathCounter) Count(ctx context.Context, s, u string) (Budget, error) {
	c.countSystem, c.countUser = s, u
	return c.delegate.Count(ctx, s, u)
}
func (c *pathCounter) Verify(ctx context.Context, s, u string) error {
	c.verifySystem, c.verifyUser = s, u
	return c.delegate.Verify(ctx, s, u)
}

func TestProductionRequestPathAgainstExportedTemplate(t *testing.T) {
	python := localPythonCounter(t)
	raw, err := os.ReadFile("../../qwen3-show.json")
	if err != nil {
		t.Fatal(err)
	}
	var show struct {
		Template string `json:"template"`
	}
	if json.Unmarshal(raw, &show) != nil {
		t.Fatal("invalid export")
	}
	tmpl, err := template.New("actual_export").Parse(show.Template)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(python.Resources + "/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Digest string `json:"model_digest"`
	}
	_ = json.Unmarshal(raw, &manifest)
	type wire struct {
		debug        bool
		system, user string
		format       json.RawMessage
	}
	observed := make(chan wire, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/version":
			_ = json.NewEncoder(w).Encode(map[string]string{"version": "0.40.5"})
		case "/api/tags":
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]string{{"name": "qwen3:8b", "digest": manifest.Digest}}})
		case "/api/show":
			_ = json.NewEncoder(w).Encode(map[string]string{"template": show.Template})
		case "/api/chat":
			var p struct {
				Debug    bool                             `json:"_debug_render_only"`
				Messages []struct{ Role, Content string } `json:"messages"`
				Format   json.RawMessage                  `json:"format"`
			}
			if json.NewDecoder(r.Body).Decode(&p) != nil || len(p.Messages) != 2 || p.Messages[0].Role != "system" || p.Messages[1].Role != "user" {
				t.Error("lost roles")
				w.WriteHeader(400)
				return
			}
			if r.Header.Get("Authorization") != "" {
				t.Error("unexpected authorization")
			}
			observed <- wire{p.Debug, p.Messages[0].Content, p.Messages[1].Content, p.Format}
			if p.Debug {
				var rendered bytes.Buffer
				root := map[string]any{"System": p.Messages[0].Content, "Tools": nil, "Think": false, "IsThinkSet": true, "Messages": []map[string]string{{"Role": "system", "Content": p.Messages[0].Content}, {"Role": "user", "Content": p.Messages[1].Content}}}
				if err := tmpl.Execute(&rendered, root); err != nil {
					t.Error(err)
					w.WriteHeader(500)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"_debug_info": map[string]string{"rendered_template": rendered.String()}})
			} else {
				var input Input
				_ = json.Unmarshal([]byte(p.Messages[1].Content), &input)
				result := goodResult
				result = string(bytes.ReplaceAll([]byte(result), []byte("m1"), []byte(input.Messages[0].ID)))
				_ = json.NewEncoder(w).Encode(envelope(result, "stop", true))
			}
		default:
			w.WriteHeader(501)
		}
	}))
	defer srv.Close()
	python.BaseURL = srv.URL
	python.Model = "qwen3:8b"
	for _, file := range []string{"04-exact-20.json", "02.json"} {
		raw, err := os.ReadFile("../../examples/" + file)
		if err != nil {
			t.Fatal(err)
		}
		var in Input
		_ = json.Unmarshal(raw, &in)
		if in.Normalize() != nil {
			t.Fatal("invalid input")
		}
		counter := &pathCounter{delegate: python}
		s, u := PromptInput(in)
		if _, _, err := NewOllama(srv.URL, "qwen3:8b", time.Second, counter).Generate(context.Background(), in); err != nil {
			t.Fatal(err)
		}
		if s != SystemPrompt || counter.countSystem != s || counter.verifySystem != s || counter.countUser != u || counter.verifyUser != u {
			t.Fatal("PromptInput/helper mismatch")
		}
		render, generation := <-observed, <-observed
		if !render.debug || generation.debug || render.system != s || generation.system != s || render.user != u || generation.user != u {
			t.Fatal("render-only/generation mismatch")
		}
		if string(generation.format) != OutputSchema || len(render.format) != 0 {
			t.Fatal("unexpected format handling")
		}
	}
}
