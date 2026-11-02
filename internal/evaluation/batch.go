// Package evaluation provides standalone and synthetic diagnostic runners, without reading annotations.
package evaluation

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"finkoper-morning-briefing/internal/briefing"
	"finkoper-morning-briefing/internal/diagnostic"
)

type Entry struct {
	ID    string `json:"id"`
	Split string `json:"split"`
	Input string `json:"input"`
}
type Manifest struct {
	Version   string  `json:"version"`
	Synthetic bool    `json:"synthetic"`
	Cases     []Entry `json:"cases"`
}
type Case struct {
	Entry
	Input     briefing.Input
	InputHash string
}
type Dataset struct {
	Version, ManifestHash string
	Cases                 []Case
}

func Load(root, split string) (Dataset, error) {
	var dataset Dataset
	if split != "development" && split != "holdout" {
		return dataset, errors.New("invalid_split")
	}
	raw, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil || len(raw) > briefing.MaxBody {
		return dataset, errors.New("invalid_dataset_manifest")
	}
	var manifest Manifest
	if briefing.Strict(raw, &manifest) != nil || !manifest.Synthetic || manifest.Version == "" || len(manifest.Cases) != 20 {
		return dataset, errors.New("invalid_dataset_manifest")
	}
	dataset.Version = manifest.Version
	dataset.ManifestHash = diagnostic.Hash(string(raw))
	seen := map[string]bool{}
	counts := map[string]int{}
	for _, entry := range manifest.Cases {
		if entry.ID == "" || seen[entry.ID] || strings.ContainsAny(entry.ID, "/\\.") || (entry.Split != "development" && entry.Split != "holdout") || entry.Input != "inputs/"+entry.Split+"/"+entry.ID+".json" {
			return dataset, errors.New("invalid_dataset_entry")
		}
		seen[entry.ID] = true
		counts[entry.Split]++
		if entry.Split != split {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(entry.Input)))
		var input briefing.Input
		if err != nil || len(raw) > briefing.MaxBody || briefing.Strict(raw, &input) != nil || input.Normalize() != nil {
			return dataset, errors.New("invalid_dataset_input")
		}
		dataset.Cases = append(dataset.Cases, Case{entry, input, diagnostic.Hash(string(raw))})
	}
	if counts["development"] != 10 || counts["holdout"] != 10 {
		return dataset, errors.New("invalid_dataset_split")
	}
	return dataset, nil
}

type Report struct {
	diagnostic.Record
	DiagnosticProfile     string    `json:"diagnostic_profile"`
	SelectedExamples      int       `json:"selected_examples"`
	ThinkingMode          string    `json:"thinking_mode"`
	CallTimeoutMS         int64     `json:"call_timeout_ms"`
	ExampleID             string    `json:"example_id"`
	Split                 string    `json:"split"`
	DatasetVersion        string    `json:"dataset_version"`
	DatasetHash           string    `json:"dataset_manifest_sha256"`
	InputFileHash         string    `json:"input_file_sha256"`
	SchemaVersion         string    `json:"schema_version"`
	TokenizerManifestHash string    `json:"tokenizer_manifest_sha256"`
	ExpectedModelDigest   string    `json:"expected_model_digest"`
	ModelDigest           *string   `json:"model_digest"`
	RecordedAt            time.Time `json:"recorded_at"`
}

// Sequential, one generation per entry at most. Fail closed on any preflight error.
func Run(ctx context.Context, dataset Dataset, runner *diagnostic.Runner, digest, tokenizerHash string, execute bool, output io.Writer) error {
	variant := diagnostic.Variants()[0]
	if runner.Thinking {
		variant = diagnostic.ThinkingVariant()
	}
	encoder := json.NewEncoder(output)
	failed := false
	for _, entry := range dataset.Cases {
		if ctx.Err() != nil {
			return errors.New("batch_cancelled")
		}
		record := runner.Compare(ctx, entry.Entry.Input, entry.Input, variant, execute)
		// No raw invalid content (and never message.thinking) in batch artifacts.
		record.InvalidContent = nil
		if diagnostic.Failed(record) {
			record.Result = nil
			failed = true
		}
		report := Report{Record: record, ExampleID: entry.ID, Split: entry.Split, DatasetVersion: dataset.Version, DatasetHash: dataset.ManifestHash, InputFileHash: entry.InputHash, SchemaVersion: briefing.SchemaVersion, ExpectedModelDigest: digest, TokenizerManifestHash: tokenizerHash, RecordedAt: time.Now().UTC()}
		report.ThinkingMode = "think-false"
		if runner.Thinking {
			report.ThinkingMode = "think-true"
		}
		report.CallTimeoutMS = runner.Client.Timeout.Milliseconds()
		report.DiagnosticProfile = Profile8B
		if runner.Model == Model14B {
			report.DiagnosticProfile = Profile14B
		}
		report.SelectedExamples = len(dataset.Cases)
		if record.Budget.Verified {
			report.ModelDigest = &digest
		}
		if encoder.Encode(report) != nil {
			return errors.New("batch_output_failed")
		}
		if diagnostic.PreflightFailed(record) {
			return errors.New("batch_preflight_failed")
		}
	}
	if failed {
		return errors.New("batch_results_failed")
	}
	return nil
}
