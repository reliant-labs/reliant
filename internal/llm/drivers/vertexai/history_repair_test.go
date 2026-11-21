// Copyright (c) 2025 Reliant Labs
package vertexai

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/llm/historyrepair"
	"github.com/reliant-labs/reliant/internal/llm/historyrepair/historyrepairtest"
)

// TestHistoryRepair_VertexRequestIsValid: a damaged history, repaired by the
// shared step with the rules of each Vertex provider, converts into a request
// that provider accepts.
func TestHistoryRepair_VertexRequestIsValid(t *testing.T) {
	t.Run("gemini", func(t *testing.T) {
		c := &VertexAIClient{provider: providerGemini}
		msgs, _ := historyrepair.Apply(historyrepairtest.BrokenHistory(), c.HistoryRules())
		historyrepairtest.CheckGeminiContents(t, c.convertMessagesToGemini(msgs))
	})
	t.Run("claude", func(t *testing.T) {
		c := &VertexAIClient{provider: providerClaude}
		msgs, _ := historyrepair.Apply(historyrepairtest.BrokenHistory(), c.HistoryRules())
		historyrepairtest.CheckAnthropicMessages(t, c.convertMessagesToClaude(msgs))
	})
}
