// Copyright (c) 2025 Reliant Labs
package threads

import (
	"context"
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
)

// TestSeqCounter_HealsAfterDirectMessageInsert pins the interaction between
// the counter-based allocator and the writers that still bypass it.
//
// Three production paths insert messages with their own MAX()-based
// allocation rather than going through SaveMessage:
//
//	internal/grpc/services/chat_branch.go   branch-point tool repair
//	internal/grpc/services/approval.go      tool-denial message
//	internal/workflow/.../cleanup.go        orphaned tool-call repair
//
// Each of those advances messages past the counter without touching it. A
// counter that then handed out its own stale next value would collide with
// messages_chat_seq_key — which is not hypothetical: it is SQLSTATE 23505, and
// it is what this test caught when the allocator was first written without the
// GREATEST().
//
// Rather than require every such writer to know the counter exists, the
// allocator heals: it takes the greater of its own next value and the table's
// real high-water mark. This test is the contract for that, and it is what
// lets those three call sites keep working unmodified.
func TestSeqCounter_HealsAfterDirectMessageInsert(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	thread, cw := h.createThread("heal-thread", h.chatID)

	// Normal save through the counter path.
	if _, err := h.svc.SaveMessage(ctx, SaveMessageOpts{
		ChatID:  h.chatID,
		Thread:  thread.ID,
		Role:    int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT),
		Content: "first",
	}); err != nil {
		t.Fatalf("failed to save first message: %v", err)
	}

	// A repair-style writer inserts directly, jumping the sequence far ahead
	// of what the counter knows. This is exactly what the three paths above do.
	jumpedSeq := int64(500)
	jumpedOrdinal := int64(500)
	ts := time.Now().UTC()
	if err := h.repo.CreateMessage(ctx, &db.Message{
		ID:              "direct-insert-msg",
		ChatID:          h.chatID,
		Ordinal:         jumpedOrdinal,
		Seq:             jumpedSeq,
		ThreadID:        thread.ID,
		ContextWindowID: cw.ID,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_TOOL,
		CreatedAt:       ts,
		UpdatedAt:       ts,
	}); err != nil {
		t.Fatalf("failed to insert direct message: %v", err)
	}

	// The next counter-allocated save must land ABOVE the direct insert, not
	// collide with it and not sort beneath it.
	res, err := h.svc.SaveMessage(ctx, SaveMessageOpts{
		ChatID:  h.chatID,
		Thread:  thread.ID,
		Role:    int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT),
		Content: "after the direct insert",
	})
	if err != nil {
		t.Fatalf("save after direct insert failed (counter did not heal): %v", err)
	}

	msgs, err := h.repo.ListMessages(ctx, h.chatID, db.MessageListOptions{})
	if err != nil {
		t.Fatalf("failed to list messages: %v", err)
	}

	var savedSeq int64 = -1
	for _, m := range msgs {
		if m.ID == res.MessageID {
			savedSeq = m.Seq
		}
	}
	if savedSeq < 0 {
		t.Fatalf("saved message %s not found", res.MessageID)
	}
	if savedSeq <= jumpedSeq {
		t.Errorf("counter handed out seq %d, at or below the direct insert's %d — "+
			"the row would collide or sort above a newer message", savedSeq, jumpedSeq)
	}

	// No duplicates anywhere.
	seen := make(map[int64]string, len(msgs))
	for _, m := range msgs {
		if prev, dup := seen[m.Seq]; dup {
			t.Errorf("seq %d used by both %s and %s", m.Seq, prev, m.ID)
		}
		seen[m.Seq] = m.ID
	}
}
