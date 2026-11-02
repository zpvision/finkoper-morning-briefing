// Manual two-stage experiment. No network calls without --run.
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
	"time"

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
	flags := flag.NewFlagSet("diagnose-two-stage", flag.ContinueOnError)
	execute := flags.Bool("run", false, "manually enable one grouping + one final generation per input; otherwise offline grouping plan")
	if err := flags.Parse(args); err != nil {
		return errors.New("invalid_diagnostic_arguments")
	}
	files := flags.Args()
	if len(files) == 0 {
		files = []string{"examples/04-exact-20.json", "examples/02.json"}
	}
	inputs := make([]briefing.Input, len(files))
	for i, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil || len(raw) > briefing.MaxBody {
			return errors.New("diagnostic_input_file_invalid")
		}
		if briefing.Strict(raw, &inputs[i]) != nil || inputs[i].Normalize() != nil {
			return errors.New("diagnostic_input_file_invalid")
		}
	}
	counter, err := briefing.NewPythonCounter()
	if err != nil {
		return err
	}
	timeout := 120 * time.Second
	if value := os.Getenv("OLLAMA_TIMEOUT"); value != "" {
		timeout, err = time.ParseDuration(value)
		if err != nil || timeout <= 0 {
			return errors.New("invalid OLLAMA_TIMEOUT")
		}
	}
	runner := diagnostic.NewRunner(counter.BaseURL, counter.Model, timeout, counter)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	encoder := json.NewEncoder(output)
	failed := false
	for i, in := range inputs {
		report := runner.TwoStage(ctx, files[i], in, *execute)
		if err := encoder.Encode(report); err != nil {
			return errors.New("diagnostic_output_failed")
		}
		failed = failed || report.Error != nil
		if diagnostic.PreflightFailed(report.Grouping) || (report.Final != nil && diagnostic.PreflightFailed(*report.Final)) {
			return errors.New("two_stage_preflight_failed (see JSONL report; no retries)")
		}
		if ctx.Err() != nil {
			return errors.New("diagnostic_cancelled")
		}
	}
	if failed {
		return errors.New("two_stage_diagnostic_failed (see JSONL reports; no retries)")
	}
	return nil
}
