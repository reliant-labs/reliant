// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
)

// A scroll-back page asks for the errors of exactly the history it loads: from
// its oldest message (whatever order the page is in) up to the main-thread
// message at the cursor, or from the chat's start on the last page; and sends
// each one as the stream would.
func TestPageErrorsBoundsTheReadByThePage(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC)
	page := []*db.Message{
		{ID: "b", Seq: 11, CreatedAt: t0.Add(2 * time.Second)},
		{ID: "a", Seq: 10, CreatedAt: t0.Add(time.Second)},
		{ID: "c", Seq: 12, CreatedAt: t0.Add(3 * time.Second)},
	}

	type call struct {
		chatID, thread string
		from           *time.Time
		beforeSeq      int64
	}
	var got call
	repo := &MockRepository{}
	repo.ListErrorUpdatesInRangeFunc = func(_ context.Context, chatID string, from *time.Time, thread string, beforeSeq int64) ([]db.ChatUpdate, error) {
		got = call{chatID: chatID, thread: thread, from: from, beforeSeq: beforeSeq}
		return []db.ChatUpdate{{
			EntityID: "err-a", SequenceNumber: 42, Data: []byte(`{"id":"err-a"}`), CreatedAt: t0.Add(time.Second),
		}}, nil
	}
	svc := &Service{deps: Deps{Repo: repo}}

	out := svc.pageErrors(context.Background(), "chat-1", "main", 13, page, true)
	if got.chatID != "chat-1" || got.thread != "main" || got.beforeSeq != 13 {
		t.Fatalf("read %+v, want chat-1 / main / before 13", got)
	}
	if got.from == nil || !got.from.Equal(t0.Add(time.Second)) {
		t.Fatalf("from = %v, want the page's oldest message, %v", got.from, t0.Add(time.Second))
	}
	if len(out) != 1 || out[0].EntityId != "err-a" || out[0].SequenceNumber != 42 ||
		out[0].DataJson != `{"id":"err-a"}` || out[0].CreatedAt != t0.Add(time.Second).Format(time.RFC3339Nano) {
		t.Fatalf("errors = %+v", out)
	}

	// The last page (nothing older) reaches back to the chat's start.
	svc.pageErrors(context.Background(), "chat-1", "main", 13, page, false)
	if got.from != nil {
		t.Fatalf("last page: from = %v, want open (the chat's start)", got.from)
	}
}

// A failed read degrades to no errors: the page's messages still render.
func TestPageErrorsDegradesOnAFailedRead(t *testing.T) {
	t.Parallel()
	repo := &MockRepository{}
	repo.ListErrorUpdatesInRangeFunc = func(context.Context, string, *time.Time, string, int64) ([]db.ChatUpdate, error) {
		return nil, errors.New("db down")
	}
	svc := &Service{deps: Deps{Repo: repo}}
	page := []*db.Message{{ID: "a", Seq: 1, CreatedAt: time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC)}}
	if out := svc.pageErrors(context.Background(), "chat-1", "main", 2, page, true); out != nil {
		t.Fatalf("errors = %+v, want none", out)
	}
}
