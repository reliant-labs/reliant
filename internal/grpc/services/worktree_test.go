// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
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
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// worktreeTestDaemonRouter implements toolexec.DaemonRouter for worktree tests.
type worktreeTestDaemonRouter struct{}

type recordingWorktreeDaemonRouter struct {
	worktreeTestDaemonRouter
	timeouts map[string]int32
}

func (r *recordingWorktreeDaemonRouter) SendDaemonCommand(ctx context.Context, userID string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	if r.timeouts == nil {
		r.timeouts = make(map[string]int32)
	}
	r.timeouts[commandType] = timeoutMs

	switch commandType {
	case "worktree.generate_repo_id":
		return json.Marshal(map[string]string{"repo_id": "test-repo-id"})
	case "worktree.create":
		return json.Marshal(map[string]interface{}{
			"success":       true,
			"worktree_path": filepath.Join(os.TempDir(), "reliant-test-worktree"),
		})
	}

	return r.worktreeTestDaemonRouter.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
}

func (r *worktreeTestDaemonRouter) IsDaemonOnline(_ context.Context, _ string) (bool, error) {
	return true, nil
}
func (r *worktreeTestDaemonRouter) SendToolRequest(_ context.Context, _ string, _ *toolexec.ToolExecutionRequest) error {
	return nil
}
func (r *worktreeTestDaemonRouter) SendToolRequestSync(_ context.Context, _ string, _ *toolexec.ToolExecutionRequest) (*toolexec.ToolExecutionResponse, error) {
	return &toolexec.ToolExecutionResponse{Success: true}, nil
}
func (r *worktreeTestDaemonRouter) SendToolRequestSyncWithSelector(_ context.Context, _ string, _ *toolexec.ToolExecutionRequest, _ *toolexec.DaemonSelector) (*toolexec.ToolExecutionResponse, error) {
	return &toolexec.ToolExecutionResponse{Success: true}, nil
}
func (r *worktreeTestDaemonRouter) SendToolExecutionCancel(_ context.Context, _, _, _ string) error {
	return nil
}
func (r *worktreeTestDaemonRouter) SendKillProcess(_ context.Context, _, _ string) error {
	return nil
}
func (r *worktreeTestDaemonRouter) SendDaemonCommand(_ context.Context, _ string, commandType string, payload []byte, _ int32) ([]byte, error) {
	switch commandType {
	case "worktree.validate_path":
		var req map[string]string
		_ = json.Unmarshal(payload, &req)
		path := req["path"]
		if _, err := os.Stat(path); err != nil {
			return json.Marshal(map[string]interface{}{"exists": false, "error": "not_found"})
		}
		return json.Marshal(map[string]interface{}{"exists": true})
	case "worktree.revert":
		var req struct {
			WorktreePath string   `json:"worktree_path"`
			Files        []string `json:"files"`
		}
		_ = json.Unmarshal(payload, &req)

		type revertResult struct {
			File    string `json:"file"`
			Success bool   `json:"success"`
			Error   string `json:"error,omitempty"`
		}
		var results []revertResult
		for _, file := range req.Files {
			absPath := filepath.Join(req.WorktreePath, file)
			cmd := exec.Command("git", "checkout", "--", file)
			cmd.Dir = req.WorktreePath
			if err := cmd.Run(); err != nil {
				if _, statErr := os.Stat(absPath); statErr == nil {
					_ = os.RemoveAll(absPath)
					results = append(results, revertResult{File: file, Success: true})
				} else {
					results = append(results, revertResult{File: file, Success: false, Error: "file not in changed state"})
				}
			} else {
				results = append(results, revertResult{File: file, Success: true})
			}
		}
		return json.Marshal(map[string]interface{}{"results": results})
	}
	return nil, fmt.Errorf("unhandled command: %s", commandType)
}
func (r *worktreeTestDaemonRouter) SendLoadProjectConfigs(_ context.Context, _, _, _ string) error {
	return nil
}
func (r *worktreeTestDaemonRouter) SendWatchProjectConfigs(_ context.Context, _, _ string, _ bool) error {
	return nil
}
func (r *worktreeTestDaemonRouter) SendTerminalInput(_ context.Context, _, _ string, _ []byte) error {
	return nil
}
func (r *worktreeTestDaemonRouter) SendTerminalResize(_ context.Context, _, _ string, _, _ uint32) error {
	return nil
}
func (r *worktreeTestDaemonRouter) SubscribeTerminalOutput(_ context.Context, _, _ string) (<-chan *toolexec.TerminalOutputEvent, func(), error) {
	ch := make(chan *toolexec.TerminalOutputEvent)
	return ch, func() { close(ch) }, nil
}
func (r *worktreeTestDaemonRouter) SubscribeProcessOutput(_ context.Context, _, _ string, _ bool) (<-chan *toolexec.ProcessOutputEvent, func(), error) {
	ch := make(chan *toolexec.ProcessOutputEvent)
	return ch, func() { close(ch) }, nil
}
func (r *worktreeTestDaemonRouter) Close() error { return nil }

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

func TestCreateWorktreeUsesExtendedDaemonTimeout(t *testing.T) {
	sqlDB, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	defer sqlDB.Close()
	require.NoError(t, db.RunMigrations(sqlDB))

	repo := db.NewRepo(sqlDB)
	router := &recordingWorktreeDaemonRouter{}
	svc := NewWorktreeService(repo, nil, router)

	userID := uuid.New().String()
	projectID := uuid.New().String()
	now := time.Now().UTC()
	require.NoError(t, repo.CreateProject(context.Background(), &db.Project{
		ID:         projectID,
		UserID:     userID,
		Name:       "Test Project",
		Path:       t.TempDir(),
		IsGitRepo:  true,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	resp, err := svc.CreateWorktree(ctx, connect.NewRequest(&reliantv1.CreateWorktreeRequest{
		ProjectId: projectID,
		Name:      "slow-worktree",
		Branch:    "slow-worktree",
	}))
	require.NoError(t, err)
	require.NotNil(t, resp.Msg.Worktree)

	assert.Equal(t, worktreeDaemonCommandTimeoutMs, router.timeouts["worktree.create"])
	assert.Greater(t, router.timeouts["worktree.create"], int32(30_000))
}

func setupTestWorktreeServiceForRevert(t *testing.T) (*WorktreeService, *sql.DB, string, string) {
	t.Helper()

	sqlDB, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.RunMigrations(sqlDB))

	repo := db.NewRepo(sqlDB)
	svc := NewWorktreeService(repo, nil, &worktreeTestDaemonRouter{})

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
