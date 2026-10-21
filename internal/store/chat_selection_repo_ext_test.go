// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/reliant/internal/chatselection"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// The chat_selections port (P5b L1b; hand SQL in internal/db), interleaved
// with the Repository.
func TestChatSelectionQueries(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	for _, id := range []string{"sel-a", "sel-b", "sel-c"} {
		if err := store.InsertChat(ctx, env.ORM, convChat(id, at)); err != nil {
			t.Fatalf("InsertChat(%s): %v", id, err)
		}
	}

	if got, err := store.GetChatSelection(ctx, env.ORM, "sel-a", false); got != nil || err != nil {
		t.Fatalf("GetChatSelection(none) = %v, %v; want (nil, nil)", got, err)
	}

	// The first write, through the store, inside the caller's transaction
	// with the lock and a locking read; read back by the Repository.
	first := &chatselection.Selection{
		UserID: storetest.SeedUserID, WorkflowName: "builder",
		Params: map[string]any{"model": "fast", "nested": map[string]any{"n": float64(2)}},
	}
	var stored *chatselection.Selection
	err := env.ORM.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
		if err := store.LockChatSelection(ctx, env.ORM, "sel-a"); err != nil {
			return err
		}
		cur, err := store.GetChatSelection(ctx, env.ORM, "sel-a", true)
		if err != nil || cur != nil {
			return errors.Join(err, errors.New("want no selection yet"))
		}
		stored, err = store.UpsertChatSelection(ctx, env.ORM, "sel-a", first, at)
		return err
	})
	if err != nil {
		t.Fatalf("first write: %v", err)
	}
	if stored.Version != 1 || stored.ChatID != "sel-a" || !stored.UpdatedAt.Equal(at) {
		t.Fatalf("UpsertChatSelection = %+v, want version 1", stored)
	}
	twin, err := env.Repo.GetChatSelection(ctx, "sel-a")
	if err != nil {
		t.Fatalf("Repo.GetChatSelection: %v", err)
	}
	requireSameSelection(t, "Repo.GetChatSelection after store write", twin, stored)

	// A Repository mutation advances the version the store reads.
	next, err := env.Repo.MutateChatSelection(ctx, "sel-a", func(cur *chatselection.Selection) (*chatselection.Selection, error) {
		edited := *cur
		edited.WorkflowName = "planner"
		edited.Params = map[string]any{}
		return &edited, nil
	})
	if err != nil {
		t.Fatalf("Repo.MutateChatSelection: %v", err)
	}
	got, err := store.GetChatSelection(ctx, env.ORM, "sel-a", false)
	if err != nil {
		t.Fatalf("GetChatSelection: %v", err)
	}
	if got.Version != 2 || got.WorkflowName != "planner" || len(got.Params) != 0 {
		t.Fatalf("GetChatSelection after Repo write = %+v, want version 2, planner, empty params", got)
	}
	requireSameSelection(t, "GetChatSelection vs Repo write", got, next)

	// Copy: onto a chat with none, and never over one that has a selection.
	if err := store.CopyChatSelection(ctx, env.ORM, "sel-a", "sel-b", "copier", at); err != nil {
		t.Fatalf("CopyChatSelection: %v", err)
	}
	copied, err := env.Repo.GetChatSelection(ctx, "sel-b")
	if err != nil || copied == nil || copied.Version != 1 || copied.UserID != "copier" || copied.WorkflowName != "planner" {
		t.Fatalf("Repo.GetChatSelection(copy) = %+v, %v", copied, err)
	}
	if err := env.Repo.CopyChatSelection(ctx, "sel-a", "sel-c", "copier"); err != nil {
		t.Fatalf("Repo.CopyChatSelection: %v", err)
	}
	if _, err := store.UpsertChatSelection(ctx, env.ORM, "sel-b", &chatselection.Selection{UserID: "copier", WorkflowName: "kept"}, at); err != nil {
		t.Fatalf("UpsertChatSelection(sel-b): %v", err)
	}
	if err := store.CopyChatSelection(ctx, env.ORM, "sel-c", "sel-b", "copier", at); err != nil {
		t.Fatalf("CopyChatSelection(onto a selection): %v", err)
	}
	if got, err := store.GetChatSelection(ctx, env.ORM, "sel-b", false); err != nil || got.WorkflowName != "kept" || got.Version != 2 {
		t.Fatalf("copy overwrote sel-b: %+v, %v", got, err)
	}
	if err := store.CopyChatSelection(ctx, env.ORM, "sel-none", "sel-x", "copier", at); err != nil {
		t.Fatalf("CopyChatSelection(no source) = %v, want a no-op", err)
	}
}

func requireSameSelection(t *testing.T, label string, got, want *chatselection.Selection) {
	t.Helper()
	g, w := *got, *want
	// Postgres keeps microseconds; a write returns the caller's instant.
	g.UpdatedAt, w.UpdatedAt = g.UpdatedAt.UTC().Truncate(time.Microsecond), w.UpdatedAt.UTC().Truncate(time.Microsecond)
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("%s:\n got  %+v\n want %+v", label, g, w)
	}
}
