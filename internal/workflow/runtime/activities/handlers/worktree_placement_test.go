// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

const activityDefaultDaemon = "daemon-default"

// placementRecordingRouter records the daemon each worktree command reached.
// The embedded interface is nil: a method the activities are not expected to
// call panics rather than quietly succeeding.
type placementRecordingRouter struct {
	toolexec.DaemonRouter

	mu      sync.Mutex
	targets map[string][]string
}

func (r *placementRecordingRouter) ResolveDaemonID(context.Context, string) (string, error) {
	return activityDefaultDaemon, nil
}

// ResolveDaemonIDForSelector models the real router's ownership-checked
// selector resolution (toolexec.ResolveDaemonIDForSelector no longer echoes an
// explicit id back unverified): the fake user owns the default machine and
// daemon-b, and any other id is not found.
func (r *placementRecordingRouter) ResolveDaemonIDForSelector(_ context.Context, _ string, sel *toolexec.DaemonSelector) (string, error) {
	switch sel.ID {
	case activityDefaultDaemon, "daemon-b":
		return sel.ID, nil
	}
	return "", errors.New("no daemon available")
}

func (r *placementRecordingRouter) SendDaemonCommand(_ context.Context, _ string, commandType string, _ []byte, _ int32) ([]byte, error) {
	return r.deliver(activityDefaultDaemon, commandType)
}

func (r *placementRecordingRouter) SendDaemonCommandToDaemon(_ context.Context, _, daemonID, commandType string, _ []byte, _ int32) ([]byte, error) {
	return r.deliver(daemonID, commandType)
}

func (r *placementRecordingRouter) deliver(daemonID, commandType string) ([]byte, error) {
	r.mu.Lock()
	if r.targets == nil {
		r.targets = make(map[string][]string)
	}
	r.targets[commandType] = append(r.targets[commandType], daemonID)
	r.mu.Unlock()

	switch commandType {
	case "worktree.create":
		return json.Marshal(map[string]any{"success": true, "worktree_path": "/home/u/.reliant/worktrees/p/feature"})
	case "worktree.delete_directory":
		return json.Marshal(map[string]any{"deleted": true})
	}
	return json.Marshal(map[string]any{})
}

func (r *placementRecordingRouter) daemonsFor(commandType string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.targets[commandType]...)
}

type activityPlacementFixture struct {
	repo      db.Repository
	router    *placementRecordingRouter
	projectID string
	userID    string
}

func newActivityPlacementFixture(t *testing.T) *activityPlacementFixture {
	t.Helper()
	f := &activityPlacementFixture{
		repo:      setupTestRepo(t),
		router:    &placementRecordingRouter{},
		projectID: uuid.NewString(),
		userID:    "test-user",
	}
	now := time.Now().UTC()
	ctx := context.Background()
	require.NoError(t, f.repo.CreateProject(ctx, &db.Project{
		ID: f.projectID, Name: "P", Path: "/home/u/projects/p", UserID: f.userID,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	require.NoError(t, f.repo.CreateRepo(ctx, &core.Repo{
		ID: uuid.NewString(), ProjectID: f.projectID, Name: "root", RelativePath: "",
		CreatedAt: now, UpdatedAt: now,
	}))
	return f
}

func (f *activityPlacementFixture) worktree(t *testing.T, name, ownerDaemonID string) string {
	t.Helper()
	now := time.Now().UTC()
	wt := &core.Worktree{
		ID: uuid.NewString(), Name: name, Path: "/home/u/.reliant/worktrees/p/" + name,
		Branch: name, ProjectID: f.projectID, Status: 1,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}
	if ownerDaemonID != "" {
		wt.DaemonID = &ownerDaemonID
	}
	require.NoError(t, f.repo.CreateWorktree(context.Background(), wt))
	return wt.ID
}

func (f *activityPlacementFixture) chat(t *testing.T, worktreeID string) string {
	t.Helper()
	now := time.Now().UTC()
	c := &db.Chat{
		ID: uuid.NewString(), ProjectID: f.projectID, UserID: f.userID,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}
	if worktreeID != "" {
		c.WorktreeID = &worktreeID
	}
	require.NoError(t, f.repo.CreateChat(context.Background(), c))
	return c.ID
}

func (f *activityPlacementFixture) createWorktree(t *testing.T, chatID string, selector *types.DaemonSelector) *db.Worktree {
	t.Helper()
	activity := NewCreateWorktreeActivity(f.repo, f.router)
	out, err := activity.Execute(context.Background(), ActivityInput{
		Runtime: types.RuntimeContext{ChatID: chatID, DaemonSelector: selector},
		Node: &reliantv1.Node{
			Type: "create_worktree",
			Args: &reliantv1.Node_CreateWorktree{CreateWorktree: &reliantv1.CreateWorktreeArgs{
				Name: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "candidate-" + uuid.NewString()[:8]}},
			}},
		},
	})
	require.NoError(t, err)
	stored, err := f.repo.GetWorktree(context.Background(), out.Id)
	require.NoError(t, err)
	return stored
}

func assertActivityPlacedOn(t *testing.T, router *placementRecordingRouter, wt *db.Worktree, daemonID string) {
	t.Helper()
	require.NotNil(t, wt.DaemonID, "worktree must record its owning daemon, or the tools of the chat that uses it route elsewhere")
	assert.Equal(t, daemonID, *wt.DaemonID)
	creates := router.daemonsFor("worktree.create")
	require.NotEmpty(t, creates)
	for _, got := range creates {
		assert.Equal(t, daemonID, got, "a checkout was created on the wrong machine")
	}
}

// TestCreateWorktreeActivity_PlacesOnRunDaemon: a create_worktree node runs in
// a workflow whose tools execute on machine B (its session/workflow daemon), so
// the worktree it creates must be on B — the run's next steps work inside it.
// Before the fix every command went to the user's default machine and the row
// recorded no owner at all.
func TestCreateWorktreeActivity_PlacesOnRunDaemon(t *testing.T) {
	f := newActivityPlacementFixture(t)
	chatID := f.chat(t, "")

	wt := f.createWorktree(t, chatID, &types.DaemonSelector{ID: "daemon-b"})

	assertActivityPlacedOn(t, f.router, wt, "daemon-b")
}

// TestCreateWorktreeActivity_PlacesOnChatWorktreeOwner: with no run-level
// daemon, a chat inside an owned worktree runs its tools on that worktree's
// machine (toolDaemonSelector), so that is where the new worktree goes.
func TestCreateWorktreeActivity_PlacesOnChatWorktreeOwner(t *testing.T) {
	f := newActivityPlacementFixture(t)
	chatID := f.chat(t, f.worktree(t, "feature", "daemon-b"))

	wt := f.createWorktree(t, chatID, nil)

	assertActivityPlacedOn(t, f.router, wt, "daemon-b")
}

// TestCreateWorktreeActivity_NoDaemonContextRecordsDefault: with nothing to
// follow, the default machine is resolved ONCE, every checkout lands there, and
// the row records it.
func TestCreateWorktreeActivity_NoDaemonContextRecordsDefault(t *testing.T) {
	f := newActivityPlacementFixture(t)
	chatID := f.chat(t, "")

	wt := f.createWorktree(t, chatID, nil)

	assertActivityPlacedOn(t, f.router, wt, activityDefaultDaemon)
}

// TestDeleteWorktreeActivity_RoutesToOwningDaemon: a worktree's checkout exists
// only on its owner, so tearing it down anywhere else deletes nothing and
// strands it there.
func TestDeleteWorktreeActivity_RoutesToOwningDaemon(t *testing.T) {
	f := newActivityPlacementFixture(t)
	f.worktree(t, "doomed", "daemon-b")
	chatID := f.chat(t, "")

	activity := NewDeleteWorktreeActivity(f.repo, f.router)
	_, err := activity.Execute(context.Background(), DeleteWorktreeInput{ChatID: chatID, Name: "doomed"})
	require.NoError(t, err)

	assert.Equal(t, []string{"daemon-b"}, f.router.daemonsFor("worktree.delete_directory"))
}
