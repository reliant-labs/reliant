// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// initGitDaemonRouter fakes a daemon whose git init always succeeds.
type initGitDaemonRouter struct {
	worktreeTestDaemonRouter
}

func (r *initGitDaemonRouter) SendDaemonCommandToDaemon(ctx context.Context, userID, _ string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return r.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
}

func (r *initGitDaemonRouter) SendDaemonCommand(_ context.Context, _ string, commandType string, _ []byte, _ int32) ([]byte, error) {
	if commandType == "project.init_git_repo" {
		return json.Marshal(map[string]any{"success": true})
	}
	return json.Marshal(map[string]any{})
}

func newInitGitProject(t *testing.T, repo db.Repository, userID, projectID string) {
	t.Helper()
	now := time.Now().UTC()
	require.NoError(t, repo.CreateProject(context.Background(), &db.Project{
		ID:         projectID,
		UserID:     userID,
		Name:       "Test Project",
		Path:       t.TempDir(),
		IsGitRepo:  false,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))
}

// TestInitializeGitRepoDoesNotPolluteNestedRepos pins the multi-repo case: when
// a project already has nested repo rows, initializing git at the ROOT must not
// register an extra root repo. Worktree creation fans out one checkout per repo
// row, so a spurious root row would create a worktree for a repo the user never
// asked for. repo.Discover deliberately drops the root once nested repos exist;
// the registry must agree.
func TestInitializeGitRepoDoesNotPolluteNestedRepos(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	userID := uuid.New().String()
	projectID := uuid.New().String()
	newInitGitProject(t, repo, userID, projectID)

	now := time.Now().UTC()
	for _, name := range []string{"api", "web"} {
		require.NoError(t, repo.CreateRepo(context.Background(), &core.Repo{
			ID:           uuid.New().String(),
			ProjectID:    projectID,
			Name:         name,
			RelativePath: name,
			CreatedAt:    now,
			UpdatedAt:    now,
		}))
	}

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	s := NewProjectService(repo, &initGitDaemonRouter{})

	_, err := s.InitializeGitRepo(ctx, connect.NewRequest(&reliantv1.InitializeGitRepoRequest{
		ProjectId:     projectID,
		InitialBranch: "main",
	}))
	require.NoError(t, err)

	repos, err := repo.ListReposByProject(ctx, projectID)
	require.NoError(t, err)
	require.Len(t, repos, 2, "must not add a root repo row when nested repos already exist")
	for _, r := range repos {
		assert.NotEqual(t, "", r.RelativePath, "no root repo row should be registered")
	}
}

// TestInitializeGitRepoRegistersRootRepoWhenNoneExist keeps the original
// behavior for the plain single-repo case: worktree creation gates on the repo
// registry, so a freshly initialized project needs its root row.
func TestInitializeGitRepoRegistersRootRepoWhenNoneExist(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	userID := uuid.New().String()
	projectID := uuid.New().String()
	newInitGitProject(t, repo, userID, projectID)

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	s := NewProjectService(repo, &initGitDaemonRouter{})

	_, err := s.InitializeGitRepo(ctx, connect.NewRequest(&reliantv1.InitializeGitRepoRequest{
		ProjectId:     projectID,
		InitialBranch: "main",
	}))
	require.NoError(t, err)

	repos, err := repo.ListReposByProject(ctx, projectID)
	require.NoError(t, err)
	require.Len(t, repos, 1, "root repo must be registered (worktrees gate on it)")
	assert.Equal(t, "", repos[0].RelativePath)
}
