// Manual only: one real generation per example, no retries and no reference-answer substitution.
package main

import (
	"context"
	"encoding/json"
	"finkoper-morning-briefing/internal/briefing"
	"fmt"
	"os"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) < 2 {
		return fmt.Errorf("usage: go run ./cmd/evaluate examples/01.json [examples/02.json]")
	}
	base := os.Getenv("OLLAMA_BASE_URL")
	if base == "" {
		base = "http://127.0.0.1:11435"
	}
	model := os.Getenv("OLLAMA_MODEL")
	if model == "" {
		model = "qwen3:8b"
	}
	timeout := 120 * time.Second
	if v := os.Getenv("OLLAMA_TIMEOUT"); v != "" {
		var err error
		timeout, err = time.ParseDuration(v)
		if err != nil || timeout <= 0 {
			return fmt.Errorf("invalid OLLAMA_TIMEOUT")
		}
	}
	o := briefing.NewOllama(base, model, timeout)
	for _, path := range os.Args[1:] {
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var in briefing.Input
		if err = briefing.Strict(b, &in); err != nil {
			return err
		}
		if err = in.Normalize(); err != nil {
			return err
		}
		start := time.Now()
		result, metrics, err := o.Generate(context.Background(), in)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		out := map[string]any{"file": path, "result": result, "metrics": metrics, "elapsed_ms": time.Since(start).Milliseconds(), "prompt_version": briefing.PromptVersion, "schema_version": briefing.SchemaVersion}
		b, _ = json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
	}
	return nil
}
