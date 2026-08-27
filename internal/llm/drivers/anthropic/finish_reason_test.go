// Copyright (c) 2025 Reliant Labs
package anthropic

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/assert"
)

// Anthropic's native stop_reason vocabulary, including "refusal" — the
// provider declining the turn. A refusal arrives with zero content blocks, so
// if it mapped to anything else it would reach the runtime indistinguishable
// from a healthy empty turn and silently end the chat.
//
// The pre-existing rows are here on purpose: the refusal mapping was added to
// a live switch, and the regression that actually costs something is one of
// the ordinary reasons drifting while attention was on the new case.
//
// The table itself now lives in anthropicwire and is pinned by its own test.
// This one stays anyway, one level up: it asserts that THIS driver still
// reaches that table, which is the wiring a refactor can quietly cut.
func TestFinishReason(t *testing.T) {
	tests := []struct {
		input    string
		expected message.FinishReason
	}{
		{"end_turn", message.FinishReasonEndTurn},
		{"max_tokens", message.FinishReasonMaxTokens},
		{"tool_use", message.FinishReasonToolUse},
		{"stop_sequence", message.FinishReasonEndTurn},
		{"pause_turn", message.FinishReasonPauseTurn},
		{"refusal", message.FinishReasonRefusal},
		{"something_unknown", message.FinishReasonUnknown},
		{"", message.FinishReasonUnknown},
	}

	// finishReason never reads the receiver, so a zero value is enough.
	b := &baseClient{}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.expected, b.finishReason(tt.input))
		})
	}
}

// Refusal must not collapse into the catch-all. Unknown is the reason the
// runtime treats as "nothing special happened"; refusal is the one it has to
// explain to the user.
func TestFinishReason_RefusalIsDistinctFromUnknown(t *testing.T) {
	b := &baseClient{}
	assert.NotEqual(t, b.finishReason("refusal"), b.finishReason("wat"))
}
