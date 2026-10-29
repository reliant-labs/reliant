// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/ptr"
)

// The owner's report: the new-chat page let a chat start in a workspace that
// lives on machine B while running on machine A. The workspace's checkout is on
// B's disk only, so the chat's tools, Files tab and terminal pointed at a
// folder A does not have. StartChat refuses it, naming the machine to pick.

// ownedWorktree inserts a branch workspace in the env's project whose
// checkout is on ownerDaemonID.
func (e *startDaemonEnv) ownedWorktree(t *testing.T, ownerDaemonID string) string {
	t.Helper()
	now := time.Now().UTC()
	wt := &db.Worktree{
		ID: uuid.NewString(), Name: "wt-" + uuid.NewString()[:8], Path: t.TempDir(),
		Branch: "feature", ProjectID: e.projectID, DaemonID: &ownerDaemonID,
		Status:    int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE),
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}
	require.NoError(t, e.repo.CreateWorktree(context.Background(), wt))
	return wt.ID
}

// namedDaemon registers a self-hosted machine whose hostname is its name.
func (e *startDaemonEnv) namedDaemon(t *testing.T, hostname string) string {
	t.Helper()
	id := uuid.NewString()
	selfHosted := "self_hosted"
	require.NoError(t, e.repo.UpsertDaemon(context.Background(), &db.Daemon{
		ID: id, UserID: "test-user", Hostname: &hostname, DaemonType: &selfHosted,
	}))
	return id
}

func TestStartChatRefusesAWorkspaceOnAnotherMachine(t *testing.T) {
	e := newStartDaemonEnv(t)
	machineA := e.namedDaemon(t, "laptop")
	machineB := e.namedDaemon(t, "gpu-box")
	worktreeOnB := e.ownedWorktree(t, machineB)

	_, err := e.start(t, func(r *reliantv1.StartChatRequest) {
		r.WorktreeId = &worktreeOnB
		r.DaemonId = ptr.Of(machineA)
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
	assert.Contains(t, err.Error(), `"gpu-box"`, "the refusal names the machine the workspace lives on")
	assert.Empty(t, e.temporal.inputs, "a refused start launches nothing")
	assert.Empty(t, e.router.selectors, "a refused start wakes nothing")
}

func TestStartChatInAWorkspaceOnItsOwnMachine(t *testing.T) {
	e := newStartDaemonEnv(t)
	machineB := e.namedDaemon(t, "gpu-box")
	worktreeOnB := e.ownedWorktree(t, machineB)

	resp, err := e.start(t, func(r *reliantv1.StartChatRequest) {
		r.WorktreeId = &worktreeOnB
		r.DaemonId = ptr.Of(machineB)
	})
	require.NoError(t, err)
	chat, err := e.repo.GetChat(e.ctx, resp.Msg.GetChat().GetId())
	require.NoError(t, err)
	require.NotNil(t, chat.ActiveDaemonID)
	assert.Equal(t, machineB, *chat.ActiveDaemonID)
}

// The main checkout is on every machine with the project: any machine may run it.
func TestStartChatOnTheMainCheckoutTakesAnyMachine(t *testing.T) {
	e := newStartDaemonEnv(t)
	machineA := e.namedDaemon(t, "laptop")

	_, err := e.start(t, func(r *reliantv1.StartChatRequest) { r.DaemonId = ptr.Of(machineA) })
	require.NoError(t, err)
}

func TestSetChatDaemonRefusesMovingAWorkspaceChatOffItsMachine(t *testing.T) {
	e := newStartDaemonEnv(t)
	machineA := e.namedDaemon(t, "laptop")
	machineB := e.namedDaemon(t, "gpu-box")
	worktreeOnB := e.ownedWorktree(t, machineB)

	resp, err := e.start(t, func(r *reliantv1.StartChatRequest) { r.WorktreeId = &worktreeOnB })
	require.NoError(t, err)
	chatID := resp.Msg.GetChat().GetId()

	_, err = e.svc.SetChatDaemon(e.ctx, connect.NewRequest(&reliantv1.SetChatDaemonRequest{ChatId: chatID, DaemonId: machineA}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
	assert.Contains(t, err.Error(), `"gpu-box"`)

	_, err = e.svc.SetChatDaemon(e.ctx, connect.NewRequest(&reliantv1.SetChatDaemonRequest{ChatId: chatID, DaemonId: machineB}))
	require.NoError(t, err, "its own machine is always allowed")
}
