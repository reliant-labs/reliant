// Copyright (c) 2025 Reliant Labs
package reliant

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/assert"
)

// The reliant driver proxies OpenAI-shaped completions through LiteLLM, so it
// carries its own copy of the OpenAI finish-reason switch — including
// "content_filter" => Refusal. A third copy of the same mapping is exactly the
// kind that drifts, which is why it is pinned here rather than assumed from
// the openai driver's test.
func TestFinishReason(t *testing.T) {
	tests := []struct {
		input    string
		expected message.FinishReason
	}{
		{"stop", message.FinishReasonEndTurn},
		{"length", message.FinishReasonMaxTokens},
		{"tool_calls", message.FinishReasonToolUse},
		{"content_filter", message.FinishReasonRefusal},
		{"something_unknown", message.FinishReasonUnknown},
		{"", message.FinishReasonUnknown},
	}

	// finishReason never reads the receiver, so a zero value is enough.
	c := &ReliantClient{}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.expected, c.finishReason(tt.input))
		})
	}
}
