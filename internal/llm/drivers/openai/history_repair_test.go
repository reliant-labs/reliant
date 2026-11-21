// Copyright (c) 2025 Reliant Labs
package openai

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/historyrepair"
	"github.com/reliant-labs/reliant/internal/llm/historyrepair/historyrepairtest"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// TestHistoryRepair_OpenAIRequestIsValid: a damaged history, repaired by the
// shared step with this driver's rules, converts into a request each OpenAI
// endpoint accepts: the 70-character id is rewritten for both.
func TestHistoryRepair_OpenAIRequestIsValid(t *testing.T) {
	t.Run("chat completions", func(t *testing.T) {
		c := NewClient(llm.DriverOptions{Model: models.Model{APIModel: "gpt-4.1"}})
		msgs, _ := historyrepair.Apply(historyrepairtest.BrokenHistory(), c.HistoryRules())
		historyrepairtest.CheckChatMessages(t, c.ConvertMessages(nil, msgs), chatToolCallIDMaxLen, false)
	})
	t.Run("responses", func(t *testing.T) {
		c := NewClient(llm.DriverOptions{Model: models.Model{APIModel: "gpt-5", PreferredEndpoint: "responses"}})
		msgs, _ := historyrepair.Apply(historyrepairtest.BrokenHistory(), c.HistoryRules())
		historyrepairtest.CheckResponsesInput(t, c.convertMessagesToResponsesInput(nil, msgs), responsesToolCallIDMaxLen)
	})
}
