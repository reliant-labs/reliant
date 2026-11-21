// Copyright (c) 2025 Reliant Labs
package antigravity

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/llm/historyrepair"
	"github.com/reliant-labs/reliant/internal/llm/historyrepair/historyrepairtest"
)

// TestHistoryRepair_AntigravityRequestIsValid: a damaged history, repaired by
// the shared step with this driver's rules, converts into contents
// Antigravity's Gemini-shaped API accepts.
func TestHistoryRepair_AntigravityRequestIsValid(t *testing.T) {
	c := &Client{}
	msgs, _ := historyrepair.Apply(historyrepairtest.BrokenHistory(), c.HistoryRules())
	historyrepairtest.CheckGeminiContents(t, convertMessages(msgs))
}
