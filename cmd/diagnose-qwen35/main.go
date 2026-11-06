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
	"sr-02":  "evaluation/summary-regressions/inputs/sr-02.json",
	"sr-03":  "evaluation/summary-regressions/inputs/sr-03.json",
	"sr-04":  "evaluation/summary-regressions/inputs/sr-04.json",
	"sr-05":  "evaluation/summary-regressions/inputs/sr-05.json",
	"sr-06":  "evaluation/summary-regressions/inputs/sr-06.json",
	"sr-07":  "evaluation/summary-regressions/inputs/sr-07.json",
	"sr-08":  "evaluation/summary-regressions/inputs/sr-08.json",
	"hol-01": "evaluation/inputs/holdout/hol-01.json",
	"hol-02": "evaluation/inputs/holdout/hol-02.json",
	"hol-03": "evaluation/inputs/holdout/hol-03.json",
	"hol-04": "evaluation/inputs/holdout/hol-04.json",
	"hol-05": "evaluation/inputs/holdout/hol-05.json",
	"hol-06": "evaluation/inputs/holdout/hol-06.json",
	"hol-07": "evaluation/inputs/holdout/hol-07.json",
	"hol-08": "evaluation/inputs/holdout/hol-08.json",
	"hol-09": "evaluation/inputs/holdout/hol-09.json",
	"hol-10": "evaluation/inputs/holdout/hol-10.json",
}

var sixCaseIDs = []string{"sr-01", "sr-04", "sr-06", "sr-07", "hol-05", "hol-09"}

// Fixed input-only selection; no directory scans, manifests with labels or annotations.
func selectCaseIDs(mode, selected string) ([]string, error) {
	if selected == "regressions" {
		if mode != "thinking-long" {
			return nil, errors.New("regressions_requires_thinking_long")
		}
		ids := make([]string, 0, 18)
		for i := 1; i <= 8; i++ {
			ids = append(ids, fmt.Sprintf("sr-%02d", i))
		}
		for i := 1; i <= 10; i++ {
			ids = append(ids, fmt.Sprintf("hol-%02d", i))
		}
		return ids, nil
	}
	if selected == "six" && mode != "thinking-long" {
		return append([]string(nil), sixCaseIDs...), nil
	}
	if _, ok := casePaths[selected]; ok {
		if mode == "thinking-long" {
			return []string{selected}, nil
		}
		for _, id := range sixCaseIDs {
			if selected == id {
				return []string{selected}, nil
			}
		}
	}
	return nil, errors.New("unknown_qwen35_case")
}

type report struct {
	diagnostic.Record
	Profile              string    `json:"profile"`
	GenerationMode       string    `json:"generation_mode"`
	CallTimeoutMS        int64     `json:"call_timeout_ms"`
	SamplingSource       string    `json:"sampling_source"`
	ModeConfigHash       string    `json:"mode_config_sha256"`
	CaseID               string    `json:"case_id"`
	AssessmentRole       string    `json:"assessment_role"`
	SchemaVersion        string    `json:"schema_version"`
	ProfileManifestHash  string    `json:"profile_manifest_sha256"`
	ExpectedModelDigest  string    `json:"expected_model_digest"`
	ModelDigest          *string   `json:"model_digest"`
	InputFileHash        string    `json:"input_file_sha256"`
	OllamaSourceCommit   string    `json:"ollama_source_commit"`
	HFRevision           string    `json:"hf_revision"`
	RecordedAt           time.Time `json:"recorded_at"`
	RuntimeOllamaVersion string    `json:"runtime_ollama_version,omitempty"`
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(args []string, output io.Writer) error {
	return runWithRunner(args, output, func(resources, mode string) (*diagnostic.Runner, error) {
		counter, err := diagnostic.NewQwen35CounterMode(resources, mode)
		if err != nil {
			return nil, err
		}
		return diagnostic.NewQwen35RunnerMode(counter.BaseURL, counter, mode)
	})
}

// Tests inject only the counter/base URL; production and mock share the HTTP transport.
func runWithRunner(args []string, output io.Writer, newRunner func(string, string) (*diagnostic.Runner, error), factories27 ...func(string, string) (*diagnostic.Runner, error)) error {
	flags := flag.NewFlagSet("diagnose-qwen35", flag.ContinueOnError)
	profile := flags.String("profile", "", "required: qwen35-9b-summary-v2-v1")
	selected := flags.String("case", "sr-01", "legacy single case or six; thinking-long also sr-01..08, hol-01..10, or regressions (18 inputs)")
	mode := flags.String("mode", "baseline", "baseline, greedy, thinking, all (three modes), or explicit thinking-long")
	resources := flags.String("profile-resources", "", "separate Qwen3.5 resource directory")
	check := flags.Bool("check-profile", false, "explicit HTTP metadata/render-only verification; zero generations")
	execute := flags.Bool("run", false, "explicitly allow one generation per selected case; no retries")
	out := flags.String("out", "", "new JSONL file; default stdout")
	probeReport := flags.String("probe-report", "", "successful sr-06 JSONL required before 27B remaining --run")
	inputDir := flags.String("input-dir", "", "external input JSON directory, 27B baseline only; default first-six")
	reviewedFirst := flags.Bool("reviewed-first", false, "operator reviewed first six before external remaining --run")
	prompt := flags.String("prompt", "v2", "v2 (default), or 27B external first-six only: v5, compare-v2-v5")
	if flags.Parse(args) != nil || flags.NArg() != 0 || (*profile != diagnostic.Qwen35Profile && *profile != diagnostic.Qwen27Profile) || (*check && *execute) {
		return errors.New("invalid_qwen35_arguments")
	}
	if *out != "" && filepath.Ext(*out) != ".jsonl" {
		return errors.New("qwen35_report_requires_jsonl")
	}
	modes := []string{*mode}
	if *mode == "" {
		return errors.New("invalid_qwen35_mode")
	}
	if *mode == "all" {
		modes = []string{"baseline", "greedy", "thinking"}
	}
	for _, name := range modes {
		if _, err := diagnostic.Qwen35Configuration(name); err != nil {
			return err
		}
	}
	ids, err := selectCaseIDs(*mode, *selected)
	externalPaths := []string(nil)
	if *inputDir != "" {
		if *profile != diagnostic.Qwen27Profile || *mode != "baseline" {
			return errors.New("external_inputs_require_qwen27_baseline")
		}
		explicitCase := false
		flags.Visit(func(f *flag.Flag) {
			if f.Name == "case" {
				explicitCase = true
			}
		})
		if !explicitCase {
			*selected = "first-six"
		}
		if *execute && *selected == "remaining" && !*reviewedFirst {
			return errors.New("external_remaining_requires_reviewed_first")
		}
		ids, externalPaths, err = externalInputs(*inputDir, *selected)
	}
	if *profile == diagnostic.Qwen27Profile && *inputDir == "" {
		if *mode != "baseline" || (*selected != "six" && *selected != "sr-06" && *selected != "remaining") || (*execute && *selected == "six") {
			return errors.New("qwen27_requires_baseline_plan_six_or_probe_sr06_or_remaining")
		}
		if *selected == "remaining" {
			ids = []string{"sr-01", "sr-04", "sr-07", "hol-05", "hol-09"}
			err = nil
		}
	}
	if err != nil {
		return err
	}
	versions := []string{diagnostic.SummaryPromptVersionV2}
	switch *prompt {
	case "v2":
	case "v5", "compare-v2-v5":
		if *profile != diagnostic.Qwen27Profile || *inputDir == "" || *selected != "first-six" || *mode != "baseline" {
			return errors.New("new_prompt_requires_qwen27_external_first_six")
		}
		versions = []string{diagnostic.SummaryPromptVersionV5}
		if *prompt == "compare-v2-v5" {
			versions = []string{diagnostic.SummaryPromptVersionV2, diagnostic.SummaryPromptVersionV5}
		}
	default:
		return errors.New("unknown_summary_prompt")
	}
	if len(ids)*len(modes)*len(versions) > 18 {
		return errors.New("qwen35_generation_limit_exceeded")
	}
	paths := []string{}
	if *inputDir != "" {
		paths = externalPaths
	} else {
		for _, id := range ids {
			path, ok := casePaths[id]
			if !ok {
				return errors.New("unknown_qwen35_case")
			}
			paths = append(paths, path)
		}
	}
	inputs, err := evaluation.LoadFiles(paths)
	if err != nil {
		return err
	}
	if *profile == diagnostic.Qwen27Profile && *inputDir == "" && *execute && *selected == "remaining" {
		if err := validate27Probe(*probeReport); err != nil {
			return err
		}
	}
	if *profile == diagnostic.Qwen27Profile {
		newRunner = func(resources, mode string) (*diagnostic.Runner, error) {
			c, err := diagnostic.NewQwen27Counter(resources)
			if err != nil {
				return nil, err
			}
			return diagnostic.NewQwen27Runner(c.BaseURL, c), nil
		}
		if len(factories27) > 0 {
			newRunner = factories27[0]
		}
	}
	runners := make([]*diagnostic.Runner, 0, len(modes))
	for _, name := range modes {
		runner, err := newRunner(*resources, name)
		if err != nil {
			return err
		}
		runner.VerifyOnly = *check
		defer runner.Client.CloseIdleConnections()
		runners = append(runners, runner)
	}
	manifest, pinHash := diagnostic.Qwen35PinnedManifest()
	if *profile == diagnostic.Qwen27Profile {
		manifest, pinHash = diagnostic.Qwen27PinnedManifest()
	}
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
		for m, runner := range runners {
			for _, version := range versions {
				if ctx.Err() != nil {
					return errors.New("qwen35_cancelled")
				}
				record := runner.SummarizeVersion(ctx, input.File, input.Input, version, *execute)
				entry := report{Record: record, Profile: diagnostic.Qwen35Profile, CaseID: ids[i], AssessmentRole: "regression", SchemaVersion: diagnostic.SummarySchemaVersion, ProfileManifestHash: pinHash, ExpectedModelDigest: manifest.Digest, InputFileHash: input.Hash, OllamaSourceCommit: manifest.OllamaCommit, HFRevision: manifest.HFRevision, RecordedAt: time.Now().UTC()}
				config, _ := diagnostic.Qwen35Configuration(modes[m])
				entry.Profile = *profile
				if *inputDir != "" {
					entry.AssessmentRole = "synthetic_external"
				}
				if *profile == diagnostic.Qwen27Profile && record.Budget.Verified {
					entry.RuntimeOllamaVersion = "0.40.15"
				}
				entry.GenerationMode, entry.CallTimeoutMS, entry.SamplingSource = config.Mode, config.Timeout.Milliseconds(), diagnostic.Qwen35SamplingSource
				configRaw, _ := json.Marshal(map[string]any{"mode": config.Mode, "think": config.Thinking, "reserve": config.Reserve, "timeout_ms": entry.CallTimeoutMS, "options": config.Options})
				entry.ModeConfigHash = diagnostic.Hash(string(configRaw))
				if record.Budget.Verified {
					entry.ModelDigest = &manifest.Digest
				}
				if encoder.Encode(entry) != nil {
					return errors.New("qwen35_output_failed")
				}
				failed = failed || diagnostic.Failed(record)
				// Per-input budget refusal is not a model/resource incompatibility.
				// Keep legacy modes' behavior unchanged; long regressions continue safely.
				if modes[m] == "thinking-long" && record.Error != nil && *record.Error == "input_budget_exceeded" {
					continue
				}
				// Stop on compatibility/count mismatch even if the sole probe already generated.
				if diagnostic.PreflightFailed(record) || (record.RuntimeTokenCountVerified != nil && !*record.RuntimeTokenCountVerified) {
					return errors.New("qwen35_compatibility_failed")
				}
			}
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
