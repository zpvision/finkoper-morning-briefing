// Offline plan unless --check-profile or --run explicitly permits HTTP.
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
	"time"

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
func run(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("evaluate-batch", flag.ContinueOnError)
	execute := flags.Bool("run", false, "enable sequential generations without retries")
	split := flags.String("split", "development", "development or holdout; holdout only for final evaluation")
	mode := flags.String("mode", "think-false", "think-false or think-true")
	profile := flags.String("profile", evaluation.Profile8B, "explicit diagnostic profile")
	profileResources := flags.String("profile-resources", "", "separate 14B resource directory; never changes 8B configuration")
	check := flags.Bool("check-profile", false, "explicit network permission: metadata and exact render-only, no generation")
	probe := flags.Bool("probe", false, "select only the first development case; --run enables one generation")
	root := flags.String("dataset", "evaluation", "synthetic dataset directory")
	out := flags.String("out", "", "JSONL file; --run default evaluation/reports/<timestamp>.jsonl")
	if flags.Parse(args) != nil || flags.NArg() != 0 || (*mode != "think-false" && *mode != "think-true") {
		return errors.New("invalid_batch_arguments")
	}
	if (*check && *execute) || (*probe && *split != "development") {
		return errors.New("invalid_batch_check_or_probe")
	}
	if *profile != evaluation.Profile8B && *profile != evaluation.Profile14B {
		return errors.New("unknown_diagnostic_profile")
	}
	if *out != "" && filepath.Ext(*out) != ".jsonl" {
		return errors.New("batch_report_requires_jsonl")
	}
	dataset, err := evaluation.Load(*root, *split)
	if err != nil {
		return err
	}
	if *probe {
		dataset.Cases = dataset.Cases[:1]
	}
	counter, err := briefing.NewPythonCounter()
	if err != nil {
		return err
	}
	if err := evaluation.ConfigureProfile(counter, *profile, *profileResources, *mode, *split); err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(counter.Resources, "manifest.json"))
	if err != nil || len(raw) > briefing.MaxBody {
		return errors.New("invalid_tokenizer_manifest")
	}
	var metadata struct {
		Digest string `json:"model_digest"`
	}
	// Manifest has other pinned fields, validated independently by the helper.
	if json.Unmarshal(raw, &metadata) != nil || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(metadata.Digest) {
		return errors.New("invalid_tokenizer_manifest")
	}
	runner := diagnostic.NewRunner(counter.BaseURL, counter.Model, 120*time.Second, counter)
	if *mode == "think-true" {
		counter.DiagnosticThinking = true
		runner = diagnostic.NewThinkingRunner(counter.BaseURL, counter)
	}
	runner.VerifyOnly = *check
	output := stdout
	var reportFile *os.File
	if *execute && *out == "" {
		*out = filepath.Join("evaluation", "reports", time.Now().UTC().Format("20060102T150405.000000000")+"-"+*split+"-"+*mode+".jsonl")
	}
	if *out != "" {
		if os.MkdirAll(filepath.Dir(*out), 0700) != nil {
			return errors.New("batch_report_directory_failed")
		}
		file, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return errors.New("batch_report_create_failed")
		}
		defer file.Close()
		output = file
		reportFile = file
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runErr := evaluation.Run(ctx, dataset, runner, metadata.Digest, diagnostic.Hash(string(raw)), *execute, output)
	if reportFile != nil && reportFile.Sync() != nil {
		return errors.New("batch_report_sync_failed")
	}
	return runErr
}
