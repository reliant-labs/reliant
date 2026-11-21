// Copyright (c) 2025 Reliant Labs
package openrouter

import "github.com/reliant-labs/reliant/internal/llm/historyrepair"

// HistoryRules tells the shared request-history repair (historyrepair) what
// OpenRouter accepts beyond the default. OpenRouter forwards the history to
// whichever upstream serves the model, so tool-call ids are held to what
// every one of them takes: Anthropic's charset and OpenAI's 40 characters.
// It overrides the embedded OpenaiClient's rules.
func (c *Client) HistoryRules() historyrepair.Rules {
	return historyrepair.Rules{ToolCallIDs: historyrepair.IDRules{Charset: true, MaxLen: 40}}
}
