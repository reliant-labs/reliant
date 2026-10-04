// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/client"
	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/runs"
	"github.com/reliant-labs/reliant/internal/threads"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// inputRecordingTemporal records the WorkflowInput each root run starts with,
// so a test can read the session_daemon_id the run was actually launched with.
type inputRecordingTemporal struct {
	atomicityTestTemporalClient
	mu     sync.Mutex
	inputs []v2.WorkflowInput
}

func (c *inputRecordingTemporal) ExecuteWorkflow(
	ctx context.Context, options client.StartWorkflowOptions, wf interface{}, args ...interface{},
) (client.WorkflowRun, error) {
	c.mu.Lock()
	for _, a := range args {
		if in, ok := a.(v2.WorkflowInput); ok {
			c.inputs = append(c.inputs, in)
		}
	}
	c.mu.Unlock()
	return c.atomicityTestTemporalClient.ExecuteWorkflow(ctx, options, wf, args...)
}

type startDaemonEnv struct {
	svc       *ChatService
	repo      *db.Repo
	ctx       context.Context
	router    *wakeRecordingRouter
	temporal  *inputRecordingTemporal
	projectID string
}

func newStartDaemonEnv(t *testing.T) *startDaemonEnv {
	t.Helper()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	projectID, _ := newInvariantTestProject(t, ctx, repo, true)

	auth.SetUserJWT("test-user", "user-jwt")
	t.Cleanup(func() { auth.SetUserJWT("test-user", "") })

	temporal := &inputRecordingTemporal{}
	router := &wakeRecordingRouter{fakeDaemonRouter: &fakeDaemonRouter{}}
	return &startDaemonEnv{
		svc: &ChatService{
			database: repo, threads: threads.NewService(repo), tempClient: temporal,
			runs: runs.NewService(repo, temporal, nil), daemonRouter: router,
		},
		repo: repo, ctx: ctx, router: router, temporal: temporal, projectID: projectID,
	}
}

func (e *startDaemonEnv) daemon(t *testing.T, userID string) string {
	t.Helper()
	id := uuid.NewString()
	require.NoError(t, e.repo.UpsertDaemon(context.Background(), &db.Daemon{ID: id, UserID: userID}))
	return id
}

func (e *startDaemonEnv) start(t *testing.T, mutate func(*reliantv1.StartChatRequest)) (*connect.Response[reliantv1.StartChatResponse], error) {
	t.Helper()
	req := &reliantv1.StartChatRequest{
		ProjectId: e.projectID,
		Workflow:  "builtin://agent",
		Messages: []*reliantv1.InputMessage{{
			Role: reliantv1.MessageRole_MESSAGE_ROLE_USER, Content: "hello",
		}},
		WorkflowParams: map[string]*structpb.Value{
			"model": mustStructValue(t, map[string]interface{}{"id": "mock"}),
		},
	}
	if mutate != nil {
		mutate(req)
	}
	return e.svc.StartChat(e.ctx, connect.NewRequest(req))
}

func TestStartChatWithDaemonIDPinsTheChatAndTheRun(t *testing.T) {
	e := newStartDaemonEnv(t)
	chosen := e.daemon(t, "test-user")

	resp, err := e.start(t, func(r *reliantv1.StartChatRequest) { r.DaemonId = strPtr(chosen) })
	require.NoError(t, err)

	chat, err := e.repo.GetChat(e.ctx, resp.Msg.GetChat().GetId())
	require.NoError(t, err)
	require.NotNil(t, chat.ActiveDaemonID)
	assert.Equal(t, chosen, *chat.ActiveDaemonID)

	require.Len(t, e.temporal.inputs, 1)
	assert.Equal(t, chosen, e.temporal.inputs[0].Inputs["session_daemon_id"])
}

func TestStartChatWithoutDaemonIDLeavesSelectionToTheRuntime(t *testing.T) {
	e := newStartDaemonEnv(t)

	resp, err := e.start(t, nil)
	require.NoError(t, err)

	chat, err := e.repo.GetChat(e.ctx, resp.Msg.GetChat().GetId())
	require.NoError(t, err)
	assert.Nil(t, chat.ActiveDaemonID)
	require.Len(t, e.temporal.inputs, 1)
	assert.NotContains(t, e.temporal.inputs[0].Inputs, "session_daemon_id")
}

func TestStartChatRejectsADaemonTheCallerDoesNotOwn(t *testing.T) {
	e := newStartDaemonEnv(t)
	foreign := e.daemon(t, "someone-else")

	for name, id := range map[string]string{"another user's": foreign, "nonexistent": uuid.NewString()} {
		t.Run(name, func(t *testing.T) {
			_, err := e.start(t, func(r *reliantv1.StartChatRequest) { r.DaemonId = strPtr(id) })
			require.Error(t, err)
			assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
			assert.Empty(t, e.temporal.inputs, "a rejected start must launch nothing")
			assert.Empty(t, e.router.selectors, "a rejected start must not wake anything")
		})
	}
}

func TestStartChatRejectsADaemonTheProjectIsNotInstalledOn(t *testing.T) {
	e := newStartDaemonEnv(t)
	installedOn := e.daemon(t, "test-user")
	elsewhere := e.daemon(t, "test-user")
	require.NoError(t, e.repo.UpsertProjectDaemon(context.Background(), e.projectID, installedOn, "/work/project", nil))

	_, err := e.start(t, func(r *reliantv1.StartChatRequest) { r.DaemonId = strPtr(elsewhere) })
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Empty(t, e.temporal.inputs)

	_, err = e.start(t, func(r *reliantv1.StartChatRequest) { r.DaemonId = strPtr(installedOn) })
	require.NoError(t, err)
}

func TestStartChatBranchRejectsADaemonThatConflictsWithItsWorktree(t *testing.T) {
	e := newStartDaemonEnv(t)
	pinned := e.daemon(t, "test-user")
	other := e.daemon(t, "test-user")

	_, fx := setupAbsorbFixture(t, e.repo, "test-user", db.Pending())
	require.NoError(t, e.repo.UpdateChatActiveDaemon(e.ctx, fx.chatID, &pinned))

	_, err := e.start(t, func(r *reliantv1.StartChatRequest) {
		r.ChatId = &fx.chatID
		r.ProjectId = ""
		r.DaemonId = strPtr(other)
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "pinned daemon")
	assert.Empty(t, e.router.selectors)
	assert.Empty(t, e.temporal.inputs)

	chat, err := e.repo.GetChat(e.ctx, fx.chatID)
	require.NoError(t, err)
	assert.Equal(t, pinned, *chat.ActiveDaemonID, "the branch keeps its worktree's daemon")
}

func TestStartChatBranchAcceptsTheDaemonItIsAlreadyPinnedTo(t *testing.T) {
	e := newStartDaemonEnv(t)
	pinned := e.daemon(t, "test-user")
	_, fx := setupAbsorbFixture(t, e.repo, "test-user", db.Pending())
	require.NoError(t, e.repo.UpdateChatActiveDaemon(e.ctx, fx.chatID, &pinned))

	_, err := e.start(t, func(r *reliantv1.StartChatRequest) {
		r.ChatId = &fx.chatID
		r.ProjectId = ""
		r.DaemonId = strPtr(pinned)
	})
	if err != nil {
		assert.NotEqual(t, connect.CodeInvalidArgument, connect.CodeOf(err), "a matching daemon_id is not a conflict: %v", err)
	}
	require.Len(t, e.router.selectors, 1)
	assert.Equal(t, pinned, e.router.selectors[0].ID)
}

func TestStartChatWakesTheChosenDaemon(t *testing.T) {
	e := newStartDaemonEnv(t)
	chosen := e.daemon(t, "test-user")

	_, err := e.start(t, func(r *reliantv1.StartChatRequest) { r.DaemonId = strPtr(chosen) })
	require.NoError(t, err)

	require.Len(t, e.router.selectors, 1, "exactly one attended wake")
	require.NotNil(t, e.router.selectors[0], "a chosen daemon must be woken by id, not by default resolution")
	assert.Equal(t, chosen, e.router.selectors[0].ID)
	assert.Equal(t, "user-jwt", e.router.jwts[0])
}

func TestStartChatWithoutDaemonIDWakesByDefaultResolution(t *testing.T) {
	e := newStartDaemonEnv(t)

	_, err := e.start(t, nil)
	require.NoError(t, err)

	require.Len(t, e.router.selectors, 1)
	assert.Nil(t, e.router.selectors[0])
}
