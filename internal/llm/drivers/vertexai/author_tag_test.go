// Copyright (c) 2025 Reliant Labs
package vertexai

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/authortag/authortagtest"
	"github.com/reliant-labs/reliant/internal/llm/historyrepair"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// TestAuthorTag_VertexSingleAuthorRequestUnchanged: a single-person chat sends the same request bytes through
// this driver whether or not its history went through author tagging
// (chat-collaboration.md §6.2), so its prompt cache survives the feature.
func TestAuthorTag_VertexSingleAuthorRequestUnchanged(t *testing.T) {
	t.Run("gemini", func(t *testing.T) {
		c := &VertexAIClient{provider: providerGemini}
		authortagtest.RequireSingleAuthorRequestUnchanged(t, func(m []message.Message) any {
			msgs, _ := historyrepair.Apply(m, c.HistoryRules())
			return c.convertMessagesToGemini(msgs)
		})
	})
	t.Run("claude", func(t *testing.T) {
		c := &VertexAIClient{provider: providerClaude}
		authortagtest.RequireSingleAuthorRequestUnchanged(t, func(m []message.Message) any {
			msgs, _ := historyrepair.Apply(m, c.HistoryRules())
			return c.convertMessagesToClaude(msgs)
		})
	})
}
