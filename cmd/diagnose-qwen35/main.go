// Separate pinned text-only Qwen3.5 profile. No HTTP without --check-profile or --run.
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

	"finkoper-morning-briefing/internal/diagnostic"
	"finkoper-morning-briefing/internal/evaluation"
)

var casePaths = map[string]string{
	"sr-01":  "evaluation/summary-regressions/inputs/sr-01.json",
	"sr-04":  "evaluation/summary-regressions/inputs/sr-04.json",
	"sr-06":  "evaluation/summary-regressions/inputs/sr-06.json",
	"sr-07":  "evaluation/summary-regressions/inputs/sr-07.json",
	"hol-05": "evaluation/inputs/holdout/hol-05.json",
	"hol-09": "evaluation/inputs/holdout/hol-09.json",
}

type report struct {
	diagnostic.Record
	Profile             string    `json:"profile"`
	CaseID              string    `json:"case_id"`
	AssessmentRole      string    `json:"assessment_role"`
	SchemaVersion       string    `json:"schema_version"`
	ProfileManifestHash string    `json:"profile_manifest_sha256"`
	ExpectedModelDigest string    `json:"expected_model_digest"`
	ModelDigest         *string   `json:"model_digest"`
	InputFileHash       string    `json:"input_file_sha256"`
	OllamaSourceCommit  string    `json:"ollama_source_commit"`
	HFRevision          string    `json:"hf_revision"`
	RecordedAt          time.Time `json:"recorded_at"`
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("diagnose-qwen35", flag.ContinueOnError)
	profile := flags.String("profile", "", "required: qwen35-9b-summary-v2-v1")
	selected := flags.String("case", "sr-01", "sr-01, sr-04, sr-06, sr-07, hol-05, hol-09, or six")
	resources := flags.String("profile-resources", "", "separate Qwen3.5 resource directory")
	check := flags.Bool("check-profile", false, "explicit HTTP metadata/render-only verification; zero generations")
	execute := flags.Bool("run", false, "explicitly allow one generation per selected case; no retries")
	out := flags.String("out", "", "new JSONL file; default stdout")
	if flags.Parse(args) != nil || flags.NArg() != 0 || *profile != diagnostic.Qwen35Profile || (*check && *execute) {
		return errors.New("invalid_qwen35_arguments")
	}
	if *out != "" && filepath.Ext(*out) != ".jsonl" {
		return errors.New("qwen35_report_requires_jsonl")
	}
	ids := []string{*selected}
	if *selected == "six" {
		ids = []string{"sr-01", "sr-04", "sr-06", "sr-07", "hol-05", "hol-09"}
	}
	paths := []string{}
	for _, id := range ids {
		path, ok := casePaths[id]
		if !ok {
			return errors.New("unknown_qwen35_case")
		}
		paths = append(paths, path)
	}
	inputs, err := evaluation.LoadFiles(paths)
	if err != nil {
		return err
	}
	counter, err := diagnostic.NewQwen35Counter(*resources)
	if err != nil {
		return err
	}
	runner := diagnostic.NewQwen35Runner(counter.BaseURL, counter)
	runner.VerifyOnly = *check
	manifest, pinHash := diagnostic.Qwen35PinnedManifest()
	var file *os.File
	if *out != "" {
		if os.MkdirAll(filepath.Dir(*out), 0700) != nil {
			return errors.New("qwen35_report_directory_failed")
		}
		file, err = os.OpenFile(*out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return errors.New("qwen35_report_create_failed")
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
			return errors.New("qwen35_cancelled")
		}
		record := runner.SummarizeVersion(ctx, input.File, input.Input, diagnostic.SummaryPromptVersionV2, *execute)
		entry := report{Record: record, Profile: diagnostic.Qwen35Profile, CaseID: ids[i], AssessmentRole: "regression", SchemaVersion: diagnostic.SummarySchemaVersion, ProfileManifestHash: pinHash, ExpectedModelDigest: manifest.Digest, InputFileHash: input.Hash, OllamaSourceCommit: manifest.OllamaCommit, HFRevision: manifest.HFRevision, RecordedAt: time.Now().UTC()}
		if record.Budget.Verified {
			entry.ModelDigest = &manifest.Digest
		}
		if encoder.Encode(entry) != nil {
			return errors.New("qwen35_output_failed")
		}
		failed = failed || diagnostic.Failed(record)
		// Stop on compatibility/count mismatch even if the sole probe already generated.
		if diagnostic.PreflightFailed(record) || (record.RuntimeTokenCountVerified != nil && !*record.RuntimeTokenCountVerified) {
			return errors.New("qwen35_compatibility_failed")
		}
	}
	if file != nil && file.Sync() != nil {
		return errors.New("qwen35_report_sync_failed")
	}
	if failed {
		return diagnostic.ErrComparison
	}
	return nil
}
