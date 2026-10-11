// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
)

func createNamed(ctx context.Context, svc *WorktreeService, projectID, name, branch string, force bool) (*connect.Response[reliantv1.CreateWorktreeResponse], error) {
	return svc.CreateWorktree(ctx, connect.NewRequest(&reliantv1.CreateWorktreeRequest{
		ProjectId: projectID, Name: name, Branch: branch, Force: force,
	}))
}

func TestCreateWorktree_NameRule(t *testing.T) {
	repo := db.NewTestRepo(t)
	svc := NewWorktreeService(repo, nil, &blockingWorktreeDaemonRouter{})
	userID := uuid.New().String()
	projectID := seedWorktreeProject(t, repo, userID)
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)

	first, err := createNamed(ctx, svc, projectID, "feat", "feat-1", false)
	require.NoError(t, err)
	awaitWorktreeStatus(t, repo, first.Msg.Worktree.Id, reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE)

	// A live row holds the name: AlreadyExists, not a raw constraint error.
	_, err = createNamed(ctx, svc, projectID, "feat", "feat-2", false)
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "already exists")

	// Archiving releases it; the old branch stays off limits, force or not.
	require.NoError(t, repo.ArchiveWorktree(ctx, first.Msg.Worktree.Id))
	for _, force := range []bool{false, true} {
		_, err = createNamed(ctx, svc, projectID, "feat", "feat-1", force)
		require.Error(t, err, "force=%v", force)
		assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
		assert.Contains(t, err.Error(), "pass a different branch")
	}
	second, err := createNamed(ctx, svc, projectID, "feat", "feat-2", false)
	require.NoError(t, err)
	awaitWorktreeStatus(t, repo, second.Msg.Worktree.Id, reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE)
}

func TestUnarchiveWorktree_RefusesWhenTheNameIsTaken(t *testing.T) {
	repo := db.NewTestRepo(t)
	svc := NewWorktreeService(repo, nil, &blockingWorktreeDaemonRouter{})
	userID := uuid.New().String()
	projectID := seedWorktreeProject(t, repo, userID)
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)

	old, err := createNamed(ctx, svc, projectID, "feat", "feat-1", false)
	require.NoError(t, err)
	awaitWorktreeStatus(t, repo, old.Msg.Worktree.Id, reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE)
	require.NoError(t, repo.ArchiveWorktree(ctx, old.Msg.Worktree.Id))
	taker, err := createNamed(ctx, svc, projectID, "feat", "feat-2", false)
	require.NoError(t, err)
	awaitWorktreeStatus(t, repo, taker.Msg.Worktree.Id, reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE)

	_, err = svc.UnarchiveWorktree(ctx, connect.NewRequest(&reliantv1.UnarchiveWorktreeRequest{WorktreeId: old.Msg.Worktree.Id}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "another worktree in this project now uses that name")
	assert.Contains(t, err.Error(), "archive or rename")

	got, err := repo.GetWorktree(ctx, old.Msg.Worktree.Id)
	require.NoError(t, err)
	assert.NotNil(t, got.DeletedAt, "still archived")

	// The store guard backs the pre-check: a raw unarchive reports the typed error.
	require.Error(t, repo.UnarchiveWorktree(ctx, old.Msg.Worktree.Id))
}
