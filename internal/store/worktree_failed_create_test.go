// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// ListFailedWorktreesForDaemon is what a connecting machine is asked about
// (reliant #2413): its own FAILED rows with no path, archived or not, recent,
// not yet recorded cleaned up. Nothing else — above all no row a live
// workspace stands on.
func TestListFailedWorktreesForDaemon(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	failed := int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_FAILED)

	insert := func(id string, mutate func(w *core.Worktree)) {
		t.Helper()
		w := newWorktree(id, id, "feat/"+id, now.Add(-time.Hour))
		w.Status, w.Path, w.DaemonID = failed, "", strp("d1")
		mutate(w)
		if err := store.InsertWorktree(ctx, env.ORM, w); err != nil {
			t.Fatalf("InsertWorktree %s: %v", id, err)
		}
	}
	insert("want", func(*core.Worktree) {})
	insert("want-archived", func(*core.Worktree) {})
	if err := store.ArchiveWorktree(ctx, env.ORM, "want-archived"); err != nil {
		t.Fatal(err)
	}
	insert("active", func(w *core.Worktree) {
		w.Status, w.Path = int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE), "/wt/active"
	})
	insert("with-path", func(w *core.Worktree) { w.Path = "/wt/with-path" })
	insert("other-machine", func(w *core.Worktree) { w.DaemonID = strp("d2") })
	insert("too-old", func(w *core.Worktree) { w.CreatedAt = now.Add(-60 * 24 * time.Hour) })
	insert("cleaned", func(*core.Worktree) {})
	if err := store.MergeWorktreeCleanupMetadata(ctx, env.ORM, "cleaned", func(m *core.CleanupMetadata, _ bool) bool {
		m.DirectoryDeleted = true
		return true
	}); err != nil {
		t.Fatal(err)
	}

	rows, err := store.ListFailedWorktreesForDaemon(ctx, env.ORM, "d1", now.Add(-30*24*time.Hour), 100)
	if err != nil {
		t.Fatalf("ListFailedWorktreesForDaemon: %v", err)
	}
	got := map[string]bool{}
	for _, w := range rows {
		got[w.ID] = true
	}
	if len(got) != 2 || !got["want"] || !got["want-archived"] {
		t.Fatalf("listed %v; want exactly want and want-archived", got)
	}

	limited, err := store.ListFailedWorktreesForDaemon(ctx, env.ORM, "d1", now.Add(-30*24*time.Hour), 1)
	if err != nil || len(limited) != 1 {
		t.Fatalf("limit 1 listed %d rows (%v)", len(limited), err)
	}
}
