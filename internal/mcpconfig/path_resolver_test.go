package mcpconfig

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/stretchr/testify/require"
)

func TestResolveProjectForMCPPath_ProjectPath(t *testing.T) {
	repo := db.NewTestRepo(t)
	t.Cleanup(func() {
		_ = repo.Close()
	})

	project := seedProject(t, repo, "/tmp/project-main")

	resolved, err := ResolveProjectForMCPPath(context.Background(), repo, project.Path)
	require.NoError(t, err)
	require.NotNil(t, resolved)
	require.Equal(t, project.ID, resolved.ID)
}

func TestResolveProjectForMCPPath_WorktreePathResolvesOwningProject(t *testing.T) {
	repo := db.NewTestRepo(t)
	t.Cleanup(func() {
		_ = repo.Close()
	})

	project := seedProject(t, repo, "/tmp/project-owning")
	seedWorktree(t, repo, project.ID, "/tmp/project-owning-worktree", false)

	resolved, err := ResolveProjectForMCPPath(context.Background(), repo, "/tmp/project-owning-worktree")
	require.NoError(t, err)
	require.NotNil(t, resolved)
	require.Equal(t, project.ID, resolved.ID)
}

func TestResolveProjectForMCPPath_NotFound(t *testing.T) {
	repo := db.NewTestRepo(t)
	t.Cleanup(func() {
		_ = repo.Close()
	})

	resolved, err := ResolveProjectForMCPPath(context.Background(), repo, "/tmp/does-not-exist")
	require.Error(t, err)
	require.Nil(t, resolved)
	require.Contains(t, err.Error(), "project or worktree not found")
}

func TestResolveProjectForMCPPath_EmptyPath(t *testing.T) {
	repo := db.NewTestRepo(t)
	t.Cleanup(func() {
		_ = repo.Close()
	})

	resolved, err := ResolveProjectForMCPPath(context.Background(), repo, "   ")
	require.Error(t, err)
	require.Nil(t, resolved)
	require.Contains(t, err.Error(), "project path is required")
}

func seedProject(t *testing.T, repo *db.Repo, path string) *db.Project {
	t.Helper()
	now := time.Now().UTC()
	project := &db.Project{
		ID:         uuid.NewString(),
		Name:       "proj-" + uuid.NewString()[:8],
		Path:       path,
		UserID:     "user-test",
		IsGitRepo:  true,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}
	require.NoError(t, repo.CreateProject(context.Background(), project))
	return project
}

func seedWorktree(t *testing.T, repo *db.Repo, projectID, path string, isMain bool) *db.Worktree {
	t.Helper()
	now := time.Now().UTC()
	worktree := &db.Worktree{
		ID:         uuid.NewString(),
		Name:       "wt-" + uuid.NewString()[:8],
		Path:       path,
		Branch:     "feature/test",
		BaseBranch: "main",
		ProjectID:  projectID,
		Status:     int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE),
		IsMain:     isMain,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}
	require.NoError(t, repo.CreateWorktree(context.Background(), worktree))
	return worktree
}
