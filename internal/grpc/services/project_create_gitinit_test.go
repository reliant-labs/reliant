// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
)

// createProjectDaemonRouter fakes the daemon side of CreateProject: an empty
// fresh directory (zero discovered repos) whose auto git-init succeeds.
type createProjectDaemonRouter struct {
	worktreeTestDaemonRouter
	gitInitCalled bool
}

func (r *createProjectDaemonRouter) SendDaemonCommandToDaemon(ctx context.Context, userID, _ string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return r.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
}

func (r *createProjectDaemonRouter) SendDaemonCommand(_ context.Context, _ string, commandType string, _ []byte, _ int32) ([]byte, error) {
	switch commandType {
	case "repo.discover":
		return json.Marshal(map[string]any{"discovered": []any{}})
	case "project.init_git_repo":
		r.gitInitCalled = true
		return json.Marshal(map[string]any{"success": true})
	default:
		// fs.mkdir, project.init_files, ... — best-effort calls the handler
		// only logs about.
		return json.Marshal(map[string]any{})
	}
}

// TestCreateProjectAutoInitRegistersRootRepo pins the fix for "new project has
// no git repos": when CreateProject auto-inits a fresh empty directory, the
// root repo must be REGISTERED, not just initialized on disk — discovery ran
// before init and found nothing, and worktree creation gates on the registry.
func TestCreateProjectAutoInitRegistersRootRepo(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	userID := "user-create-project-git"
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)

	router := &createProjectDaemonRouter{}
	s := NewProjectService(repo, router)

	resp, err := s.CreateProject(ctx, connect.NewRequest(&reliantv1.CreateProjectRequest{
		Name: "fresh-project",
		Path: "/home/workspace/projects/fresh-project",
	}))
	require.NoError(t, err)
	require.True(t, router.gitInitCalled, "auto git-init should run for a fresh empty dir")
	require.True(t, resp.Msg.Project.IsGitRepo, "project should be git after auto-init")

	repos, err := repo.ListReposByProject(ctx, resp.Msg.Project.Id)
	require.NoError(t, err)
	require.Len(t, repos, 1, "auto-init must register the root repo row (worktrees gate on it)")
	assert.Equal(t, "", repos[0].RelativePath)
	assert.Equal(t, "fresh-project", repos[0].Name)
}