// Copyright (c) 2025 Reliant Labs
package local

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/historyrepair"
	"github.com/reliant-labs/reliant/internal/llm/historyrepair/historyrepairtest"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// TestHistoryRepair_LocalRequestIsValid: a damaged history, repaired by the
// shared step with this driver's rules, converts into a chat request a local
// OpenAI-compatible server accepts.
func TestHistoryRepair_LocalRequestIsValid(t *testing.T) {
	c := NewClient(llm.DriverOptions{Model: models.Model{APIModel: "local-model"}})
	msgs, _ := historyrepair.Apply(historyrepairtest.BrokenHistory(), c.HistoryRules())
	historyrepairtest.CheckChatMessages(t, c.ConvertMessages(nil, msgs), 40, true)
}
