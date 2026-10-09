// Copyright (c) 2025 Reliant Labs
package workspacecreate

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// seedProject returns the project every DB-backed test database starts with,
// so rows go through the real unique index.
func seedProject(t *testing.T, repo db.Repository) *db.Project {
	t.Helper()
	p, err := repo.GetProject(context.Background(), "test-project")
	require.NoError(t, err)
	return p
}

func TestNameRule_ReuseAfterArchive(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	project := seedProject(t, repo)

	req := Request{Project: project, Name: "feat", Branch: "feat-1", OwnerDaemonID: "d1"}
	first, err := Insert(ctx, repo, req)
	require.NoError(t, err)

	// A live row holds the name.
	_, err = Insert(ctx, repo, Request{Project: project, Name: "feat", Branch: "feat-2", OwnerDaemonID: "d1"})
	var taken *NameTakenError
	require.ErrorAs(t, err, &taken)
	assert.True(t, errors.Is(err, core.ErrWorktreeNameTaken))
	assert.Equal(t, first.ID, taken.Holder.ID)

	// Archiving releases it.
	require.NoError(t, repo.ArchiveWorktree(ctx, first.ID))
	second, err := Insert(ctx, repo, Request{Project: project, Name: "feat", Branch: "feat-2", OwnerDaemonID: "d1"})
	require.NoError(t, err)
	live, err := repo.GetLiveWorktreeByName(ctx, project.ID, "feat")
	require.NoError(t, err)
	assert.Equal(t, second.ID, live.ID)

	// The store backs the rule with the index, so a racing insert is still
	// a typed refusal.
	dup := *second
	dup.ID = "racer"
	assert.ErrorIs(t, repo.CreateWorktree(ctx, &dup), core.ErrWorktreeNameTaken)
}

func TestNameRule_FailedRowIsReplaced(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	project := seedProject(t, repo)

	req := Request{Project: project, Repos: []*core.Repo{{ID: "r", Name: "demo"}}, Name: "feat", Branch: "feat-1", OwnerDaemonID: "d1"}
	failed, err := Insert(ctx, repo, req)
	require.NoError(t, err)
	failed.Status = int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_FAILED)
	require.NoError(t, repo.UpdateWorktree(ctx, failed))

	// The same name AND the same branch: the failed row never held work.
	retry, err := Insert(ctx, repo, req)
	require.NoError(t, err)
	old, err := repo.GetWorktree(ctx, failed.ID)
	require.NoError(t, err)
	assert.NotNil(t, old.DeletedAt)
	live, err := repo.GetLiveWorktreeByName(ctx, project.ID, "feat")
	require.NoError(t, err)
	assert.Equal(t, retry.ID, live.ID)
}

func TestNameRule_ArchivedBranchIsNeverTaken(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	project := seedProject(t, repo)

	first, err := Insert(ctx, repo, Request{Project: project, Name: "feat", Branch: "feat-1", OwnerDaemonID: "d1"})
	require.NoError(t, err)
	require.NoError(t, repo.ArchiveWorktree(ctx, first.ID))

	for _, force := range []bool{false, true} {
		_, err = Insert(ctx, repo, Request{Project: project, Name: "feat", Branch: "feat-1", Force: force, OwnerDaemonID: "d1"})
		var held *BranchHeldError
		require.ErrorAs(t, err, &held, "force=%v", force)
		assert.Contains(t, err.Error(), "pass a different branch")
	}
	live, err := repo.GetLiveWorktreeByName(ctx, project.ID, "feat")
	require.NoError(t, err)
	assert.Nil(t, live, "a refused create leaves no row")

	// Once the sweep has deleted the branch, it is free again.
	require.NoError(t, repo.UpdateWorktreeCleanupMetadata(ctx, first.ID, &db.CleanupMetadata{DirectoryDeleted: true, BranchDeleted: true}))
	_, err = Insert(ctx, repo, Request{Project: project, Name: "feat", Branch: "feat-1", OwnerDaemonID: "d1"})
	require.NoError(t, err)
}
