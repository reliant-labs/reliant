package services

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/stretchr/testify/require"
)

func TestProjectServiceBuildProjectConfigResolver_ResolvesWorktreePathToProjectConfig(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	now := time.Now().UTC()
	projectID := "proj-" + uuid.NewString()
	projectPath := t.TempDir()
	worktreePath := t.TempDir()

	require.NoError(t, repo.CreateProject(context.Background(), &db.Project{
		ID:         projectID,
		UserID:     "test-user",
		Name:       "Resolver Test",
		Path:       projectPath,
		IsGitRepo:  true,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))

	require.NoError(t, repo.CreateWorktree(context.Background(), &db.Worktree{
		ID:         "wt-" + uuid.NewString(),
		Name:       "worktree-a",
		Path:       worktreePath,
		Branch:     "feature/resolver",
		BaseBranch: "main",
		ProjectID:  projectID,
		Status:     int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE),
		IsMain:     false,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))

	storedMCP := `{"project":"{\"mcpServers\":{\"worktree-server\":{\"command\":\"go\",\"args\":[\"version\"],\"enabled\":true}}}"}`
	require.NoError(t, repo.UpsertProjectConfigRecord(context.Background(), &db.ProjectConfigRecord{
		ProjectID:  projectID,
		DaemonID:   "daemon-test",
		MCPConfigs: &storedMCP,
		PushedAt:   now,
	}))

	svc := &ProjectService{database: repo}
	resolver := svc.buildProjectConfigResolver()
	require.NotNil(t, resolver)

	cfg, err := resolver(context.Background(), worktreePath)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.Contains(t, cfg.MCPServers, "worktree-server")
	require.Equal(t, "go", cfg.MCPServers["worktree-server"].Command)
	require.Equal(t, []string{"version"}, cfg.MCPServers["worktree-server"].Args)
	require.True(t, cfg.MCPServers["worktree-server"].Enabled)
}

func TestProjectServiceBuildProjectConfigResolver_UnknownPathReturnsNotFound(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	svc := &ProjectService{database: repo}
	resolver := svc.buildProjectConfigResolver()
	require.NotNil(t, resolver)

	cfg, err := resolver(context.Background(), t.TempDir())
	require.Error(t, err)
	require.Nil(t, cfg)
	require.Contains(t, err.Error(), "project or worktree not found")
}

func TestProjectServiceBuildProjectConfigResolver_EmptyPathReturnsError(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	svc := &ProjectService{database: repo}
	resolver := svc.buildProjectConfigResolver()
	require.NotNil(t, resolver)

	cfg, err := resolver(context.Background(), " ")
	require.Error(t, err)
	require.Nil(t, cfg)
	require.Contains(t, err.Error(), "project path is required")
}

func TestProjectServiceBuildProjectConfigResolver_MissingSnapshotReturnsDefaultConfig(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	now := time.Now().UTC()
	projectID := "proj-" + uuid.NewString()
	projectPath := t.TempDir()

	require.NoError(t, repo.CreateProject(context.Background(), &db.Project{
		ID:         projectID,
		UserID:     "test-user",
		Name:       "Resolver Missing Snapshot",
		Path:       projectPath,
		IsGitRepo:  true,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))

	svc := &ProjectService{database: repo}
	resolver := svc.buildProjectConfigResolver()
	require.NotNil(t, resolver)

	cfg, err := resolver(context.Background(), projectPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.Equal(t, &config.Config{}, cfg)
}
