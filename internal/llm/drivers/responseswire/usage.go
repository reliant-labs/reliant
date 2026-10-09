// Copyright (c) 2025 Reliant Labs
package responseswire

import (
	"github.com/openai/openai-go/v3/responses"
	"github.com/reliant-labs/reliant/internal/llm"
)

// TokenUsage maps a Responses API usage block onto llm.TokenUsage. The openai
// and codex drivers both decode this block, so the mapping lives here once.
//
// In Responses semantics input_tokens ALREADY INCLUDES the cached prefix
// (input_tokens_details.cached_tokens). llm.TokenUsage splits them, as every
// other driver does: InputTokens is the uncached remainder and
// CacheReadInputTokens the cached prefix, so InputTokens + CacheRead +
// CacheCreation is the real prompt size. Copying input_tokens into InputTokens
// whole left CacheReadInputTokens at 0, and `[CallLLM] Usage` reported every
// codex turn as a 0% cache hit; populating CacheRead without subtracting it
// would count the cached prefix twice.
//
// TokenCount is total_tokens as reported — the context size this turn left
// behind, which drives compaction and the trim backstop. Cached tokens are
// inside it exactly once.
//
// A zero total (a response with no usage block) maps to the zero value.
func TokenUsage(u responses.ResponseUsage) llm.TokenUsage {
	if u.TotalTokens <= 0 {
		return llm.TokenUsage{}
	}
	cached := u.InputTokensDetails.CachedTokens
	uncached := u.InputTokens - cached
	if cached < 0 || uncached < 0 {
		// A cached count the input does not contain is not a split we can
		// trust; keep the reported input whole rather than invent one.
		cached, uncached = 0, u.InputTokens
	}
	return llm.TokenUsage{
		TokenCount:           u.TotalTokens,
		InputTokens:          uncached,
		OutputTokens:         u.OutputTokens,
		ReasoningTokens:      u.OutputTokensDetails.ReasoningTokens,
		CachedInputTokens:    cached,
		CacheReadInputTokens: cached,
	}
}
