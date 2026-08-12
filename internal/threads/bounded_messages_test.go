package threads

import (
	"context"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
)

// assertCountMatchesLoad is the core correctness check for
// CountCurrentMessages: it must always equal len(LoadCurrentMessages(...)),
// since it is meant to be the count-only mirror of that resolution.
func assertCountMatchesLoad(t *testing.T, ctx context.Context, h *testHelper, threadID string) {
	t.Helper()
	msgs, err := h.svc.LoadCurrentMessages(ctx, threadID)
	if err != nil {
		t.Fatalf("LoadCurrentMessages: %v", err)
	}
	count, err := h.svc.CountCurrentMessages(ctx, threadID)
	if err != nil {
		t.Fatalf("CountCurrentMessages: %v", err)
	}
	if count != len(msgs) {
		t.Errorf("CountCurrentMessages(%s) = %d, want %d (len of LoadCurrentMessages)", threadID, count, len(msgs))
	}
}

func TestCountCurrentMessages_SimpleThread(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	thread, cw := h.createThread("simple-thread", h.chatID)
	h.addMessageWithID("msg-1", h.chatID, thread.ID, cw.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))
	h.addMessageWithID("msg-2", h.chatID, thread.ID, cw.ID, 2, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))
	h.addMessageWithID("msg-3", h.chatID, thread.ID, cw.ID, 3, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

	assertCountMatchesLoad(t, ctx, h, thread.ID)
}

func TestCountCurrentMessages_ForkedThread(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	parentThread, parentCW := h.createThread("parent-thread", h.chatID)
	h.addMessageWithID("parent-1", h.chatID, parentThread.ID, parentCW.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))
	h.addMessageWithID("parent-2", h.chatID, parentThread.ID, parentCW.ID, 2, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))
	h.addMessageWithID("parent-3", h.chatID, parentThread.ID, parentCW.ID, 3, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

	childThread, childCW := h.forkThread("child-thread", h.chatID, parentThread.ID, 2, parentCW.ID)
	h.addMessageWithID("child-1", h.chatID, childThread.ID, childCW.ID, 4, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))

	assertCountMatchesLoad(t, ctx, h, childThread.ID)
}

func TestCountCurrentMessages_NestedForks(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	threadA, cwA := h.createThread("thread-a", h.chatID)
	h.addMessageWithID("a-1", h.chatID, threadA.ID, cwA.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))
	h.addMessageWithID("a-2", h.chatID, threadA.ID, cwA.ID, 2, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))

	threadB, cwB := h.forkThread("thread-b", h.chatID, threadA.ID, 2, cwA.ID)
	h.addMessageWithID("b-1", h.chatID, threadB.ID, cwB.ID, 3, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))
	h.addMessageWithID("b-2", h.chatID, threadB.ID, cwB.ID, 4, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))

	threadC, cwC := h.forkThread("thread-c", h.chatID, threadB.ID, 4, cwB.ID)
	h.addMessageWithID("c-1", h.chatID, threadC.ID, cwC.ID, 5, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

	assertCountMatchesLoad(t, ctx, h, threadC.ID)
}

func TestCountCurrentMessages_Compaction(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	thread, cw0 := h.createThread("compact-thread", h.chatID)
	h.addMessageWithID("pre-compact-1", h.chatID, thread.ID, cw0.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))
	h.addMessageWithID("pre-compact-2", h.chatID, thread.ID, cw0.ID, 2, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))

	cw1 := h.compact(thread.ID, "summary-msg")
	h.addMessageWithID("summary-msg", h.chatID, thread.ID, cw1.ID, 3, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM))
	h.addMessageWithID("post-compact-1", h.chatID, thread.ID, cw1.ID, 4, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

	assertCountMatchesLoad(t, ctx, h, thread.ID)
}

func TestCountCurrentMessages_ForkAfterCompaction(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	parentThread, parentCW0 := h.createThread("parent-thread", h.chatID)
	h.addMessageWithID("pre-compact", h.chatID, parentThread.ID, parentCW0.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

	parentCW1 := h.compact(parentThread.ID, "summary-msg")
	h.addMessageWithID("summary-msg", h.chatID, parentThread.ID, parentCW1.ID, 2, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM))
	h.addMessageWithID("post-compact-1", h.chatID, parentThread.ID, parentCW1.ID, 3, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

	childThread, childCW := h.forkThread("child-thread", h.chatID, parentThread.ID, 3, parentCW1.ID)
	h.addMessageWithID("child-msg", h.chatID, childThread.ID, childCW.ID, 4, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))

	assertCountMatchesLoad(t, ctx, h, childThread.ID)
}

func TestCountCurrentMessages_MultipleCompactionsInChain(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	threadA, cwA0 := h.createThread("thread-a", h.chatID)
	h.addMessageWithID("a-pre", h.chatID, threadA.ID, cwA0.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

	cwA1 := h.compact(threadA.ID, "a-summary")
	h.addMessageWithID("a-summary", h.chatID, threadA.ID, cwA1.ID, 2, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM))
	h.addMessageWithID("a-post", h.chatID, threadA.ID, cwA1.ID, 3, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

	threadB, cwB0 := h.forkThread("thread-b", h.chatID, threadA.ID, 3, cwA1.ID)
	h.addMessageWithID("b-initial", h.chatID, threadB.ID, cwB0.ID, 4, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))

	cwB1 := h.compact(threadB.ID, "b-summary")
	h.addMessageWithID("b-summary", h.chatID, threadB.ID, cwB1.ID, 5, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM))
	h.addMessageWithID("b-post", h.chatID, threadB.ID, cwB1.ID, 6, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

	assertCountMatchesLoad(t, ctx, h, threadB.ID)
}

func TestCountCurrentMessages_EmptyThread(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	thread, _ := h.createThread("empty-thread", h.chatID)
	assertCountMatchesLoad(t, ctx, h, thread.ID)

	count, err := h.svc.CountCurrentMessages(ctx, thread.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0, got %d", count)
	}
}

func TestCountCurrentMessages_ForkFromEmptyParent(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	parentThread, parentCW := h.createThread("empty-parent", h.chatID)

	childThread, childCW := h.forkThread("child-from-empty", h.chatID, parentThread.ID, 0, parentCW.ID)
	h.addMessageWithID("child-msg", h.chatID, childThread.ID, childCW.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

	assertCountMatchesLoad(t, ctx, h, childThread.ID)
}

// TestCountCurrentMessages_ForkFromEmptyParent_ParentHasAncestor: a fork of
// an empty thread (nil ForkAtMessageID) whose empty parent thread ITSELF has
// ancestors -- exercises the countAncestorMessages path where localCount for
// the direct parent CW must be excluded but the grandparent's contribution
// must still be summed.
func TestCountCurrentMessages_ForkFromEmptyIntermediateThread(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	threadA, cwA := h.createThread("thread-a", h.chatID)
	h.addMessageWithID("a-1", h.chatID, threadA.ID, cwA.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))
	h.addMessageWithID("a-2", h.chatID, threadA.ID, cwA.ID, 2, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))

	// Thread B forks from A but gets no local messages of its own.
	threadB, cwB := h.forkThread("thread-b-empty", h.chatID, threadA.ID, 2, cwA.ID)

	// Thread C forks from B's (empty) context window at ordinal 0 -- there is
	// no message in cwB to reference, so ForkAtMessageID is nil.
	threadC, cwC := h.forkThread("thread-c", h.chatID, threadB.ID, 0, cwB.ID)
	h.addMessageWithID("c-1", h.chatID, threadC.ID, cwC.ID, 3, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

	assertCountMatchesLoad(t, ctx, h, threadB.ID)
	assertCountMatchesLoad(t, ctx, h, threadC.ID)
}

// TestLoadRecentMessagesBefore_FastPath_MatchesSlowPath verifies the SQL-
// bounded fast path (unforked thread) returns byte-identical results to
// slicing the full resolution, across several cursors and limits.
func TestLoadRecentMessagesBefore_FastPath_MatchesSlowPath(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	thread, cw := h.createThread("simple-thread", h.chatID)
	var ids []string
	for i := int64(1); i <= 20; i++ {
		id := "msg-" + string(rune('a'+i))
		h.addMessageWithID(id, h.chatID, thread.ID, cw.ID, i, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))
		ids = append(ids, id)
	}

	all, err := h.svc.LoadCurrentMessages(ctx, thread.ID)
	if err != nil {
		t.Fatalf("LoadCurrentMessages: %v", err)
	}
	if len(all) != 20 {
		t.Fatalf("setup: expected 20 messages, got %d", len(all))
	}

	cases := []struct {
		beforeSeq int64
		limit     int
	}{
		{0, 5},   // newest page
		{0, 100}, // newest page, limit exceeds total
		{all[15].Seq, 5},
		{all[5].Seq, 5},
		{all[0].Seq, 5}, // before the very first message
	}

	for _, tc := range cases {
		got, gotHasMore, err := h.svc.LoadRecentMessagesBefore(ctx, thread.ID, tc.beforeSeq, tc.limit)
		if err != nil {
			t.Fatalf("LoadRecentMessagesBefore(beforeSeq=%d, limit=%d): %v", tc.beforeSeq, tc.limit, err)
		}

		// Slow-path equivalent computed directly from the full resolution.
		var filtered []int
		for i, m := range all {
			if tc.beforeSeq == 0 || m.Seq < tc.beforeSeq {
				filtered = append(filtered, i)
			}
		}
		if len(filtered) > tc.limit {
			filtered = filtered[len(filtered)-tc.limit:]
		}

		if len(got) != len(filtered) {
			t.Fatalf("beforeSeq=%d limit=%d: got %d messages, want %d", tc.beforeSeq, tc.limit, len(got), len(filtered))
		}
		for i, idx := range filtered {
			if got[i].ID != all[idx].ID {
				t.Errorf("beforeSeq=%d limit=%d: message[%d] = %s, want %s", tc.beforeSeq, tc.limit, i, got[i].ID, all[idx].ID)
			}
		}

		wantHasMore := len(filtered) > 0 && all[filtered[0]].Seq > all[0].Seq
		if gotHasMore != wantHasMore {
			t.Errorf("beforeSeq=%d limit=%d: hasMore = %v, want %v", tc.beforeSeq, tc.limit, gotHasMore, wantHasMore)
		}
	}
}

// TestLoadRecentMessagesBefore_ForkedThread_UsesSlowPathAndIsUnaffected
// proves a forked thread still resolves via the fork-safe slow path and
// returns the same result as slicing LoadCurrentMessages directly.
func TestLoadRecentMessagesBefore_ForkedThread_UsesSlowPathAndIsUnaffected(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	parentThread, parentCW := h.createThread("parent-thread", h.chatID)
	h.addMessageWithID("parent-1", h.chatID, parentThread.ID, parentCW.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))
	h.addMessageWithID("parent-2", h.chatID, parentThread.ID, parentCW.ID, 2, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))
	h.addMessageWithID("parent-3", h.chatID, parentThread.ID, parentCW.ID, 3, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

	childThread, childCW := h.forkThread("child-thread", h.chatID, parentThread.ID, 2, parentCW.ID)
	h.addMessageWithID("child-1", h.chatID, childThread.ID, childCW.ID, 4, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))
	h.addMessageWithID("child-2", h.chatID, childThread.ID, childCW.ID, 5, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

	all, err := h.svc.LoadCurrentMessages(ctx, childThread.ID)
	if err != nil {
		t.Fatalf("LoadCurrentMessages: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("setup: expected 4 messages (parent-1, parent-2, child-1, child-2), got %d", len(all))
	}

	got, hasMore, err := h.svc.LoadRecentMessagesBefore(ctx, childThread.ID, 0, 2)
	if err != nil {
		t.Fatalf("LoadRecentMessagesBefore: %v", err)
	}
	if len(got) != 2 || got[0].ID != "child-1" || got[1].ID != "child-2" {
		t.Fatalf("got %v, want [child-1 child-2]", messageIDs(got))
	}
	if !hasMore {
		t.Error("expected hasMore=true: parent-1 and parent-2 precede this page")
	}

	got, hasMore, err = h.svc.LoadRecentMessagesBefore(ctx, childThread.ID, all[2].Seq, 2)
	if err != nil {
		t.Fatalf("LoadRecentMessagesBefore: %v", err)
	}
	if len(got) != 2 || got[0].ID != "parent-1" || got[1].ID != "parent-2" {
		t.Fatalf("got %v, want [parent-1 parent-2]", messageIDs(got))
	}
	if hasMore {
		t.Error("expected hasMore=false: parent-1 is the oldest message in the resolved history")
	}
}

// TestLoadMessagesInSeqRange_FastPath verifies the SQL-bounded range read
// (unforked thread) matches filtering the full resolution in Go.
func TestLoadMessagesInSeqRange_FastPath(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	thread, cw := h.createThread("range-thread", h.chatID)
	for i := int64(1); i <= 10; i++ {
		h.addMessageWithID("msg-"+string(rune('a'+i)), h.chatID, thread.ID, cw.ID, i, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))
	}

	all, err := h.svc.LoadCurrentMessages(ctx, thread.ID)
	if err != nil {
		t.Fatalf("LoadCurrentMessages: %v", err)
	}
	if len(all) != 10 {
		t.Fatalf("setup: expected 10 messages, got %d", len(all))
	}

	from := all[2].Seq
	to := all[7].Seq

	got, err := h.svc.LoadMessagesInSeqRange(ctx, thread.ID, from, &to)
	if err != nil {
		t.Fatalf("LoadMessagesInSeqRange: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("got %d messages, want 5 (indices 2..6)", len(got))
	}
	for i := 0; i < 5; i++ {
		if got[i].ID != all[2+i].ID {
			t.Errorf("message[%d] = %s, want %s", i, got[i].ID, all[2+i].ID)
		}
	}

	// Unbounded above.
	got, err = h.svc.LoadMessagesInSeqRange(ctx, thread.ID, from, nil)
	if err != nil {
		t.Fatalf("LoadMessagesInSeqRange (unbounded): %v", err)
	}
	if len(got) != 8 {
		t.Fatalf("got %d messages, want 8 (indices 2..9)", len(got))
	}
}

// TestLoadMessagesInSeqRange_ForkedThread_UsesSlowPath proves a forked
// thread's range read still goes through the fork-safe LoadCurrentMessages
// resolution and returns the same result as filtering it directly.
func TestLoadMessagesInSeqRange_ForkedThread_UsesSlowPath(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	parentThread, parentCW := h.createThread("parent-thread", h.chatID)
	h.addMessageWithID("parent-1", h.chatID, parentThread.ID, parentCW.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))
	h.addMessageWithID("parent-2", h.chatID, parentThread.ID, parentCW.ID, 2, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))

	childThread, childCW := h.forkThread("child-thread", h.chatID, parentThread.ID, 2, parentCW.ID)
	h.addMessageWithID("child-1", h.chatID, childThread.ID, childCW.ID, 3, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))

	all, err := h.svc.LoadCurrentMessages(ctx, childThread.ID)
	if err != nil {
		t.Fatalf("LoadCurrentMessages: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("setup: expected 3 messages, got %d", len(all))
	}

	got, err := h.svc.LoadMessagesInSeqRange(ctx, childThread.ID, all[0].Seq, nil)
	if err != nil {
		t.Fatalf("LoadMessagesInSeqRange: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d messages, want 3", len(got))
	}
	for i := range all {
		if got[i].ID != all[i].ID {
			t.Errorf("message[%d] = %s, want %s", i, got[i].ID, all[i].ID)
		}
	}
}

func messageIDs(msgs []*db.Message) []string {
	ids := make([]string, len(msgs))
	for i, m := range msgs {
		ids[i] = m.ID
	}
	return ids
}
