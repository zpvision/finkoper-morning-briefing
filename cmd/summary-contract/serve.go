package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"finkoper-morning-briefing/internal/briefing"
	"io"
	"os"
	"strings"
)

const maxEnvelope = 8*briefing.MaxBody + 4096
const maxHelperRequests = 100000

// serve is a bounded, stdin/stdout JSONL diagnostic protocol, never an HTTP API.
// The caller owns correlation and timeouts. Request bodies and reasoning are never logged.
func serve(input io.Reader, output io.Writer) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), maxEnvelope)
	encoder := json.NewEncoder(output)
	seen := make(map[string]struct{})
	for scanner.Scan() {
		var envelope struct {
			ID      string          `json:"request_id"`
			Request json.RawMessage `json:"request"`
		}
		var result json.RawMessage
		var protocolError any
		if briefing.Strict(scanner.Bytes(), &envelope) != nil || envelope.ID == "" || len(envelope.ID) > 128 || strings.TrimSpace(envelope.ID) != envelope.ID {
			protocolError = "invalid_envelope"
		} else if _, duplicate := seen[envelope.ID]; duplicate {
			protocolError = "duplicate_request_id"
		} else if len(seen) >= maxHelperRequests {
			protocolError = "helper_request_limit"
		} else {
			seen[envelope.ID] = struct{}{}
			var header struct{ Action, RequestID string }
			if json.Unmarshal(envelope.Request, &header) != nil || header.RequestID != envelope.ID {
				protocolError = "request_id_mismatch"
			} else if header.Action == "ping" {
				result, _ = json.Marshal(map[string]any{"ready": true, "protocol": "briefing-validation-v1", "request_id": envelope.ID, "process_id": os.Getpid()})
			} else if header.Action != "validate" {
				protocolError = "unsupported_persistent_action"
			} else {
				var body bytes.Buffer
				if run(bytes.NewReader(envelope.Request), &body) != nil {
					protocolError = "validation_contract_error"
				} else {
					result = append(json.RawMessage(nil), body.Bytes()...)
				}
			}
		}
		if err := encoder.Encode(map[string]any{"request_id": envelope.ID, "process_id": os.Getpid(), "result": result, "protocol_error": protocolError}); err != nil {
			return errors.New("helper_response_write_failed")
		}
	}
	if scanner.Err() != nil {
		return errors.New("helper_input_read_failed_or_too_large")
	}
	return nil
}
