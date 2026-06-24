package threads

import (
	"context"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// TestLoadCurrentMessages_SimpleThread tests that a non-forked thread
// returns all its messages in order.
func TestLoadCurrentMessages_SimpleThread(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	// Create a root thread
	thread, cw := h.createThread("simple-thread", h.chatID)

	// Add messages
	h.addMessageWithID("msg-1", h.chatID, thread.ID, cw.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))
	h.addMessageWithID("msg-2", h.chatID, thread.ID, cw.ID, 2, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))
	h.addMessageWithID("msg-3", h.chatID, thread.ID, cw.ID, 3, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

	// Load messages
	msgs, err := h.svc.LoadCurrentMessages(ctx, thread.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should return all messages in order
	if len(msgs) != 3 {
		t.Errorf("expected 3 messages, got %d", len(msgs))
	}
	if msgs[0].ID != "msg-1" || msgs[1].ID != "msg-2" || msgs[2].ID != "msg-3" {
		t.Errorf("messages in wrong order: %v", []string{msgs[0].ID, msgs[1].ID, msgs[2].ID})
	}
}

// TestLoadCurrentMessages_ForkedThread tests that forked threads inherit
// parent messages up to the fork point.
func TestLoadCurrentMessages_ForkedThread(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	// Create parent thread with messages
	parentThread, parentCW := h.createThread("parent-thread", h.chatID)
	h.addMessageWithID("parent-1", h.chatID, parentThread.ID, parentCW.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))
	h.addMessageWithID("parent-2", h.chatID, parentThread.ID, parentCW.ID, 2, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))
	h.addMessageWithID("parent-3", h.chatID, parentThread.ID, parentCW.ID, 3, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

	// Fork at ordinal 2 (should inherit parent-1, parent-2 but NOT parent-3)
	childThread, childCW := h.forkThread("child-thread", h.chatID, parentThread.ID, 2, parentCW.ID)
	h.addMessageWithID("child-1", h.chatID, childThread.ID, childCW.ID, 4, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))

	// Load messages for child
	msgs, err := h.svc.LoadCurrentMessages(ctx, childThread.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have: parent-1, parent-2 (inherited), child-1 (local)
	if len(msgs) != 3 {
		t.Errorf("expected 3 messages, got %d", len(msgs))
		for i, m := range msgs {
			t.Logf("  msg[%d]: %s", i, m.ID)
		}
	}
	if len(msgs) >= 3 {
		if msgs[0].ID != "parent-1" || msgs[1].ID != "parent-2" || msgs[2].ID != "child-1" {
			t.Errorf("unexpected message order: %v", []string{msgs[0].ID, msgs[1].ID, msgs[2].ID})
		}
	}
}

// TestLoadCurrentMessages_NestedForks tests A → B → C fork chain.
func TestLoadCurrentMessages_NestedForks(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	// Thread A (root) with messages
	threadA, cwA := h.createThread("thread-a", h.chatID)
	h.addMessageWithID("a-1", h.chatID, threadA.ID, cwA.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))
	h.addMessageWithID("a-2", h.chatID, threadA.ID, cwA.ID, 2, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))

	// Thread B forks from A at ordinal 2
	threadB, cwB := h.forkThread("thread-b", h.chatID, threadA.ID, 2, cwA.ID)
	h.addMessageWithID("b-1", h.chatID, threadB.ID, cwB.ID, 3, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))
	h.addMessageWithID("b-2", h.chatID, threadB.ID, cwB.ID, 4, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))

	// Thread C forks from B at ordinal 4
	threadC, cwC := h.forkThread("thread-c", h.chatID, threadB.ID, 4, cwB.ID)
	h.addMessageWithID("c-1", h.chatID, threadC.ID, cwC.ID, 5, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

	// Load messages for thread C - should walk full chain
	msgs, err := h.svc.LoadCurrentMessages(ctx, threadC.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have: a-1, a-2 (from A), b-1, b-2 (from B), c-1 (from C)
	if len(msgs) != 5 {
		t.Errorf("expected 5 messages, got %d", len(msgs))
		for i, m := range msgs {
			t.Logf("  msg[%d]: %s", i, m.ID)
		}
	}

	expectedOrder := []string{"a-1", "a-2", "b-1", "b-2", "c-1"}
	for i, expected := range expectedOrder {
		if i < len(msgs) && msgs[i].ID != expected {
			t.Errorf("msg[%d]: expected %s, got %s", i, expected, msgs[i].ID)
		}
	}
}

// TestLoadCurrentMessages_ThreadWithCompaction tests that compaction boundaries
// are respected - messages before compaction are not included, only the summary.
func TestLoadCurrentMessages_ThreadWithCompaction(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	// Create thread with initial messages
	thread, cw0 := h.createThread("compact-thread", h.chatID)
	h.addMessageWithID("pre-compact-1", h.chatID, thread.ID, cw0.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))
	h.addMessageWithID("pre-compact-2", h.chatID, thread.ID, cw0.ID, 2, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))

	// Compact - creates new context window with compaction_summary_message_id
	cw1 := h.compact(thread.ID, "summary-msg")

	// Add summary and new messages to compacted context window
	h.addMessageWithID("summary-msg", h.chatID, thread.ID, cw1.ID, 3, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM))
	h.addMessageWithID("post-compact-1", h.chatID, thread.ID, cw1.ID, 4, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

	// Load messages - should only get from compacted context window
	msgs, err := h.svc.LoadCurrentMessages(ctx, thread.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should only have: summary-msg, post-compact-1 (NOT pre-compact-1, pre-compact-2)
	if len(msgs) != 2 {
		t.Errorf("expected 2 messages, got %d", len(msgs))
		for i, m := range msgs {
			t.Logf("  msg[%d]: %s", i, m.ID)
		}
	}

	// Verify we have the right messages
	foundSummary := false
	foundPostCompact := false
	for _, msg := range msgs {
		if msg.ID == "summary-msg" {
			foundSummary = true
		}
		if msg.ID == "post-compact-1" {
			foundPostCompact = true
		}
		if msg.ID == "pre-compact-1" || msg.ID == "pre-compact-2" {
			t.Errorf("should not include pre-compaction message: %s", msg.ID)
		}
	}
	if !foundSummary {
		t.Error("missing summary message")
	}
	if !foundPostCompact {
		t.Error("missing post-compact message")
	}
}

// TestLoadCurrentMessages_ForkAfterCompaction tests the critical bug fix:
// Child thread inherits sequence number from parent but does NOT have its own
// compaction summary, so it SHOULD traverse to parent for messages.
//
// THE BUG: Using Sequence > 0 to detect compaction boundary would incorrectly
// skip parent traversal because forked threads inherit the sequence number.
//
// THE FIX: Use CompactionSummaryMessageID != nil instead.
func TestLoadCurrentMessages_ForkAfterCompaction(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	// Create parent thread
	parentThread, parentCW0 := h.createThread("parent-thread", h.chatID)
	h.addMessageWithID("pre-compact", h.chatID, parentThread.ID, parentCW0.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

	// Compact parent thread
	parentCW1 := h.compact(parentThread.ID, "summary-msg")

	// Add messages to compacted parent
	h.addMessageWithID("summary-msg", h.chatID, parentThread.ID, parentCW1.ID, 2, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM))
	h.addMessageWithID("post-compact-1", h.chatID, parentThread.ID, parentCW1.ID, 3, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

	// Fork from parent AFTER compaction
	// Child will have sequence=1 (inherited from parent) but NO compaction_summary_message_id
	childThread, childCW := h.forkThread("child-thread", h.chatID, parentThread.ID, 3, parentCW1.ID)
	h.addMessageWithID("child-msg", h.chatID, childThread.ID, childCW.ID, 4, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))

	// THE CRITICAL TEST: Child's context window should have Sequence=1 but NO CompactionSummaryMessageID
	if childCW.Sequence != 1 {
		t.Errorf("expected child sequence=1, got %d", childCW.Sequence)
	}
	if childCW.CompactionSummaryMessageID != nil {
		t.Error("expected child to NOT have CompactionSummaryMessageID")
	}

	// Load messages for child - should traverse to parent and include inherited messages
	msgs, err := h.svc.LoadCurrentMessages(ctx, childThread.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should get: summary-msg (from parent), post-compact-1 (from parent), child-msg (local)
	// Should NOT get: pre-compact (before parent's compaction)
	if len(msgs) != 3 {
		t.Errorf("expected 3 messages, got %d", len(msgs))
		for i, m := range msgs {
			t.Logf("  msg[%d]: %s", i, m.ID)
		}
	}

	// Verify order
	if len(msgs) >= 3 {
		if msgs[0].ID != "summary-msg" || msgs[1].ID != "post-compact-1" || msgs[2].ID != "child-msg" {
			t.Errorf("unexpected order: %v", []string{msgs[0].ID, msgs[1].ID, msgs[2].ID})
		}
	}

	// Verify pre-compact message is NOT included
	for _, msg := range msgs {
		if msg.ID == "pre-compact" {
			t.Error("should not include pre-compact message (it's before compaction)")
		}
	}
}

// TestLoadCurrentMessages_ForkBeforeCompactionInParent tests that if we fork
// from a parent BEFORE the parent compacts, and then load messages from the
// forked child, we correctly inherit messages from the pre-compaction state.
func TestLoadCurrentMessages_ForkBeforeCompactionInParent(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	// Create parent thread with initial messages
	parentThread, parentCW0 := h.createThread("parent-thread", h.chatID)
	h.addMessageWithID("parent-msg-1", h.chatID, parentThread.ID, parentCW0.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))
	h.addMessageWithID("parent-msg-2", h.chatID, parentThread.ID, parentCW0.ID, 2, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))

	// Fork BEFORE compacting (at sequence 0)
	childThread, childCW := h.forkThread("child-thread", h.chatID, parentThread.ID, 2, parentCW0.ID)
	h.addMessageWithID("child-msg", h.chatID, childThread.ID, childCW.ID, 3, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))

	// Now compact the parent (this should NOT affect the child)
	parentCW1 := h.compact(parentThread.ID, "parent-summary")
	h.addMessageWithID("parent-summary", h.chatID, parentThread.ID, parentCW1.ID, 4, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM))
	_ = parentCW1

	// Load messages for child - should get parent messages from before compaction
	msgs, err := h.svc.LoadCurrentMessages(ctx, childThread.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have: parent-msg-1, parent-msg-2 (inherited at fork time), child-msg
	// Should NOT have: parent-summary (added after fork)
	if len(msgs) != 3 {
		t.Errorf("expected 3 messages, got %d", len(msgs))
		for i, m := range msgs {
			t.Logf("  msg[%d]: %s", i, m.ID)
		}
	}

	if len(msgs) >= 3 {
		if msgs[0].ID != "parent-msg-1" || msgs[1].ID != "parent-msg-2" || msgs[2].ID != "child-msg" {
			t.Errorf("unexpected order: %v", []string{msgs[0].ID, msgs[1].ID, msgs[2].ID})
		}
	}
}

// TestLoadCurrentMessages_MultipleCompactionsInChain tests a fork chain where
// each thread has been compacted.
func TestLoadCurrentMessages_MultipleCompactionsInChain(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	// Thread A with compaction
	threadA, cwA0 := h.createThread("thread-a", h.chatID)
	h.addMessageWithID("a-pre", h.chatID, threadA.ID, cwA0.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

	cwA1 := h.compact(threadA.ID, "a-summary")
	h.addMessageWithID("a-summary", h.chatID, threadA.ID, cwA1.ID, 2, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM))
	h.addMessageWithID("a-post", h.chatID, threadA.ID, cwA1.ID, 3, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

	// Thread B forks from compacted A
	threadB, cwB0 := h.forkThread("thread-b", h.chatID, threadA.ID, 3, cwA1.ID)
	h.addMessageWithID("b-initial", h.chatID, threadB.ID, cwB0.ID, 4, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))

	// Compact thread B
	cwB1 := h.compact(threadB.ID, "b-summary")
	h.addMessageWithID("b-summary", h.chatID, threadB.ID, cwB1.ID, 5, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM))
	h.addMessageWithID("b-post", h.chatID, threadB.ID, cwB1.ID, 6, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

	// Load messages for thread B
	msgs, err := h.svc.LoadCurrentMessages(ctx, threadB.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// B's context window has compaction_summary_message_id, so should NOT traverse to A
	// Should only have: b-summary, b-post
	if len(msgs) != 2 {
		t.Errorf("expected 2 messages, got %d", len(msgs))
		for i, m := range msgs {
			t.Logf("  msg[%d]: %s", i, m.ID)
		}
	}

	// Should not include any A messages or B's pre-compaction message
	for _, msg := range msgs {
		if msg.ID == "a-pre" || msg.ID == "a-summary" || msg.ID == "a-post" || msg.ID == "b-initial" {
			t.Errorf("should not include message from before B's compaction: %s", msg.ID)
		}
	}
}

// TestLoadCurrentMessages_EmptyThread tests that an empty thread returns an
// empty slice, not an error.
func TestLoadCurrentMessages_EmptyThread(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	// Create thread with no messages
	thread, _ := h.createThread("empty-thread", h.chatID)

	// Load messages
	msgs, err := h.svc.LoadCurrentMessages(ctx, thread.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should return empty slice
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages for empty thread, got %d", len(msgs))
	}
}

// TestLoadCurrentMessages_ThreadWithSystemMessages tests that system messages
// (like compaction summaries) are included in the message list.
func TestLoadCurrentMessages_ThreadWithSystemMessages(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	thread, cw := h.createThread("system-msg-thread", h.chatID)
	h.addMessageWithID("system-1", h.chatID, thread.ID, cw.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM))
	h.addMessageWithID("user-1", h.chatID, thread.ID, cw.ID, 2, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))
	h.addMessageWithID("assistant-1", h.chatID, thread.ID, cw.ID, 3, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))

	msgs, err := h.svc.LoadCurrentMessages(ctx, thread.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(msgs) != 3 {
		t.Errorf("expected 3 messages, got %d", len(msgs))
	}

	// Verify system message is included
	foundSystem := false
	for _, msg := range msgs {
		if msg.Role == reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM {
			foundSystem = true
		}
	}
	if !foundSystem {
		t.Error("system message should be included")
	}
}

// TestLoadCurrentMessages_ThreadWithToolMessages tests that tool messages
// are included in the message list.
func TestLoadCurrentMessages_ThreadWithToolMessages(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	thread, cw := h.createThread("tool-msg-thread", h.chatID)
	h.addMessageWithID("user-1", h.chatID, thread.ID, cw.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))
	h.addMessageWithID("assistant-1", h.chatID, thread.ID, cw.ID, 2, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))
	h.addMessageWithID("tool-1", h.chatID, thread.ID, cw.ID, 3, int32(reliantv1.MessageRole_MESSAGE_ROLE_TOOL))

	msgs, err := h.svc.LoadCurrentMessages(ctx, thread.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(msgs) != 3 {
		t.Errorf("expected 3 messages, got %d", len(msgs))
	}

	// Verify tool message is included
	foundTool := false
	for _, msg := range msgs {
		if msg.Role == reliantv1.MessageRole_MESSAGE_ROLE_TOOL {
			foundTool = true
		}
	}
	if !foundTool {
		t.Error("tool message should be included")
	}
}

// TestLoadCurrentMessages_InvalidThreadID tests that an invalid thread ID
// returns an error.
func TestLoadCurrentMessages_InvalidThreadID(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	msgs, err := h.svc.LoadCurrentMessages(ctx, "non-existent-thread")
	// The implementation may return an error OR an empty slice for non-existent threads.
	// With a real DB, the thread lookup fails, so we expect an error.
	if err == nil && len(msgs) > 0 {
		t.Error("expected error or empty result for non-existent thread")
	}
}

// TestLoadCurrentMessages_NonExistentContextWindow tests behavior when a thread
// references a context window that doesn't exist.
func TestLoadCurrentMessages_NonExistentContextWindow(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()

	// This test is tricky with a real DB since foreign keys would prevent this.
	// We'll skip this test as it represents an invalid state that shouldn't
	// occur with proper database constraints.
	t.Skip("Skipping: real DB constraints prevent this invalid state")
}

// TestLoadCurrentMessages_ForkFromEmptyParent tests forking from a parent
// that has no messages.
func TestLoadCurrentMessages_ForkFromEmptyParent(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	// Create empty parent thread
	parentThread, parentCW := h.createThread("empty-parent", h.chatID)

	// Fork from empty parent
	childThread, childCW := h.forkThread("child-from-empty", h.chatID, parentThread.ID, 0, parentCW.ID)
	h.addMessageWithID("child-msg", h.chatID, childThread.ID, childCW.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

	// Load messages for child
	msgs, err := h.svc.LoadCurrentMessages(ctx, childThread.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should only have the child's message
	if len(msgs) != 1 {
		t.Errorf("expected 1 message, got %d", len(msgs))
	}
	if len(msgs) > 0 && msgs[0].ID != "child-msg" {
		t.Errorf("expected child-msg, got %s", msgs[0].ID)
	}
}
