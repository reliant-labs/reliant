// Copyright (c) 2025 Reliant Labs
package responseswire

import (
	"testing"

	"github.com/openai/openai-go/v3/responses"
)

func TestTokenUsage(t *testing.T) {
	usage := func(input, cached, output, total int64) responses.ResponseUsage {
		var u responses.ResponseUsage
		u.InputTokens, u.InputTokensDetails.CachedTokens = input, cached
		u.OutputTokens, u.TotalTokens = output, total
		return u
	}

	t.Run("cached prefix is split out of input, counted once in the total", func(t *testing.T) {
		got := TokenUsage(usage(520_000, 500_000, 300, 520_300))
		if got.TokenCount != 520_300 || got.InputTokens != 20_000 || got.CacheReadInputTokens != 500_000 || got.CachedInputTokens != 500_000 || got.OutputTokens != 300 {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("no usage block maps to zero", func(t *testing.T) {
		if got := TokenUsage(responses.ResponseUsage{}); got.TokenCount != 0 || got.InputTokens != 0 {
			t.Errorf("got %+v, want zero", got)
		}
	})
	t.Run("cached larger than input is not trusted as a split", func(t *testing.T) {
		got := TokenUsage(usage(100, 4096, 10, 110))
		if got.InputTokens != 100 || got.CacheReadInputTokens != 0 || got.TokenCount != 110 {
			t.Errorf("got %+v, want input kept whole with no cache split", got)
		}
	})
}
