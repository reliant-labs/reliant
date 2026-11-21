// Copyright (c) 2025 Reliant Labs
package gemini

import "github.com/reliant-labs/reliant/internal/llm/historyrepair"

// HistoryRules tells the shared request-history repair (historyrepair) what
// the Gemini API accepts beyond the default: no two user turns in a row
// ("multiturn requests must alternate"), so they are merged; convertMessages
// sends every text and binary part of a user message, so nothing is lost.
// Tool-call ids are not sent (results pair with calls by name), so they are
// left alone.
func (g *GeminiClient) HistoryRules() historyrepair.Rules {
	return historyrepair.Rules{MergeConsecutiveUserTurns: true}
}
