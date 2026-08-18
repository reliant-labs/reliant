// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/models/message"
)

// An assistant message with no content, no tool calls and no reasoning renders
// as a row with ZERO content blocks. That row is durable poison:
//
//   - it becomes the tail of the loaded conversation,
//   - CallLLM's end-of-history guard sees an assistant message last and
//     refuses to build a request ("conversation history ends with assistant
//     message after all transformations ..."),
//   - the retry ladder re-runs against the same row, so it fails identically
//     every time and the chat can never advance again.
//
// Measured on the live database: 22 such rows, and the two newest (written
// within 50ms of each other) wedged their two chats at 24 logged failures each.
//
// SaveMessage now refuses to create them. This is the RECOVERY half — it lets a
// thread already carrying one heal on its next turn instead of staying stuck.

func assistantWithText(id, text string) message.Message {
	return message.Message{
		ID:    id,
		Role:  message.Assistant,
		Parts: []message.ContentPart{message.TextContent{Text: text}},
	}
}

func blocklessAssistant(id string) message.Message {
	return message.Message{ID: id, Role: message.Assistant}
}

func userWithText(id, text string) message.Message {
	return message.Message{
		ID:    id,
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: text}},
	}
}

func TestDropBlocklessAssistantMessages(t *testing.T) {
	t.Run("drops a blockless assistant message", func(t *testing.T) {
		msgs := []message.Message{
			userWithText("u1", "hello"),
			assistantWithText("a1", "hi"),
			userWithText("u2", "again"),
			blocklessAssistant("a2"),
		}

		kept, dropped := dropBlocklessAssistantMessages(msgs)

		if dropped != 1 {
			t.Fatalf("expected 1 dropped, got %d", dropped)
		}
		if len(kept) != 3 {
			t.Fatalf("expected 3 kept, got %d", len(kept))
		}
		// The whole point: the history must no longer END on the poison row.
		if last := kept[len(kept)-1]; last.Role != message.User {
			t.Fatalf("after dropping, history must end with the user message, got role %v (id %s)",
				last.Role, last.ID)
		}
	})

	t.Run("keeps an assistant message with text", func(t *testing.T) {
		msgs := []message.Message{assistantWithText("a1", "real answer")}
		kept, dropped := dropBlocklessAssistantMessages(msgs)
		if dropped != 0 || len(kept) != 1 {
			t.Fatalf("a message with text must be kept, got dropped=%d kept=%d", dropped, len(kept))
		}
	})

	// Whitespace-only is still blockless in effect — it produces no usable
	// content — and would wedge the thread the same way.
	t.Run("treats whitespace-only content as blockless", func(t *testing.T) {
		msgs := []message.Message{assistantWithText("a1", "   \n\t ")}
		_, dropped := dropBlocklessAssistantMessages(msgs)
		if dropped != 1 {
			t.Fatalf("whitespace-only assistant content must be treated as blockless, got dropped=%d", dropped)
		}
	})

	// Thinking counts as content: createAssistantContentBlocks emits a
	// thinking block, so a reasoning-only turn is NOT blockless and dropping it
	// would silently discard the model's reasoning.
	t.Run("keeps a reasoning-only assistant message", func(t *testing.T) {
		msgs := []message.Message{{
			ID:    "a1",
			Role:  message.Assistant,
			Parts: []message.ContentPart{message.ReasoningContent{Thinking: "let me consider"}},
		}}
		_, dropped := dropBlocklessAssistantMessages(msgs)
		if dropped != 0 {
			t.Fatal("a reasoning-only assistant message carries a thinking block and must be kept")
		}
	})

	// Only ASSISTANT rows are filtered. An empty tool or user message is a
	// different bug with a different repair, and silently dropping either would
	// hide it.
	t.Run("never drops non-assistant messages", func(t *testing.T) {
		msgs := []message.Message{
			{ID: "u1", Role: message.User},
			{ID: "t1", Role: message.Tool},
			{ID: "s1", Role: message.System},
		}
		kept, dropped := dropBlocklessAssistantMessages(msgs)
		if dropped != 0 || len(kept) != 3 {
			t.Fatalf("non-assistant roles must never be dropped, got dropped=%d kept=%d", dropped, len(kept))
		}
	})

	// The common path must not pay for a copy.
	t.Run("returns the original slice when nothing is dropped", func(t *testing.T) {
		msgs := []message.Message{assistantWithText("a1", "x")}
		kept, dropped := dropBlocklessAssistantMessages(msgs)
		if dropped != 0 {
			t.Fatalf("expected nothing dropped, got %d", dropped)
		}
		if len(kept) != len(msgs) {
			t.Fatalf("length changed: %d vs %d", len(kept), len(msgs))
		}
	})

	t.Run("empty input is safe", func(t *testing.T) {
		kept, dropped := dropBlocklessAssistantMessages(nil)
		if dropped != 0 || len(kept) != 0 {
			t.Fatalf("nil input must be a no-op, got dropped=%d kept=%d", dropped, len(kept))
		}
	})
}

// The end-to-end shape of the production failure: a thread whose tail is a
// blockless assistant row must, after filtering, end on something CallLLM will
// accept (user or tool).
func TestDropBlocklessAssistant_UnwedgesHistoryTail(t *testing.T) {
	msgs := []message.Message{
		userWithText("u1", "do the thing"),
		assistantWithText("a1", "working on it"),
		userWithText("u2", "?"),
		blocklessAssistant("a-poison"),
	}

	kept, dropped := dropBlocklessAssistantMessages(msgs)
	if dropped == 0 {
		t.Fatal("the poison row must be dropped")
	}

	last := kept[len(kept)-1]
	if last.Role == message.Assistant {
		t.Fatalf("history still ends with an assistant message (id %s) — CallLLM's "+
			"guard would still refuse to build a request and the chat stays wedged", last.ID)
	}
}
