// Copyright (c) 2025 Reliant Labs
package antigravity

import "testing"

// Wire capture (gemini-3.8-flash@antigravity): candidatesTokenCount:6,
// thoughtsTokenCount:1280, totalTokenCount:1343. Thought tokens are billed and
// generated, so they belong in OutputTokens.
func TestUsage_OutputTokensIncludeThoughts(t *testing.T) {
	resp := &generateResp{}
	resp.UsageMetadata = &usageMetadata{
		PromptTokenCount:     57,
		CandidatesTokenCount: 6,
		ThoughtsTokenCount:   1280,
		TotalTokenCount:      1343,
	}
	got := usage(resp)
	if got.OutputTokens != 1286 {
		t.Errorf("OutputTokens = %d, want 1286 (candidates + thoughts)", got.OutputTokens)
	}
	if got.ReasoningTokens != 1280 {
		t.Errorf("ReasoningTokens = %d, want 1280", got.ReasoningTokens)
	}
	if got.TokenCount != 1343 {
		t.Errorf("TokenCount = %d, want 1343 (provider total, unchanged)", got.TokenCount)
	}
}
