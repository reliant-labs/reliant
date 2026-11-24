// Copyright (c) 2025 Reliant Labs
package openai

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/authortag/authortagtest"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/historyrepair"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// TestAuthorTag_OpenAISingleAuthorRequestUnchanged: a single-person chat sends the same request bytes through
// this driver whether or not its history went through author tagging
// (chat-collaboration.md §6.2), so its prompt cache survives the feature.
func TestAuthorTag_OpenAISingleAuthorRequestUnchanged(t *testing.T) {
	t.Run("chat completions", func(t *testing.T) {
		c := NewClient(llm.DriverOptions{Model: models.Model{APIModel: "gpt-4.1"}})
		authortagtest.RequireSingleAuthorRequestUnchanged(t, func(m []message.Message) any {
			msgs, _ := historyrepair.Apply(m, c.HistoryRules())
			return c.ConvertMessages([]string{"You are a coding agent."}, msgs)
		})
	})
	t.Run("responses", func(t *testing.T) {
		c := NewClient(llm.DriverOptions{Model: models.Model{APIModel: "gpt-5", PreferredEndpoint: "responses"}})
		authortagtest.RequireSingleAuthorRequestUnchanged(t, func(m []message.Message) any {
			msgs, _ := historyrepair.Apply(m, c.HistoryRules())
			return c.convertMessagesToResponsesInput([]string{"You are a coding agent."}, msgs)
		})
	})
}
