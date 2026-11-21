// Copyright (c) 2025 Reliant Labs
package antigravity

import "github.com/reliant-labs/reliant/internal/llm/historyrepair"

// HistoryRules tells the shared request-history repair (historyrepair) what
// Antigravity's Gemini-shaped API accepts beyond the default: no two user
// turns in a row, so they are merged (convertMessages sends every text and
// binary part of a user message). Tool-call ids are not sent, so they are
// left alone.
func (c *Client) HistoryRules() historyrepair.Rules {
	return historyrepair.Rules{MergeConsecutiveUserTurns: true}
}
