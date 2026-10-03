// Copyright (c) 2025 Reliant Labs
package threads

import (
	"context"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/workflow/messageconv"
)

// TestSaveMessage_PhaseRoundTrip walks the whole persistence leg of the phase
// pipeline against a real Postgres: SaveMessage writes it onto the assistant's
// TEXT content block, and messageconv reads it back as TextContent.Phase —
// which is the value the Responses drivers then resend.
//
// It goes through messageconv rather than asserting on the raw block because
// that is the actual consumer. A column that persists but never reaches
// TextContent is indistinguishable, from the provider's point of view, from
// not having stored it at all.
func TestSaveMessage_PhaseRoundTrip(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	thread, _ := h.createThread("phase-thread", h.chatID)

	textPartPhase := func(t *testing.T, messageID string) (phase string, found bool) {
		t.Helper()
		blocks, err := h.repo.ListContentBlocks(ctx, messageID)
		if err != nil {
			t.Fatalf("list content blocks: %v", err)
		}
		for _, block := range blocks {
			if block.BlockType != reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT {
				continue
			}
			part := messageconv.ContentBlockToPart(ctx, h.chatID, block, h.repo)
			text, ok := part.(message.TextContent)
			if !ok {
				t.Fatalf("text block converted to %T, want message.TextContent", part)
			}
			return text.Phase, true
		}
		return "", false
	}

	t.Run("phase survives save and load", func(t *testing.T) {
		result, err := h.svc.SaveMessage(ctx, SaveMessageOpts{
			ChatID:  h.chatID,
			Thread:  thread.ID,
			Role:    int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT),
			Content: "I'll inspect the local dev auth setup.",
			Phase:   "commentary",
		})
		if err != nil {
			t.Fatalf("SaveMessage: %v", err)
		}

		phase, found := textPartPhase(t, result.MessageID)
		if !found {
			t.Fatal("no text content block was written")
		}
		if phase != "commentary" {
			t.Errorf("TextContent.Phase = %q, want %q", phase, "commentary")
		}
	})

	t.Run("final_answer survives too", func(t *testing.T) {
		result, err := h.svc.SaveMessage(ctx, SaveMessageOpts{
			ChatID:  h.chatID,
			Thread:  thread.ID,
			Role:    int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT),
			Content: "Done — the auth setup was missing a token.",
			Phase:   "final_answer",
		})
		if err != nil {
			t.Fatalf("SaveMessage: %v", err)
		}

		phase, _ := textPartPhase(t, result.MessageID)
		if phase != "final_answer" {
			t.Errorf("TextContent.Phase = %q, want %q", phase, "final_answer")
		}
	})

	// A provider that reports no phase must read back as empty, not as a
	// phase we invented. An invented one would be RESENT on the next turn,
	// telling the model it had labelled an emission it never labelled.
	t.Run("absent phase stays empty", func(t *testing.T) {
		result, err := h.svc.SaveMessage(ctx, SaveMessageOpts{
			ChatID:  h.chatID,
			Thread:  thread.ID,
			Role:    int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT),
			Content: "no phase from this provider",
		})
		if err != nil {
			t.Fatalf("SaveMessage: %v", err)
		}

		phase, found := textPartPhase(t, result.MessageID)
		if !found {
			t.Fatal("no text content block was written")
		}
		if phase != "" {
			t.Errorf("TextContent.Phase = %q, want empty", phase)
		}
	})
}
