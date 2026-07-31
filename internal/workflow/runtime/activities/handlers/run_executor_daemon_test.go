// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// TestResolveRunExecutorContext_PinsWorktreeDaemon verifies that a chat bound to
// a worktree whose owning daemon is recorded resolves a DaemonSelector pinned to
// that daemon — this is what routes a branch chat's tool execution to the
// machine that actually holds its checkout on disk.
func TestResolveRunExecutorContext_PinsWorktreeDaemon(t *testing.T) {
	repo := setupTestRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()

	userID := "test-user"
	projectID := uuid.New().String()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, Name: "P", Path: "/tmp/project-main", UserID: userID,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))

	daemonID := "daemon-owning-worktree"
	worktreeID := uuid.New().String()
	require.NoError(t, repo.CreateWorktree(ctx, &core.Worktree{
		ID: worktreeID, Name: "feature", Path: "/home/u/.reliant/worktrees/feature",
		Branch: "feature", BaseBranch: "main", ProjectID: projectID,
		DaemonID: &daemonID, Status: 1, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))

	chatID := uuid.New().String()
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, ProjectID: projectID, UserID: userID, WorktreeID: &worktreeID,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))

	execCtx, err := resolveRunExecutorContext(ctx, repo, chatID)
	require.NoError(t, err)

	assert.Equal(t, worktreeID, execCtx.WorktreeID)
	assert.Equal(t, "/home/u/.reliant/worktrees/feature", execCtx.WorktreePath)
	require.NotNil(t, execCtx.DaemonSelector, "worktree-bound chat must pin a daemon selector")
	assert.Equal(t, daemonID, execCtx.DaemonSelector.ID)
}

// TestResolveRunExecutorContext_NoWorktreeNoDaemonSelector verifies a plain chat
// (no worktree) resolves no selector, leaving default daemon resolution intact.
func TestResolveRunExecutorContext_NoWorktreeNoDaemonSelector(t *testing.T) {
	repo := setupTestRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()

	userID := "test-user"
	projectID := uuid.New().String()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, Name: "P", Path: "/tmp/project-main", UserID: userID,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))

	chatID := uuid.New().String()
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, ProjectID: projectID, UserID: userID,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))

	execCtx, err := resolveRunExecutorContext(ctx, repo, chatID)
	require.NoError(t, err)

	assert.Empty(t, execCtx.WorktreeID)
	assert.Nil(t, execCtx.DaemonSelector, "plain chat must not pin a daemon selector")
	assert.Equal(t, "/tmp/project-main", execCtx.ProjectPath)
}

// TestResolveRunExecutorContext_WorktreeWithoutDaemon verifies a legacy worktree
// row with no recorded daemon leaves the selector nil (falls back to default
// resolution) rather than pinning an empty daemon id.
func TestResolveRunExecutorContext_WorktreeWithoutDaemon(t *testing.T) {
	repo := setupTestRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()

	userID := "test-user"
	projectID := uuid.New().String()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, Name: "P", Path: "/tmp/project-main", UserID: userID,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))

	worktreeID := uuid.New().String()
	require.NoError(t, repo.CreateWorktree(ctx, &core.Worktree{
		ID: worktreeID, Name: "legacy", Path: "/home/u/.reliant/worktrees/legacy",
		Branch: "legacy", BaseBranch: "main", ProjectID: projectID,
		DaemonID: nil, Status: 1, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))

	chatID := uuid.New().String()
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, ProjectID: projectID, UserID: userID, WorktreeID: &worktreeID,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))

	execCtx, err := resolveRunExecutorContext(ctx, repo, chatID)
	require.NoError(t, err)

	assert.Equal(t, worktreeID, execCtx.WorktreeID)
	assert.Nil(t, execCtx.DaemonSelector, "worktree without recorded daemon must not pin a selector")
}
