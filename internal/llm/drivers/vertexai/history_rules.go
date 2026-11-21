// Copyright (c) 2025 Reliant Labs
package vertexai

import "github.com/reliant-labs/reliant/internal/llm/historyrepair"

// HistoryRules tells the shared request-history repair (historyrepair) what
// the model's Vertex API accepts beyond the default. Claude takes the
// Anthropic tool-call id charset. Gemini refuses two user turns in a row, and
// its converter sends every text and binary part of a user message, so they
// are merged; it sends no tool-call ids, so ids are left alone.
func (c *VertexAIClient) HistoryRules() historyrepair.Rules {
	if c.provider == providerClaude {
		return historyrepair.Rules{ToolCallIDs: historyrepair.AnthropicIDRules()}
	}
	return historyrepair.Rules{MergeConsecutiveUserTurns: true}
}
