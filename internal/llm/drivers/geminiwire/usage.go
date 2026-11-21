// Copyright (c) 2025 Reliant Labs
package geminiwire

import (
	"github.com/reliant-labs/reliant/internal/llm"
	"google.golang.org/genai"
)

// Usage is Gemini's usageMetadata, in the shape every transport can fill:
// the genai SDK's GenerateContentResponseUsageMetadata and antigravity's
// hand-rolled envelope both carry these counters, as int32 and int64.
type Usage struct {
	PromptTokenCount        int64
	CachedContentTokenCount int64
	ToolUsePromptTokenCount int64
	CandidatesTokenCount    int64
	ThoughtsTokenCount      int64
	TotalTokenCount         int64
}

// UsageFromGenAI reads the genai SDK's usage block; nil is the zero Usage.
func UsageFromGenAI(m *genai.GenerateContentResponseUsageMetadata) Usage {
	if m == nil {
		return Usage{}
	}
	return Usage{
		PromptTokenCount:        int64(m.PromptTokenCount),
		CachedContentTokenCount: int64(m.CachedContentTokenCount),
		ToolUsePromptTokenCount: int64(m.ToolUsePromptTokenCount),
		CandidatesTokenCount:    int64(m.CandidatesTokenCount),
		ThoughtsTokenCount:      int64(m.ThoughtsTokenCount),
		TotalTokenCount:         int64(m.TotalTokenCount),
	}
}

// TokenUsage maps Gemini's counters onto llm.TokenUsage.
//
// promptTokenCount INCLUDES cachedContentTokenCount (opencode gemini.ts:
// "non-cached input = prompt − cached"). llm.TokenUsage splits them, as
// responseswire.TokenUsage does for the same overlap on the Responses API:
// InputTokens is the uncached remainder and CacheReadInputTokens the cached
// prefix, so InputTokens + CacheReadInputTokens is the real prompt. The
// direct Gemini and Vertex drivers reported only the total, so their cache
// hits were invisible; antigravity reported both but copied the whole prompt
// into InputTokens, counting the cached prefix twice.
//
// toolUsePromptTokenCount (the results of Google's own server-side tools) is
// billed as input on top of the prompt, so it joins InputTokens.
//
// candidatesTokenCount excludes thoughtsTokenCount, though thoughts are
// generated and billed as output; OutputTokens is their sum and
// ReasoningTokens the thoughts alone, the subset llm.TokenUsage expects.
//
// TokenCount is totalTokenCount as reported, the context size the turn left
// behind; it is computed from the parts only if the provider omitted it.
func (u Usage) TokenUsage() llm.TokenUsage {
	if u == (Usage{}) {
		return llm.TokenUsage{}
	}
	cached := u.CachedContentTokenCount
	uncached := u.PromptTokenCount - cached
	if cached < 0 || uncached < 0 {
		// A cached count the prompt does not contain is not a split to
		// trust; keep the prompt whole rather than invent one.
		cached, uncached = 0, u.PromptTokenCount
	}
	total := u.TotalTokenCount
	if total == 0 {
		total = u.PromptTokenCount + u.ToolUsePromptTokenCount + u.CandidatesTokenCount + u.ThoughtsTokenCount
	}
	return llm.TokenUsage{
		TokenCount:           total,
		InputTokens:          uncached + u.ToolUsePromptTokenCount,
		OutputTokens:         u.CandidatesTokenCount + u.ThoughtsTokenCount,
		ReasoningTokens:      u.ThoughtsTokenCount,
		CachedInputTokens:    cached,
		CacheReadInputTokens: cached,
	}
}
