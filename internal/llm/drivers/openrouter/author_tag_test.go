// Copyright (c) 2025 Reliant Labs
package openrouter

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/authortag/authortagtest"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/historyrepair"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// TestAuthorTag_OpenRouterSingleAuthorRequestUnchanged: a single-person chat sends the same request bytes through
// this driver whether or not its history went through author tagging
// (chat-collaboration.md §6.2), so its prompt cache survives the feature.
func TestAuthorTag_OpenRouterSingleAuthorRequestUnchanged(t *testing.T) {
	for _, model := range []string{"anthropic/claude-sonnet-4.5", "google/gemini-2.5-pro", "openai/gpt-4.1"} {
		t.Run(model, func(t *testing.T) {
			c := NewClient(llm.DriverOptions{Model: models.Model{APIModel: model}})
			authortagtest.RequireSingleAuthorRequestUnchanged(t, func(m []message.Message) any {
				msgs, _ := historyrepair.Apply(m, c.HistoryRules())
				return c.convertMessagesWithCacheControl([]string{"You are a coding agent."}, msgs)
			})
		})
	}
}
