// Manual thinking experiment; default mode never accesses the network.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"finkoper-morning-briefing/internal/briefing"
	"finkoper-morning-briefing/internal/diagnostic"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("diagnose-thinking", flag.ContinueOnError)
	execute := flags.Bool("run", false, "enable at most two sequential generations; default: offline plan")
	if flags.Parse(args) != nil || flags.NArg() != 0 {
		return errors.New("invalid_diagnostic_arguments")
	}
	files := []string{"examples/04-exact-20.json", "examples/02.json"}
	inputs := make([]briefing.Input, len(files))
	for i, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil || len(raw) > briefing.MaxBody || briefing.Strict(raw, &inputs[i]) != nil || inputs[i].Normalize() != nil {
			return errors.New("diagnostic_input_file_invalid")
		}
	}
	counter, err := briefing.NewPythonCounter()
	if err != nil {
		return err
	}
	if counter.Model != "qwen3:8b" {
		return errors.New("thinking_experiment_requires_qwen3_8b")
	}
	counter.DiagnosticThinking = true
	runner := diagnostic.NewThinkingRunner(counter.BaseURL, counter)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	encoder := json.NewEncoder(output)
	failed := false
	for i, in := range inputs {
		report := runner.Compare(ctx, files[i], in, diagnostic.ThinkingVariant(), *execute)
		if encoder.Encode(report) != nil {
			return errors.New("diagnostic_output_failed")
		}
		failed = failed || diagnostic.Failed(report)
		if diagnostic.PreflightFailed(report) {
			return errors.New("thinking_preflight_failed (see JSONL report; no retries)")
		}
		if ctx.Err() != nil {
			return errors.New("diagnostic_cancelled")
		}
	}
	if failed {
		return diagnostic.ErrComparison
	}
	return nil
}
