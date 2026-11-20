// Copyright (c) 2025 Reliant Labs
package anthropic

import "github.com/reliant-labs/reliant/internal/llm/historyrepair"

// HistoryRules tells the shared request-history repair (historyrepair) what
// the Messages API accepts beyond the default: tool-call ids matching
// ^[a-zA-Z0-9_-]+$. A history that carries another provider's ids (a model
// switch mid-chat) is sent with those ids rewritten. Shared by the API-key and
// Claude Code clients, which embed baseClient.
func (b *baseClient) HistoryRules() historyrepair.Rules {
	return historyrepair.Rules{ToolCallIDs: historyrepair.AnthropicIDRules()}
}
