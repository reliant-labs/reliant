// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// errorRangeFixture is seedTranscript's chat (main-thread messages m0, m1, m4
// and m7 at t0+0s, +1s, +4s and +7s; n2 on the node thread at +2s) plus
// ERROR updates at fixed times, keyed by name to their sequence numbers:
//
//	early     t0-1s
//	span#1    t0+2s   one error's retry series, straddling m4
//	span#2    t0+3s
//	span#3    t0+5s
//	edge      t0+4s   exactly m4's creation
//	late      t0+6s
//
// and an INFO update at t0+3s, which is never an error.
func errorRangeFixture(t *testing.T) (*msgFixture, map[string]int64) {
	t.Helper()
	f := newMsgFixture(t, "chat-error-range")
	f.seedTranscript(t)
	ctx := context.Background()
	errType := reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_ERROR
	seqs := map[string]int64{}
	for _, u := range []struct {
		name, entity string
		typ          reliantv1.ChatUpdateType
		at           time.Duration
	}{
		{"early", "err-early", errType, -time.Second},
		{"span#1", "err-span", errType, 2 * time.Second},
		{"span#2", "err-span", errType, 3 * time.Second},
		{"info", "info", reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_INFO, 3 * time.Second},
		{"span#3", "err-span", errType, 5 * time.Second},
		{"edge", "err-edge", errType, 4 * time.Second},
		{"late", "err-late", errType, 6 * time.Second},
	} {
		if err := f.env.Repo.CreateChatUpdate(ctx, f.chat, u.typ, u.entity, fmt.Sprintf(`{"id":%q}`, u.entity)); err != nil {
			t.Fatalf("CreateChatUpdate %s: %v", u.name, err)
		}
		var seq int64
		if err := f.env.SQL.QueryRowContext(ctx,
			`UPDATE chat_updates SET created_at = $1
			  WHERE chat_id = $2 AND sequence_number = (SELECT max(sequence_number) FROM chat_updates WHERE chat_id = $2)
			  RETURNING sequence_number`,
			t0.Add(u.at), f.chat).Scan(&seq); err != nil {
			t.Fatalf("pin %s's time: %v", u.name, err)
		}
		seqs[u.name] = seq
	}
	return f, seqs
}

// got is the (entity, sequence) pairs of updates, in order.
func gotErrors(updates []core.ChatUpdate) string {
	out := make([]string, len(updates))
	for i, u := range updates {
		out[i] = fmt.Sprintf("%s@%d", u.EntityID, u.SequenceNumber)
	}
	return fmt.Sprint(out)
}

// ErrorUpdatesInRange is a scroll-back page's errors: [from, the creation of
// the thread's first message at or after beforeSeq), errors only, one per id
// (its newest attempt inside the range), oldest first.
func TestErrorUpdatesInRange(t *testing.T) {
	f, seq := errorRangeFixture(t)
	ctx := context.Background()
	want := func(names ...string) string {
		out := make([]string, len(names))
		for i, n := range names {
			entity := map[string]string{
				"early": "err-early", "span#1": "err-span", "span#2": "err-span", "span#3": "err-span",
				"edge": "err-edge", "late": "err-late",
			}[n]
			out[i] = fmt.Sprintf("%s@%d", entity, seq[n])
		}
		return fmt.Sprint(out)
	}
	at := func(d time.Duration) *time.Time { v := t0.Add(d); return &v }

	for _, c := range []struct {
		name      string
		from      *time.Time
		thread    string
		beforeSeq int64
		want      string
	}{
		// beforeSeq 4 is m4 itself: the range ends at its creation, so the
		// error raised at that same instant ("edge") belongs to the newer side,
		// and span's newest attempt here is #2, not #3 past the edge.
		{"cursor on a message", nil, f.main, 4, want("early", "span#2")},
		// No main-thread message has seq 3: the first at or after it is m4.
		{"cursor between messages", nil, f.main, 3, want("early", "span#2")},
		{"lower bound is inclusive", at(3 * time.Second), f.main, 4, want("span#2")},
		{"lower bound excludes older attempts", at(2*time.Second + time.Millisecond), f.main, 4, want("span#2")},
		{"lower bound past every error", at(3*time.Second + time.Millisecond), f.main, 4, want()},
		{"cursor at m1", nil, f.main, 1, want("early")},
		// The cursor is a main-thread seq: the node thread's n2 bounds its own
		// range, not the main thread's.
		{"another thread's cursor", nil, f.node, 2, want("early")},
		// Nothing at or after the cursor: open above, so each error's newest.
		{"no message at or after the cursor", nil, f.main, 8, want("early", "span#3", "edge", "late")},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := store.ErrorUpdatesInRange(ctx, f.env.ORM, f.chat, c.from, c.thread, c.beforeSeq)
			if err != nil {
				t.Fatalf("ErrorUpdatesInRange: %v", err)
			}
			if gotErrors(got) != c.want {
				t.Fatalf("got %s, want %s", gotErrors(got), c.want)
			}
		})
	}

	// The Repository method is the same query.
	viaRepo, err := f.env.Repo.ListErrorUpdatesInRange(ctx, f.chat, nil, f.main, 4)
	if err != nil {
		t.Fatalf("ListErrorUpdatesInRange: %v", err)
	}
	if gotErrors(viaRepo) != want("early", "span#2") {
		t.Fatalf("Repo.ListErrorUpdatesInRange = %s, want %s", gotErrors(viaRepo), want("early", "span#2"))
	}
}

// The migration's index exists in the migrated schema exactly as the query
// needs it: (chat_id, created_at), only ERROR updates, and valid (a failed
// CREATE INDEX CONCURRENTLY leaves an invalid one behind).
func TestErrorUpdatesInRangeIndex(t *testing.T) {
	f := newMsgFixture(t, "chat-error-range-index")
	var def string
	var valid bool
	if err := f.env.SQL.QueryRowContext(context.Background(), `
		SELECT pg_get_indexdef(i.indexrelid), i.indisvalid
		  FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
		 WHERE c.relname = 'idx_chat_updates_errors_created'`).Scan(&def, &valid); err != nil {
		t.Fatalf("idx_chat_updates_errors_created is not in the migrated schema: %v", err)
	}
	const wantDef = "CREATE INDEX idx_chat_updates_errors_created ON public.chat_updates USING btree (chat_id, created_at) WHERE (update_type = 6)"
	if def != wantDef {
		t.Fatalf("index definition:\n got %s\nwant %s", def, wantDef)
	}
	if !valid {
		t.Fatal("idx_chat_updates_errors_created is invalid")
	}
}
