package main

import (
	"bytes"
	"encoding/json"
	"finkoper-morning-briefing/internal/briefing"
	"finkoper-morning-briefing/internal/diagnostic"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestFrozenContractAndBaselineIdentity(t *testing.T) {
	root, _ := filepath.Abs("../..")
	fixture, err := os.ReadFile(filepath.Join(root, "evaluation/summary-regressions/inputs/sr-01.json"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	baseline := filepath.Join(dir, "baseline.jsonl")
	var lines bytes.Buffer
	for i := 1; i <= 6; i++ {
		var fields map[string]any
		json.Unmarshal(fixture, &fields)
		fields["client_id"] = fmt.Sprintf("syn-%02d", i)
		raw, _ := json.Marshal(fields)
		os.WriteFile(filepath.Join(dir, fmt.Sprintf("syn-%02d.json", i)), raw, 0600)
		var input briefing.Input
		json.Unmarshal(raw, &input)
		input.Normalize()
		_, user := briefing.PromptInput(input)
		result, _ := json.Marshal(map[string]any{"summary": "Контролируемый ответ mock.", "source_message_ids": []string{input.Messages[0].ID}})
		row := map[string]any{"case_id": fmt.Sprintf("syn-%02d", i), "model": diagnostic.Qwen27Model, "prompt_version": diagnostic.SummaryPromptVersionV2, "prompt_sha256": diagnostic.Hash(diagnostic.SummaryPromptV2), "user_json_sha256": diagnostic.Hash(user), "format_sha256": diagnostic.Hash(diagnostic.SummarySchema), "input_file_sha256": diagnostic.Hash(string(raw)), "done": true, "done_reason": "stop", "result": json.RawMessage(result)}
		pin, _ := diagnostic.Qwen27PinnedManifest()
		row["generation_mode"] = "baseline"
		row["model_digest"] = pin.Digest
		row["parameters"] = map[string]any{"think": false, "options": diagnostic.Qwen35Options()}
		json.NewEncoder(&lines).Encode(row)
	}
	os.WriteFile(baseline, lines.Bytes(), 0600)
	os.WriteFile(filepath.Join(dir, "review.md"), []byte("PRIVATE_REVIEW"), 0600)
	request, _ := json.Marshal(map[string]string{"Action": "prepare", "InputDir": dir, "Baseline": baseline})
	var output bytes.Buffer
	if err := run(bytes.NewReader(request), &output); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Prompt string
		Cases  []map[string]any
	}
	json.Unmarshal(output.Bytes(), &result)
	if diagnostic.Hash(result.Prompt) != "8cc6dc4d3fad8eba8da327ecf9837aaa0213c6f978678e65c4ad105464d8ff31" || len(result.Cases) != 6 || bytes.Contains(output.Bytes(), []byte("PRIVATE_REVIEW")) {
		t.Fatal("contract/annotations")
	}
	os.WriteFile(filepath.Join(dir, "syn-02.json"), fixture, 0600)
	if run(bytes.NewReader(request), &bytes.Buffer{}) == nil {
		t.Fatal("changed input accepted")
	}
}

func TestRestContractOnly07To24WithoutReviewOrBaseline(t *testing.T) {
	root, _ := filepath.Abs("../..")
	fixture, err := os.ReadFile(filepath.Join(root, "evaluation/summary-regressions/inputs/sr-01.json"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for i := 7; i <= 24; i++ {
		var fields map[string]any
		json.Unmarshal(fixture, &fields)
		fields["client_id"] = fmt.Sprintf("syn-%02d", i)
		raw, _ := json.Marshal(fields)
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("syn-%02d.json", i)), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(dir, "review.md"), []byte("PRIVATE_REVIEW"), 0600)
	request, _ := json.Marshal(map[string]string{"Action": "prepare-rest", "InputDir": dir, "Baseline": "DO_NOT_READ"})
	var output bytes.Buffer
	if err := run(bytes.NewReader(request), &output); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Prompt string
		Cases  []struct {
			Case string `json:"case_id"`
			User string
		}
	}
	if json.Unmarshal(output.Bytes(), &result) != nil || len(result.Cases) != 18 || result.Prompt != diagnostic.SummaryPromptV2 || bytes.Contains(output.Bytes(), []byte("PRIVATE_REVIEW")) {
		t.Fatal("selection/contract")
	}
	for i, c := range result.Cases {
		if c.Case != fmt.Sprintf("syn-%02d", i+7) {
			t.Fatal("case order")
		}
	}
	os.WriteFile(filepath.Join(dir, "syn-24.json"), []byte("invalid"), 0600)
	if run(bytes.NewReader(request), &bytes.Buffer{}) == nil {
		t.Fatal("invalid input accepted")
	}
}
