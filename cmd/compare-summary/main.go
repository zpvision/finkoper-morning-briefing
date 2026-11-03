// Frozen v2 versus v3, sequentially, with no network unless --run is explicit.
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
	"path/filepath"
	"regexp"
	"syscall"

	"finkoper-morning-briefing/internal/briefing"
	"finkoper-morning-briefing/internal/diagnostic"
	"finkoper-morning-briefing/internal/evaluation"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("compare-summary", flag.ContinueOnError)
	set := flags.String("set", "regressions", "regressions (8 new + 10 previously used holdout), new-regressions, previous-holdout, development, examples")
	execute := flags.Bool("run", false, "one v2 and one v3 generation per input, sequential, no retries")
	out := flags.String("out", "", "new .jsonl file, default stdout")
	if flags.Parse(args) != nil || flags.NArg() != 0 {
		return errors.New("invalid_summary_comparison_arguments")
	}
	if *out != "" && filepath.Ext(*out) != ".jsonl" {
		return errors.New("summary_comparison_report_requires_jsonl")
	}
	selection, err := evaluation.LoadSummaryComparison(".", *set)
	if err != nil {
		return err
	}
	counter, err := briefing.NewPythonCounter()
	if err != nil {
		return err
	}
	if counter.Model != "qwen3:8b" {
		return errors.New("summary_requires_qwen3_8b_think_false")
	}
	counter.DiagnosticSummary = true
	raw, err := os.ReadFile(filepath.Join(counter.Resources, "manifest.json"))
	if err != nil || len(raw) > briefing.MaxBody {
		return errors.New("invalid_tokenizer_manifest")
	}
	var metadata struct {
		Digest string `json:"model_digest"`
	}
	if json.Unmarshal(raw, &metadata) != nil || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(metadata.Digest) {
		return errors.New("invalid_tokenizer_manifest")
	}
	var file *os.File
	if *out != "" {
		if os.MkdirAll(filepath.Dir(*out), 0700) != nil {
			return errors.New("summary_comparison_report_directory_failed")
		}
		file, err = os.OpenFile(*out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return errors.New("summary_comparison_report_create_failed")
		}
		defer file.Close()
		output = file
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result := evaluation.CompareSummaries(ctx, selection, diagnostic.NewSummaryRunner(counter.BaseURL, counter), metadata.Digest, diagnostic.Hash(string(raw)), *execute, output)
	if file != nil && file.Sync() != nil {
		return errors.New("summary_comparison_report_sync_failed")
	}
	return result
}
