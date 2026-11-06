package main

import (
	"encoding/json"
	"errors"
	"finkoper-morning-briefing/internal/briefing"
	"finkoper-morning-briefing/internal/diagnostic"
	"finkoper-morning-briefing/internal/evaluation"
	"os"
)

func validate27Probe(path string) error {
	fail := errors.New("qwen27_successful_sr06_probe_required")
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > briefing.MaxBody {
		return fail
	}
	var r report
	if json.Unmarshal(raw, &r) != nil {
		return fail
	}
	m, hash := diagnostic.Qwen27PinnedManifest()
	options, _ := json.Marshal(r.Parameters["options"])
	expected, _ := json.Marshal(diagnostic.Qwen35Options())
	if string(options) != string(expected) || r.Parameters["think"] != false || r.Budget.OutputReserve != 1000 || r.Budget.ContextTokens != 8192 || r.PromptVersion != diagnostic.SummaryPromptVersionV2 || r.SchemaVersion != diagnostic.SummarySchemaVersion {
		return fail
	}
	if r.Profile != diagnostic.Qwen27Profile || r.Model != diagnostic.Qwen27Model || r.CaseID != "sr-06" || r.GenerationMode != "baseline" || r.GenerationAttempts != 1 || r.Mode != "run" || r.ModelDigest == nil || *r.ModelDigest != m.Digest || r.ProfileManifestHash != hash || r.Error != nil || r.StructuralError != nil || r.RuntimeTokenCountVerified == nil || !*r.RuntimeTokenCountVerified || !r.Budget.Verified || r.SystemPreserved == nil || !*r.SystemPreserved || r.Done == nil || !*r.Done || r.DoneReason == nil || *r.DoneReason != "stop" {
		return fail
	}
	inputs, err := evaluation.LoadFiles([]string{casePaths["sr-06"]})
	if err != nil {
		return fail
	}
	_, user := briefing.PromptInput(inputs[0].Input)
	if r.InputFileHash != inputs[0].Hash || r.UserHash != diagnostic.Hash(user) || r.PromptHash != diagnostic.Hash(diagnostic.SummaryPromptV2) || r.FormatHash != diagnostic.Hash(diagnostic.SummarySchema) {
		return fail
	}
	_, err = diagnostic.ValidateSummary(r.Result, inputs[0].Input)
	if err != nil {
		return fail
	}
	return nil
}
