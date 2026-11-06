package main

import (
	"bytes"
	"encoding/json"
	"finkoper-morning-briefing/internal/briefing"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPersistentProtocolCorrelationValidationAndDuplicates(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("../..", "evaluation/summary-regressions/inputs/sr-01.json"))
	if err != nil {
		t.Fatal(err)
	}
	var input briefing.Input
	if json.Unmarshal(raw, &input) != nil || input.Normalize() != nil {
		t.Fatal("fixture")
	}
	valid, _ := json.Marshal(map[string]any{"summary": "fixture", "source_message_ids": []string{input.Messages[0].ID}})
	var requests bytes.Buffer
	encode := func(id, action, content string) {
		body := map[string]any{"Action": action, "RequestID": id, "Input": json.RawMessage(raw), "Content": content}
		json.NewEncoder(&requests).Encode(map[string]any{"request_id": id, "request": body})
	}
	encode("ready", "ping", "")
	encode("job-1", "validate", `{"summary":"fixture","source_message_ids":["missing"]}`)
	encode("job-2", "validate", string(valid))
	encode("job-2", "validate", string(valid))
	encode("job-3", "prepare", "")
	encode("job-4", "validate", string(valid))
	var output bytes.Buffer
	if err := serve(&requests, &output); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(&output)
	for i, id := range []string{"ready", "job-1", "job-2", "job-2", "job-3", "job-4"} {
		var response struct {
			ID     string         `json:"request_id"`
			PID    int            `json:"process_id"`
			Error  *string        `json:"protocol_error"`
			Result map[string]any `json:"result"`
		}
		if decoder.Decode(&response) != nil || response.ID != id || response.PID != os.Getpid() {
			t.Fatal("correlation/PID")
		}
		if i == 3 || i == 4 {
			if response.Error == nil {
				t.Fatal("duplicate/action accepted")
			}
			continue
		}
		if response.Error != nil {
			t.Fatal(*response.Error)
		}
		if i == 0 {
			if response.Result["ready"] != true {
				t.Fatal("handshake")
			}
			continue
		}
		if response.Result["request_id"] != id {
			t.Fatal("result correlation")
		}
		if (response.Result["error"] != nil) != (i == 1) {
			t.Fatal("per-request validation state leaked or skipped")
		}
	}
}

func TestPersistentLimitsAndIDMismatch(t *testing.T) {
	var output bytes.Buffer
	if serve(strings.NewReader(strings.Repeat("x", maxEnvelope+1)), &output) == nil {
		t.Fatal("unbounded frame")
	}
	output.Reset()
	line := `{"request_id":"one","request":{"Action":"ping","RequestID":"two"}}` + "\n"
	if serve(strings.NewReader(line), &output) != nil || !strings.Contains(output.String(), "request_id_mismatch") {
		t.Fatal("ID mismatch")
	}
}
