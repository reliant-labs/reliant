// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
)

func newReconcileTestProject(t *testing.T, repo db.Repository, isGit bool) *db.Project {
	t.Helper()
	now := time.Now().UTC()
	project := &db.Project{
		ID:         uuid.New().String(),
		UserID:     "user-" + uuid.New().String(),
		Name:       "reconcile-test",
		Path:       "/home/workspace/projects/reconcile-test",
		IsGitRepo:  isGit,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}
	require.NoError(t, repo.CreateProject(context.Background(), project))
	return project
}

// TestReconcileProjectGitRepo_Bidirectional is the whole point of treating
// is_git_repo as a daemon-observed cache rather than a monotonic/count-derived
// flag: a project that gains a .git flips true, and one that loses it flips
// false. The prior behavior only ever flipped false->true.
func TestReconcileProjectGitRepo_Bidirectional(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	t.Run("false to true when daemon now sees a .git", func(t *testing.T) {
		p := newReconcileTestProject(t, repo, false)
		require.NoError(t, reconcileProjectGitRepo(ctx, repo, p, true))
		got, err := repo.GetProject(ctx, p.ID)
		require.NoError(t, err)
		assert.True(t, got.IsGitRepo, "gaining a .git must flip the cached flag true")
	})

	t.Run("true to false when the .git is gone", func(t *testing.T) {
		p := newReconcileTestProject(t, repo, true)
		require.NoError(t, reconcileProjectGitRepo(ctx, repo, p, false))
		got, err := repo.GetProject(ctx, p.ID)
		require.NoError(t, err)
		assert.False(t, got.IsGitRepo, "losing the .git must flip the cached flag false — this is the regression the old monotonic logic allowed")
	})
}

// TestReconcileProjectGitRepo_NoopWhenUnchanged pins that a matching
// observation writes nothing (no spurious updated_at churn / DB writes).
func TestReconcileProjectGitRepo_NoopWhenUnchanged(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	p := newReconcileTestProject(t, repo, true)
	before, err := repo.GetProject(ctx, p.ID)
	require.NoError(t, err)

	require.NoError(t, reconcileProjectGitRepo(ctx, repo, p, true))

	after, err := repo.GetProject(ctx, p.ID)
	require.NoError(t, err)
	assert.Equal(t, before.UpdatedAt, after.UpdatedAt, "no-op reconcile must not bump updated_at")
}
