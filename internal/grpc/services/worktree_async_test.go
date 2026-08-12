// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"sync"
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

// blockingWorktreeDaemonRouter holds `worktree.create` until released, so a
// test can observe the state CreateWorktree returns *while the daemon work is
// still running* — the window that used to be invisible because the handler
// blocked until everything finished.
type blockingWorktreeDaemonRouter struct {
	worktreeTestDaemonRouter

	release chan struct{}
	// failCreate makes the daemon report an unsuccessful create, which is the
	// realistic failure (a git error), not a transport error.
	failCreate bool

	mu       sync.Mutex
	commands []string
}

func (r *blockingWorktreeDaemonRouter) recorded() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.commands...)
}

func (r *blockingWorktreeDaemonRouter) SendDaemonCommandToDaemon(ctx context.Context, userID, _ string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return r.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
}

func (r *blockingWorktreeDaemonRouter) SendDaemonCommand(ctx context.Context, userID string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	r.mu.Lock()
	r.commands = append(r.commands, commandType)
	r.mu.Unlock()

	switch commandType {
	case "worktree.generate_repo_id":
		return json.Marshal(map[string]string{"repo_id": "test-repo-id"})
	case "worktree.create":
		if r.release != nil {
			<-r.release
		}
		// Honour cancellation the way a real daemon call does — it dials over
		// NATS with this context. Without this the mock is blind to the very
		// thing the detached context exists to prevent, and a test asserting
		// survival would pass even with cancellation wired straight through.
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if r.failCreate {
			return json.Marshal(map[string]interface{}{
				"success": false,
				"error":   "fatal: invalid reference: nonexistent-base",
			})
		}
		return json.Marshal(map[string]interface{}{
			"success":       true,
			"worktree_path": "/tmp/reliant-test-worktree",
		})
	}
	return r.worktreeTestDaemonRouter.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
}

// seedWorktreeProject creates the project + one nested repo CreateWorktree needs.
func seedWorktreeProject(t *testing.T, repo db.Repository, userID string) string {
	t.Helper()
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
	return projectID
}

// TestCreateWorktreeReturnsBeforeDaemonWorkFinishes is the core of the change.
// Creation spans 30-120s across repos; holding the response open for that long
// is what let a client disconnect cancel the request context and abort the work
// mid-flight.
func TestCreateWorktreeReturnsBeforeDaemonWorkFinishes(t *testing.T) {
	repo := db.NewTestRepo(t)
	router := &blockingWorktreeDaemonRouter{release: make(chan struct{})}
	svc := NewWorktreeService(repo, nil, router)

	userID := uuid.New().String()
	projectID := seedWorktreeProject(t, repo, userID)
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)

	resp, err := svc.CreateWorktree(ctx, connect.NewRequest(&reliantv1.CreateWorktreeRequest{
		ProjectId: projectID,
		Name:      "pending-worktree",
		Branch:    "pending-worktree",
	}))
	// Returned while `worktree.create` is still blocked inside the daemon.
	require.NoError(t, err)
	require.NotNil(t, resp.Msg.Worktree)
	assert.Equal(t, reliantv1.WorktreeStatus_WORKTREE_STATUS_CREATING, resp.Msg.Worktree.Status,
		"caller must get a pending row it can render, not a finished one")

	stored, err := repo.GetWorktree(ctx, resp.Msg.Worktree.Id)
	require.NoError(t, err)
	assert.Equal(t, int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_CREATING), stored.Status)

	close(router.release)
	settled := awaitWorktreeStatus(t, repo, resp.Msg.Worktree.Id, reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE)
	// The workspace ROOT, not the per-repo checkout: the handler strips the
	// repo's relative path off what the daemon reported. What matters here is
	// that the CREATING row's empty path got replaced at all.
	assert.NotEmpty(t, settled.Path, "the settled row must record a path")
	assert.Equal(t, "/tmp", settled.Path)
}

// TestCreateWorktreeSurvivesClientDisconnect is the bug this change exists to
// fix: the handler threaded the request context into every daemon call, so a
// client going away cancelled the work — and the rollback meant to clean up
// after it.
func TestCreateWorktreeSurvivesClientDisconnect(t *testing.T) {
	repo := db.NewTestRepo(t)
	router := &blockingWorktreeDaemonRouter{release: make(chan struct{})}
	svc := NewWorktreeService(repo, nil, router)

	userID := uuid.New().String()
	projectID := seedWorktreeProject(t, repo, userID)

	// A cancellable context standing in for a client that hangs up — a phone
	// backgrounding mid-create is exactly this.
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), auth.UserIDContextKey, userID))

	resp, err := svc.CreateWorktree(ctx, connect.NewRequest(&reliantv1.CreateWorktreeRequest{
		ProjectId: projectID,
		Name:      "disconnect-worktree",
		Branch:    "disconnect-worktree",
	}))
	require.NoError(t, err)

	// Order matters. Cancel FIRST, while the goroutine is parked on `release`,
	// so the cancellation is already visible when it proceeds. Releasing first
	// lets the work finish before cancel lands, and the test would pass even
	// with cancellation wired straight through.
	cancel()
	close(router.release)

	settled := awaitWorktreeStatus(t, repo, resp.Msg.Worktree.Id, reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE)
	assert.NotEmpty(t, settled.Path,
		"work must complete, and record its path, after the caller disconnects")
}

// TestCreateWorktreeMarksFailedRatherThanVanishing pins the product decision: a
// failed create leaves a visible row the user can retry or dismiss, rather than
// a workspace that silently never appears.
func TestCreateWorktreeMarksFailedRatherThanVanishing(t *testing.T) {
	repo := db.NewTestRepo(t)
	router := &blockingWorktreeDaemonRouter{failCreate: true}
	svc := NewWorktreeService(repo, nil, router)

	userID := uuid.New().String()
	projectID := seedWorktreeProject(t, repo, userID)
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)

	resp, err := svc.CreateWorktree(ctx, connect.NewRequest(&reliantv1.CreateWorktreeRequest{
		ProjectId:  projectID,
		Name:       "doomed-worktree",
		Branch:     "doomed-worktree",
		BaseBranch: stringPtr("nonexistent-base"),
	}))
	require.NoError(t, err, "the failure happens after the response, so the RPC still succeeds")

	awaitWorktreeStatus(t, repo, resp.Msg.Worktree.Id, reliantv1.WorktreeStatus_WORKTREE_STATUS_FAILED)
}

// TestCreateWorktreeIdempotencyKeyPreventsDuplicates covers the hazard async
// creation introduces: a client that loses the response cannot tell failure
// from a dropped reply, and the natural retry would otherwise produce a second
// workspace with a second on-disk checkout.
func TestCreateWorktreeIdempotencyKeyPreventsDuplicates(t *testing.T) {
	repo := db.NewTestRepo(t)
	router := &blockingWorktreeDaemonRouter{}
	svc := NewWorktreeService(repo, nil, router)

	userID := uuid.New().String()
	projectID := seedWorktreeProject(t, repo, userID)
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)

	key := "retry-key-1"
	first, err := svc.CreateWorktree(ctx, connect.NewRequest(&reliantv1.CreateWorktreeRequest{
		ProjectId:      projectID,
		Name:           "idempotent-worktree",
		Branch:         "idempotent-worktree",
		IdempotencyKey: &key,
	}))
	require.NoError(t, err)
	awaitWorktreeStatus(t, repo, first.Msg.Worktree.Id, reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE)

	second, err := svc.CreateWorktree(ctx, connect.NewRequest(&reliantv1.CreateWorktreeRequest{
		ProjectId:      projectID,
		Name:           "idempotent-worktree",
		Branch:         "idempotent-worktree",
		IdempotencyKey: &key,
	}))
	require.NoError(t, err, "a retry must not error, it must return the original")
	assert.Equal(t, first.Msg.Worktree.Id, second.Msg.Worktree.Id,
		"the same key must resolve to the same worktree")

	worktrees, err := repo.ListWorktrees(ctx, core.WorktreeFilters{ProjectID: &projectID})
	require.NoError(t, err)
	assert.Len(t, worktrees, 1, "a retry must not create a second workspace")
}

// TestCreateWorktreeWithoutIdempotencyKeyStillCreates guards the opt-out: the
// desktop UI sends no key today, and every create must still work.
func TestCreateWorktreeWithoutIdempotencyKeyStillCreates(t *testing.T) {
	repo := db.NewTestRepo(t)
	router := &blockingWorktreeDaemonRouter{}
	svc := NewWorktreeService(repo, nil, router)

	userID := uuid.New().String()
	projectID := seedWorktreeProject(t, repo, userID)
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)

	for _, name := range []string{"first-worktree", "second-worktree"} {
		resp, err := svc.CreateWorktree(ctx, connect.NewRequest(&reliantv1.CreateWorktreeRequest{
			ProjectId: projectID,
			Name:      name,
			Branch:    name,
		}))
		require.NoError(t, err)
		awaitWorktreeStatus(t, repo, resp.Msg.Worktree.Id, reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE)
	}

	worktrees, err := repo.ListWorktrees(ctx, core.WorktreeFilters{ProjectID: &projectID})
	require.NoError(t, err)
	assert.Len(t, worktrees, 2, "keyless creates are independent")
}

func stringPtr(s string) *string { return &s }
