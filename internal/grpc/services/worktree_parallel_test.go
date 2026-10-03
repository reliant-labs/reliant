// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"path/filepath"
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

// A workspace spanning several repos used to check them out ONE AT A TIME, so
// the time it sat in CREATING (the terminal's "Waiting for the workspace to
// finish setting up…") was the SUM of every checkout — ~29s for the
// control-plane/forge/reliant workspace. These tests pin the concurrent
// fan-out that replaced it, and the two rules that keep it safe: nested repos
// still wait for their container, and a failure still tears down every
// checkout that succeeded.

// concurrentCreateRouter answers worktree.create the way a daemon would, and
// records how the creates overlapped.
type concurrentCreateRouter struct {
	worktreeTestDaemonRouter

	// rendezvous, when > 0, holds every create until that many are in flight
	// at once — or until rendezvousTimeout passes. Sequential creates can
	// never reach it, so each one waits out the timeout and the test sees
	// peak == 1.
	rendezvous        int
	rendezvousTimeout time.Duration
	// failSubPath, when set, makes the create for that sub_path report a git
	// failure. A pointer because "" is a real sub_path — the root repo's.
	failSubPath *string
	// successDelay holds every SUCCESSFUL create after the rendezvous, so a
	// failing sibling returns while they are still checking out — the case
	// where cancelling them would orphan their checkouts.
	successDelay time.Duration

	home string

	mu          sync.Mutex
	cond        *sync.Cond
	inFlight    int
	peak        int
	started     []string // sub_paths, in the order their create began
	finished    []string // sub_paths, in the order their create returned
	deleted     []string // worktree_path of every worktree.delete_directory
	workspaceID string   // as sent by the handler; one per workspace
}

func newConcurrentCreateRouter(t *testing.T) *concurrentCreateRouter {
	r := &concurrentCreateRouter{home: t.TempDir(), rendezvousTimeout: 2 * time.Second}
	r.cond = sync.NewCond(&r.mu)
	return r
}

func (r *concurrentCreateRouter) SendDaemonCommandToDaemon(ctx context.Context, userID, _ string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return r.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
}

func (r *concurrentCreateRouter) SendDaemonCommand(ctx context.Context, userID string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	switch commandType {
	case "worktree.create":
		return r.create(payload)
	case "worktree.delete_directory":
		var req struct {
			WorktreePath string `json:"worktree_path"`
		}
		_ = json.Unmarshal(payload, &req)
		r.mu.Lock()
		r.deleted = append(r.deleted, req.WorktreePath)
		r.mu.Unlock()
		return json.Marshal(map[string]bool{"deleted": true})
	}
	return r.worktreeTestDaemonRouter.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
}

func (r *concurrentCreateRouter) create(payload []byte) ([]byte, error) {
	var req struct {
		WorkspaceID string `json:"workspace_id"`
		SubPath     string `json:"sub_path"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, err
	}

	r.mu.Lock()
	r.inFlight++
	r.peak = max(r.peak, r.inFlight)
	r.started = append(r.started, req.SubPath)
	r.workspaceID = req.WorkspaceID
	r.cond.Broadcast()
	if r.rendezvous > 0 {
		// sync.Cond has no timed wait, so one timer wakes every waiter when
		// the deadline passes.
		deadline := time.Now().Add(r.rendezvousTimeout)
		timer := time.AfterFunc(r.rendezvousTimeout, func() {
			r.mu.Lock()
			r.cond.Broadcast()
			r.mu.Unlock()
		})
		for r.inFlight < r.rendezvous && time.Now().Before(deadline) {
			r.cond.Wait()
		}
		timer.Stop()
	}
	r.mu.Unlock()

	failing := r.failSubPath != nil && req.SubPath == *r.failSubPath
	if !failing {
		time.Sleep(r.successDelay)
	}

	r.mu.Lock()
	r.inFlight--
	r.finished = append(r.finished, req.SubPath)
	r.mu.Unlock()

	if failing {
		return json.Marshal(map[string]any{"success": false, "error": "fatal: invalid reference: nope"})
	}
	path := filepath.Join(r.home, ".reliant", "worktrees", req.WorkspaceID)
	if req.SubPath != "" && req.SubPath != "." {
		path = filepath.Join(path, req.SubPath)
	}
	return json.Marshal(map[string]any{"success": true, "worktree_path": path, "base_branch": "main"})
}

func (r *concurrentCreateRouter) snapshot() (peak int, started, finished, deleted []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.peak, append([]string(nil), r.started...), append([]string(nil), r.finished...), append([]string(nil), r.deleted...)
}

// workspaceRoot is where the fake daemon put this workspace's checkouts.
func (r *concurrentCreateRouter) workspaceRoot() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return filepath.Join(r.home, ".reliant", "worktrees", r.workspaceID)
}

// seedMultiRepoProject creates a project whose repos sit at the given
// relative paths.
func seedMultiRepoProject(t *testing.T, repo db.Repository, userID string, relPaths ...string) string {
	t.Helper()
	projectID := uuid.New().String()
	now := time.Now().UTC()
	require.NoError(t, repo.CreateProject(context.Background(), &db.Project{
		ID: projectID, UserID: userID, Name: "Multi Repo", Path: t.TempDir(),
		IsGitRepo: true, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	for _, rel := range relPaths {
		require.NoError(t, repo.CreateRepo(context.Background(), &core.Repo{
			ID: uuid.New().String(), ProjectID: projectID, Name: filepath.Base(rel),
			RelativePath: rel, CreatedAt: now, UpdatedAt: now,
		}))
	}
	return projectID
}

func createWorkspace(t *testing.T, svc *WorktreeService, userID, projectID, name string) (context.Context, string) {
	t.Helper()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	resp, err := svc.CreateWorktree(ctx, connect.NewRequest(&reliantv1.CreateWorktreeRequest{
		ProjectId: projectID, Name: name, Branch: name,
	}))
	require.NoError(t, err)
	return ctx, resp.Msg.Worktree.Id
}

// TestCreateWorktreeChecksOutSiblingReposConcurrently is the change itself.
// The fake daemon holds each create until all three are in flight; a
// sequential fan-out can never get there, so it reports a peak of 1.
func TestCreateWorktreeChecksOutSiblingReposConcurrently(t *testing.T) {
	repo := db.NewTestRepo(t)
	router := newConcurrentCreateRouter(t)
	router.rendezvous = 3
	svc := NewWorktreeService(repo, nil, router)

	userID := uuid.New().String()
	projectID := seedMultiRepoProject(t, repo, userID, "control-plane", "forge", "reliant")
	_, id := createWorkspace(t, svc, userID, projectID, "parallel")

	settled := awaitWorktreeStatus(t, repo, id, reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE)
	peak, started, _, _ := router.snapshot()
	assert.Equal(t, 3, peak, "all three sibling checkouts must be in flight together, not one after another")
	assert.ElementsMatch(t, []string{"control-plane", "forge", "reliant"}, started)

	// The settled row is what the sequential loop produced: the workspace
	// root, and a base branch per repo.
	assert.Equal(t, router.workspaceRoot(), settled.Path)
	assert.Len(t, settled.BaseBranches, 3)
}

// TestCreateWorktreeChecksOutContainerBeforeNestedRepo pins the one ordering
// constraint left: a repo nested inside another lands inside its checkout, so
// the container must finish first. Unrelated repos still run together.
func TestCreateWorktreeChecksOutContainerBeforeNestedRepo(t *testing.T) {
	repo := db.NewTestRepo(t)
	router := newConcurrentCreateRouter(t)
	svc := NewWorktreeService(repo, nil, router)

	userID := uuid.New().String()
	// "" is the project root, which contains both nested repos.
	projectID := seedMultiRepoProject(t, repo, userID, "", "apps/web", "apps/api")
	_, id := createWorkspace(t, svc, userID, projectID, "nested")

	settled := awaitWorktreeStatus(t, repo, id, reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE)
	_, started, finished, _ := router.snapshot()
	require.Len(t, started, 3)
	require.NotEmpty(t, finished)
	assert.Equal(t, "", started[0], "the root repo must start first")
	assert.Equal(t, "", finished[0], "the root repo must finish before a nested repo starts")
	assert.ElementsMatch(t, []string{"apps/web", "apps/api"}, started[1:])
	// The root repo's checkout IS the workspace root.
	assert.Equal(t, router.workspaceRoot(), settled.Path)
}

// TestCreateWorktreeFailureTearsDownEverySiblingCheckout is the hazard the
// concurrent fan-out introduces. The failing repo fails while its siblings
// are still checking out. Cancelling them would only stop the server
// waiting — the daemon's git finishes anyway, at a path the server never
// learns — so both must run to completion and BOTH must be rolled back.
func TestCreateWorktreeFailureTearsDownEverySiblingCheckout(t *testing.T) {
	repo := db.NewTestRepo(t)
	router := newConcurrentCreateRouter(t)
	router.rendezvous = 3
	router.failSubPath = stringPtr("forge")
	router.successDelay = 300 * time.Millisecond
	svc := NewWorktreeService(repo, nil, router)

	userID := uuid.New().String()
	projectID := seedMultiRepoProject(t, repo, userID, "control-plane", "forge", "reliant")
	_, id := createWorkspace(t, svc, userID, projectID, "doomed")

	settled := awaitWorktreeStatus(t, repo, id, reliantv1.WorktreeStatus_WORKTREE_STATUS_FAILED)
	assert.Empty(t, settled.Path, "a FAILED row carries no path")

	_, _, finished, deleted := router.snapshot()
	require.Len(t, finished, 3, "every sibling must run to completion, not be abandoned mid-checkout")
	assert.Equal(t, "forge", finished[0], "the failure must land while its siblings are still running")
	root := router.workspaceRoot()
	assert.ElementsMatch(t, []string{
		filepath.Join(root, "control-plane"),
		filepath.Join(root, "reliant"),
		root, // the workspace root itself, once its checkouts are gone
	}, deleted, "both checkouts that succeeded must be torn down, then the root")
}
