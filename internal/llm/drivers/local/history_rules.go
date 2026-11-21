// Copyright (c) 2025 Reliant Labs
package local

import "github.com/reliant-labs/reliant/internal/llm/historyrepair"

// HistoryRules tells the shared request-history repair (historyrepair) what a
// local OpenAI-compatible server accepts beyond the default: tool-call ids of
// the shape llm.NewToolCallID mints (at most 40 characters of [a-zA-Z0-9_-]),
// which every server and chat template we route to takes. Consecutive user
// turns are not merged: ConvertMessages sends only a user message's first
// text part, so a merge would drop the rest.
func (c *LocalClient) HistoryRules() historyrepair.Rules {
	return historyrepair.Rules{ToolCallIDs: historyrepair.IDRules{Charset: true, MaxLen: 40}}
}
