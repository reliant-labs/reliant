// Copyright (c) 2025 Reliant Labs
package codex

import "github.com/reliant-labs/reliant/internal/llm/historyrepair"

// responsesToolCallIDMaxLen is the longest call_id the Responses API takes.
const responsesToolCallIDMaxLen = 64

// HistoryRules tells the shared request-history repair (historyrepair) what
// the Responses API accepts beyond the default: call_ids of at most 64
// characters. A longer id (another provider's, after a model switch) is sent
// rewritten.
func (c *CodexClient) HistoryRules() historyrepair.Rules {
	return historyrepair.Rules{ToolCallIDs: historyrepair.IDRules{MaxLen: responsesToolCallIDMaxLen}}
}
