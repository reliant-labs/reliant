// Copyright (c) 2025 Reliant Labs
package copilot

import "github.com/reliant-labs/reliant/internal/llm/historyrepair"

// historyRuler is a dialect client with history rules of its own.
type historyRuler interface {
	HistoryRules() historyrepair.Rules
}

// HistoryRules are the rules of the dialect client the model is routed to: the
// Anthropic client's for Claude, the OpenAI client's (by endpoint) otherwise.
func (c *CopilotClient) HistoryRules() historyrepair.Rules {
	if r, ok := c.impl.(historyRuler); ok {
		return r.HistoryRules()
	}
	return historyrepair.Rules{}
}
