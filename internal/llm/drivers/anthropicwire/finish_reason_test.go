// Copyright (c) 2025 Reliant Labs
package anthropicwire

import (
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/assert"
)

// The whole stop_reason vocabulary, pinned in the one place both drivers now
// read it from.
//
// The inputs are the SDK's own StopReason constants rather than hand-typed
// strings: anthropic-sdk-go declares exactly these six, so if a value is
// renamed or removed under us this test stops compiling instead of quietly
// asserting a string the provider no longer sends. (A value ADDED to the SDK
// still has to be noticed by hand — Go cannot enumerate the constants — which
// is what the unknown-reason warning in FinishReason is for.)
func TestFinishReason_FullVocabulary(t *testing.T) {
	tests := []struct {
		input    anthropic.StopReason
		expected message.FinishReason
	}{
		{anthropic.StopReasonEndTurn, message.FinishReasonEndTurn},
		{anthropic.StopReasonMaxTokens, message.FinishReasonMaxTokens},
		// A custom stop sequence ends the turn. Mapped here and NOT on Vertex
		// once, which is the drift that made this package exist.
		{anthropic.StopReasonStopSequence, message.FinishReasonEndTurn},
		{anthropic.StopReasonToolUse, message.FinishReasonToolUse},
		// Paused, not finished. It must not collapse into EndTurn (which would
		// claim the model was done) or Unknown (which would leave the runtime
		// with nothing truthful to say).
		{anthropic.StopReasonPauseTurn, message.FinishReasonPauseTurn},
		{anthropic.StopReasonRefusal, message.FinishReasonRefusal},
	}

	for _, tt := range tests {
		t.Run(string(tt.input), func(t *testing.T) {
			assert.Equal(t, tt.expected, FinishReason(SourceAnthropic, string(tt.input)))
			// Same table for every transport: that is the invariant the two
			// per-driver copies could not hold on their own.
			assert.Equal(t, tt.expected, FinishReason(SourceVertexAI, string(tt.input)))
		})
	}
}

// Anything outside the vocabulary is Unknown — including the empty string,
// which is what the SDK reports for a message whose stop_reason has not
// arrived yet (message_start, or a truncated response).
func TestFinishReason_UnrecognizedIsUnknown(t *testing.T) {
	for _, input := range []string{"", "something_unknown", "END_TURN", "stop"} {
		t.Run("input="+input, func(t *testing.T) {
			assert.Equal(t, message.FinishReasonUnknown, FinishReason(SourceAnthropic, input))
		})
	}
}

// Every mapped reason must be distinguishable from the catch-all. Unknown is
// the reason the runtime reads as "nothing specific happened"; each of the
// others is something it has to be able to say out loud.
func TestFinishReason_KnownReasonsAreNeverUnknown(t *testing.T) {
	for _, input := range []anthropic.StopReason{
		anthropic.StopReasonEndTurn,
		anthropic.StopReasonMaxTokens,
		anthropic.StopReasonStopSequence,
		anthropic.StopReasonToolUse,
		anthropic.StopReasonPauseTurn,
		anthropic.StopReasonRefusal,
	} {
		t.Run(string(input), func(t *testing.T) {
			assert.NotEqual(t, message.FinishReasonUnknown, FinishReason(SourceAnthropic, string(input)))
		})
	}
}
