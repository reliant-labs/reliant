// Copyright (c) 2025 Reliant Labs
package threads

import (
	"context"
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
)

// A compaction summary replaces the turns before it for LLM context, but those
// turns are still the user's history and the transcript must keep showing them.
//
// Conflating the two truncated the UI badly on a compacted BRANCH: a branch's
// fork link to its parent lives on its first context window, so once the branch
// compacts, the compaction boundary sits between the newest window and the one
// carrying that link. Stopping at the boundary hid not just the summarized
// turns but the entire inherited parent history — one real chat displayed 19
// messages instead of ~1,755.
func TestDisplayCrossesCompactionIntoInheritedHistory(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	// Parent chat with real history.
	parentChat := h.createChat("parent-compact-display")
	parentThread, parentCW := h.createThread("parent-thread-compact", parentChat)
	for i := 0; i < 6; i++ {
		h.addMessageWithID("pm-"+string(rune('a'+i)), parentChat, parentThread.ID, parentCW.ID, int64(i), 1)
	}

	// Branch from the parent's last message.
	branchChat := h.createChat("branch-compact-display")
	branchThread, branchCW := h.forkThread("branch-thread-compact", branchChat, parentThread.ID, 5, parentCW.ID)

	// The branch does some work of its own...
	h.addMessageWithID("bm-1", branchChat, branchThread.ID, branchCW.ID, 0, 1)
	h.addMessageWithID("bm-2", branchChat, branchThread.ID, branchCW.ID, 1, 2)

	svc := NewService(h.repo)

	before, err := svc.LoadDisplayMessages(ctx, branchThread.ID)
	if err != nil {
		t.Fatalf("display before compaction: %v", err)
	}
	if len(before) == 0 {
		t.Fatal("branch inherited nothing before compaction; fixture is not exercising a fork")
	}

	// ...and is then compacted, which is what hid everything.
	summary := h.addMessageWithID("summary-1", branchChat, branchThread.ID, branchCW.ID, 2, 3)
	h.compact(branchThread.ID, summary.ID)

	llm, err := svc.LoadCurrentMessages(ctx, branchThread.ID)
	if err != nil {
		t.Fatalf("LoadCurrentMessages: %v", err)
	}
	display, err := svc.LoadDisplayMessages(ctx, branchThread.ID)
	if err != nil {
		t.Fatalf("LoadDisplayMessages: %v", err)
	}

	// The LLM must STOP at the summary: replaying summarized turns alongside
	// the summary duplicates content and defeats the compaction.
	if len(llm) >= len(before) {
		t.Fatalf("LLM context resolved %d messages after compaction (was %d before) — it must stop at the summary",
			len(llm), len(before))
	}

	// The transcript must NOT stop there.
	if len(display) <= len(llm) {
		t.Fatalf("display resolved %d messages, LLM %d — display must cross the compaction boundary", len(display), len(llm))
	}

	// And specifically, it must still reach the inherited parent messages,
	// which live beyond the boundary.
	seen := make(map[string]bool, len(display))
	for _, m := range display {
		seen[m.ID] = true
	}
	if !seen["pm-a"] {
		t.Error("inherited parent history is missing from the transcript after the branch was compacted — this is the reported bug")
	}
}

// Crossing a compaction boundary must inherit the parent window's messages
// WHOLE. A compaction CW is not a fork -- Compact always sets
// ForkAtMessageID = nil -- so running the fork cut on it resolves forkSeq to
// -1, and since no real seq is negative that drops every message belonging to
// the direct parent window.
//
// Before this was fixed the drop compounded down a chain of compactions: each
// boundary discarded everything the one below it had resolved, so a chat that
// had compacted several times kept only the newest window's messages, and a
// branch taken off that chat inherited just that truncated remainder. In the
// reported chat the transcript bottomed out at the newest compaction summary
// with ~2,200 older messages unreachable, no matter how far the user scrolled.
func TestDisplayCrossingCompactionKeepsParentWindowMessages(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	chat := h.createChat("compact-chain-display")
	thread, cw0 := h.createThread("thread-compact-chain", chat)

	// Ordinal is unique per THREAD, not per context window, so it keeps
	// climbing across compaction boundaries the way the real writer does.
	ordinal := int64(0)
	add := func(id, cwID string, role int32) *db.Message {
		msg := h.addMessageWithID(id, chat, thread.ID, cwID, ordinal, role)
		ordinal++
		return msg
	}

	// Oldest window: the history that must stay reachable.
	for i := 0; i < 4; i++ {
		add("old-"+string(rune('a'+i)), cw0.ID, 1)
	}

	// First compaction. The summary lives in the window being closed.
	summary1 := add("summary-1", cw0.ID, 3)
	cw1 := h.compact(thread.ID, summary1.ID)
	for i := 0; i < 3; i++ {
		add("mid-"+string(rune('a'+i)), cw1.ID, 1)
	}

	// Second compaction, so the test covers a CHAIN rather than one boundary.
	summary2 := add("summary-2", cw1.ID, 3)
	cw2 := h.compact(thread.ID, summary2.ID)
	add("new-a", cw2.ID, 1)

	svc := NewService(h.repo)

	display, err := svc.LoadDisplayMessages(ctx, thread.ID)
	if err != nil {
		t.Fatalf("LoadDisplayMessages: %v", err)
	}
	seen := make(map[string]bool, len(display))
	for _, m := range display {
		seen[m.ID] = true
	}

	// Every message is real history the user wrote or received, across both
	// boundaries. None of it may be dropped by the transcript read.
	for _, id := range []string{
		"old-a", "old-b", "old-c", "old-d", "summary-1",
		"mid-a", "mid-b", "mid-c", "summary-2", "new-a",
	} {
		if !seen[id] {
			t.Errorf("message %q is missing from the transcript: crossing a compaction boundary dropped the parent window's messages", id)
		}
	}

	// The LLM path must still stop at the newest summary -- the fix is scoped
	// to the display read and must not widen what gets replayed as context.
	llm, err := svc.LoadCurrentMessages(ctx, thread.ID)
	if err != nil {
		t.Fatalf("LoadCurrentMessages: %v", err)
	}
	for _, m := range llm {
		if m.ID == "old-a" {
			t.Error("LLM context reached pre-compaction history; it must stop at the summary")
		}
	}
}

// The `total` a display read reports must count what that read can actually
// return. There are two resolutions in this package -- the LLM one stops at a
// compaction summary, the display one crosses it -- so there have to be two
// counts, and a display read that borrows the LLM count reports a number
// wrong by the entire summarized history.
//
// On the reported chat that was CountCurrentMessages = 13 against a 1,539
// message transcript, because every one of the three compactions in its
// ancestry was excluded from the count but present in the transcript.
func TestCountDisplayMessagesMatchesDisplayLoad(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	chat := h.createChat("compact-chain-count")
	thread, cw0 := h.createThread("thread-compact-count", chat)

	ordinal := int64(0)
	add := func(id, cwID string, role int32) *db.Message {
		msg := h.addMessageWithID(id, chat, thread.ID, cwID, ordinal, role)
		ordinal++
		return msg
	}

	for i := 0; i < 4; i++ {
		add("c-old-"+string(rune('a'+i)), cw0.ID, 1)
	}
	summary1 := add("c-summary-1", cw0.ID, 3)
	cw1 := h.compact(thread.ID, summary1.ID)
	for i := 0; i < 3; i++ {
		add("c-mid-"+string(rune('a'+i)), cw1.ID, 1)
	}
	summary2 := add("c-summary-2", cw1.ID, 3)
	cw2 := h.compact(thread.ID, summary2.ID)
	add("c-new-a", cw2.ID, 1)

	svc := NewService(h.repo)

	display, err := svc.LoadDisplayMessages(ctx, thread.ID)
	if err != nil {
		t.Fatalf("LoadDisplayMessages: %v", err)
	}
	displayCount, err := svc.CountDisplayMessages(ctx, thread.ID)
	if err != nil {
		t.Fatalf("CountDisplayMessages: %v", err)
	}
	if displayCount != len(display) {
		t.Errorf("CountDisplayMessages = %d, want %d (len of LoadDisplayMessages) — the transcript's total must count what the transcript shows",
			displayCount, len(display))
	}

	// And the LLM count keeps its own, different contract: it mirrors
	// LoadCurrentMessages, which stops at the summary.
	assertCountMatchesLoad(t, ctx, h, thread.ID)
}

// A branch whose ancestry contains a compaction is the shape that made the
// two counts diverge in production: the fork inherits across the boundary, so
// its transcript is far longer than the LLM context that stops at the summary.
func TestCountDisplayMessagesForkAcrossCompaction(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	parentChat := h.createChat("fork-count-parent")
	parentThread, parentCW := h.createThread("fork-count-parent-thread", parentChat)
	parentOrdinal := int64(0)
	for i := 0; i < 5; i++ {
		h.addMessageWithID("fc-p-"+string(rune('a'+i)), parentChat, parentThread.ID, parentCW.ID, parentOrdinal, 1)
		parentOrdinal++
	}
	summary := h.addMessageWithID("fc-summary", parentChat, parentThread.ID, parentCW.ID, parentOrdinal, 3)
	parentOrdinal++
	compactedCW := h.compact(parentThread.ID, summary.ID)
	h.addMessageWithID("fc-p-post", parentChat, parentThread.ID, compactedCW.ID, parentOrdinal, 1)

	// Branch off the compacted parent, exactly like a "branched chat".
	branchChat := h.createChat("fork-count-branch")
	branchThread, branchCW := h.forkThread(
		"fork-count-branch-thread", branchChat, parentThread.ID, parentOrdinal, compactedCW.ID)
	h.addMessageWithID("fc-b-1", branchChat, branchThread.ID, branchCW.ID, 0, 1)

	svc := NewService(h.repo)

	display, err := svc.LoadDisplayMessages(ctx, branchThread.ID)
	if err != nil {
		t.Fatalf("LoadDisplayMessages: %v", err)
	}
	displayCount, err := svc.CountDisplayMessages(ctx, branchThread.ID)
	if err != nil {
		t.Fatalf("CountDisplayMessages: %v", err)
	}
	if displayCount != len(display) {
		t.Errorf("CountDisplayMessages = %d, want %d (len of LoadDisplayMessages)", displayCount, len(display))
	}
	assertCountMatchesLoad(t, ctx, h, branchThread.ID)
}
