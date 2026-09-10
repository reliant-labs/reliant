// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These pin the boundary where a real turn was being thrown away.
//
// Observed on chat 7c37ad6c (2026-09-09 01:28:03 and 01:28:35, thirty seconds
// apart). The provider returned stop_reason "end_turn" with exactly one content
// block — a THINKING block, signed, holding 399 and 208 output tokens of real
// reasoning. The worker logged `thinkingLen=0 signatureLen=1456`, save_message
// found nothing to write, and the chat stopped with nothing on screen. Resending
// replayed a byte-identical prompt, so it failed the same way, indefinitely.
//
// Two independent defects produced that, and each gets a test here:
//
//  1. thinkingParts was fed ONLY by streamed thinking_delta events. The
//     authoritative final response's Thinking was never read, though Content
//     right beside it was. A driver that accumulates the block itself — which
//     the Anthropic SDK does — delivered its reasoning nowhere.
//  2. redacted_thinking blocks were not extracted at all.
//
// The signature half already worked, and is asserted alongside so a future
// change cannot fix one and silently drop the other.

// completeEventState runs handleComplete against a fresh state and returns it.
func completeEventState(t *testing.T, resp *llm.DriverResponse) *streamProcessingState {
	t.Helper()

	activity := &CallLLMActivity{}
	state := &streamProcessingState{blockStates: NewBlockStreamState()}
	activity.handleComplete(context.Background(), llm.DriverEvent{
		Type:     llm.EventComplete,
		Response: resp,
	}, state)

	return state
}

// The regression test for the stall. Thinking arrives on the final response and
// nowhere else — no thinking_delta was ever streamed — which is exactly the
// shape that wedged the chat.
func TestHandleComplete_TakesThinkingFromAuthoritativeResponse(t *testing.T) {
	state := completeEventState(t, &llm.DriverResponse{
		Thinking:          "reasoning the provider accumulated but never streamed",
		ThinkingSignature: "sig-1456",
		FinishReason:      message.FinishReasonEndTurn,
	})

	require.NotEmpty(t, state.thinkingParts,
		"thinking arrived on the final response and was dropped — this is the stall: "+
			"the signature survives, the reasoning it signs does not, and save_message "+
			"then sees a content-free turn")
	assert.Equal(t, "reasoning the provider accumulated but never streamed",
		joinThinking(state))
	assert.Equal(t, "sig-1456", state.thinkingSignature)
}

// A driver that DID stream its thinking must not be disturbed: the authoritative
// text is the same text, and overriding with it is a no-op rather than a
// duplication.
func TestHandleComplete_StreamedThinkingIsNotDuplicated(t *testing.T) {
	activity := &CallLLMActivity{}
	state := &streamProcessingState{
		blockStates:   NewBlockStreamState(),
		thinkingParts: []string{"streamed reasoning"},
	}

	activity.handleComplete(context.Background(), llm.DriverEvent{
		Type: llm.EventComplete,
		Response: &llm.DriverResponse{
			Thinking:     "streamed reasoning",
			FinishReason: message.FinishReasonEndTurn,
		},
	}, state)

	assert.Equal(t, "streamed reasoning", joinThinking(state),
		"the authoritative response repeats what was streamed; it must replace, not append")
}

// A turn that genuinely produced no thinking keeps producing none. This is the
// case that would break if the override were unconditional.
func TestHandleComplete_NoThinkingStaysEmpty(t *testing.T) {
	state := completeEventState(t, &llm.DriverResponse{
		Content:      "just an answer",
		FinishReason: message.FinishReasonEndTurn,
	})

	assert.Empty(t, joinThinking(state))
	assert.Equal(t, "just an answer", joinText(state))
}

// Redacted thinking is captured, and captured SEPARATELY. Concatenating the
// sealed payload into thinkingParts would put ciphertext on screen and into the
// transcript as if the model had said it.
func TestHandleComplete_CapturesRedactedThinkingWithoutRenderingIt(t *testing.T) {
	state := completeEventState(t, &llm.DriverResponse{
		RedactedThinking: "EncryptedOpaquePayload==",
		FinishReason:     message.FinishReasonEndTurn,
	})

	assert.Equal(t, "EncryptedOpaquePayload==", state.redactedThinking,
		"a redacted block must be kept — the API requires it be replayed unchanged")
	assert.Empty(t, joinThinking(state),
		"the sealed payload is not readable reasoning and must never reach thinking text")
}

// A turn holding only a signature, or only a sealed block, is NOT content-free.
// It is recoverable work, and reporting it as an empty response is what put the
// user in a resend loop that could not converge.
func TestContentFreeTurnExplanation_SignedAndRedactedTurnsAreNotEmpty(t *testing.T) {
	tests := []struct {
		name          string
		thinking      string
		signature     string
		redacted      string
		wantReported  bool
		wantReasoning string
	}{
		{
			name:         "signature alone is real work",
			signature:    "sig-1456",
			wantReported: false,
		},
		{
			name:         "sealed reasoning alone is real work",
			redacted:     "EncryptedOpaquePayload==",
			wantReported: false,
		},
		{
			name:         "readable thinking alone is real work",
			thinking:     "deliberating",
			wantReported: false,
		},
		{
			// The genuine empty turn still has to be reported, or the silent
			// stop this guard exists to prevent comes back.
			name:         "nothing at all is still reported",
			wantReported: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			explanation, reported := contentFreeTurnExplanation(
				false, "", tc.thinking, tc.signature, tc.redacted, 0,
				message.FinishReasonEndTurn,
			)

			assert.Equal(t, tc.wantReported, reported)
			if tc.wantReported {
				assert.NotEmpty(t, explanation)
			} else {
				assert.Empty(t, explanation,
					"a turn carrying recoverable reasoning must not be reported as empty")
			}
		})
	}
}

func joinThinking(state *streamProcessingState) string {
	out := ""
	for _, p := range state.thinkingParts {
		out += p
	}
	return out
}

func joinText(state *streamProcessingState) string {
	out := ""
	for _, p := range state.textParts {
		out += p
	}
	return out
}
