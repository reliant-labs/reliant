// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupTestGitRepoWithRemote creates a git repo with a bare remote
func setupTestGitRepoWithRemote(t *testing.T, defaultBranch string) (repoDir string, remoteDir string) {
	t.Helper()

	tempDir := t.TempDir()

	// Create bare remote repo first
	remoteDir = filepath.Join(tempDir, "remote.git")
	require.NoError(t, os.MkdirAll(remoteDir, 0755))

	// Initialize bare repo with specified default branch
	cmd := exec.Command("git", "init", "--bare", "-b", defaultBranch)
	cmd.Dir = remoteDir
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "Failed to init bare repo: %s", output)

	// Create local repo
	repoDir = filepath.Join(tempDir, "repo")
	require.NoError(t, os.MkdirAll(repoDir, 0755))

	// Initialize local repo and add remote
	gitCommands := [][]string{
		{"git", "init", "-b", defaultBranch},
		{"git", "config", "user.name", "Test User"},
		{"git", "config", "user.email", "test@example.com"},
		{"git", "remote", "add", "origin", remoteDir},
		{"git", "commit", "--allow-empty", "-m", "Initial commit"},
		{"git", "push", "-u", "origin", defaultBranch},
	}

	for _, cmdArgs := range gitCommands {
		cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...)
		cmd.Dir = repoDir
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to run %v: %v\nOutput: %s", cmdArgs, err, output)
		}
	}

	return repoDir, remoteDir
}

func setupTestWorktreeServiceForRevert(t *testing.T) (*WorktreeService, *sql.DB, string, string) {
	t.Helper()

	sqlDB, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.RunMigrations(sqlDB))

	repo := db.NewRepo(sqlDB)
	svc := NewWorktreeService(repo, nil, nil)

	userID := uuid.New().String()
	projectID := uuid.New().String()
	worktreeID := uuid.New().String()
	repoDir, _ := setupTestGitRepoWithRemote(t, "main")
	now := time.Now().UTC()

	err = repo.CreateProject(context.Background(), &db.Project{
		ID:            projectID,
		UserID:        userID,
		Name:          "Test Project",
		Path:          repoDir,
		IsGitRepo:     true,
		DefaultBranch: nil,
		CreatedAt:     now,
		UpdatedAt:     now,
		LastActive:    now,
	})
	require.NoError(t, err)

	err = repo.CreateWorktree(context.Background(), &db.Worktree{
		ID:         worktreeID,
		Name:       "test-worktree",
		Path:       repoDir,
		Branch:     "main",
		BaseBranch: "main",
		ProjectID:  projectID,
		Status:     int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE),
		IsMain:     false,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	})
	require.NoError(t, err)

	return svc, sqlDB, userID, worktreeID
}

func TestRevertFiles_DeletesNestedUntrackedFile(t *testing.T) {
	svc, sqlDB, userID, worktreeID := setupTestWorktreeServiceForRevert(t)
	defer sqlDB.Close()

	nestedRelPath := filepath.Join("tmp-untracked", "nested", "file.txt")
	nestedAbsPath := filepath.Join((func() string {
		worktree, err := svc.database.GetWorktree(context.Background(), worktreeID)
		require.NoError(t, err)
		return worktree.Path
	})(), nestedRelPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(nestedAbsPath), 0755))
	require.NoError(t, os.WriteFile(nestedAbsPath, []byte("temp\n"), 0644))

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	resp, err := svc.RevertFiles(ctx, connect.NewRequest(&reliantv1.RevertFilesRequest{
		WorktreeId: worktreeID,
		Files:      []string{nestedRelPath},
	}))
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Contains(t, resp.Msg.Files, nestedRelPath)

	_, statErr := os.Stat(nestedAbsPath)
	assert.True(t, os.IsNotExist(statErr), "expected nested untracked file to be deleted")
}

func TestRevertFiles_ReturnsFailedPreconditionWhenAllRequestedFilesFail(t *testing.T) {
	svc, sqlDB, userID, worktreeID := setupTestWorktreeServiceForRevert(t)
	defer sqlDB.Close()

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	_, err := svc.RevertFiles(ctx, connect.NewRequest(&reliantv1.RevertFilesRequest{
		WorktreeId: worktreeID,
		Files:      []string{"does-not-exist.txt"},
	}))
	require.Error(t, err)

	connectErr := new(connect.Error)
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, connect.CodeFailedPrecondition, connectErr.Code())
	assert.Contains(t, connectErr.Message(), "file not in changed state")
}
