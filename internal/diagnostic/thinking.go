package diagnostic

import (
	"encoding/json"
	"time"

	"finkoper-morning-briefing/internal/briefing"
)

// Diagnostic only. Working API constructors keep think=false and reserve=1500.
func NewThinkingRunner(base string, counter briefing.BudgetCounter) *Runner {
	runner := NewRunner(base, "qwen3:8b", 180*time.Second, counter)
	runner.Thinking = true
	return runner
}
func ThinkingVariant() Variant {
	return Variant{"THINK", briefing.SystemPrompt, briefing.PromptVersion, json.RawMessage(briefing.OutputSchema)}
}
