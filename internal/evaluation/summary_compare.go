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

type SummarySelection struct {
	Name, Hash string
	Inputs     []FileInput
}

// Only input manifests and API payloads are loaded, never manual-checks or annotations.
func LoadSummaryComparison(root, set string) (SummarySelection, error) {
	selection := SummarySelection{Name: set}
	paths := []string{}
	if set == "regressions" || set == "new-regressions" {
		directory := filepath.Join(root, "evaluation/summary-regressions")
		raw, err := os.ReadFile(filepath.Join(directory, "manifest.json"))
		if err != nil || len(raw) > briefing.MaxBody {
			return selection, errors.New("invalid_summary_regression_manifest")
		}
		var manifest struct {
			Version string `json:"version"`
			Cases   []struct {
				ID    string `json:"id"`
				Input string `json:"input"`
			} `json:"cases"`
		}
		if briefing.Strict(raw, &manifest) != nil || manifest.Version != "summary-regressions-v1" || len(manifest.Cases) != 8 {
			return selection, errors.New("invalid_summary_regression_manifest")
		}
		seen := map[string]bool{}
		for _, entry := range manifest.Cases {
			if entry.ID == "" || strings.ContainsAny(entry.ID, "/\\.") || seen[entry.ID] || entry.Input != "inputs/"+entry.ID+".json" {
				return selection, errors.New("invalid_summary_regression_entry")
			}
			seen[entry.ID] = true
			paths = append(paths, filepath.Join(directory, filepath.FromSlash(entry.Input)))
		}
	}
	switch set {
	case "regressions", "previous-holdout", "development":
		split := "holdout"
		if set == "development" {
			split = "development"
		}
		dataset, err := Load(filepath.Join(root, "evaluation"), split)
		if err != nil {
			return selection, err
		}
		for _, entry := range dataset.Cases {
			paths = append(paths, filepath.Join(root, "evaluation", filepath.FromSlash(entry.Entry.Input)))
		}
	case "examples":
		paths = []string{filepath.Join(root, "examples/04-exact-20.json"), filepath.Join(root, "examples/02.json")}
	case "new-regressions":
	default:
		return selection, errors.New("unknown_summary_comparison_set")
	}
	var err error
	selection.Inputs, err = LoadFiles(paths)
	if err != nil {
		return selection, err
	}
	hashes := []string{}
	for _, input := range selection.Inputs {
		hashes = append(hashes, input.Hash)
	}
	raw, _ := json.Marshal(hashes)
	selection.Hash = diagnostic.Hash(string(raw))
	return selection, nil
}

type SummaryComparisonRecord struct {
	diagnostic.Record
	Selection             string    `json:"selection"`
	Comparison            string    `json:"comparison"`
	SelectionHash         string    `json:"selection_sha256"`
	AssessmentRole        string    `json:"assessment_role"`
	InputFileHash         string    `json:"input_file_sha256"`
	SchemaVersion         string    `json:"schema_version"`
	ExpectedModelDigest   string    `json:"expected_model_digest"`
	ModelDigest           *string   `json:"model_digest"`
	TokenizerManifestHash string    `json:"tokenizer_manifest_sha256"`
	PairIndex             int       `json:"pair_index"`
	VersionIndex          int       `json:"version_index"`
	RecordedAt            time.Time `json:"recorded_at"`
}

func CompareSummaries(ctx context.Context, selection SummarySelection, runner *diagnostic.Runner, digest, tokenizerHash string, execute bool, output io.Writer, pairOption ...string) error {
	pair := "v2-v3"
	if len(pairOption) > 1 {
		return errors.New("unknown_summary_comparison_pair")
	}
	if len(pairOption) == 1 {
		pair = pairOption[0]
	}
	variants, err := diagnostic.SummaryComparisonVariants(pair)
	if err != nil {
		return err
	}
	if runner == nil || !runner.SummaryMode || runner.Thinking || runner.Model != "qwen3:8b" || runner.VerifyOnly {
		return errors.New("invalid_summary_comparison_runner")
	}
	encoder := json.NewEncoder(output)
	failed := false
	for i, input := range selection.Inputs {
		for j, variant := range variants {
			if ctx.Err() != nil {
				return errors.New("summary_comparison_cancelled")
			}
			record := runner.SummarizeVersion(ctx, input.File, input.Input, variant.PromptVersion, execute)
			entry := SummaryComparisonRecord{Record: record, Selection: selection.Name, SelectionHash: selection.Hash, AssessmentRole: "regression", InputFileHash: input.Hash, SchemaVersion: diagnostic.SummarySchemaVersion, ExpectedModelDigest: digest, TokenizerManifestHash: tokenizerHash, PairIndex: i + 1, VersionIndex: j + 1, RecordedAt: time.Now().UTC()}
			entry.Comparison = pair
			if selection.Name == "development" {
				entry.AssessmentRole = "development"
			}
			if record.Budget.Verified {
				entry.ModelDigest = &digest
			}
			if encoder.Encode(entry) != nil {
				return errors.New("summary_comparison_output_failed")
			}
			if diagnostic.PreflightFailed(record) {
				return errors.New("summary_comparison_preflight_failed")
			}
			failed = failed || diagnostic.Failed(record)
		}
	}
	if failed {
		return diagnostic.ErrComparison
	}
	return nil
}
