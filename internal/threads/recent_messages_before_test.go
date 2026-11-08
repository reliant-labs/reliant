package threads

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
)

// legacyRecentMessagesBefore is what LoadRecentMessagesBefore returned for a
// forked or compacted thread before it was bounded: resolve the whole
// transcript (all, LoadDisplayMessages' answer), drop everything at or past the
// cursor, keep the newest `limit`. The bounded walk has to be indistinguishable
// from it, over the transcript's rows: callers pass the resolution through
// transcriptOf, since the walk leaves HIDDEN rows out. The caller resolves the transcript once: it does not change while
// the test reads it, and resolving it again for every (cursor, limit) pair was
// three quarters of the test's run time (13s -> 3.5s).
func legacyRecentMessagesBefore(all []*db.Message, beforeSeq int64, limit int) (page []*db.Message, hasMore bool) {
	if len(all) == 0 {
		return []*db.Message{}, false
	}
	oldest := all[0].Seq
	page = all
	if beforeSeq > 0 {
		page = make([]*db.Message, 0, len(all))
		for _, m := range all {
			if m.Seq < beforeSeq {
				page = append(page, m)
			}
		}
	}
	if len(page) > limit {
		page = page[len(page)-limit:]
	}
	return page, len(page) > 0 && page[0].Seq > oldest
}

// The bounded scroll-back read is held to the full-load answer on a chain
// shaped like the chats that motivated it (root -> branch -> two compactions
// -> branch -> two compactions): every cursor (none, before each message,
// past the end) at every page size, comparing ids, order, hasMore and
// visual-thread normalization.
func TestLoadRecentMessagesBefore_CompactedAndBranched_MatchesFullLoad(t *testing.T) {
	t.Parallel()

	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	leaf, _ := forkCompactionChain(t, h)
	full, err := h.svc.LoadDisplayMessages(ctx, leaf)
	if err != nil {
		t.Fatalf("LoadDisplayMessages: %v", err)
	}
	full = transcriptOf(full)
	if len(full) < 20 {
		t.Fatalf("fixture resolved only %d messages; it is not exercising a deep chain", len(full))
	}

	cursors := []int64{0}
	for _, m := range full {
		cursors = append(cursors, m.Seq)
	}
	cursors = append(cursors, full[len(full)-1].Seq+100)

	// One subtest per cursor, run in parallel: each sweeps every limit against
	// the same read-only chain, and in series the ~600 reads cost seconds.
	var checked atomic.Int64
	t.Run("pages", func(t *testing.T) {
		for _, cursor := range cursors {
			t.Run(fmt.Sprintf("cursor=%d", cursor), func(t *testing.T) {
				t.Parallel()
				checked.Add(checkRecentPagesAt(t, h, leaf, full, cursor))
			})
		}
	})
	t.Logf("%d (cursor, limit) pages over a %d-message, 6-window transcript match the full load", checked.Load(), len(full))
}

// checkRecentPagesAt holds every limit's page before cursor to the full-load
// answer, and returns how many it checked.
func checkRecentPagesAt(t *testing.T, h *testHelper, leaf string, full []*db.Message, cursor int64) int64 {
	t.Helper()
	ctx := context.Background()
	var checked int64
	for limit := 1; limit <= len(full)+2; limit++ {
		want, wantMore := legacyRecentMessagesBefore(full, cursor, limit)
		got, gotMore, err := h.svc.LoadRecentMessagesBefore(ctx, leaf, cursor, limit)
		if err != nil {
			t.Fatalf("LoadRecentMessagesBefore(cursor=%d, limit=%d): %v", cursor, limit, err)
		}
		if gotMore != wantMore {
			t.Fatalf("cursor=%d limit=%d: hasMore=%v, want %v", cursor, limit, gotMore, wantMore)
		}
		if len(got) != len(want) {
			t.Fatalf("cursor=%d limit=%d: got %d messages %v, want %d %v",
				cursor, limit, len(got), messageIDs(got), len(want), messageIDs(want))
		}
		for i := range want {
			g, w := got[i], want[i]
			if g.ID != w.ID || g.Seq != w.Seq || g.ThreadID != w.ThreadID || g.ChatID != w.ChatID || !samePtr(g.WorkflowID, w.WorkflowID) {
				t.Fatalf("cursor=%d limit=%d: position %d is %s seq=%d thread=%s chat=%s, want %s seq=%d thread=%s chat=%s",
					cursor, limit, i, g.ID, g.Seq, g.ThreadID, g.ChatID, w.ID, w.Seq, w.ThreadID, w.ChatID)
			}
		}
		checked++
	}
	return checked
}

// Walking a chat back page by page, the way the client scrolls, tiles the
// transcript exactly once, newest to oldest, ending with hasMore=false at the
// very first message.
func TestLoadRecentMessagesBefore_CompactedAndBranched_PagesTileTheTranscript(t *testing.T) {
	t.Parallel()

	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	leaf, _ := forkCompactionChain(t, h)
	full, err := h.svc.LoadDisplayMessages(ctx, leaf)
	if err != nil {
		t.Fatalf("LoadDisplayMessages: %v", err)
	}
	full = transcriptOf(full)

	const pageSize = 4
	var walked []string
	var cursor int64
	for pages := 0; ; pages++ {
		if pages > len(full) {
			t.Fatal("scroll-back never terminated")
		}
		page, more, err := h.svc.LoadRecentMessagesBefore(ctx, leaf, cursor, pageSize)
		if err != nil {
			t.Fatalf("LoadRecentMessagesBefore(cursor=%d): %v", cursor, err)
		}
		walked = append(messageIDs(page), walked...)
		if !more {
			break
		}
		cursor = page[0].Seq
	}

	want := messageIDs(full)
	if len(walked) != len(want) {
		t.Fatalf("pages cover %d messages, transcript has %d\n got: %v\nwant: %v", len(walked), len(want), walked, want)
	}
	for i := range want {
		if walked[i] != want[i] {
			t.Fatalf("pages diverge at %d: got %s, want %s\n got: %v\nwant: %v", i, walked[i], want[i], walked, want)
		}
	}
}

// The cost half of the contract: a page inside the newest window of a
// compacted, branched thread reads that window alone, never the whole
// transcript.
func TestLoadRecentMessagesBefore_CompactedThreadReadsOnlyTheWindowsItNeeds(t *testing.T) {
	t.Parallel()

	h := newTestHelper(t)
	defer h.Close()

	leaf, newest := forkCompactionChain(t, h)
	repo := &windowReadCounter{Repository: h.repo}
	svc := NewService(repo)

	if _, _, err := svc.LoadRecentMessagesBefore(context.Background(), leaf, 0, newest-1); err != nil {
		t.Fatalf("LoadRecentMessagesBefore: %v", err)
	}
	if repo.boundedReads != 1 || repo.fullReads != 0 {
		t.Fatalf("a page inside the newest window read %d windows bounded and %d whole; want 1 and 0",
			repo.boundedReads, repo.fullReads)
	}
}
