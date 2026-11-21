// Copyright (c) 2025 Reliant Labs
package openrouter

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/historyrepair"
	"github.com/reliant-labs/reliant/internal/llm/historyrepair/historyrepairtest"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// TestHistoryRepair_OpenRouterRequestIsValid: a damaged history, repaired by
// the shared step with this driver's rules, converts into a chat request every
// upstream accepts: ids within Anthropic's charset and OpenAI's 40 characters.
func TestHistoryRepair_OpenRouterRequestIsValid(t *testing.T) {
	c := NewClient(llm.DriverOptions{Model: models.Model{APIModel: "anthropic/claude-sonnet-4.5"}})
	msgs, _ := historyrepair.Apply(historyrepairtest.BrokenHistory(), c.HistoryRules())
	historyrepairtest.CheckChatMessages(t, c.ConvertMessages(nil, msgs), 40, true)
}
