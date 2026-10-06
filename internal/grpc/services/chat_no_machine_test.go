// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
)

// The upgrade path out of a chat with no machine (research/NO_MACHINE_CHATS.md
// §2.3): "Connect a machine" is SetChatDaemon and clears no_machine in the same
// write; "Continue without machine" is a no-machine BRANCH that leaves the
// original alone. Neither direction may turn a machine chat into one without.

func (e *startDaemonEnv) startNoMachine(t *testing.T) *db.Chat {
	t.Helper()
	resp, err := e.start(t, func(r *reliantv1.StartChatRequest) { r.NoMachine = boolPtr(true) })
	require.NoError(t, err)
	chat, err := e.repo.GetChat(e.ctx, resp.Msg.GetChat().GetId())
	require.NoError(t, err)
	require.True(t, chat.NoMachine)
	return chat
}

func (e *startDaemonEnv) setDaemon(chatID, daemonID string) (*connect.Response[reliantv1.SetChatDaemonResponse], error) {
	return e.svc.SetChatDaemon(e.ctx, connect.NewRequest(&reliantv1.SetChatDaemonRequest{ChatId: chatID, DaemonId: daemonID}))
}

// firstMessageID is the chat's seed user message, a branch point.
func (e *startDaemonEnv) firstMessageID(t *testing.T, chatID string) string {
	t.Helper()
	msgs, err := e.repo.ListMessages(e.ctx, chatID, db.MessageListOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, msgs)
	return msgs[0].ID
}

func (e *startDaemonEnv) branch(t *testing.T, chatID string, mutate func(*reliantv1.BranchChatRequest)) (*connect.Response[reliantv1.BranchChatResponse], error) {
	t.Helper()
	req := &reliantv1.BranchChatRequest{ChatId: chatID, MessageId: e.firstMessageID(t, chatID)}
	if mutate != nil {
		mutate(req)
	}
	return e.svc.BranchChat(e.ctx, connect.NewRequest(req))
}

// mainWorktreeID is the project's main checkout, which every no-machine chat
// binds to.
func (e *startDaemonEnv) mainWorktreeID(t *testing.T) string {
	t.Helper()
	id, err := e.svc.launcher().ResolveChatWorktreeID(e.ctx, e.projectID, nil)
	require.NoError(t, err)
	return *id
}

// machineWorktree is a branch worktree whose checkout lives on daemonID.
func (e *startDaemonEnv) machineWorktree(t *testing.T, daemonID string) string {
	t.Helper()
	now := time.Now().UTC()
	id := uuid.NewString()
	require.NoError(t, e.repo.CreateWorktree(e.ctx, &db.Worktree{
		ID: id, Name: "feature", Path: t.TempDir(), Branch: "feature", BaseBranch: "main",
		ProjectID: e.projectID, DaemonID: &daemonID,
		Status:    int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE),
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	return id
}

func boolPtr(b bool) *bool { return &b }

func TestSetChatDaemonConnectsAChatWithNoMachine(t *testing.T) {
	e := newStartDaemonEnv(t)
	chat := e.startNoMachine(t)
	daemonID := e.daemon(t, "test-user")

	resp, err := e.setDaemon(chat.ID, daemonID)
	require.NoError(t, err)
	assert.False(t, resp.Msg.GetChat().GetNoMachine(), "the response is the connected chat")
	assert.Equal(t, daemonID, resp.Msg.GetChat().GetActiveDaemonId())

	stored, err := e.repo.GetChat(e.ctx, chat.ID)
	require.NoError(t, err)
	assert.False(t, stored.NoMachine, "connecting clears no_machine in the same write")
	require.NotNil(t, stored.ActiveDaemonID)
	assert.Equal(t, daemonID, *stored.ActiveDaemonID)

	// Other clients learn of it through chat_config_changed.
	updates, err := e.repo.GetUserUpdatesSince(e.ctx, "test-user", 0, 100)
	require.NoError(t, err)
	var announced map[string]any
	for _, u := range updates {
		if u.UpdateType == db.UserUpdateChatConfigChanged && u.EntityID == chat.ID {
			require.NoError(t, json.Unmarshal(u.Data, &announced))
		}
	}
	require.NotNil(t, announced, "SetChatDaemon must announce the change")
	assert.Equal(t, false, announced["no_machine"])
	assert.Equal(t, daemonID, announced["active_daemon_id"])
}

func TestSetChatDaemonClearingNeverGivesAMachineChatNoMachine(t *testing.T) {
	e := newStartDaemonEnv(t)
	daemonID := e.daemon(t, "test-user")
	resp, err := e.start(t, func(r *reliantv1.StartChatRequest) { r.DaemonId = strPtr(daemonID) })
	require.NoError(t, err)
	chatID := resp.Msg.GetChat().GetId()

	cleared, err := e.setDaemon(chatID, "")
	require.NoError(t, err)
	assert.False(t, cleared.Msg.GetChat().GetNoMachine())

	stored, err := e.repo.GetChat(e.ctx, chatID)
	require.NoError(t, err)
	assert.Nil(t, stored.ActiveDaemonID)
	assert.False(t, stored.NoMachine, "a chat on a machine can never become no-machine")
}

func TestSetChatDaemonRejectsADaemonTheCallerDoesNotOwn(t *testing.T) {
	e := newStartDaemonEnv(t)
	chat := e.startNoMachine(t)
	foreign := e.daemon(t, "someone-else")

	_, err := e.setDaemon(chat.ID, foreign)
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	stored, err := e.repo.GetChat(e.ctx, chat.ID)
	require.NoError(t, err)
	assert.True(t, stored.NoMachine, "a rejected connect leaves the chat as it was")
	assert.Nil(t, stored.ActiveDaemonID)
}

// "Continue without machine" from a chat whose worktree lives on a machine:
// the branch carries the conversation but none of the machine — it binds to the
// project's main worktree, pins no daemon, and wakes nothing. The original is
// left exactly as it was.
func TestBranchChatWithNoMachineCarriesTheConversationAndNoMachine(t *testing.T) {
	e := newStartDaemonEnv(t)
	daemonID := e.daemon(t, "test-user")
	worktreeID := e.machineWorktree(t, daemonID)
	resp, err := e.start(t, func(r *reliantv1.StartChatRequest) {
		r.WorktreeId = strPtr(worktreeID)
		r.DaemonId = strPtr(daemonID)
	})
	require.NoError(t, err)
	sourceID := resp.Msg.GetChat().GetId()
	wakesBefore := len(e.router.selectors)

	branched, err := e.branch(t, sourceID, func(r *reliantv1.BranchChatRequest) { r.NoMachine = boolPtr(true) })
	require.NoError(t, err)
	assert.True(t, branched.Msg.GetChat().GetNoMachine())

	branch, err := e.repo.GetChat(e.ctx, branched.Msg.GetChat().GetId())
	require.NoError(t, err)
	assert.True(t, branch.NoMachine)
	assert.Nil(t, branch.ActiveDaemonID, "a branch with no machine pins no daemon")
	require.NotNil(t, branch.WorktreeID)
	assert.Equal(t, e.mainWorktreeID(t), *branch.WorktreeID, "not the source's machine-bound worktree")
	assert.Len(t, e.router.selectors, wakesBefore, "branching wakes nothing")

	source, err := e.repo.GetChat(e.ctx, sourceID)
	require.NoError(t, err)
	assert.False(t, source.NoMachine, "the original chat is left intact")
	require.NotNil(t, source.ActiveDaemonID)
	assert.Equal(t, daemonID, *source.ActiveDaemonID)
	assert.Equal(t, worktreeID, *source.WorktreeID)

	// The branch's first send runs with no machine and wakes nothing, whether
	// or not the client repeats the flag.
	for name, flag := range map[string]*bool{"with no_machine": boolPtr(true), "without it": nil} {
		t.Run(name, func(t *testing.T) {
			b, err := e.branch(t, sourceID, func(r *reliantv1.BranchChatRequest) { r.NoMachine = boolPtr(true) })
			require.NoError(t, err)
			branchID := b.Msg.GetChat().GetId()
			wakes := len(e.router.selectors)
			_, err = e.start(t, func(r *reliantv1.StartChatRequest) {
				r.ChatId = &branchID
				r.ProjectId = ""
				r.NoMachine = flag
			})
			require.NoError(t, err)
			assert.Len(t, e.router.selectors, wakes, "a no-machine branch's first send must not wake a daemon")
			started, err := e.repo.GetChat(e.ctx, branchID)
			require.NoError(t, err)
			assert.True(t, started.NoMachine)
			assert.Nil(t, started.ActiveDaemonID)
		})
	}
}

// A worktree is a machine's checkout, so a no-machine branch cannot name one.
func TestBranchChatWithNoMachineRejectsAWorktree(t *testing.T) {
	e := newStartDaemonEnv(t)
	resp, err := e.start(t, nil)
	require.NoError(t, err)
	mainID := e.mainWorktreeID(t)

	_, err = e.branch(t, resp.Msg.GetChat().GetId(), func(r *reliantv1.BranchChatRequest) {
		r.NoMachine = boolPtr(true)
		r.WorktreeId = &mainID
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

// Branching a chat that has no machine keeps the branch without one: there is
// no machine for it to inherit. Naming a worktree is how a branch gets one.
func TestBranchOfAChatWithNoMachineStaysWithoutOne(t *testing.T) {
	e := newStartDaemonEnv(t)
	source := e.startNoMachine(t)

	branched, err := e.branch(t, source.ID, nil)
	require.NoError(t, err)
	assert.True(t, branched.Msg.GetChat().GetNoMachine())

	daemonID := e.daemon(t, "test-user")
	worktreeID := e.machineWorktree(t, daemonID)
	onMachine, err := e.branch(t, source.ID, func(r *reliantv1.BranchChatRequest) { r.WorktreeId = &worktreeID })
	require.NoError(t, err)
	assert.False(t, onMachine.Msg.GetChat().GetNoMachine(), "a branch onto a worktree runs on its machine")
	assert.Equal(t, daemonID, onMachine.Msg.GetChat().GetActiveDaemonId())
}

// A branch onto a machine still cannot start with no machine: its worktree is
// a checkout on that machine.
func TestStartChatRefusesNoMachineForAMachineBranch(t *testing.T) {
	e := newStartDaemonEnv(t)
	resp, err := e.start(t, nil)
	require.NoError(t, err)
	branched, err := e.branch(t, resp.Msg.GetChat().GetId(), nil)
	require.NoError(t, err)
	require.False(t, branched.Msg.GetChat().GetNoMachine())
	branchID := branched.Msg.GetChat().GetId()

	_, err = e.start(t, func(r *reliantv1.StartChatRequest) {
		r.ChatId = &branchID
		r.ProjectId = ""
		r.NoMachine = boolPtr(true)
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	stored, err := e.repo.GetChat(context.Background(), branchID)
	require.NoError(t, err)
	assert.False(t, stored.NoMachine)
}
