// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// The context_win.sql port (P5b L1b), interleaved with the Repository.
func TestContextWindowQueries(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	if err := store.InsertChat(ctx, env.ORM, convChat("cw-chat", at)); err != nil {
		t.Fatalf("InsertChat: %v", err)
	}
	for _, th := range []*core.Thread{
		{ID: "cw-main", ChatID: "cw-chat", CreatedAt: at, ForkAtMessageID: nil},
		{ID: "cw-fork", ChatID: "cw-chat", ParentThreadID: convStr("cw-main"), Origin: core.ThreadOriginFork, CreatedAt: at},
		{ID: "cw-empty", ChatID: "cw-chat", CreatedAt: at},
	} {
		if _, err := store.InsertThread(ctx, env.ORM, th); err != nil {
			t.Fatalf("InsertThread(%s): %v", th.ID, err)
		}
	}

	if seq, err := store.GetMaxSequenceForThread(ctx, env.ORM, "cw-main"); err != nil || seq != -1 {
		t.Fatalf("GetMaxSequenceForThread(no windows) = %d, %v; want -1", seq, err)
	}
	// Windows written through both layers.
	w0, err := store.InsertContextWindow(ctx, env.ORM, &core.ContextWindow{ID: "cw-0", ThreadID: "cw-main", Sequence: 0, CreatedAt: at})
	if err != nil {
		t.Fatalf("InsertContextWindow: %v", err)
	}
	if _, err := env.Repo.CreateContextWindow(ctx, &core.ContextWindow{ID: "cw-1", ThreadID: "cw-main", Sequence: 1, ParentContextWindowID: convStr("cw-0"), CreatedAt: at.Add(time.Minute)}); err != nil {
		t.Fatalf("Repo.CreateContextWindow: %v", err)
	}
	if _, err := store.InsertContextWindow(ctx, env.ORM, &core.ContextWindow{ID: "cw-f0", ThreadID: "cw-fork", Sequence: 0, ParentContextWindowID: convStr("cw-1"), CreatedAt: at}); err != nil {
		t.Fatalf("InsertContextWindow(fork): %v", err)
	}
	twin, err := env.Repo.GetContextWindow(ctx, "cw-0")
	if err != nil {
		t.Fatalf("Repo.GetContextWindow: %v", err)
	}
	requireSameWindow(t, "InsertContextWindow vs Repo", w0, twin)
	got, err := store.GetContextWindow(ctx, env.ORM, "cw-1")
	if err != nil || got.Sequence != 1 || *got.ParentContextWindowID != "cw-0" {
		t.Fatalf("GetContextWindow(cw-1) = %+v, %v", got, err)
	}

	latest, err := store.GetLatestContextWindow(ctx, env.ORM, "cw-main")
	if err != nil || latest.ID != "cw-1" {
		t.Fatalf("GetLatestContextWindow = %+v, %v; want cw-1", latest, err)
	}
	bySeq, err := store.GetContextWindowBySequence(ctx, env.ORM, "cw-main", 0)
	if err != nil || bySeq.ID != "cw-0" {
		t.Fatalf("GetContextWindowBySequence = %+v, %v; want cw-0", bySeq, err)
	}
	if seq, err := store.GetMaxSequenceForThread(ctx, env.ORM, "cw-main"); err != nil || seq != 1 {
		t.Fatalf("GetMaxSequenceForThread = %d, %v; want 1", seq, err)
	}
	if seq, err := env.Repo.GetMaxSequenceForThread(ctx, "cw-main"); err != nil || seq != 1 {
		t.Fatalf("Repo.GetMaxSequenceForThread = %d, %v; want 1", seq, err)
	}

	misses := map[string]func() error{
		"GetContextWindow":           func() error { _, err := store.GetContextWindow(ctx, env.ORM, "cw-none"); return err },
		"GetLatestContextWindow":     func() error { _, err := store.GetLatestContextWindow(ctx, env.ORM, "cw-empty"); return err },
		"GetContextWindowBySequence": func() error { _, err := store.GetContextWindowBySequence(ctx, env.ORM, "cw-main", 9); return err },
		"GetContextWindowWithThread": func() error {
			_, _, _, _, err := store.GetContextWindowWithThread(ctx, env.ORM, "cw-none")
			return err
		},
		"SetCompactionSummaryMessage": func() error {
			_, err := store.SetCompactionSummaryMessage(ctx, env.ORM, "cw-none", "")
			return err
		},
		"Repo.GetLatestContextWindow": func() error { _, err := env.Repo.GetLatestContextWindow(ctx, "cw-empty"); return err },
	}
	for name, miss := range misses {
		if err := miss(); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("%s(missing) err = %v, want a wrapped sql.ErrNoRows", name, err)
		}
	}

	windows, err := store.ListContextWindowsByThread(ctx, env.ORM, "cw-main")
	if err != nil || len(windows) != 2 || windows[0].ID != "cw-0" || windows[1].ID != "cw-1" {
		t.Fatalf("ListContextWindowsByThread = %+v, %v", windows, err)
	}

	// The fork's window, with its thread's chat, parent and fork point.
	cw, chatID, parent, forkAt, err := store.GetContextWindowWithThread(ctx, env.ORM, "cw-f0")
	if err != nil || cw.ID != "cw-f0" || chatID != "cw-chat" || parent == nil || *parent != "cw-main" || forkAt != nil {
		t.Fatalf("GetContextWindowWithThread = %+v, %q, %v, %v, %v", cw, chatID, parent, forkAt, err)
	}
	tcw, tChat, tParent, tFork, err := env.Repo.GetContextWindowWithThread(ctx, "cw-f0")
	if err != nil || tChat != chatID || *tParent != *parent || tFork != nil {
		t.Fatalf("Repo.GetContextWindowWithThread = %v, %q, %v, %v, %v", tcw, tChat, tParent, tFork, err)
	}
	requireSameWindow(t, "GetContextWindowWithThread", cw, tcw)

	forked, err := store.ListForkedThreadIDs(ctx, env.ORM, []string{"cw-main", "cw-fork", "cw-empty", "cw-none"})
	if err != nil || !reflect.DeepEqual(forked, []string{"cw-fork"}) {
		t.Fatalf("ListForkedThreadIDs = %v, %v; want [cw-fork]", forked, err)
	}
	twinForked, err := env.Repo.ListForkedThreadIDs(ctx, []string{"cw-main", "cw-fork"})
	sort.Strings(twinForked)
	if err != nil || !reflect.DeepEqual(twinForked, forked) {
		t.Fatalf("Repo.ListForkedThreadIDs = %v, %v", twinForked, err)
	}

	// Compaction summary: set, read back by the Repository, then cleared.
	if _, err := env.SQL.Exec(`INSERT INTO messages (id, chat_id, thread_id, context_window_id, role, ordinal, seq, created_at, updated_at)
		VALUES ('msg-sum', 'cw-chat', 'cw-main', 'cw-1', 2, 0, 1, NOW(), NOW())`); err != nil {
		t.Fatalf("insert summary message: %v", err)
	}
	set, err := store.SetCompactionSummaryMessage(ctx, env.ORM, "cw-1", "msg-sum")
	if err != nil || set.CompactionSummaryMessageID == nil || *set.CompactionSummaryMessageID != "msg-sum" || set.Sequence != 1 {
		t.Fatalf("SetCompactionSummaryMessage = %+v, %v; want the stored row", set, err)
	}
	if twin, err := env.Repo.GetContextWindow(ctx, "cw-1"); err != nil || *twin.CompactionSummaryMessageID != "msg-sum" {
		t.Fatalf("Repo.GetContextWindow after summary = %+v, %v", twin, err)
	}
	if cleared, err := store.SetCompactionSummaryMessage(ctx, env.ORM, "cw-1", ""); err != nil || cleared.CompactionSummaryMessageID != nil {
		t.Fatalf("SetCompactionSummaryMessage(\"\") = %+v, %v; want cleared", cleared, err)
	}

}

func requireSameWindow(t *testing.T, label string, got, want *core.ContextWindow) {
	t.Helper()
	g, w := *got, *want
	g.CreatedAt, w.CreatedAt = g.CreatedAt.UTC(), w.CreatedAt.UTC()
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("%s:\n got  %+v\n want %+v", label, g, w)
	}
}
