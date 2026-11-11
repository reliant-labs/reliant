package threads

import (
	"context"
	"fmt"
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
)

// The display walk counts transcript rows, not HIDDEN ones. Hidden rows
// (mailbox envelopes, sub-agent result bodies, run-to-run deliveries,
// compaction summaries) go to the LLM but never reach a display response, so
// a page sized over them came back short by its hidden rows — and a page
// window holding nothing but hidden rows came back EMPTY while has_more said
// there was older history. The web client read the short page as the start of
// the chat, rendered "Start of conversation" in the middle of the transcript
// and stopped paging.

// transcriptOf is a resolution with its HIDDEN rows left out: what the
// transcript renders, and so what the bounded display walk returns.
func transcriptOf(msgs []*db.Message) []*db.Message {
	out := make([]*db.Message, 0, len(msgs))
	for _, m := range msgs {
		if m.DisplayStyle != nil && *m.DisplayStyle == reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN {
			continue
		}
		out = append(out, m)
	}
	return out
}

// addHiddenMessageWithID is addMessageWithID for a DISPLAY_STYLE_HIDDEN row.
func (h *testHelper) addHiddenMessageWithID(id, chatID, threadID, contextWindowID string, ordinal int64, role int32) *db.Message {
	h.t.Helper()
	ctx := context.Background()

	_, seq, err := h.repo.AllocateMessageOrder(ctx, threadID, chatID)
	if err != nil {
		h.t.Fatalf("failed to get next seq: %v", err)
	}
	hidden := reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN
	msg := &db.Message{
		ID:              id,
		ChatID:          chatID,
		ThreadID:        threadID,
		ContextWindowID: contextWindowID,
		Ordinal:         ordinal,
		Seq:             seq,
		Role:            reliantv1.MessageRole(role),
		DisplayStyle:    &hidden,
		CreatedAt:       time.Now(),
	}
	if err := h.repo.CreateMessage(ctx, msg); err != nil {
		h.t.Fatalf("failed to create message: %v", err)
	}
	return msg
}

// hiddenRowsThread builds one thread across a compaction, shaped like an
// orchestrator chat's main thread:
//
//	cw0: p0..p2 (hidden rows before the first visible one), v0..v4,
//	     h0..h9 (a run of hidden rows longer than a page), v5, v6,
//	     a HIDDEN compaction summary                  ── compaction
//	cw1: h10..h12, v7..v9, h13
//
// It returns the thread and every row it wrote, in seq order.
func hiddenRowsThread(t *testing.T, h *testHelper) (threadID string, raw []*db.Message) {
	t.Helper()
	thread, cw0 := h.createThread("hidden-rows", h.chatID)
	var ordinal int64
	add := func(id, cwID string, role int32, hidden bool) *db.Message {
		var msg *db.Message
		if hidden {
			msg = h.addHiddenMessageWithID(id, h.chatID, thread.ID, cwID, ordinal, role)
		} else {
			msg = h.addMessageWithID(id, h.chatID, thread.ID, cwID, ordinal, role)
		}
		ordinal++
		raw = append(raw, msg)
		return msg
	}
	visible := func(from, to int, cwID string) {
		for i := from; i <= to; i++ {
			role := roleUser
			if i%2 == 1 {
				role = roleAssistant
			}
			add(fmt.Sprintf("v%d", i), cwID, role, false)
		}
	}
	hiddenRun := func(from, to int, cwID string) {
		for i := from; i <= to; i++ {
			add(fmt.Sprintf("h%d", i), cwID, roleUser, true)
		}
	}

	for i := 0; i <= 2; i++ {
		add(fmt.Sprintf("p%d", i), cw0.ID, roleUser, true)
	}
	visible(0, 4, cw0.ID)
	hiddenRun(0, 9, cw0.ID)
	visible(5, 6, cw0.ID)
	cw1 := h.compact(thread.ID, add("summary", cw0.ID, roleSummary, true).ID)
	hiddenRun(10, 12, cw1.ID)
	visible(7, 9, cw1.ID)
	hiddenRun(13, 13, cw1.ID)
	return thread.ID, raw
}

func TestLoadRecentDisplayMessages_HiddenRows_IsTailOfTranscript(t *testing.T) {
	t.Parallel()

	h := newTestHelper(t)
	defer h.Close()

	threadID, _ := hiddenRowsThread(t, h)
	// The snapshot's read: for every limit, exactly the newest `limit`
	// visible rows — 200 rendered messages, not 200 minus the hidden ones.
	assertRecentDisplayIsTailOfDisplay(t, h.svc, threadID)
}

func TestLoadRecentMessagesBefore_HiddenRows_PagesCountTranscriptRows(t *testing.T) {
	t.Parallel()

	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	threadID, raw := hiddenRowsThread(t, h)
	full, err := h.svc.LoadDisplayMessages(ctx, threadID)
	if err != nil {
		t.Fatalf("LoadDisplayMessages: %v", err)
	}
	transcript := transcriptOf(full)
	if got := messageIDs(transcript); fmt.Sprint(got) != "[v0 v1 v2 v3 v4 v5 v6 v7 v8 v9]" {
		t.Fatalf("fixture transcript = %v", got)
	}
	seqOf := map[string]int64{}
	for _, m := range raw {
		seqOf[m.ID] = m.Seq
	}

	t.Run("a window of hidden rows below the cursor", func(t *testing.T) {
		// The ten rows below v5 are all hidden. Counted, a page of 5 was
		// h5..h9 — empty once displayed, with hasMore true.
		page, more, err := h.svc.LoadRecentMessagesBefore(ctx, threadID, seqOf["v5"], 5)
		if err != nil {
			t.Fatalf("LoadRecentMessagesBefore: %v", err)
		}
		if got := fmt.Sprint(messageIDs(page)); got != "[v0 v1 v2 v3 v4]" || more {
			t.Fatalf("page below v5 = %s, hasMore=%v; want [v0 v1 v2 v3 v4], false", got, more)
		}
	})

	t.Run("only hidden rows left below the cursor", func(t *testing.T) {
		page, more, err := h.svc.LoadRecentMessagesBefore(ctx, threadID, seqOf["v7"], 2)
		if err != nil {
			t.Fatalf("LoadRecentMessagesBefore: %v", err)
		}
		// Crosses h10..h12 and the hidden summary at the compaction edge.
		if got := fmt.Sprint(messageIDs(page)); got != "[v5 v6]" || !more {
			t.Fatalf("page below v7 = %s, hasMore=%v; want [v5 v6], true", got, more)
		}
		page, more, err = h.svc.LoadRecentMessagesBefore(ctx, threadID, seqOf["v0"], 5)
		if err != nil {
			t.Fatalf("LoadRecentMessagesBefore: %v", err)
		}
		if len(page) != 0 || more {
			t.Fatalf("page below v0 = %v, hasMore=%v; want empty, false", messageIDs(page), more)
		}
	})

	t.Run("every cursor and page size matches the transcript", func(t *testing.T) {
		// Cursors on every row, hidden ones included: a cursor can land
		// anywhere a client's oldest row was.
		cursors := []int64{0}
		for _, m := range raw {
			cursors = append(cursors, m.Seq)
		}
		for _, cursor := range cursors {
			for limit := 1; limit <= len(transcript)+1; limit++ {
				want, wantMore := legacyRecentMessagesBefore(transcript, cursor, limit)
				got, gotMore, err := h.svc.LoadRecentMessagesBefore(ctx, threadID, cursor, limit)
				if err != nil {
					t.Fatalf("LoadRecentMessagesBefore(cursor=%d, limit=%d): %v", cursor, limit, err)
				}
				if fmt.Sprint(messageIDs(got)) != fmt.Sprint(messageIDs(want)) || gotMore != wantMore {
					t.Fatalf("cursor=%d limit=%d: %v hasMore=%v, want %v hasMore=%v",
						cursor, limit, messageIDs(got), gotMore, messageIDs(want), wantMore)
				}
			}
		}
	})

	t.Run("scroll-back tiles the transcript with full pages", func(t *testing.T) {
		const pageSize = 3
		var walked []string
		var cursor int64
		for pages := 0; ; pages++ {
			if pages > len(raw) {
				t.Fatal("scroll-back never terminated")
			}
			page, more, err := h.svc.LoadRecentMessagesBefore(ctx, threadID, cursor, pageSize)
			if err != nil {
				t.Fatalf("LoadRecentMessagesBefore(cursor=%d): %v", cursor, err)
			}
			if more && len(page) != pageSize {
				t.Fatalf("page %d below %d holds %d rows %v with hasMore; every page but the last must be full",
					pages, cursor, len(page), messageIDs(page))
			}
			walked = append(messageIDs(page), walked...)
			if !more {
				break
			}
			cursor = page[0].Seq
		}
		if got, want := fmt.Sprint(walked), fmt.Sprint(messageIDs(transcript)); got != want {
			t.Fatalf("pages cover %s, want %s", got, want)
		}
	})
}
