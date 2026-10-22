// Copyright (c) 2025 Reliant Labs
package worktree

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/toolexec/toolexectest"
	typedconfig "github.com/reliant-labs/reliant/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newWorktreeService builds the Service these tests drive, as server.go
// does but without Temporal. A settler, when given, settles archived
// directories (worktreesweep); without one archiving leaves them.
func newWorktreeService(t testing.TB, repo Repository, router Router, settler ...Settler) *Service {
	t.Helper()
	deps := Deps{Logger: slog.Default(), Config: &typedconfig.Config{}, Repo: repo, Router: router}
	if len(settler) > 0 {
		deps.Settler = settler[0]
	}
	svc, err := New(deps)
	require.NoError(t, err)
	return svc
}

type recordingWorktreeDaemonRouter struct {
	toolexectest.Stub
	// Guarded because CreateWorktree now performs its daemon work on a
	// detached goroutine, so these are written from that goroutine while the
	// test body reads them.
	mu       sync.Mutex
	timeouts map[string]int32
}

// timeoutFor reads a recorded timeout under the lock. Tests must wait for the
// creation goroutine to settle (see awaitWorktreeStatus) before relying on it.
func (r *recordingWorktreeDaemonRouter) timeoutFor(commandType string) int32 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.timeouts[commandType]
}

// CreateWorktree pins the whole fan-out to one daemon, so it routes through
// SendDaemonCommandToDaemon. Embedding alone would send that to the base
// router's SendDaemonCommand, which knows nothing about worktree.create.
func (r *recordingWorktreeDaemonRouter) SendDaemonCommandToDaemon(ctx context.Context, userID, _ string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return r.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
}

func (r *recordingWorktreeDaemonRouter) SendDaemonCommand(ctx context.Context, userID string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	r.mu.Lock()
	if r.timeouts == nil {
		r.timeouts = make(map[string]int32)
	}
	r.timeouts[commandType] = timeoutMs
	r.mu.Unlock()

	switch commandType {
	case "worktree.generate_repo_id":
		return json.Marshal(map[string]string{"repo_id": "test-repo-id"})
	case "worktree.create":
		return json.Marshal(map[string]interface{}{
			"success":       true,
			"worktree_path": filepath.Join(os.TempDir(), "reliant-test-worktree"),
		})
	}

	return r.Stub.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
}

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

// awaitWorktreeStatus blocks until a worktree reaches the expected status.
//
// CreateWorktree returns as soon as the CREATING row is written and finishes
// the daemon work on a detached goroutine, so every assertion about the
// finished state has to wait for that goroutine rather than read immediately.
func awaitWorktreeStatus(t *testing.T, repo Repository, worktreeID string, want reliantv1.WorktreeStatus) *db.Worktree {
	t.Helper()
	var last *db.Worktree
	require.Eventually(t, func() bool {
		wt, err := repo.GetWorktree(context.Background(), worktreeID)
		if err != nil {
			return false
		}
		last = wt
		return wt.Status == int32(want)
	}, 5*time.Second, 10*time.Millisecond,
		"worktree %s never reached %s (last status %v)", worktreeID, want, last)
	return last
}

func TestCreateWorktreeUsesExtendedDaemonTimeout(t *testing.T) {
	repo := db.NewTestRepo(t)

	router := &recordingWorktreeDaemonRouter{}
	svc := newWorktreeService(t, repo, router)

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
	// CreateWorktree requires the project to have at least one nested repo.
	require.NoError(t, repo.CreateRepo(context.Background(), &core.Repo{
		ID:           uuid.New().String(),
		ProjectID:    projectID,
		Name:         "root",
		RelativePath: ".",
		CreatedAt:    now,
		UpdatedAt:    now,
	}))

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	resp, err := svc.CreateWorktree(ctx, connect.NewRequest(&reliantv1.CreateWorktreeRequest{
		ProjectId: projectID,
		Name:      "slow-worktree",
		Branch:    "slow-worktree",
	}))
	require.NoError(t, err)
	require.NotNil(t, resp.Msg.Worktree)

	// The daemon command runs on the detached goroutine, so wait for creation
	// to settle before reading what timeout it used.
	awaitWorktreeStatus(t, repo, resp.Msg.Worktree.Id, reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE)

	assert.Equal(t, worktreeDaemonCommandTimeoutMs, router.timeoutFor("worktree.create"))
	assert.Greater(t, router.timeoutFor("worktree.create"), int32(30_000))
}

// TestCreateWorktreePersistsOwningDaemon verifies the worktree row records the
// daemon that created it (router.ResolveDaemonID), so tool execution for a
// worktree-bound chat can later route back to the machine holding the checkout.
func TestCreateWorktreePersistsOwningDaemon(t *testing.T) {
	repo := db.NewTestRepo(t)

	router := &recordingWorktreeDaemonRouter{}
	svc := newWorktreeService(t, repo, router)

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
	require.NoError(t, repo.CreateRepo(context.Background(), &core.Repo{
		ID:           uuid.New().String(),
		ProjectID:    projectID,
		Name:         "root",
		RelativePath: ".",
		CreatedAt:    now,
		UpdatedAt:    now,
	}))

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	resp, err := svc.CreateWorktree(ctx, connect.NewRequest(&reliantv1.CreateWorktreeRequest{
		ProjectId: projectID,
		Name:      "owned-worktree",
		Branch:    "owned-worktree",
	}))
	require.NoError(t, err)
	require.NotNil(t, resp.Msg.Worktree)

	// Assert against the settled row rather than the CREATING one: the owning
	// daemon must survive the async completion, not just the initial insert.
	stored := awaitWorktreeStatus(t, repo, resp.Msg.Worktree.Id, reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE)
	// The mock router's ResolveDaemonID returns "test-daemon-id".
	require.NotNil(t, stored.DaemonID, "worktree must record its owning daemon")
	assert.Equal(t, "test-daemon-id", *stored.DaemonID)
}

func setupTestWorktreeServiceForRevert(t *testing.T) (*Service, string, string) {
	t.Helper()

	repo := db.NewTestRepo(t)
	svc := newWorktreeService(t, repo, &toolexectest.Stub{})

	userID := uuid.New().String()
	projectID := uuid.New().String()
	worktreeID := uuid.New().String()
	repoDir, _ := setupTestGitRepoWithRemote(t, "main")
	now := time.Now().UTC()

	err := repo.CreateProject(context.Background(), &db.Project{
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

	return svc, userID, worktreeID
}

func TestRevertFiles_DeletesNestedUntrackedFile(t *testing.T) {
	svc, userID, worktreeID := setupTestWorktreeServiceForRevert(t)

	nestedRelPath := filepath.Join("tmp-untracked", "nested", "file.txt")
	nestedAbsPath := filepath.Join((func() string {
		worktree, err := svc.deps.Repo.GetWorktree(context.Background(), worktreeID)
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
	svc, userID, worktreeID := setupTestWorktreeServiceForRevert(t)

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	_, err := svc.RevertFiles(ctx, connect.NewRequest(&reliantv1.RevertFilesRequest{
		WorktreeId: worktreeID,
		Files:      []string{"does-not-exist.txt"},
	}))
	require.Error(t, err)

	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
	assert.Contains(t, svcerr.ToConnect(err).Message(), "file not in changed state")
}
