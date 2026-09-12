// Copyright (c) 2025 Reliant Labs
package threads

import (
	"context"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// TestForkedThread_SeqContinuesAboveInheritedHistory pins the one behaviour
// that moving seq allocation onto a counter could plausibly break, and whose
// failure is a VISIBLE product bug rather than an error.
//
// A branched chat DISPLAYS its parent's messages but does not OWN those rows:
// they keep the parent's chat_id and are resolved on read by walking the
// context-window chain. So a counter seeded only from the branch's own rows
// starts at 0, and every reply the user sends lands numerically BENEATH the
// inherited transcript rendered above it. The message saves and streams
// correctly and appears at the TOP of the conversation — which reads to the
// user as "my message never arrived".
//
// This is exactly what GetNextSeqByChat's per-save recursive fork walk existed
// to prevent, so the counter has to inherit the same high-water mark at fork
// time. SeedSeqCounterForFork does that walk once per fork instead of once per
// message.
func TestForkedThread_SeqContinuesAboveInheritedHistory(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	// Parent thread with real history, saved through the production path so
	// the counter reflects it.
	parentThread, parentCW := h.createThread("fork-seed-parent", h.chatID)

	var lastParentSeq int64
	for i := 0; i < 5; i++ {
		if _, err := h.svc.SaveMessage(ctx, SaveMessageOpts{
			ChatID:  h.chatID,
			Thread:  parentThread.ID,
			Role:    int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT),
			Content: "parent history",
		}); err != nil {
			t.Fatalf("failed to save parent message %d: %v", i, err)
		}
	}

	parentMsgs, err := h.svc.LoadCurrentMessages(ctx, parentThread.ID)
	if err != nil {
		t.Fatalf("failed to load parent messages: %v", err)
	}
	if len(parentMsgs) == 0 {
		t.Fatal("expected parent to have messages")
	}
	for _, m := range parentMsgs {
		if m.Seq > lastParentSeq {
			lastParentSeq = m.Seq
		}
	}

	// Fork at the parent's newest message, into a DIFFERENT chat — this is
	// the branch case, where the child owns none of the inherited rows.
	branchChatID := h.createChat("")
	forkMsgID := parentMsgs[len(parentMsgs)-1].ID
	forkedThread, _, err := h.svc.ForkThread(ctx, ForkThreadOpts{
		ID:                    "fork-seed-branch",
		ChatID:                branchChatID,
		ParentThreadID:        parentThread.ID,
		ForkAtContextWindowID: parentCW.ID,
		ForkAtMessageID:       &forkMsgID,
	})
	if err != nil {
		t.Fatalf("failed to fork thread: %v", err)
	}

	// The branch's first message must sort ABOVE everything it inherited.
	res, err := h.svc.SaveMessage(ctx, SaveMessageOpts{
		ChatID:  branchChatID,
		Thread:  forkedThread.ID,
		Role:    int32(reliantv1.MessageRole_MESSAGE_ROLE_USER),
		Content: "first message in the branch",
	})
	if err != nil {
		t.Fatalf("failed to save branch message: %v", err)
	}

	branchMsgs, err := h.svc.LoadCurrentMessages(ctx, forkedThread.ID)
	if err != nil {
		t.Fatalf("failed to load branch messages: %v", err)
	}

	var saved *int64
	for _, m := range branchMsgs {
		if m.ID == res.MessageID {
			seq := m.Seq
			saved = &seq
		}
	}
	if saved == nil {
		t.Fatalf("branch message %s not found in resolved history", res.MessageID)
	}

	if *saved <= lastParentSeq {
		t.Errorf("branch message got seq %d, which is not above the inherited high-water mark %d; "+
			"it will render at the TOP of the transcript and read as 'my message never arrived'",
			*saved, lastParentSeq)
	}

	// And it must genuinely be last in the resolved order the UI sorts by.
	var maxSeq int64 = -1
	var lastID string
	for _, m := range branchMsgs {
		if m.Seq > maxSeq {
			maxSeq = m.Seq
			lastID = m.ID
		}
	}
	if lastID != res.MessageID {
		t.Errorf("expected the newly saved branch message to sort last; got %s at seq %d", lastID, maxSeq)
	}
}
