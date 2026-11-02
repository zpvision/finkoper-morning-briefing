package evaluation

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"

	"finkoper-morning-briefing/internal/briefing"
	"finkoper-morning-briefing/internal/diagnostic"
)

type FileInput struct {
	File  string
	Input briefing.Input
	Hash  string
}

// LoadFiles validates all inputs before networking; original argument names are preserved.
func LoadFiles(paths []string) ([]FileInput, error) {
	if len(paths) == 0 {
		return nil, errors.New("diagnostic_files_required")
	}
	inputs := make([]FileInput, 0, len(paths))
	seen := []os.FileInfo{}
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			return nil, errors.New("diagnostic_input_file_invalid")
		}
		info, statErr := file.Stat()
		if statErr != nil || !info.Mode().IsRegular() {
			file.Close()
			return nil, errors.New("diagnostic_input_file_invalid")
		}
		for _, previous := range seen {
			if os.SameFile(info, previous) {
				file.Close()
				return nil, errors.New("duplicate_diagnostic_input_file")
			}
		}
		raw, err := io.ReadAll(io.LimitReader(file, briefing.MaxBody+1))
		file.Close()
		var input briefing.Input
		if err != nil || len(raw) > briefing.MaxBody || briefing.Strict(raw, &input) != nil || input.Normalize() != nil {
			return nil, errors.New("diagnostic_input_file_invalid")
		}
		seen = append(seen, info)
		inputs = append(inputs, FileInput{path, input, diagnostic.Hash(string(raw))})
	}
	return inputs, nil
}

type FileReport struct {
	diagnostic.Record
	InputScope            string    `json:"input_scope"`
	DiagnosticProfile     string    `json:"diagnostic_profile"`
	ThinkingMode          string    `json:"thinking_mode"`
	SelectedFiles         int       `json:"selected_files"`
	InputFileHash         string    `json:"input_file_sha256"`
	SchemaVersion         string    `json:"schema_version"`
	TokenizerManifestHash string    `json:"tokenizer_manifest_sha256"`
	ExpectedModelDigest   string    `json:"expected_model_digest"`
	ModelDigest           *string   `json:"model_digest"`
	CallTimeoutMS         int64     `json:"call_timeout_ms"`
	RecordedAt            time.Time `json:"recorded_at"`
}

// RunFiles uses exactly variant A once per file, sequentially. No dataset or annotations.
func RunFiles(ctx context.Context, inputs []FileInput, runner *diagnostic.Runner, digest, tokenizerHash string, execute bool, output io.Writer) error {
	if runner == nil || runner.Model != Model14B || runner.Thinking || runner.VerifyOnly {
		return errors.New("invalid_standalone_14b_runner")
	}
	encoder := json.NewEncoder(output)
	failed := false
	for _, input := range inputs {
		if ctx.Err() != nil {
			return errors.New("diagnostic_cancelled")
		}
		record := runner.Compare(ctx, input.File, input.Input, diagnostic.Variants()[0], execute)
		record.InvalidContent = nil
		if diagnostic.Failed(record) {
			record.Result = nil
			failed = true
		}
		report := FileReport{Record: record, InputScope: "standalone_files", DiagnosticProfile: Profile14B, ThinkingMode: "think-false", SelectedFiles: len(inputs), InputFileHash: input.Hash, SchemaVersion: briefing.SchemaVersion, TokenizerManifestHash: tokenizerHash, ExpectedModelDigest: digest, CallTimeoutMS: runner.Client.Timeout.Milliseconds(), RecordedAt: time.Now().UTC()}
		if record.Budget.Verified {
			report.ModelDigest = &digest
		}
		if encoder.Encode(report) != nil {
			return errors.New("diagnostic_output_failed")
		}
		if diagnostic.PreflightFailed(record) {
			return errors.New("diagnostic_files_preflight_failed")
		}
	}
	if failed {
		return diagnostic.ErrComparison
	}
	return nil
}
