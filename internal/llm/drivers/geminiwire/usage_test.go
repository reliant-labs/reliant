// Copyright (c) 2025 Reliant Labs
package geminiwire

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/genai"

	"github.com/reliant-labs/reliant/internal/llm"
)

func TestUsageTokenUsage(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		in   Usage
		want llm.TokenUsage
	}{
		{
			name: "zero",
			in:   Usage{},
			want: llm.TokenUsage{},
		},
		{
			// promptTokenCount includes the cached prefix: input is the
			// remainder, so input + cache read is the real prompt.
			name: "cached prefix is split out of the prompt",
			in:   Usage{PromptTokenCount: 10000, CachedContentTokenCount: 8000, CandidatesTokenCount: 50, TotalTokenCount: 10050},
			want: llm.TokenUsage{TokenCount: 10050, InputTokens: 2000, OutputTokens: 50, CachedInputTokens: 8000, CacheReadInputTokens: 8000},
		},
		{
			// candidatesTokenCount excludes thoughts, which are billed output.
			name: "thoughts are output and reasoning",
			in:   Usage{PromptTokenCount: 57, CandidatesTokenCount: 6, ThoughtsTokenCount: 1280, TotalTokenCount: 1343},
			want: llm.TokenUsage{TokenCount: 1343, InputTokens: 57, OutputTokens: 1286, ReasoningTokens: 1280},
		},
		{
			name: "tool-use prompt tokens are billed input",
			in:   Usage{PromptTokenCount: 100, ToolUsePromptTokenCount: 40, CandidatesTokenCount: 10, TotalTokenCount: 150},
			want: llm.TokenUsage{TokenCount: 150, InputTokens: 140, OutputTokens: 10},
		},
		{
			name: "a cached count larger than the prompt is not trusted",
			in:   Usage{PromptTokenCount: 10, CachedContentTokenCount: 20, TotalTokenCount: 10},
			want: llm.TokenUsage{TokenCount: 10, InputTokens: 10},
		},
		{
			name: "a missing total is computed",
			in:   Usage{PromptTokenCount: 10, CandidatesTokenCount: 5, ThoughtsTokenCount: 3},
			want: llm.TokenUsage{TokenCount: 18, InputTokens: 10, OutputTokens: 8, ReasoningTokens: 3},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.in.TokenUsage())
		})
	}
}

func TestUsageFromGenAI(t *testing.T) {
	t.Parallel()
	assert.Equal(t, Usage{}, UsageFromGenAI(nil))
	assert.Equal(t, Usage{
		PromptTokenCount: 1, CachedContentTokenCount: 2, ToolUsePromptTokenCount: 3,
		CandidatesTokenCount: 4, ThoughtsTokenCount: 5, TotalTokenCount: 6,
	}, UsageFromGenAI(&genai.GenerateContentResponseUsageMetadata{
		PromptTokenCount: 1, CachedContentTokenCount: 2, ToolUsePromptTokenCount: 3,
		CandidatesTokenCount: 4, ThoughtsTokenCount: 5, TotalTokenCount: 6,
	}))
}
