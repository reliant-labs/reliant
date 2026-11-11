// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// Reclaim marks, the database half: a mark is recorded only on a quiet live
// row, and a reclaim under it only while the mark stands and nothing has
// happened since it was placed.

func TestMarkWorktreeForReclaim(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	c := landedFixture(t, env, "marked") // its rows quiet for an hour
	id := *c.WorktreeID
	use := time.Now().Add(-time.Minute).UTC().Truncate(time.Microsecond)

	ok, err := store.MarkWorktreeForReclaim(ctx, env.ORM, id, "m-1", time.Now().Add(-2*time.Hour), &use, core.ArchivedBecauseLanded)
	require.NoError(t, err)
	require.False(t, ok, "active within the quiet period")

	ok, err = store.MarkWorktreeForReclaim(ctx, env.ORM, id, "m-1", time.Now().Add(-30*time.Minute), &use, core.ArchivedBecauseLanded)
	require.NoError(t, err)
	require.True(t, ok)
	wt, err := env.Repo.GetWorktree(ctx, id)
	require.NoError(t, err)
	m := wt.CleanupMetadata
	require.True(t, m.Marked())
	require.Equal(t, "m-1", m.ReclaimMarkID)
	require.WithinDuration(t, time.Now(), *m.ReclaimMarkedAt, time.Minute, "the database's clock")
	require.True(t, use.Equal(*m.ReclaimMarkUse))
	require.Nil(t, wt.DeletedAt, "a mark archives nothing")

	ok, err = store.MarkWorktreeForReclaim(ctx, env.ORM, id, "m-2", time.Now().Add(-30*time.Minute), &use, core.ArchivedBecauseLanded)
	require.NoError(t, err)
	require.False(t, ok, "already marked")

	cleared, err := store.ClearWorktreeMark(ctx, env.ORM, id, "m-other")
	require.NoError(t, err)
	require.False(t, cleared, "another mark's cancel leaves it")
	cleared, err = store.ClearWorktreeMark(ctx, env.ORM, id, "m-1")
	require.NoError(t, err)
	require.True(t, cleared)
	wt, err = env.Repo.GetWorktree(ctx, id)
	require.NoError(t, err)
	require.False(t, wt.CleanupMetadata.Marked())
	require.Nil(t, wt.CleanupMetadata.ReclaimMarkedAt)
}

func TestMarkWorktreeForReclaim_NotWhileARunIsGoing(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	c := landedFixture(t, env, "running")
	convWorkflow(t, env, "wf", c.ID, core.WorkflowStateActive, core.StopReasonUnspecified)
	mustExecConv(t, env, `UPDATE workflows SET created_at = NOW() - interval '2 days'`)
	ok, err := store.MarkWorktreeForReclaim(ctx, env.ORM, *c.WorktreeID, "m-1", time.Now(), nil, core.ArchivedBecauseLanded)
	require.NoError(t, err)
	require.False(t, ok)
}

func TestArchiveMarkedWorktree(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	c := landedFixture(t, env, "reclaimed")
	id := *c.WorktreeID
	ok, err := store.ArchiveMarkedWorktree(ctx, env.ORM, id, "m-1")
	require.NoError(t, err)
	require.False(t, ok, "never archived without a mark")

	ok, err = store.MarkWorktreeForReclaim(ctx, env.ORM, id, "m-1", time.Now().Add(-30*time.Minute), nil, core.ArchivedBecauseRecoverableIdle)
	require.NoError(t, err)
	require.True(t, ok)

	ok, err = store.ArchiveMarkedWorktree(ctx, env.ORM, id, "m-2")
	require.NoError(t, err)
	require.False(t, ok, "another mark does not authorize it")

	// A chat did something after the mark.
	mustExecConv(t, env, `UPDATE chats SET last_active = NOW() + interval '1 second' WHERE id = $1`, c.ID)
	ok, err = store.ArchiveMarkedWorktree(ctx, env.ORM, id, "m-1")
	require.NoError(t, err)
	require.False(t, ok, "activity since the mark")

	mustExecConv(t, env, `UPDATE chats SET last_active = NOW() - interval '1 hour' WHERE id = $1`, c.ID)
	ok, err = store.ArchiveMarkedWorktree(ctx, env.ORM, id, "m-1")
	require.NoError(t, err)
	require.True(t, ok)
	wt, err := env.Repo.GetWorktree(ctx, id)
	require.NoError(t, err)
	require.NotNil(t, wt.DeletedAt)
	require.Equal(t, core.ArchivedBecauseRecoverableIdle, wt.CleanupMetadata.ArchivedBecause)
	require.Equal(t, "m-1", wt.CleanupMetadata.ReclaimMarkID, "kept, so the removal is sent under it")
}

func TestApproveAndListMarkedWorktrees(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	c := landedFixture(t, env, "listed")
	id := *c.WorktreeID
	landedFixture(t, env, "unmarked")

	rows, err := store.ListMarkedWorktreesForUser(ctx, env.ORM, storetest.SeedUserID)
	require.NoError(t, err)
	require.Empty(t, rows)

	ok, err := store.ApproveWorktreeMark(ctx, env.ORM, id, "m-1")
	require.NoError(t, err)
	require.False(t, ok, "nothing to approve without a mark")

	ok, err = store.MarkWorktreeForReclaim(ctx, env.ORM, id, "m-1", time.Now().Add(-30*time.Minute), nil, core.ArchivedBecauseLanded)
	require.NoError(t, err)
	require.True(t, ok)

	rows, err = store.ListMarkedWorktreesForUser(ctx, env.ORM, storetest.SeedUserID)
	require.NoError(t, err)
	require.Len(t, rows, 1, "only the marked one")
	require.Equal(t, id, rows[0].Worktree.ID)
	rows, err = store.ListMarkedWorktreesForUser(ctx, env.ORM, "someone-else")
	require.NoError(t, err)
	require.Empty(t, rows, "another user's worktree is never listed")

	ok, err = store.ApproveWorktreeMark(ctx, env.ORM, id, "m-other")
	require.NoError(t, err)
	require.False(t, ok, "an approval of another mark")
	ok, err = store.ApproveWorktreeMark(ctx, env.ORM, id, "m-1")
	require.NoError(t, err)
	require.True(t, ok)
	wt, err := env.Repo.GetWorktree(ctx, id)
	require.NoError(t, err)
	require.NotNil(t, wt.CleanupMetadata.ReclaimApprovedAt)

	cleared, err := store.ClearWorktreeMark(ctx, env.ORM, id, "m-1")
	require.NoError(t, err)
	require.True(t, cleared)
	wt, err = env.Repo.GetWorktree(ctx, id)
	require.NoError(t, err)
	require.Nil(t, wt.CleanupMetadata.ReclaimApprovedAt, "the approval goes with the mark")
}
