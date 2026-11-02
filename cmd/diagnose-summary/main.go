// Separate experimental summary contract; no network without --run.
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

type report struct {
	diagnostic.Record
	SchemaVersion         string    `json:"schema_version"`
	InputScope            string    `json:"input_scope"`
	InputFileHash         string    `json:"input_file_sha256"`
	DatasetVersion        string    `json:"dataset_version,omitempty"`
	DatasetHash           string    `json:"dataset_manifest_sha256,omitempty"`
	ExampleID             string    `json:"example_id,omitempty"`
	ExpectedModelDigest   string    `json:"expected_model_digest"`
	ModelDigest           *string   `json:"model_digest"`
	TokenizerManifestHash string    `json:"tokenizer_manifest_sha256"`
	RecordedAt            time.Time `json:"recorded_at"`
}

func run(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("diagnose-summary", flag.ContinueOnError)
	set := flags.String("set", "development", "development (default), holdout (explicit final evaluation), or examples")
	execute := flags.Bool("run", false, "explicitly enable one generation per client, sequentially, no retries")
	out := flags.String("out", "", "new .jsonl report; default stdout")
	if flags.Parse(args) != nil || flags.NArg() != 0 || (*set != "examples" && *set != "development" && *set != "holdout") {
		return errors.New("invalid_summary_arguments")
	}
	if *out != "" && filepath.Ext(*out) != ".jsonl" {
		return errors.New("summary_report_requires_jsonl")
	}
	paths := []string{"examples/04-exact-20.json", "examples/02.json"}
	var dataset evaluation.Dataset
	var err error
	if *set != "examples" {
		dataset, err = evaluation.Load("evaluation", *set)
		if err != nil {
			return err
		}
		paths = nil
		for _, entry := range dataset.Cases {
			paths = append(paths, filepath.Join("evaluation", filepath.FromSlash(entry.Entry.Input)))
		}
	}
	inputs, err := evaluation.LoadFiles(paths)
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
	runner := diagnostic.NewSummaryRunner(counter.BaseURL, counter)
	var file *os.File
	if *out != "" {
		if os.MkdirAll(filepath.Dir(*out), 0700) != nil {
			return errors.New("summary_report_directory_failed")
		}
		file, err = os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return errors.New("summary_report_create_failed")
		}
		defer file.Close()
		output = file
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	encoder := json.NewEncoder(output)
	failed := false
	for i, input := range inputs {
		if ctx.Err() != nil {
			return errors.New("summary_cancelled")
		}
		record := runner.Summarize(ctx, input.File, input.Input, *execute)
		entry := report{Record: record, SchemaVersion: diagnostic.SummarySchemaVersion, InputScope: *set, InputFileHash: input.Hash, ExpectedModelDigest: metadata.Digest, TokenizerManifestHash: diagnostic.Hash(string(raw)), RecordedAt: time.Now().UTC()}
		if *set != "examples" {
			entry.DatasetVersion = dataset.Version
			entry.DatasetHash = dataset.ManifestHash
			entry.ExampleID = dataset.Cases[i].ID
		}
		if record.Budget.Verified {
			entry.ModelDigest = &metadata.Digest
		}
		if encoder.Encode(entry) != nil {
			return errors.New("summary_output_failed")
		}
		if diagnostic.PreflightFailed(record) {
			return errors.New("summary_preflight_failed")
		}
		failed = failed || diagnostic.Failed(record)
	}
	if file != nil && file.Sync() != nil {
		return errors.New("summary_report_sync_failed")
	}
	if failed {
		return diagnostic.ErrComparison
	}
	return nil
}
