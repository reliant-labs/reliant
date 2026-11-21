// Copyright (c) 2025 Reliant Labs
package reliant

import "github.com/reliant-labs/reliant/internal/llm/historyrepair"

// HistoryRules tells the shared request-history repair (historyrepair) to
// replay tool-call ids verbatim: the LiteLLM gateway carries a Gemini thought
// signature in the id (llm.NewToolCallIDKeepingThoughtSignature) and reads it
// back from the next request, so rewriting one would drop the signature.
func (c *ReliantClient) HistoryRules() historyrepair.Rules {
	return historyrepair.Rules{}
}
