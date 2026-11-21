// Copyright (c) 2025 Reliant Labs
package codex

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/llm/historyrepair"
	"github.com/reliant-labs/reliant/internal/llm/historyrepair/historyrepairtest"
)

// TestHistoryRepair_CodexRequestIsValid: a damaged history, repaired by the
// shared step with this driver's rules, converts into Responses input the
// Codex backend accepts.
func TestHistoryRepair_CodexRequestIsValid(t *testing.T) {
	c := &CodexClient{}
	msgs, _ := historyrepair.Apply(historyrepairtest.BrokenHistory(), c.HistoryRules())
	historyrepairtest.CheckResponsesInput(t, c.convertMessages(msgs), responsesToolCallIDMaxLen)
}
