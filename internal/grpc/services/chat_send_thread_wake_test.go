// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/converter"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/runs"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/threadwake"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wakeTestTemporalClient answers the liveness probe SendMessage makes and
// records every signal, so a test can assert on the doorbell without a real
// Temporal server. Embeds absorbTestTemporalClient for the Describe/Execute
// behavior and adds signal recording.
type wakeTestTemporalClient struct {
	absorbTestTemporalClient
	signals []recordedSignal
}

func (c *wakeTestTemporalClient) SignalWorkflow(
	_ context.Context, workflowID, _ string, signalName string, arg interface{},
) error {
	c.signals = append(c.signals, recordedSignal{workflowID: workflowID, name: signalName, arg: arg})
	return nil
}

// Resuming a paused run goes through this; the runs service calls it and the
// test only needs it not to fail.
func (c *wakeTestTemporalClient) ResetWorkflowExecution(
	_ context.Context, _ *workflowservice.ResetWorkflowExecutionRequest,
) (*workflowservice.ResetWorkflowExecutionResponse, error) {
	return &workflowservice.ResetWorkflowExecutionResponse{RunId: "run-reset"}, nil
}

// SendMessage queries the running workflow's current inputs to decide whether
// params actually changed. Returning an error is the honest answer for a fake
// with no workflow behind it, and the caller treats that as "cannot tell" —
// which keeps this test focused on the doorbell rather than on param diffing.
func (c *wakeTestTemporalClient) QueryWorkflow(
	_ context.Context, _, _, _ string, _ ...interface{},
) (converter.EncodedValue, error) {
	return nil, assertNotFound{}
}

// succeedingPauseController resumes without touching Temporal, so the paused
// branch reaches the point this test is about: what SendMessage does AFTER a
// successful resume.
type succeedingPauseController struct{}

func (succeedingPauseController) PauseWorkflow(_ context.Context, _, _, _ string) error { return nil }
func (succeedingPauseController) ResumeWorkflow(_ context.Context, _, _ string) error   { return nil }
func (succeedingPauseController) ResumeInterruptedWorkflow(_ context.Context, _, _ string) (string, error) {
	return "run-resumed", nil
}
func (succeedingPauseController) SignalWithRecovery(_ context.Context, _, _ string, _ interface{}) error {
	return nil
}

// threadWakeSignals returns just the thread-wake signals, ignoring unrelated
// ones (update_workflow_state) the send path may also emit.
func threadWakeSignals(signals []recordedSignal) []recordedSignal {
	var out []recordedSignal
	for _, s := range signals {
		if s.name == v2.ThreadWakeSignalName {
			out = append(out, s)
		}
	}
	return out
}

// TestSendMessage_RunningWorkflowWakesTargetThread is the regression for chat
// 7da3935c-97ec-4843-af78-c3807fe336cb.
//
// "The workflow is running" does not mean "the loop is taking turns". A thread
// that has fanned work out to background spawns is parked in
// awaitLiveDetachedSpawns, and that gate cannot observe a row appearing in
// `messages` — a user message is not a mailbox row, so nothing is queued for
// the drain to find either. Before this signal the root thread had no wake
// reason that could describe a user message at all, so it stayed parked with
// the message already durable in the database, and the chat read as "blocked
// on the spawned chat".
//
// The signal must name the recipient thread and address the CHAT's workflow: a
// spawn has no Temporal execution of its own, so one execution drives every
// thread and the payload is what selects which gate wakes.
func TestSendMessage_RunningWorkflowWakesTargetThread(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Active())
	temporal := &wakeTestTemporalClient{
		absorbTestTemporalClient: absorbTestTemporalClient{
			exists: true, status: enums.WORKFLOW_EXECUTION_STATUS_RUNNING,
		},
	}
	service := &ChatService{
		database:   repo,
		tempClient: temporal,
		runs:       runs.NewService(repo, temporal, nil),
	}

	_, err := service.SendMessage(ctx, sendMessageRequest(t, fx.chatID, "stop, that is the wrong file"))
	require.NoError(t, err)

	wakes := threadWakeSignals(temporal.signals)
	require.Len(t, wakes, 1,
		"a user message to a live thread must ring the wake doorbell; without it a thread "+
			"parked on its sub-agents never takes another turn and never sees the message")
	assert.Equal(t, fx.chatID, wakes[0].workflowID,
		"a spawn has no execution of its own — the chat's workflow drives every thread")

	sig, ok := wakes[0].arg.(v2.ThreadWakeSignal)
	require.True(t, ok, "signal payload must be a ThreadWakeSignal")
	assert.Equal(t, fx.rootThreadID, sig.Thread,
		"the payload names which thread's gate should wake")
	assert.Equal(t, threadwake.ReasonUserMessage, sig.Reason,
		"the reason distinguishes this from a mailbox row, which has a different delivery path")
}

// A paused run takes the resume branch, and resuming is NOT sufficient on its
// own: broadcastResume clears the pause gate, but a thread parked in
// awaitLiveDetachedSpawns is blocked on a different Await whose predicate never
// looks at the pause epoch. That is precisely what happened on chat 7da3935c —
// the resume woke the spawn's loop and left the root thread parked.
func TestSendMessage_PausedWorkflowWakesTargetThreadOnResume(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Paused())
	temporal := &wakeTestTemporalClient{
		absorbTestTemporalClient: absorbTestTemporalClient{
			exists: true, status: enums.WORKFLOW_EXECUTION_STATUS_RUNNING,
		},
	}
	service := &ChatService{
		database:   repo,
		tempClient: temporal,
		runs:       runs.NewService(repo, temporal, succeedingPauseController{}),
	}

	_, err := service.SendMessage(ctx, sendMessageRequest(t, fx.chatID, "actually, stop"))
	require.NoError(t, err)

	wakes := threadWakeSignals(temporal.signals)
	require.Len(t, wakes, 1,
		"resuming a paused run does not release a thread parked on its sub-agents — "+
			"that is a different Await, and it needs its own wake")
	sig, ok := wakes[0].arg.(v2.ThreadWakeSignal)
	require.True(t, ok, "signal payload must be a ThreadWakeSignal")
	assert.Equal(t, fx.rootThreadID, sig.Thread)
	assert.Equal(t, threadwake.ReasonUserMessage, sig.Reason)
}

// A send that starts a NEW run must not ring the doorbell. There is no parked
// loop to wake — the run is about to begin and its first call_llm reads
// history and drains the mailbox as a matter of course. Signalling here would
// address a workflow that does not exist yet.
//
// This is the boundary of the fix: the wake belongs only on the paths where a
// run is already live and may be parked.
func TestSendMessage_NewRunDoesNotWake(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Completed())
	temporal := &wakeTestTemporalClient{
		absorbTestTemporalClient: absorbTestTemporalClient{
			exists: true, status: enums.WORKFLOW_EXECUTION_STATUS_COMPLETED,
		},
	}
	service := &ChatService{
		database:   repo,
		tempClient: temporal,
		runs:       runs.NewService(repo, temporal, nil),
	}

	_, err := service.SendMessage(ctx, sendMessageRequest(t, fx.chatID, "start something new"))
	require.NoError(t, err)

	assert.Empty(t, threadWakeSignals(temporal.signals),
		"a fresh run has no parked loop to wake; its first call_llm reads history anyway")
}

// The wake is best-effort: whatever prompted it is already durable, so a
// signal that cannot be delivered costs a late pickup, never lost input.
// Failing the RPC would report failure for a message that IS saved.
func TestSendMessage_WakeFailureStillSavesMessage(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Active())
	temporal := &failingWakeTemporalClient{
		wakeTestTemporalClient: wakeTestTemporalClient{
			absorbTestTemporalClient: absorbTestTemporalClient{
				exists: true, status: enums.WORKFLOW_EXECUTION_STATUS_RUNNING,
			},
		},
	}
	service := &ChatService{
		database:   repo,
		tempClient: temporal,
		runs:       runs.NewService(repo, temporal, nil),
	}

	resp, err := service.SendMessage(ctx, sendMessageRequest(t, fx.chatID, "keep this"))
	require.NoError(t, err, "a doorbell that cannot be rung must not fail the send")
	require.NotEmpty(t, resp.Msg.MessageId)

	assert.Contains(t, transcriptBodies(t, ctx, repo, fx.chatID), "keep this",
		"the message must be durable even when the wake failed")
}

type failingWakeTemporalClient struct {
	wakeTestTemporalClient
}

func (c *failingWakeTemporalClient) SignalWorkflow(
	_ context.Context, _, _ string, _ string, _ interface{},
) error {
	return connect.NewError(connect.CodeUnavailable, assertNotFound{})
}
