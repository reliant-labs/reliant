// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/runs"
	"github.com/reliant-labs/reliant/internal/threads"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// codePresenceHangingRouter is a daemon that never answers the greenfield
// code-presence probe — the remote, suspended or cold machine the request
// path must not wait on. Every other command behaves like the plain fake.
type codePresenceHangingRouter struct {
	*wakeRecordingRouter
	release chan struct{}

	mu     sync.Mutex
	probes int
}

func (r *codePresenceHangingRouter) SendDaemonCommand(ctx context.Context, userID string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	if commandType == "project.code_presence" {
		r.mu.Lock()
		r.probes++
		r.mu.Unlock()
		// Deliberately ignores ctx: a hung daemon is exactly the case where
		// nothing on the far side honours the caller's deadline.
		<-r.release
		return nil, errors.New("daemon never answered")
	}
	return r.wakeRecordingRouter.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
}

func (r *codePresenceHangingRouter) SendDaemonCommandToDaemon(ctx context.Context, userID, _ string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return r.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
}

func (r *codePresenceHangingRouter) probeCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.probes
}

// StartChat returns once the chat and its first message are committed and
// Temporal has accepted the run. Whether the project directory holds code is
// a question for the user's daemon, which may be remote, suspended or cold, so
// it is asked inside the run — never on the request path, where a daemon that
// does not answer used to hold the response for the probe's whole timeout.
func TestStartChatDoesNotWaitOnTheGreenfieldProbe(t *testing.T) {
	e := newStartDaemonEnv(t)
	router := &codePresenceHangingRouter{
		wakeRecordingRouter: e.router,
		release:             make(chan struct{}),
	}
	t.Cleanup(func() { close(router.release) })
	e.svc = &ChatService{
		database: e.repo, threads: threads.NewService(e.repo), tempClient: e.temporal,
		runs: runs.NewService(e.repo, e.temporal, nil), daemonRouter: router,
	}

	type outcome struct {
		resp *connect.Response[reliantv1.StartChatResponse]
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		resp, err := e.start(t, nil)
		done <- outcome{resp: resp, err: err}
	}()

	var got outcome
	select {
	case got = <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("StartChat is waiting on the daemon's project.code_presence probe; " +
			"the request path must return once the chat is committed and the run accepted")
	}
	require.NoError(t, got.err)
	require.NotNil(t, got.resp.Msg.GetChat())

	assert.Zero(t, router.probeCount(),
		"StartChat must not reach the daemon for the greenfield probe at all")
	require.Len(t, e.temporal.inputs, 1)
	assert.True(t, e.temporal.inputs[0].GreenfieldProbe,
		"a new chat's first run must be the one that asks")
}

// sendRecordingTemporal records the WorkflowInput of each root run SendMessage
// starts.
type sendRecordingTemporal struct {
	absorbTestTemporalClient
	mu     sync.Mutex
	inputs []v2.WorkflowInput
}

func (c *sendRecordingTemporal) ExecuteWorkflow(
	ctx context.Context, options client.StartWorkflowOptions, wf interface{}, args ...interface{},
) (client.WorkflowRun, error) {
	c.mu.Lock()
	for _, a := range args {
		if in, ok := a.(v2.WorkflowInput); ok {
			c.inputs = append(c.inputs, in)
		}
	}
	c.mu.Unlock()
	return c.absorbTestTemporalClient.ExecuteWorkflow(ctx, options, wf, args...)
}

func (c *sendRecordingTemporal) lastInput(t *testing.T) v2.WorkflowInput {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	require.NotEmpty(t, c.inputs, "SendMessage started no run")
	return c.inputs[len(c.inputs)-1]
}

// newFirstTurnSendFixture is a started chat whose run completed, with a daemon
// that never answers the code-presence probe.
func newFirstTurnSendFixture(t *testing.T) (*ChatService, *codePresenceHangingRouter, *sendRecordingTemporal, *db.Repo, context.Context, absorbFixture) {
	t.Helper()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Completed())

	router := &codePresenceHangingRouter{
		wakeRecordingRouter: &wakeRecordingRouter{fakeDaemonRouter: &fakeDaemonRouter{}},
		release:             make(chan struct{}),
	}
	t.Cleanup(func() { close(router.release) })
	temporal := &sendRecordingTemporal{absorbTestTemporalClient: absorbTestTemporalClient{
		exists: true, status: enums.WORKFLOW_EXECUTION_STATUS_COMPLETED,
	}}
	svc := &ChatService{
		database: repo, tempClient: temporal, daemonRouter: router,
		runs: runs.NewService(repo, temporal, nil),
	}
	return svc, router, temporal, repo, ctx, fx
}

// A started chat that already has a turn is past the point the guidance is
// about, so its next run does not probe.
func TestSendMessageLaterTurnDoesNotProbe(t *testing.T) {
	svc, router, temporal, repo, ctx, fx := newFirstTurnSendFixture(t)
	_, err := repo.SaveMessageToThread(ctx, fx.chatID, fx.rootThreadID,
		int32(reliantv1.MessageRole_MESSAGE_ROLE_USER), "an earlier turn", nil, nil, nil)
	require.NoError(t, err)

	_, err = svc.SendMessage(ctx, sendMessageRequest(t, fx.chatID, "and another thing"))
	require.NoError(t, err)

	assert.False(t, temporal.lastInput(t).GreenfieldProbe)
	assert.Zero(t, router.probeCount())
}

// A fresh run of a started chat that still has no messages is that chat's
// first turn. SendMessage gets there through the same in-run probe as
// StartChat, and does not wait on the daemon either.
func TestSendMessageFirstTurnDoesNotWaitOnTheGreenfieldProbe(t *testing.T) {
	svc, router, temporal, _, ctx, fx := newFirstTurnSendFixture(t)

	done := make(chan error, 1)
	go func() {
		_, err := svc.SendMessage(ctx, sendMessageRequest(t, fx.chatID, "build me a landing page"))
		done <- err
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("SendMessage is waiting on the daemon's project.code_presence probe")
	}
	assert.Zero(t, router.probeCount(),
		"SendMessage must not reach the daemon for the greenfield probe at all")
	assert.True(t, temporal.lastInput(t).GreenfieldProbe,
		"a chat with no messages yet is on its first turn, so its run asks")
}
