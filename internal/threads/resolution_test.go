package threads

import (
	"context"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// TestResolution_SimpleCW tests resolution from a simple context window with no parent.
// This is the base case - a root thread with no inheritance.
func TestResolution_SimpleCW(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	// Create a root thread with context window
	thread, cw := h.createThread("thread-1", h.chatID)

	// Add messages to this context window
	h.addMessageWithID("msg-1", h.chatID, thread.ID, cw.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))
	h.addMessageWithID("msg-2", h.chatID, thread.ID, cw.ID, 2, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))
	h.addMessageWithID("msg-3", h.chatID, thread.ID, cw.ID, 3, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

	// Resolve messages
	msgs, err := h.svc.ResolveMessagesFromCW(ctx, cw.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should return all messages from this CW only
	if len(msgs) != 3 {
		t.Errorf("expected 3 messages, got %d", len(msgs))
	}
	expectedIDs := []string{"msg-1", "msg-2", "msg-3"}
	for i, expected := range expectedIDs {
		if i >= len(msgs) || msgs[i].ID != expected {
			t.Errorf("msg[%d]: expected %s, got %s", i, expected, msgs[i].ID)
		}
	}
}

// TestResolution_CompactionChain tests that a compaction summary stops traversal.
// CW1 -> CW2 (with compaction summary)
// Resolution should only return messages from CW2, not traverse to CW1.
func TestResolution_CompactionChain(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	// Create thread with initial context window
	thread, cw1 := h.createThread("thread-1", h.chatID)
	h.addMessageWithID("pre-compact-1", h.chatID, thread.ID, cw1.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))
	h.addMessageWithID("pre-compact-2", h.chatID, thread.ID, cw1.ID, 2, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))

	// Compact - creates CW2 with compaction summary
	cw2 := h.compact(thread.ID, "summary-msg")
	h.addMessageWithID("summary-msg", h.chatID, thread.ID, cw2.ID, 3, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM))
	h.addMessageWithID("post-compact-1", h.chatID, thread.ID, cw2.ID, 4, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

	// Resolve from CW2
	msgs, err := h.svc.ResolveMessagesFromCW(ctx, cw2.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should only have messages from CW2 (compaction boundary stops traversal)
	if len(msgs) != 2 {
		t.Errorf("expected 2 messages (summary + post-compact), got %d", len(msgs))
		for i, m := range msgs {
			t.Logf("  msg[%d]: %s", i, m.ID)
		}
	}

	// Should NOT include pre-compaction messages
	for _, msg := range msgs {
		if msg.ID == "pre-compact-1" || msg.ID == "pre-compact-2" {
			t.Errorf("should not include pre-compaction message: %s", msg.ID)
		}
	}

	// Should include summary and post-compact messages
	foundSummary := false
	foundPostCompact := false
	for _, msg := range msgs {
		if msg.ID == "summary-msg" {
			foundSummary = true
		}
		if msg.ID == "post-compact-1" {
			foundPostCompact = true
		}
	}
	if !foundSummary {
		t.Error("missing summary message")
	}
	if !foundPostCompact {
		t.Error("missing post-compact message")
	}
}

// TestResolution_BranchChain tests a fork/branch relationship.
// CWA (parent) -> CWB (child, forked at ordinal 2)
// Resolution from CWB should inherit filtered messages from CWA.
func TestResolution_BranchChain(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	// Parent thread with messages
	threadA, cwA := h.createThread("thread-a", h.chatID)
	h.addMessageWithID("a-1", h.chatID, threadA.ID, cwA.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))
	h.addMessageWithID("a-2", h.chatID, threadA.ID, cwA.ID, 2, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))
	h.addMessageWithID("a-3", h.chatID, threadA.ID, cwA.ID, 3, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER)) // Should NOT be inherited

	// Child thread forked at ordinal 2
	threadB, cwB := h.forkThread("thread-b", h.chatID, threadA.ID, 2, cwA.ID)
	h.addMessageWithID("b-1", h.chatID, threadB.ID, cwB.ID, 4, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))

	// Resolve from CWB
	msgs, err := h.svc.ResolveMessagesFromCW(ctx, cwB.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have: a-1, a-2 (inherited, filtered by ordinal), b-1 (local)
	if len(msgs) != 3 {
		t.Errorf("expected 3 messages, got %d", len(msgs))
		for i, m := range msgs {
			t.Logf("  msg[%d]: %s (ordinal=%d)", i, m.ID, m.Ordinal)
		}
	}

	expectedIDs := []string{"a-1", "a-2", "b-1"}
	for i, expected := range expectedIDs {
		if i >= len(msgs) || msgs[i].ID != expected {
			t.Errorf("msg[%d]: expected %s, got %s", i, expected, msgs[i].ID)
		}
	}

	// Verify a-3 is NOT included (beyond fork point)
	for _, msg := range msgs {
		if msg.ID == "a-3" {
			t.Error("should not include a-3 (beyond fork point)")
		}
	}
}

// TestResolution_NestedBranches tests a multi-level fork chain: A → B → C
// Each level should correctly inherit and filter messages.
func TestResolution_NestedBranches(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	// Thread A (root)
	threadA, cwA := h.createThread("thread-a", h.chatID)
	h.addMessageWithID("a-1", h.chatID, threadA.ID, cwA.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))
	h.addMessageWithID("a-2", h.chatID, threadA.ID, cwA.ID, 2, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))
	h.addMessageWithID("a-3", h.chatID, threadA.ID, cwA.ID, 3, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

	// Thread B forks from A at ordinal 2
	threadB, cwB := h.forkThread("thread-b", h.chatID, threadA.ID, 2, cwA.ID)
	h.addMessageWithID("b-1", h.chatID, threadB.ID, cwB.ID, 4, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))
	h.addMessageWithID("b-2", h.chatID, threadB.ID, cwB.ID, 5, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))
	h.addMessageWithID("b-3", h.chatID, threadB.ID, cwB.ID, 6, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

	// Thread C forks from B at ordinal 5
	threadC, cwC := h.forkThread("thread-c", h.chatID, threadB.ID, 5, cwB.ID)
	h.addMessageWithID("c-1", h.chatID, threadC.ID, cwC.ID, 7, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))

	// Resolve from CWC
	msgs, err := h.svc.ResolveMessagesFromCW(ctx, cwC.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have: a-1, a-2 (from A), b-1, b-2 (from B up to fork point), c-1 (local)
	// Should NOT have: a-3 (filtered by B's fork), b-3 (filtered by C's fork)
	expectedIDs := []string{"a-1", "a-2", "b-1", "b-2", "c-1"}
	if len(msgs) != len(expectedIDs) {
		t.Errorf("expected %d messages, got %d", len(expectedIDs), len(msgs))
		for i, m := range msgs {
			t.Logf("  msg[%d]: %s (ordinal=%d)", i, m.ID, m.Ordinal)
		}
	}

	for i, expected := range expectedIDs {
		if i >= len(msgs) || msgs[i].ID != expected {
			t.Errorf("msg[%d]: expected %s, got %s", i, expected, msgs[i].ID)
		}
	}

	// Verify filtered messages are not included
	for _, msg := range msgs {
		if msg.ID == "a-3" {
			t.Error("should not include a-3 (filtered by B's fork)")
		}
		if msg.ID == "b-3" {
			t.Error("should not include b-3 (filtered by C's fork)")
		}
	}
}

// TestResolution_BranchAfterCompaction tests branching from a compacted context window.
// CWA (seq=0) -> CWA' (seq=1, compacted) -> CWB (forked from CWA')
// CWB should inherit the compaction summary from CWA' but NOT traverse to CWA.
func TestResolution_BranchAfterCompaction(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	// Thread A with initial messages
	threadA, cwA0 := h.createThread("thread-a", h.chatID)
	h.addMessageWithID("a-pre", h.chatID, threadA.ID, cwA0.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

	// Compact A
	cwA1 := h.compact(threadA.ID, "a-summary")
	h.addMessageWithID("a-summary", h.chatID, threadA.ID, cwA1.ID, 2, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM))
	h.addMessageWithID("a-post", h.chatID, threadA.ID, cwA1.ID, 3, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

	// Thread B forks from compacted A at ordinal 3
	threadB, cwB := h.forkThread("thread-b", h.chatID, threadA.ID, 3, cwA1.ID)
	h.addMessageWithID("b-1", h.chatID, threadB.ID, cwB.ID, 4, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))

	// Resolve from CWB
	msgs, err := h.svc.ResolveMessagesFromCW(ctx, cwB.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// B's CW links to A's compacted CW (cwA1)
	// When resolving B, it should traverse to cwA1 and stop at compaction boundary
	// Result: a-summary (from parent, at compaction boundary), a-post, b-1

	expectedIDs := []string{"a-summary", "a-post", "b-1"}
	if len(msgs) != len(expectedIDs) {
		t.Errorf("expected %d messages, got %d", len(expectedIDs), len(msgs))
		for i, m := range msgs {
			t.Logf("  msg[%d]: %s", i, m.ID)
		}
	}

	for i, expected := range expectedIDs {
		if i >= len(msgs) || msgs[i].ID != expected {
			t.Errorf("msg[%d]: expected %s, got %s", i, expected, msgs[i].ID)
		}
	}

	// Should NOT include a-pre (before compaction)
	for _, msg := range msgs {
		if msg.ID == "a-pre" {
			t.Error("should not include a-pre (before compaction)")
		}
	}
}

// TestResolution_BranchThenCompact tests branching first, then compacting.
// CWA (parent) -> CWB (forked) -> CWB' (compacted)
// Resolution from CWB' should stop at compaction, not traverse to CWA.
func TestResolution_BranchThenCompact(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	// Thread A
	threadA, cwA := h.createThread("thread-a", h.chatID)
	h.addMessageWithID("a-1", h.chatID, threadA.ID, cwA.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))
	h.addMessageWithID("a-2", h.chatID, threadA.ID, cwA.ID, 2, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))

	// Thread B forks from A at ordinal 2
	threadB, cwB0 := h.forkThread("thread-b", h.chatID, threadA.ID, 2, cwA.ID)
	h.addMessageWithID("b-1", h.chatID, threadB.ID, cwB0.ID, 3, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))
	h.addMessageWithID("b-2", h.chatID, threadB.ID, cwB0.ID, 4, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))

	// Compact thread B
	cwB1 := h.compact(threadB.ID, "b-summary")
	h.addMessageWithID("b-summary", h.chatID, threadB.ID, cwB1.ID, 5, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM))
	h.addMessageWithID("b-3", h.chatID, threadB.ID, cwB1.ID, 6, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

	// Resolve from CWB1
	msgs, err := h.svc.ResolveMessagesFromCW(ctx, cwB1.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should only have messages from CWB1 (compaction boundary)
	// Should NOT traverse to CWB0 or CWA
	expectedIDs := []string{"b-summary", "b-3"}
	if len(msgs) != len(expectedIDs) {
		t.Errorf("expected %d messages, got %d", len(expectedIDs), len(msgs))
		for i, m := range msgs {
			t.Logf("  msg[%d]: %s", i, m.ID)
		}
	}

	for i, expected := range expectedIDs {
		if i >= len(msgs) || msgs[i].ID != expected {
			t.Errorf("msg[%d]: expected %s, got %s", i, expected, msgs[i].ID)
		}
	}

	// Verify no traversal beyond compaction
	for _, msg := range msgs {
		if msg.ID == "a-1" || msg.ID == "a-2" || msg.ID == "b-1" || msg.ID == "b-2" {
			t.Errorf("should not include message before compaction: %s", msg.ID)
		}
	}
}

// TestResolution_MultipleCompactionsInChain tests a chain with multiple compactions.
// CWA -> CWA' (compact) -> CWB (fork) -> CWB' (compact)
// Resolution from CWB' should only return messages from CWB' (stops at first compaction).
func TestResolution_MultipleCompactionsInChain(t *testing.T) {
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

	// Resolve from CWB1
	msgs, err := h.svc.ResolveMessagesFromCW(ctx, cwB1.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should only have messages from CWB1 (B's compaction boundary)
	expectedIDs := []string{"b-summary", "b-post"}
	if len(msgs) != len(expectedIDs) {
		t.Errorf("expected %d messages, got %d", len(expectedIDs), len(msgs))
		for i, m := range msgs {
			t.Logf("  msg[%d]: %s", i, m.ID)
		}
	}

	for i, expected := range expectedIDs {
		if i >= len(msgs) || msgs[i].ID != expected {
			t.Errorf("msg[%d]: expected %s, got %s", i, expected, msgs[i].ID)
		}
	}

	// Should NOT include any messages from before B's compaction
	for _, msg := range msgs {
		if msg.ID == "a-pre" || msg.ID == "a-summary" || msg.ID == "a-post" || msg.ID == "b-initial" {
			t.Errorf("should not include message before B's compaction: %s", msg.ID)
		}
	}
}

// TestResolution_EmptyContextWindow tests resolving from an empty context window.
func TestResolution_EmptyContextWindow(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	// Create thread with empty CW
	_, cw := h.createThread("empty-thread", h.chatID)

	// Resolve from empty CW
	msgs, err := h.svc.ResolveMessagesFromCW(ctx, cw.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should return empty slice
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages for empty CW, got %d", len(msgs))
	}
}

// TestResolution_EmptyForkFromNonEmpty tests resolving from an empty forked CW
// that has a parent with messages.
func TestResolution_EmptyForkFromNonEmpty(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	// Thread A with messages
	threadA, cwA := h.createThread("thread-a", h.chatID)
	h.addMessageWithID("a-1", h.chatID, threadA.ID, cwA.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))
	h.addMessageWithID("a-2", h.chatID, threadA.ID, cwA.ID, 2, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))

	// Thread B forks from A (but has no local messages)
	_, cwB := h.forkThread("thread-b", h.chatID, threadA.ID, 2, cwA.ID)

	// Resolve from CWB
	msgs, err := h.svc.ResolveMessagesFromCW(ctx, cwB.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should inherit parent messages (a-1, a-2)
	expectedIDs := []string{"a-1", "a-2"}
	if len(msgs) != len(expectedIDs) {
		t.Errorf("expected %d messages, got %d", len(expectedIDs), len(msgs))
	}

	for i, expected := range expectedIDs {
		if i >= len(msgs) || msgs[i].ID != expected {
			t.Errorf("msg[%d]: expected %s, got %s", i, expected, msgs[i].ID)
		}
	}
}

// TestResolution_MaxOrdinalFilter tests that MaxOrdinal correctly filters messages.
func TestResolution_MaxOrdinalFilter(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	thread, cw := h.createThread("ordinal-thread", h.chatID)
	h.addMessageWithID("msg-1", h.chatID, thread.ID, cw.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))
	h.addMessageWithID("msg-2", h.chatID, thread.ID, cw.ID, 2, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))
	h.addMessageWithID("msg-3", h.chatID, thread.ID, cw.ID, 3, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))
	h.addMessageWithID("msg-4", h.chatID, thread.ID, cw.ID, 4, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))

	// Resolve with MaxOrdinal=2
	maxOrdinal := int64(2)
	msgs, err := h.svc.ResolveMessages(ctx, ResolveMessagesOpts{
		ThreadID:        thread.ID,
		ContextWindowID: &cw.ID,
		MaxOrdinal:      &maxOrdinal,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should only have messages with ordinal <= 2
	if len(msgs) != 2 {
		t.Errorf("expected 2 messages, got %d", len(msgs))
	}

	for _, msg := range msgs {
		if msg.Ordinal > 2 {
			t.Errorf("should not include message with ordinal > 2: %s (ordinal=%d)", msg.ID, msg.Ordinal)
		}
	}
}
