// Copyright (c) 2025 Reliant Labs
package vertexai

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/assert"
)

// Claude-on-Vertex speaks Anthropic's stop_reason vocabulary, so it needs the
// same mappings as the direct Anthropic driver. There is no longer a second
// switch to disagree with — both call anthropicwire.FinishReason — but this
// test keeps asserting the behaviour callers actually see, so cutting that
// delegation fails here rather than in production.
func TestConvertClaudeFinishReason(t *testing.T) {
	tests := []struct {
		input    string
		expected message.FinishReason
	}{
		{"end_turn", message.FinishReasonEndTurn},
		{"max_tokens", message.FinishReasonMaxTokens},
		{"tool_use", message.FinishReasonToolUse},
		// Must match anthropic/base.go: same provider, same stop_reason
		// vocabulary. This case was absent here until a test asserted actual
		// behaviour instead of assuming parity, and Vertex was silently
		// reporting Unknown for an ordinary end of turn.
		{"stop_sequence", message.FinishReasonEndTurn},
		// Paused, not finished — same value, same meaning, both transports.
		{"pause_turn", message.FinishReasonPauseTurn},
		{"refusal", message.FinishReasonRefusal},
		{"something_unknown", message.FinishReasonUnknown},
		{"", message.FinishReasonUnknown},
	}

	// convertClaudeFinishReason never reads the receiver.
	c := &VertexAIClient{}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.expected, c.convertClaudeFinishReason(tt.input))
		})
	}
}
