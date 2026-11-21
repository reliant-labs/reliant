// Copyright (c) 2025 Reliant Labs
package openai

import "github.com/reliant-labs/reliant/internal/llm/historyrepair"

// Tool-call id limits: Chat Completions refuses a tool_calls[].id over 40
// characters, the Responses API a call_id over 64.
const (
	chatToolCallIDMaxLen      = 40
	responsesToolCallIDMaxLen = 64
)

// HistoryRules tells the shared request-history repair (historyrepair) what
// the endpoint this model uses accepts beyond the default: the length of a
// tool-call id. A longer id (another provider's, after a model switch) is
// sent rewritten.
func (o *OpenaiClient) HistoryRules() historyrepair.Rules {
	if o.Options.Model.PreferredEndpoint == "responses" {
		return historyrepair.Rules{ToolCallIDs: historyrepair.IDRules{MaxLen: responsesToolCallIDMaxLen}}
	}
	return historyrepair.Rules{ToolCallIDs: historyrepair.IDRules{MaxLen: chatToolCallIDMaxLen}}
}
