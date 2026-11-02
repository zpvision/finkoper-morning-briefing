// Standalone files on the pinned 14B profile. No HTTP without --run.
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
func run(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("diagnose-files", flag.ContinueOnError)
	profile := flags.String("profile", "", "required: qwen3-14b-q4-k-m-v1")
	resources := flags.String("profile-resources", "", "separate 14B resource directory")
	execute := flags.Bool("run", false, "explicitly permit one sequential generation per input; no retries")
	out := flags.String("out", "", "new JSONL file; default stdout")
	if flags.Parse(args) != nil {
		return errors.New("invalid_diagnostic_arguments")
	}
	if *profile != evaluation.Profile14B {
		return errors.New("standalone_files_require_explicit_14b_profile")
	}
	if *out != "" && filepath.Ext(*out) != ".jsonl" {
		return errors.New("diagnostic_report_requires_jsonl")
	}
	inputs, err := evaluation.LoadFiles(flags.Args())
	if err != nil {
		return err
	}
	counter, err := briefing.NewPythonCounter()
	if err != nil {
		return err
	}
	if err := evaluation.ConfigureFileProfile(counter, *profile, *resources); err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(counter.Resources, "manifest.json"))
	if err != nil {
		return errors.New("invalid_tokenizer_manifest")
	}
	var metadata struct {
		Digest string `json:"model_digest"`
	}
	if json.Unmarshal(raw, &metadata) != nil {
		return errors.New("invalid_tokenizer_manifest")
	}
	runner := diagnostic.NewRunner(counter.BaseURL, counter.Model, 120*time.Second, counter)
	var reportFile *os.File
	if *out != "" {
		if os.MkdirAll(filepath.Dir(*out), 0700) != nil {
			return errors.New("diagnostic_report_directory_failed")
		}
		reportFile, err = os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return errors.New("diagnostic_report_create_failed")
		}
		defer reportFile.Close()
		output = reportFile
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result := evaluation.RunFiles(ctx, inputs, runner, metadata.Digest, diagnostic.Hash(string(raw)), *execute, output)
	if reportFile != nil && reportFile.Sync() != nil {
		return errors.New("diagnostic_report_sync_failed")
	}
	return result
}
