// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedFilterWorktree(t *testing.T, repo *Repo, projectID, id string, isMain bool, lastActive time.Time) {
	t.Helper()
	require.NoError(t, repo.CreateWorktree(context.Background(), &Worktree{
		ID: id, Name: id, Path: "/ws/" + id, Branch: "b-" + id, BaseBranch: "main",
		ProjectID: projectID, Status: 1, IsMain: isMain,
		CreatedAt: lastActive, UpdatedAt: lastActive, LastActive: lastActive,
	}))
}

func worktreeIDs(ws []*Worktree) []string {
	ids := make([]string, len(ws))
	for i, w := range ws {
		ids[i] = w.ID
	}
	return ids
}

// The active list never carries archived rows, the archived list carries only
// them, and main_only finds the main workspace without paging the rest.
func TestListWorktrees_ActiveArchivedAndMainAreSeparateQueries(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	project, err := repo.GetProject(ctx, "test-project")
	require.NoError(t, err)
	base := time.Now().UTC().Add(-time.Hour)

	seedFilterWorktree(t, repo, project.ID, "main", true, base)
	for i := 0; i < 5; i++ {
		seedFilterWorktree(t, repo, project.ID, fmt.Sprintf("live-%d", i), false, base.Add(time.Duration(i)*time.Minute))
	}
	for i := 0; i < 4; i++ {
		id := fmt.Sprintf("old-%d", i)
		seedFilterWorktree(t, repo, project.ID, id, false, base.Add(time.Duration(i)*time.Minute))
	}
	// Archive in a known order: old-0 first, old-3 last.
	for i := 0; i < 4; i++ {
		require.NoError(t, repo.ArchiveWorktree(ctx, fmt.Sprintf("old-%d", i)))
		time.Sleep(5 * time.Millisecond)
	}
	pid := project.ID

	t.Run("active list holds no archived row", func(t *testing.T) {
		ws, err := repo.ListWorktrees(ctx, WorktreeFilters{ProjectID: &pid, Limit: 100})
		require.NoError(t, err)
		assert.Len(t, ws, 6)
		assert.Equal(t, "main", ws[0].ID, "main workspace is first")
		for _, w := range ws {
			assert.Nil(t, w.DeletedAt, w.ID)
		}
	})

	t.Run("archived list holds only archived rows, newest archive first, and pages", func(t *testing.T) {
		page1, err := repo.ListWorktrees(ctx, WorktreeFilters{ProjectID: &pid, ArchivedOnly: true, Limit: 3})
		require.NoError(t, err)
		assert.Equal(t, []string{"old-3", "old-2", "old-1"}, worktreeIDs(page1))
		page2, err := repo.ListWorktrees(ctx, WorktreeFilters{ProjectID: &pid, ArchivedOnly: true, Limit: 3, Offset: 3})
		require.NoError(t, err)
		assert.Equal(t, []string{"old-0"}, worktreeIDs(page2))
	})

	t.Run("main_only returns just main", func(t *testing.T) {
		ws, err := repo.ListWorktrees(ctx, WorktreeFilters{ProjectID: &pid, MainOnly: true, Limit: 1})
		require.NoError(t, err)
		assert.Equal(t, []string{"main"}, worktreeIDs(ws))
	})

	t.Run("include_archived still returns everything for server-internal callers", func(t *testing.T) {
		ws, err := repo.ListWorktrees(ctx, WorktreeFilters{ProjectID: &pid, IncludeArchived: true, Limit: 100})
		require.NoError(t, err)
		assert.Len(t, ws, 10)
	})
}

func TestListWorktreesByIDs_ReturnsArchivedAndSkipsUnknown(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	project, err := repo.GetProject(ctx, "test-project")
	require.NoError(t, err)
	now := time.Now().UTC()

	seedFilterWorktree(t, repo, project.ID, "a", false, now)
	seedFilterWorktree(t, repo, project.ID, "b", false, now)
	require.NoError(t, repo.ArchiveWorktree(ctx, "b"))

	ws, err := repo.ListWorktreesByIDs(ctx, []string{"a", "b", "nope"})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"a", "b"}, worktreeIDs(ws))

	none, err := repo.ListWorktreesByIDs(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, none)
}
