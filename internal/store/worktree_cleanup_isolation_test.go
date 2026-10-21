// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// MergeWorktreeCleanupMetadata's guarantee — settle, sweep and clean-up never
// lose each other's updates — comes from its row lock, under READ COMMITTED
// (the hand SQL's BeginTx(nil) it was ported from). The race: another writer
// holds the row locked with its update written; the merge queues behind it;
// the writer commits. At READ COMMITTED the merge's locking read is the first
// statement able to see the write, so it merges into it on its only attempt.
// At SERIALIZABLE the same read fails with 40001 and the merge only lands on a
// retry — the probe sees the serialization failure even though RunTx's retry
// hides it from the caller, and under enough contention the retries run out
// and a cleanup flag is lost.
func TestMergeWorktreeCleanupMetadataLoserReadsTheWinnersWrite(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond)
	if err := store.InsertWorktree(ctx, env.ORM, newWorktree("wt-race", "race", "feat/race", at)); err != nil {
		t.Fatalf("InsertWorktree: %v", err)
	}
	probe := env.Probe(t)

	winner := env.HoldLocks(t,
		`SELECT 1 FROM worktrees WHERE id = 'wt-race' FOR UPDATE`,
		`UPDATE worktrees SET cleanup_metadata = '{"branch_deleted":true}' WHERE id = 'wt-race'`)

	var seen []core.CleanupMetadata
	done := make(chan error, 1)
	go func() {
		done <- store.MergeWorktreeCleanupMetadata(ctx, probe.ORM, "wt-race", func(m *core.CleanupMetadata, _ bool) bool {
			seen = append(seen, *m)
			m.DirectoryDeleted = true
			return true
		})
	}()
	winner.WaitForWaiter()
	winner.Commit()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("MergeWorktreeCleanupMetadata: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("MergeWorktreeCleanupMetadata did not return within 30s of the winner committing")
	}

	if states := probe.SQLStates(); slices.Contains(states, "40001") {
		t.Fatalf("the merge lost the lock race with a serialization failure (SQLSTATE %v) and was retried; its transactions began at %v, want [read committed]", states, probe.Isolations())
	}
	if got := probe.Isolations(); !slices.Equal(got, []string{"read committed"}) {
		t.Fatalf("transactions began at %v, want one at [read committed]", got)
	}
	if len(seen) != 1 || !seen[0].BranchDeleted {
		t.Fatalf("fn saw %+v; want one call, seeing the winner's branch_deleted", seen)
	}
	wt, err := store.GetWorktree(ctx, env.ORM, "wt-race")
	if err != nil {
		t.Fatalf("GetWorktree: %v", err)
	}
	if m := wt.CleanupMetadata; m == nil || !m.BranchDeleted || !m.DirectoryDeleted {
		t.Fatalf("stored cleanup metadata = %+v; want the winner's branch_deleted and the merge's directory_deleted", m)
	}
}
