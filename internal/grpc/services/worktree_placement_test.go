// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"os"
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

// placementDefaultDaemon is what default resolution answers in these tests:
// the user's default machine, which is NOT the chat's machine.
const placementDefaultDaemon = "daemon-default"

// placementRouter records the daemon every worktree command was delivered to.
// A command that fell through to default resolution is recorded against
// placementDefaultDaemon, so it is distinguishable from one pinned to a chat's
// machine.
type placementRouter struct {
	worktreeTestDaemonRouter

	// Guarded because CreateWorktree does its daemon work on a detached
	// goroutine while the test body reads.
	mu      sync.Mutex
	targets map[string][]string
	// discoverPath is the one linked worktree worktree.discover_repos reports.
	discoverPath string
}

func (r *placementRouter) ResolveDaemonID(context.Context, string) (string, error) {
	return placementDefaultDaemon, nil
}

func (r *placementRouter) SendDaemonCommand(_ context.Context, _ string, commandType string, payload []byte, _ int32) ([]byte, error) {
	return r.deliver(placementDefaultDaemon, commandType, payload)
}

func (r *placementRouter) SendDaemonCommandToDaemon(_ context.Context, _, daemonID, commandType string, payload []byte, _ int32) ([]byte, error) {
	return r.deliver(daemonID, commandType, payload)
}

func (r *placementRouter) deliver(daemonID, commandType string, payload []byte) ([]byte, error) {
	r.mu.Lock()
	if r.targets == nil {
		r.targets = make(map[string][]string)
	}
	r.targets[commandType] = append(r.targets[commandType], daemonID)
	r.mu.Unlock()

	switch commandType {
	case "worktree.create":
		return json.Marshal(map[string]any{
			"success":       true,
			"worktree_path": filepath.Join(os.TempDir(), "reliant-placement-test-worktree"),
		})
	case "worktree.copy_paths":
		return json.Marshal(map[string]any{})
	case "worktree.discover_repos":
		var req struct {
			Repos []struct {
				RepoID string `json:"repo_id"`
			} `json:"repos"`
		}
		_ = json.Unmarshal(payload, &req)
		r.mu.Lock()
		path := r.discoverPath
		r.mu.Unlock()
		return json.Marshal(map[string]any{"worktrees": []map[string]any{
			{"repo_id": req.Repos[0].RepoID, "path": path, "name": filepath.Base(path), "branch": "imported"},
		}})
	case "worktree.validate_path":
		return json.Marshal(map[string]any{"exists": true})
	case "worktree.git_status":
		return json.Marshal(map[string]any{"branch": "feature", "status": "clean"})
	}
	return r.worktreeTestDaemonRouter.SendDaemonCommand(context.Background(), "", commandType, payload, 0)
}

// daemonsFor returns the daemons commandType was delivered to, in send order.
func (r *placementRouter) daemonsFor(commandType string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.targets[commandType]...)
}

// placementFixture is a project with one repo, owned by userID.
type placementFixture struct {
	repo      *db.Repo
	router    *placementRouter
	svc       *WorktreeService
	userID    string
	projectID string
	ctx       context.Context
}

func newPlacementFixture(t *testing.T) *placementFixture {
	t.Helper()
	repo := db.NewTestRepo(t)
	router := &placementRouter{}
	f := &placementFixture{
		repo:      repo,
		router:    router,
		svc:       NewWorktreeService(repo, nil, router),
		userID:    uuid.NewString(),
		projectID: uuid.NewString(),
	}
	f.ctx = context.WithValue(context.Background(), auth.UserIDContextKey, f.userID)

	now := time.Now().UTC()
	require.NoError(t, repo.CreateProject(context.Background(), &db.Project{
		ID: f.projectID, UserID: f.userID, Name: "Placement Project", Path: t.TempDir(),
		IsGitRepo: true, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	require.NoError(t, repo.CreateRepo(context.Background(), &core.Repo{
		ID: uuid.NewString(), ProjectID: f.projectID, Name: "root", RelativePath: ".",
		CreatedAt: now, UpdatedAt: now,
	}))
	return f
}

// worktree inserts an ACTIVE worktree owned by ownerDaemonID ("" = no owner).
func (f *placementFixture) worktree(t *testing.T, ownerDaemonID string) *db.Worktree {
	t.Helper()
	now := time.Now().UTC()
	wt := &db.Worktree{
		ID: uuid.NewString(), Name: "wt-" + uuid.NewString()[:8], Path: t.TempDir(),
		Branch: "feature", ProjectID: f.projectID,
		Status:    int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE),
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}
	if ownerDaemonID != "" {
		wt.DaemonID = &ownerDaemonID
	}
	require.NoError(t, f.repo.CreateWorktree(context.Background(), wt))
	return wt
}

// chat inserts a chat in the fixture's project. pinnedDaemonID ("" = none)
// becomes its active_daemon_id; worktreeID ("" = none) its worktree.
func (f *placementFixture) chat(t *testing.T, userID, pinnedDaemonID, worktreeID string) string {
	t.Helper()
	now := time.Now().UTC()
	c := &db.Chat{
		ID: uuid.NewString(), UserID: userID, Title: "Placement Chat", ProjectID: f.projectID,
		State: db.ChatStateIdle, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}
	if pinnedDaemonID != "" {
		c.ActiveDaemonID = &pinnedDaemonID
	}
	if worktreeID != "" {
		c.WorktreeID = &worktreeID
	}
	require.NoError(t, f.repo.CreateChat(context.Background(), c))
	return c.ID
}

func (f *placementFixture) createWorktree(t *testing.T, req *reliantv1.CreateWorktreeRequest) (*db.Worktree, error) {
	t.Helper()
	req.ProjectId = f.projectID
	req.Name = "placed-" + uuid.NewString()[:8]
	req.Branch = req.Name
	resp, err := f.svc.CreateWorktree(f.ctx, connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return awaitWorktreeStatus(t, f.repo, resp.Msg.Worktree.Id, reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE), nil
}

// assertPlacedOn checks the row records daemonID as its owner AND that every
// checkout was actually made there: a row that names B while the checkout
// landed on A is the same bug, only harder to see.
func assertPlacedOn(t *testing.T, router *placementRouter, wt *db.Worktree, daemonID string) {
	t.Helper()
	require.NotNil(t, wt.DaemonID, "worktree must record its owning daemon")
	assert.Equal(t, daemonID, *wt.DaemonID, "worktree row names the wrong machine")
	creates := router.daemonsFor("worktree.create")
	require.NotEmpty(t, creates, "no worktree.create was sent")
	for _, got := range creates {
		assert.Equal(t, daemonID, got, "a checkout was created on the wrong machine")
	}
}

// TestCreateWorktree_PlacesOnChatMachine is the regression for "CreateWorktree
// puts a new worktree on the user's default machine": a chat running on
// machine B creates a workspace, and the workspace must be on B. Before the
// fix it landed on the default machine, and the branch chat's tools followed it
// there.
func TestCreateWorktree_PlacesOnChatMachine(t *testing.T) {
	f := newPlacementFixture(t)
	chatID := f.chat(t, f.userID, "daemon-b", "")

	wt, err := f.createWorktree(t, &reliantv1.CreateWorktreeRequest{ChatId: &chatID})
	require.NoError(t, err)

	assertPlacedOn(t, f.router, wt, "daemon-b")
}

// TestCreateWorktree_PlacesOnChatWorktreeOwner covers a chat with no pin that
// runs in an owned worktree: its tools route to that worktree's machine, so a
// workspace created from it belongs there too.
func TestCreateWorktree_PlacesOnChatWorktreeOwner(t *testing.T) {
	f := newPlacementFixture(t)
	owned := f.worktree(t, "daemon-b")
	chatID := f.chat(t, f.userID, "", owned.ID)

	wt, err := f.createWorktree(t, &reliantv1.CreateWorktreeRequest{ChatId: &chatID})
	require.NoError(t, err)

	assertPlacedOn(t, f.router, wt, "daemon-b")
}

// TestCreateWorktree_PlacesOnSourceWorktreeOwner covers a workspace created
// FROM another workspace with no chat named: its files are copied from the
// source, which exists only on the source's machine.
func TestCreateWorktree_PlacesOnSourceWorktreeOwner(t *testing.T) {
	f := newPlacementFixture(t)
	source := f.worktree(t, "daemon-b")

	wt, err := f.createWorktree(t, &reliantv1.CreateWorktreeRequest{
		SourceWorktreeId: &source.ID,
		CopyFiles:        []string{".env"},
	})
	require.NoError(t, err)

	assertPlacedOn(t, f.router, wt, "daemon-b")
	assert.Equal(t, []string{"daemon-b"}, f.router.daemonsFor("worktree.copy_paths"),
		"files must be copied on the machine holding the source workspace")
}

// TestCreateWorktree_NoChatContextUsesDefaultMachine pins the fallback: with no
// chat and no source workspace there is nothing to follow, so the user's
// default machine is used, as before.
func TestCreateWorktree_NoChatContextUsesDefaultMachine(t *testing.T) {
	f := newPlacementFixture(t)

	wt, err := f.createWorktree(t, &reliantv1.CreateWorktreeRequest{})
	require.NoError(t, err)

	assertPlacedOn(t, f.router, wt, placementDefaultDaemon)
}

// TestCreateWorktree_RejectsAnotherUsersChat: chat_id now decides which
// machine a checkout lands on, so it must not accept a chat the caller does not
// own.
func TestCreateWorktree_RejectsAnotherUsersChat(t *testing.T) {
	f := newPlacementFixture(t)
	foreignChatID := f.chat(t, uuid.NewString(), "daemon-b", "")

	_, err := f.createWorktree(t, &reliantv1.CreateWorktreeRequest{ChatId: &foreignChatID})
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	assert.Empty(t, f.router.daemonsFor("worktree.create"), "nothing may be created for a rejected request")
}

// TestImportWorktree_PlacesOnChatMachine: an import names a directory on a
// machine; with a chat named, that is the chat's machine.
func TestImportWorktree_PlacesOnChatMachine(t *testing.T) {
	f := newPlacementFixture(t)
	chatID := f.chat(t, f.userID, "daemon-b", "")
	path := filepath.Join(t.TempDir(), "hand-made")
	f.router.discoverPath = path

	resp, err := f.svc.ImportWorktree(f.ctx, connect.NewRequest(&reliantv1.ImportWorktreeRequest{
		Path:      path,
		ProjectId: f.projectID,
		ChatId:    &chatID,
	}))
	require.NoError(t, err)

	stored, err := f.repo.GetWorktree(context.Background(), resp.Msg.Worktree.Id)
	require.NoError(t, err)
	require.NotNil(t, stored.DaemonID)
	assert.Equal(t, "daemon-b", *stored.DaemonID)
	assert.Equal(t, []string{"daemon-b"}, f.router.daemonsFor("worktree.discover_repos"))
	assert.Equal(t, path, stored.Path, "a single-repo project registers in place")
	assert.Equal(t, "imported", stored.Branch)
}

// TestWorktreeGitOps_RouteToOwningDaemon: once a worktree lives on a machine
// other than the default, operations on it must go to that machine. Otherwise
// placing it correctly only moves the failure from the chat's tools to the
// workspace panel ("worktree directory does not exist" on the default).
func TestWorktreeGitOps_RouteToOwningDaemon(t *testing.T) {
	f := newPlacementFixture(t)
	owned := f.worktree(t, "daemon-b")

	_, err := f.svc.GetWorktreeGitStatus(f.ctx, connect.NewRequest(&reliantv1.GetWorktreeGitStatusRequest{
		WorktreeId: owned.ID,
	}))
	require.NoError(t, err)

	assert.Equal(t, []string{"daemon-b"}, f.router.daemonsFor("worktree.validate_path"))
	assert.Equal(t, []string{"daemon-b"}, f.router.daemonsFor("worktree.git_status"))
}

// TestWorktreeGitOps_UnownedWorktreeUsesDefault: the main checkout (and rows
// from before ownership was recorded) name no machine and keep default
// resolution.
func TestWorktreeGitOps_UnownedWorktreeUsesDefault(t *testing.T) {
	f := newPlacementFixture(t)
	unowned := f.worktree(t, "")

	_, err := f.svc.GetWorktreeGitStatus(f.ctx, connect.NewRequest(&reliantv1.GetWorktreeGitStatusRequest{
		WorktreeId: unowned.ID,
	}))
	require.NoError(t, err)

	assert.Equal(t, []string{placementDefaultDaemon}, f.router.daemonsFor("worktree.git_status"))
}
