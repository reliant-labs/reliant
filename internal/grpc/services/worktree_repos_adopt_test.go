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

	"github.com/reliant-labs/reliant/internal/db"
)

// adoptTestDaemonRouter serves canned repo.discover results so tests can
// simulate a filesystem that has repos the registry doesn't know about.
type adoptTestDaemonRouter struct {
	worktreeTestDaemonRouter
	discovered    []map[string]any
	discoverCalls int
}

func (r *adoptTestDaemonRouter) SendDaemonCommandToDaemon(ctx context.Context, userID, _ string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return r.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
}

func (r *adoptTestDaemonRouter) SendDaemonCommand(ctx context.Context, userID, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	if commandType == "repo.discover" {
		r.discoverCalls++
		return json.Marshal(map[string]any{"discovered": r.discovered})
	}
	return r.worktreeTestDaemonRouter.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
}

func newAdoptTestProject(t *testing.T, repo db.Repository) *db.Project {
	t.Helper()
	now := time.Now().UTC()
	project := &db.Project{
		ID:         uuid.New().String(),
		UserID:     "user-" + uuid.New().String(),
		Name:       "adopt-test",
		Path:       "/home/workspace/projects/adopt-test",
		IsGitRepo:  false,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}
	require.NoError(t, repo.CreateProject(context.Background(), project))
	return project
}

// TestListProjectReposAdoptsUnregisteredRepos pins the self-heal: a repo that
// exists on disk (e.g. manual `git init` in the workspace terminal) but has no
// registry row is adopted at the gate instead of failing the precondition.
func TestListProjectReposAdoptsUnregisteredRepos(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	project := newAdoptTestProject(t, repo)
	router := &adoptTestDaemonRouter{
		discovered: []map[string]any{{"relative_path": "", "name": "adopt-test"}},
	}
	s := NewWorktreeService(repo, nil, router)

	repos, err := s.listProjectRepos(ctx, project)
	require.NoError(t, err)
	require.Len(t, repos, 1)
	assert.Equal(t, "", repos[0].RelativePath)

	// The adoption persisted: registry row exists and IsGitRepo trued up.
	persisted, err := repo.ListReposByProject(ctx, project.ID)
	require.NoError(t, err)
	require.Len(t, persisted, 1)
	updated, err := repo.GetProject(ctx, project.ID)
	require.NoError(t, err)
	assert.True(t, updated.IsGitRepo)

	// Second call is served from the registry — no rediscovery, no duplicates.
	repos, err = s.listProjectRepos(ctx, project)
	require.NoError(t, err)
	assert.Len(t, repos, 1)
	assert.Equal(t, 1, router.discoverCalls)
}

// TestListProjectReposStillFailsWhenNothingOnDisk pins that the precondition
// error survives when the daemon confirms there is genuinely no repo.
func TestListProjectReposStillFailsWhenNothingOnDisk(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	project := newAdoptTestProject(t, repo)
	router := &adoptTestDaemonRouter{discovered: nil}
	s := NewWorktreeService(repo, nil, router)

	_, err := s.listProjectRepos(ctx, project)
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Equal(t, 1, router.discoverCalls)
}
