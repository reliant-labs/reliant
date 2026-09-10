// Copyright (c) 2025 Reliant Labs
package anthropic

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/models/message"
)

// Redacted reasoning must survive the round trip.
//
// Anthropic returns a redacted_thinking block when its safety system withholds
// the model's reasoning: an opaque, encrypted payload with no readable text.
// The API requires those blocks be passed back UNCHANGED on the next turn of a
// multi-turn exchange, exactly as ordinary thinking blocks are.
//
// Neither driver extracted the block at all before this, so it was dropped on
// arrival — and a dropped block means the history replayed to the provider no
// longer matches what it sent, which invalidates the cached prefix and can
// fail verification outright.
//
// The other half of the contract is that the payload is NOT text: it must never
// be rendered, and must never be concatenated into thinking content where a UI
// would print ciphertext as the model's reasoning.

// assistantWithRedactedThinking builds an assistant turn carrying a sealed
// reasoning block alongside ordinary text.
func assistantWithRedactedThinking(data string) message.Message {
	return message.Message{
		ID:   "m-redacted",
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.RedactedReasoningContent{Data: data},
			message.TextContent{Text: "the answer"},
		},
	}
}

// countRedactedBlocks reports how many redacted_thinking blocks convertMessages
// emitted.
func countRedactedBlocks(t *testing.T, msgs []message.Message) int {
	t.Helper()
	b := &baseClient{options: adaptiveOpts()}
	converted := b.convertMessages(msgs)

	n := 0
	for _, m := range converted {
		for _, blk := range m.Content {
			if blk.OfRedactedThinking != nil {
				n++
			}
		}
	}
	return n
}

func TestConvertMessages_ReplaysRedactedThinkingBlock(t *testing.T) {
	got := countRedactedBlocks(t, []message.Message{assistantWithRedactedThinking("sealed==")})

	if got != 1 {
		t.Fatalf("replayed %d redacted thinking blocks, want 1 — the API requires "+
			"they be passed back unchanged, and dropping one desyncs the history "+
			"from what the provider actually sent", got)
	}
}

// A sealed block carries no signature of its own — the payload IS the sealed
// artifact. The signature gate that (correctly) guards ordinary thinking blocks
// must not be applied to it, or every redacted block would be silently dropped.
func TestConvertMessages_RedactedThinkingNeedsNoSignature(t *testing.T) {
	msgs := []message.Message{{
		ID:   "m-1",
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.RedactedReasoningContent{Data: "sealed=="},
		},
	}}

	if got := countRedactedBlocks(t, msgs); got != 1 {
		t.Fatalf("replayed %d redacted blocks for a turn with no signature, want 1", got)
	}
}

// Several sealed blocks in one turn all replay. Returning only the first would
// silently truncate the provider's own history.
func TestConvertMessages_ReplaysEveryRedactedBlock(t *testing.T) {
	msgs := []message.Message{{
		ID:   "m-1",
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.RedactedReasoningContent{Data: "first=="},
			message.RedactedReasoningContent{Data: "second=="},
			message.TextContent{Text: "the answer"},
		},
	}}

	if got := countRedactedBlocks(t, msgs); got != 2 {
		t.Fatalf("replayed %d redacted blocks, want 2 — dropping any of them "+
			"truncates the history the provider sent", got)
	}
}

// An empty payload is not a block. Sending one would be a malformed request.
func TestConvertMessages_SkipsEmptyRedactedPayload(t *testing.T) {
	if got := countRedactedBlocks(t, []message.Message{assistantWithRedactedThinking("")}); got != 0 {
		t.Fatalf("replayed %d redacted blocks for an empty payload, want 0", got)
	}
}

// The display contract: sealed reasoning has no readable text, so String() —
// which the UI treats as displayable content — must yield nothing. If this ever
// returns the payload, ciphertext renders in the transcript as though the model
// had written it.
func TestRedactedReasoningContent_IsNeverDisplayable(t *testing.T) {
	part := message.RedactedReasoningContent{Data: "EncryptedOpaquePayload=="}

	if got := part.String(); got != "" {
		t.Fatalf("redacted reasoning rendered as %q, want empty — the payload is "+
			"ciphertext and must never reach a display surface", got)
	}
}
