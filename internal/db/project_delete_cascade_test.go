// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Deleting a project deletes its chats and worktrees (FK ON DELETE CASCADE),
// so no surface can list a chat whose project is gone.
func TestDeleteProjectLeavesNoOrphanedChatsOrWorktrees(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now()

	for _, id := range []string{"doomed", "survivor"} {
		require.NoError(t, repo.CreateProject(ctx, &Project{
			ID: id, Name: id, Path: "/tmp/" + id, UserID: "owner",
			CreatedAt: now, UpdatedAt: now, LastActive: now,
		}))
		require.NoError(t, repo.CreateChat(ctx, &Chat{
			ID: "chat-" + id, Title: id, ProjectID: id, UserID: "owner",
			State: 2, CreatedAt: now, UpdatedAt: now, LastActive: now,
		}))
		require.NoError(t, repo.CreateWorktree(ctx, &Worktree{
			ID: "wt-" + id, Name: id, Path: "/tmp/wt-" + id, Branch: "b", BaseBranch: "main",
			ProjectID: id, Status: 1, CreatedAt: now, UpdatedAt: now, LastActive: now,
		}))
	}

	require.NoError(t, repo.DeleteProject(ctx, "doomed", "owner"))

	chats, err := repo.ListChats(ctx, ChatFilters{UserID: "owner", Limit: 50})
	require.NoError(t, err)
	require.Len(t, chats, 1)
	assert.Equal(t, "chat-survivor", chats[0].ID)

	archived, err := repo.ListArchivedChats(ctx, "owner")
	require.NoError(t, err)
	assert.Empty(t, archived)

	wts, err := repo.ListWorktrees(ctx, WorktreeFilters{IncludeArchived: true, Limit: 50})
	require.NoError(t, err)
	for _, w := range wts {
		assert.NotEqual(t, "doomed", w.ProjectID)
	}
}

// Inserting a chat for a project that does not exist is now rejected, so the
// orphan state cannot be re-created.
func TestCreateChatRejectsMissingProject(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	now := time.Now()
	err := repo.CreateChat(context.Background(), &Chat{
		ID: "ghost", Title: "ghost", ProjectID: "no-such-project", UserID: "owner",
		State: 2, CreatedAt: now, UpdatedAt: now, LastActive: now,
	})
	require.Error(t, err)
}
