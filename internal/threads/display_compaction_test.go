// Copyright (c) 2025 Reliant Labs
package threads

import (
	"context"
	"testing"
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
