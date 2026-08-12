// Copyright (c) 2025 Reliant Labs
package services

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
)

// msgsWithSeqs builds a seq-ascending message slice, the shape paginateBySeq
// requires.
func msgsWithSeqs(seqs ...int64) []*db.Message {
	out := make([]*db.Message, len(seqs))
	for i, s := range seqs {
		out[i] = &db.Message{ID: string(rune('a' + i)), Seq: s}
	}
	return out
}

func seqsOf(msgs []*db.Message) []int64 {
	out := make([]int64, len(msgs))
	for i, m := range msgs {
		out[i] = m.Seq
	}
	return out
}

func equalSeqs(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Scrolling back through a conversation has to terminate, and it has to
// terminate at the beginning rather than before it. The regression this guards
// against reported hasMore=true for every request that carried a cursor, so the
// client kept asking for older pages forever and never rendered a real start of
// history — which presented as a chat that would not load its earlier messages.
func TestPaginateBySeq_ScrollbackReachesTheBeginningAndStops(t *testing.T) {
	// A ten-message chat, seq 0..9, walked backwards three at a time.
	all := msgsWithSeqs(0, 1, 2, 3, 4, 5, 6, 7, 8, 9)

	page, meta := paginateBySeq(all, 0, 3, "")
	if got := seqsOf(page); !equalSeqs(got, []int64{7, 8, 9}) {
		t.Fatalf("first page = %v, want the newest three [7 8 9]", got)
	}
	if !meta.HasMore {
		t.Fatal("first page must report more history: seq 0-6 are older")
	}
	if meta.OldestSeq != 7 {
		t.Fatalf("cursor = %d, want 7", meta.OldestSeq)
	}

	page, meta = paginateBySeq(all, meta.OldestSeq, 3, "")
	if got := seqsOf(page); !equalSeqs(got, []int64{4, 5, 6}) {
		t.Fatalf("second page = %v, want [4 5 6]", got)
	}
	if !meta.HasMore {
		t.Fatal("second page must report more history: seq 0-3 are older")
	}

	page, meta = paginateBySeq(all, meta.OldestSeq, 3, "")
	if got := seqsOf(page); !equalSeqs(got, []int64{1, 2, 3}) {
		t.Fatalf("third page = %v, want [1 2 3]", got)
	}
	if !meta.HasMore {
		t.Fatal("third page must report more history: seq 0 is older")
	}

	// The final page contains the chat's oldest message, so scrollback ends.
	page, meta = paginateBySeq(all, meta.OldestSeq, 3, "")
	if got := seqsOf(page); !equalSeqs(got, []int64{0}) {
		t.Fatalf("final page = %v, want [0]", got)
	}
	if meta.HasMore {
		t.Fatal("final page holds the chat's oldest message; hasMore must be false or the client scrolls forever")
	}
}

// A page that already covers the whole chat has nothing before it, even though
// no cursor was supplied.
func TestPaginateBySeq_WholeChatFitsInOnePage(t *testing.T) {
	all := msgsWithSeqs(0, 1, 2)

	page, meta := paginateBySeq(all, 0, 10, "")
	if len(page) != 3 {
		t.Fatalf("page length = %d, want 3", len(page))
	}
	if meta.HasMore {
		t.Fatal("the page contains every message; hasMore must be false")
	}
	if meta.OldestSeq != 0 {
		t.Fatalf("cursor = %d, want 0", meta.OldestSeq)
	}
}

// Asking for messages before the oldest one is a legitimate request the client
// makes when it is already at the top. It must come back empty and terminal
// rather than reporting more history.
//
// Note seq 0 is reserved as "cursor unset" on the wire, so this uses a chat
// whose numbering starts at 1 to exercise a real before-the-beginning cursor.
func TestPaginateBySeq_CursorAtBeginningReturnsEmptyAndTerminal(t *testing.T) {
	all := msgsWithSeqs(1, 2, 3)

	page, meta := paginateBySeq(all, all[0].Seq, 10, "")
	if len(page) != 0 {
		t.Fatalf("page length = %d, want 0 — nothing precedes the oldest message", len(page))
	}
	if meta.HasMore {
		t.Fatal("nothing precedes the oldest message; hasMore must be false")
	}
}

// A chat whose seq numbering does not start at zero (compaction, branching, or
// any history that begins mid-stream) must still terminate at ITS oldest
// message, not at an assumed seq 0.
func TestPaginateBySeq_NonZeroStartingSeqStillTerminates(t *testing.T) {
	all := msgsWithSeqs(100, 101, 102, 103)

	page, meta := paginateBySeq(all, 0, 2, "")
	if got := seqsOf(page); !equalSeqs(got, []int64{102, 103}) {
		t.Fatalf("first page = %v, want [102 103]", got)
	}
	if !meta.HasMore {
		t.Fatal("seq 100-101 are older; hasMore must be true")
	}

	page, meta = paginateBySeq(all, meta.OldestSeq, 2, "")
	if got := seqsOf(page); !equalSeqs(got, []int64{100, 101}) {
		t.Fatalf("second page = %v, want [100 101]", got)
	}
	if meta.HasMore {
		t.Fatal("seq 100 is the chat's oldest; hasMore must be false")
	}
}

func TestPaginateBySeq_EmptyChat(t *testing.T) {
	page, meta := paginateBySeq(nil, 0, 10, "")
	if len(page) != 0 || meta.HasMore || meta.OldestSeq != 0 {
		t.Fatalf("empty chat = (%d msgs, hasMore=%v, oldest=%d), want (0, false, 0)",
			len(page), meta.HasMore, meta.OldestSeq)
	}
}

// A spawn-heavy chat must advance the TRANSCRIPT by a full page per scroll, not
// just the raw row count. Spawn messages render collapsed inside their tool
// call, so a page counted across all threads can be almost entirely invisible:
// in the real chat this reproduces, one cursor had 5,675 rows below it but only
// 781 on the main thread, so a 200-row page moved the visible conversation by
// about 27 messages and scroll-back appeared frozen.
func TestPaginateBySeq_PageIsCountedInMainThreadMessages(t *testing.T) {
	const mainThread = "main"
	var all []*db.Message
	seq := int64(0)
	// 1 main-thread message for every 9 spawn messages.
	for i := 0; i < 100; i++ {
		all = append(all, &db.Message{ID: "main", Seq: seq, ThreadID: mainThread})
		seq++
		for j := 0; j < 9; j++ {
			all = append(all, &db.Message{ID: "spawn", Seq: seq, ThreadID: "spawn-1"})
			seq++
		}
	}

	page, _ := paginateBySeq(all, 0, 20, mainThread)
	mainInPage := 0
	for _, m := range page {
		if m.ThreadID == mainThread {
			mainInPage++
		}
	}
	if mainInPage != 20 {
		t.Fatalf("page carried %d main-thread messages, want 20 — the transcript is what scrolls, so the page size must be measured in it", mainInPage)
	}
	if len(page) <= mainInPage {
		t.Fatal("sibling-thread messages were dropped; spawn tool-call previews render from them")
	}
}
