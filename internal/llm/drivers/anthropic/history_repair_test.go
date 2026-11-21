// Copyright (c) 2025 Reliant Labs
package anthropic

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/llm/historyrepair"
	"github.com/reliant-labs/reliant/internal/llm/historyrepair/historyrepairtest"
)

// TestHistoryRepair_AnthropicRequestIsValid: a damaged history, repaired by
// the shared step with this driver's rules, converts into a messages array the
// Messages API accepts. The repair replaced the orphan drop convertMessages
// used to do itself.
func TestHistoryRepair_AnthropicRequestIsValid(t *testing.T) {
	c := newTestClient()
	msgs, _ := historyrepair.Apply(historyrepairtest.BrokenHistory(), c.HistoryRules())
	historyrepairtest.CheckAnthropicMessages(t, c.convertMessages(msgs))

	cc := NewClaudeCodeClient(c.options)
	msgs, _ = historyrepair.Apply(historyrepairtest.BrokenHistory(), cc.HistoryRules())
	historyrepairtest.CheckAnthropicMessages(t, cc.convertMessages(msgs))
}
