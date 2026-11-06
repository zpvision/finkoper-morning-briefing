// Frozen Go contract bridge for separate cross-model diagnostics, never the API.
package main

import (
	"encoding/json"
	"finkoper-morning-briefing/internal/briefing"
	"finkoper-morning-briefing/internal/diagnostic"
	"finkoper-morning-briefing/internal/evaluation"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

func main() {
	var err error
	if len(os.Args) == 2 && os.Args[1] == "--serve" {
		err = serve(os.Stdin, os.Stdout)
	} else if len(os.Args) != 1 {
		err = fmt.Errorf("unknown_helper_option")
	} else {
		err = run(os.Stdin, os.Stdout)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(input io.Reader, output io.Writer) error {
	var req struct {
		Action, InputDir, Baseline, RequestID string
		Input                                 json.RawMessage
		Content                               string
	}
	raw, err := io.ReadAll(io.LimitReader(input, 8*briefing.MaxBody))
	if err != nil {
		return err
	}
	if err = json.Unmarshal(raw, &req); err != nil {
		return err
	}
	if req.Action == "validate" {
		validationStarted := time.Now()
		var in briefing.Input
		if err := briefing.Strict(req.Input, &in); err != nil {
			return err
		}
		if err := in.Normalize(); err != nil {
			return err
		}
		_, err := diagnostic.ValidateSummary([]byte(req.Content), in)
		result := map[string]any{"error": nil, "validation_ms": float64(time.Since(validationStarted).Nanoseconds()) / 1e6}
		if req.RequestID != "" {
			result["request_id"] = req.RequestID
			result["process_id"] = os.Getpid()
		}
		if err != nil {
			result["error"] = err.Error()
		}
		return json.NewEncoder(output).Encode(result)
	}
	if req.Action != "prepare" && req.Action != "prepare-rest" && req.Action != "prepare-gemma30" {
		return fmt.Errorf("unknown_contract_action")
	}
	paths := []string{}
	first, last, prefix := 1, 6, "syn"
	if req.Action == "prepare-rest" {
		first, last = 7, 24
	}
	if req.Action == "prepare-gemma30" {
		first, last, prefix = 1, 30, "long"
	}
	for i := first; i <= last; i++ {
		paths = append(paths, filepath.Join(req.InputDir, fmt.Sprintf("%s-%02d.json", prefix, i)))
	}
	inputs, err := evaluation.LoadFiles(paths)
	if err != nil {
		return err
	}
	if req.Action == "prepare-rest" || req.Action == "prepare-gemma30" {
		contracts := []map[string]any{}
		for i, input := range inputs {
			_, user := briefing.PromptInput(input.Input)
			raw, err := os.ReadFile(input.File)
			if err != nil {
				return err
			}
			contracts = append(contracts, map[string]any{"case_id": fmt.Sprintf("%s-%02d", prefix, i+first), "input": json.RawMessage(raw), "user": user, "input_file_sha256": input.Hash, "user_sha256": diagnostic.Hash(user)})
		}
		return json.NewEncoder(output).Encode(map[string]any{"prompt": diagnostic.SummaryPromptV2, "prompt_version": diagnostic.SummaryPromptVersionV2, "prompt_sha256": diagnostic.Hash(diagnostic.SummaryPromptV2), "schema": json.RawMessage(diagnostic.SummarySchema), "schema_version": diagnostic.SummarySchemaVersion, "schema_sha256": diagnostic.Hash(diagnostic.SummarySchema), "cases": contracts})
	}
	file, err := os.Open(req.Baseline)
	if err != nil {
		return err
	}
	defer file.Close()
	// JSONL Decoder rejects truncated data and duplicate baseline cases.
	decoder := json.NewDecoder(io.LimitReader(file, 16*briefing.MaxBody))
	baselines := map[string]json.RawMessage{}
	for {
		var row json.RawMessage
		err := decoder.Decode(&row)
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		var header struct {
			CaseID         string  `json:"case_id"`
			GenerationMode string  `json:"generation_mode"`
			ModelDigest    *string `json:"model_digest"`
			diagnostic.Record
		}
		if err := json.Unmarshal(row, &header); err != nil {
			return err
		}
		if header.PromptVersion != diagnostic.SummaryPromptVersionV2 {
			continue
		}
		if _, exists := baselines[header.CaseID]; exists {
			return fmt.Errorf("duplicate_qwen_baseline")
		}
		pin, _ := diagnostic.Qwen27PinnedManifest()
		actualOptions, _ := json.Marshal(header.Parameters["options"])
		expectedOptions, _ := json.Marshal(diagnostic.Qwen35Options())
		if header.Model != diagnostic.Qwen27Model || header.GenerationMode != "baseline" || header.ModelDigest == nil || *header.ModelDigest != pin.Digest || header.Parameters["think"] != false || string(actualOptions) != string(expectedOptions) || diagnostic.Failed(header.Record) || header.Done == nil || !*header.Done || header.DoneReason == nil || *header.DoneReason != "stop" {
			return fmt.Errorf("invalid_qwen_baseline")
		}
		baselines[header.CaseID] = row
	}
	contracts := []map[string]any{}
	for i, input := range inputs {
		id := fmt.Sprintf("syn-%02d", i+1)
		row := baselines[id]
		if row == nil {
			return fmt.Errorf("missing_qwen_baseline_%s", id)
		}
		var ref struct {
			diagnostic.Record
			InputHash string `json:"input_file_sha256"`
		}
		json.Unmarshal(row, &ref)
		_, user := briefing.PromptInput(input.Input)
		if ref.PromptHash != diagnostic.Hash(diagnostic.SummaryPromptV2) || ref.UserHash != diagnostic.Hash(user) || ref.FormatHash != diagnostic.Hash(diagnostic.SummarySchema) || ref.InputHash != input.Hash {
			return fmt.Errorf("qwen_baseline_contract_mismatch_%s", id)
		}
		if _, err := diagnostic.ValidateSummary(ref.Result, input.Input); err != nil {
			return err
		}
		source, err := os.ReadFile(input.File)
		if err != nil {
			return err
		}
		contracts = append(contracts, map[string]any{"case_id": id, "input": json.RawMessage(source), "user": user, "input_file_sha256": input.Hash, "user_sha256": ref.UserHash, "qwen_baseline": row})
	}
	return json.NewEncoder(output).Encode(map[string]any{"prompt": diagnostic.SummaryPromptV2, "prompt_version": diagnostic.SummaryPromptVersionV2, "prompt_sha256": diagnostic.Hash(diagnostic.SummaryPromptV2), "schema": json.RawMessage(diagnostic.SummarySchema), "schema_version": diagnostic.SummarySchemaVersion, "schema_sha256": diagnostic.Hash(diagnostic.SummarySchema), "cases": contracts})
}
