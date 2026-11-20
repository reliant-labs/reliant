// Copyright (c) 2025 Reliant Labs
package gemini

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/llm/historyrepair"
	"github.com/reliant-labs/reliant/internal/llm/historyrepair/historyrepairtest"
)

// TestHistoryRepair_GeminiRequestIsValid: a damaged history, repaired by the
// shared step with this driver's rules, converts into contents the Gemini API
// accepts.
func TestHistoryRepair_GeminiRequestIsValid(t *testing.T) {
	g := &GeminiClient{}
	msgs, _ := historyrepair.Apply(historyrepairtest.BrokenHistory(), g.HistoryRules())
	historyrepairtest.CheckGeminiContents(t, g.convertMessages(msgs))
}
