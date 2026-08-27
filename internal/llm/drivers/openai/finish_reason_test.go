// Copyright (c) 2025 Reliant Labs
package openai

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/assert"
)

// "content_filter" is the OpenAI-shaped spelling of a provider refusal. Like
// Anthropic's "refusal" it can arrive with no content at all, so it has to
// reach the runtime as Refusal rather than Unknown — Unknown is the reason the
// runtime reads as an ordinary turn.
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
	o := &OpenaiClient{}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.expected, o.finishReason(tt.input))
		})
	}
}
