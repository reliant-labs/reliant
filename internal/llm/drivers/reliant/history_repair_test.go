// Copyright (c) 2025 Reliant Labs
package reliant

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/historyrepair"
	"github.com/reliant-labs/reliant/internal/llm/historyrepair/historyrepairtest"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// TestHistoryRepair_ReliantRequestIsValid: a damaged history, repaired by the
// shared step with this driver's rules, converts into a valid chat request,
// and a tool-call id carrying a Gemini thought signature reaches the gateway
// untouched.
func TestHistoryRepair_ReliantRequestIsValid(t *testing.T) {
	c := NewClient(llm.DriverOptions{ApiKey: "rlk-test", Model: models.Model{ID: "claude-opus-5"}})
	msgs, _ := historyrepair.Apply(historyrepairtest.BrokenHistory(), c.HistoryRules())
	historyrepairtest.CheckChatMessages(t, c.ConvertMessages(nil, msgs), 0, false)

	const signed = "call_0123__thought__c2lnbmF0dXJl+/="
	history := []message.Message{
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "go"}}},
		{Role: message.Assistant, Parts: []message.ContentPart{message.ToolCall{ID: signed, Name: "read", Input: "{}"}}},
		{Role: message.Tool, Parts: []message.ContentPart{message.ToolResult{ToolCallID: signed, Name: "read", Content: "A"}}},
	}
	msgs, _ = historyrepair.Apply(history, c.HistoryRules())
	require.Equal(t, signed, msgs[1].ToolCalls()[0].ID)
}
