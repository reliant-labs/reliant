// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The worktree_owner_backfill migration, re-run against rows it must leave
// alone and a chat it must move. It is idempotent, so applying it again over
// the migrated test database exercises exactly its statements.
//
// An unattributed workspace keeps no owner even when its project is installed
// on exactly one machine: the install row on the machine that made it can be
// removed while the workspace survives there, so the remaining install proves
// nothing about its disk (daemonplacement.Resolver settles it from disk).
func TestWorktreeOwnerBackfill(t *testing.T) {
	sqlBytes, err := os.ReadFile("../../db/migrations/20261030113936_worktree_owner_backfill.up.sql")
	require.NoError(t, err)

	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC()

	user := "u-" + uuid.NewString()
	projectID := uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &Project{ID: projectID, Name: "P", Path: "/p", UserID: user,
		CreatedAt: now, UpdatedAt: now, LastActive: now}))
	for _, id := range []string{"machine-a", "machine-b"} {
		require.NoError(t, repo.UpsertDaemon(ctx, &Daemon{ID: id + user, UserID: user}))
	}
	a, b := "machine-a"+user, "machine-b"+user
	// The project was cloned on A and B; A's install row was removed since.
	require.NoError(t, repo.UpsertProjectDaemon(ctx, projectID, b, "/p", nil))

	worktree := func(owner *string) string {
		id := uuid.NewString()
		require.NoError(t, repo.CreateWorktree(ctx, &Worktree{ID: id, Name: id, Path: "/wt/" + id, Branch: id,
			BaseBranch: "main", ProjectID: projectID, DaemonID: owner, Status: 1,
			CreatedAt: now, UpdatedAt: now, LastActive: now}))
		return id
	}
	chat := func(worktreeID string, pin *string, noMachine bool) string {
		id := uuid.NewString()
		require.NoError(t, repo.CreateChat(ctx, &Chat{ID: id, ProjectID: projectID, UserID: user,
			WorktreeID: &worktreeID, ActiveDaemonID: pin, NoMachine: noMachine,
			CreatedAt: now, UpdatedAt: now, LastActive: now}))
		return id
	}

	survivingOnA := worktree(nil) // made on A, before owners were recorded
	ownedByA := worktree(&a)
	misPinned := chat(ownedByA, &b, false)
	onOwner := chat(ownedByA, &a, false)
	inUnattributed := chat(survivingOnA, &a, false)

	_, err = raw.ExecContext(ctx, string(sqlBytes))
	require.NoError(t, err)

	wt, err := repo.GetWorktree(ctx, survivingOnA)
	require.NoError(t, err)
	assert.Nil(t, wt.DaemonID, "one remaining install is not proof of where a workspace lives")

	pin := func(id string) string {
		c, err := repo.GetChat(ctx, id)
		require.NoError(t, err)
		require.NotNil(t, c.ActiveDaemonID)
		return *c.ActiveDaemonID
	}
	assert.Equal(t, a, pin(misPinned), "a chat pinned off its workspace's machine moves to it")
	assert.Equal(t, a, pin(onOwner))
	assert.Equal(t, a, pin(inUnattributed), "a chat in an unattributed workspace keeps its pin")
}
