// Copyright (c) 2025 Reliant Labs
package temporal

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/client"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/workflow/threadcancel"
)

// stopperTemporalClient records signals. It embeds client.Client so any other
// SDK call panics rather than silently succeeding — the original version of
// this cancel path called TerminateWorkflow, which can never work for a spawn,
// and a permissive fake could not catch that.
type stopperTemporalClient struct {
	client.Client

	signals   []recordedStopSignal
	signalErr error
}

type recordedStopSignal struct {
	workflowID string
	name       string
	arg        interface{}
}

func (c *stopperTemporalClient) SignalWorkflow(
	_ context.Context, workflowID, _ string, signalName string, arg interface{},
) error {
	c.signals = append(c.signals, recordedStopSignal{workflowID: workflowID, name: signalName, arg: arg})
	return c.signalErr
}

// recordingReconciler captures the compare-and-swap attempts.
type recordingReconciler struct {
	attempts []struct {
		id             string
		to, expectFrom core.WorkflowStatus
	}
	swapOn *core.WorkflowStatus
	err    error
}

func (r *recordingReconciler) CompareAndSwapWorkflowStatus(
	_ context.Context, id string, newStatus, expectedStatus core.WorkflowStatus,
) (bool, error) {
	r.attempts = append(r.attempts, struct {
		id             string
		to, expectFrom core.WorkflowStatus
	}{id, newStatus, expectedStatus})
	if r.err != nil {
		return false, r.err
	}
	if r.swapOn != nil && *r.swapOn == expectedStatus {
		return true, nil
	}
	return false, nil
}

// The signal must be named "cancel_thread" and carry BOTH ids. The name is
// load-bearing history: every live workflow execution's signal channel is
// already bound to that exact string, and a signal to a name nothing listens
// on is not an error — it is a spawn that keeps running while every surface
// reports it cancelled.
func TestSpawnStopper_SignalsCancelThreadWithBothIDs(t *testing.T) {
	t.Parallel()
	tc := &stopperTemporalClient{}
	stopper := NewSpawnStopper(tc, nil, nil)

	require.NoError(t, stopper.StopSpawn(context.Background(), "chat-1", SpawnRef{
		ThreadID: "child-thread-1", WorkflowID: "child-wf-1", ToolCallID: "toolu_abc"}))

	require.Len(t, tc.signals, 1)
	require.Equal(t, "cancel_thread", tc.signals[0].name)
	require.Equal(t, "chat-1", tc.signals[0].workflowID,
		"with no lookup the chat id is the workflow's default identity")

	sig, ok := tc.signals[0].arg.(threadcancel.Signal)
	require.True(t, ok, "payload must be a threadcancel.Signal, got %T", tc.signals[0].arg)
	require.Equal(t, "child-thread-1", sig.Thread)
	require.Equal(t, "toolu_abc", sig.ToolCallID,
		"the two ids are not derivable from each other, so a caller that knows both sends both")
}

// chats.workflow_id is authoritative: a run restarted after the history limit
// carries an id that is not the chat id, and signalling the chat id would
// silently reach nothing.
func TestSpawnStopper_AddressesResolvedWorkflowID(t *testing.T) {
	t.Parallel()
	tc := &stopperTemporalClient{}
	stopper := NewSpawnStopper(tc, func(context.Context, string) (string, bool) {
		return "restarted-workflow-id", true
	}, nil)

	require.NoError(t, stopper.StopSpawn(context.Background(), "chat-1", SpawnRef{
		ThreadID: "child-thread-1", WorkflowID: "child-wf-1", ToolCallID: "toolu_abc"}))

	require.Len(t, tc.signals, 1)
	require.Equal(t, "restarted-workflow-id", tc.signals[0].workflowID)
}

// A spawn whose signal was delivered has its workflows row reconciled, so a
// reload agrees with what was asked for. CAS from ACTIVE first, then PAUSED —
// an explicit stop overrides a pause.
func TestSpawnStopper_ReconcilesWorkflowRow(t *testing.T) {
	t.Parallel()
	active := core.Active()
	rec := &recordingReconciler{swapOn: &active}
	stopper := NewSpawnStopper(&stopperTemporalClient{}, nil, rec)

	require.NoError(t, stopper.StopSpawn(context.Background(), "chat-1", SpawnRef{
		ThreadID: "child-thread-1", WorkflowID: "child-wf-1", ToolCallID: "toolu_abc"}))

	require.Len(t, rec.attempts, 1, "the ACTIVE swap succeeded, so PAUSED must not be tried")
	require.Equal(t, "child-wf-1", rec.attempts[0].id,
		"the reconcile must CAS the WORKFLOW row id, not the thread id")
	require.Equal(t, core.Cancelled(), rec.attempts[0].to)
	require.Equal(t, core.Active(), rec.attempts[0].expectFrom)
}

// The resumed-spawn case, where the two ids diverge: the signal must name the
// THREAD (the running loop) while the reconcile targets the WORKFLOW row for
// this resumption. Reconciling the thread id instead hits the original run's
// row — long since completed — so nothing swaps from active and the live row
// stays active after a stop that reported success.
func TestSpawnStopper_ResumedSpawn_SignalsThreadButReconcilesWorkflowRow(t *testing.T) {
	t.Parallel()
	tc := &stopperTemporalClient{}
	active := core.Active()
	rec := &recordingReconciler{swapOn: &active}
	stopper := NewSpawnStopper(tc, nil, rec)

	// A resumption keeps the original thread and gets a fresh workflow id
	// derived from the new tool call.
	require.NoError(t, stopper.StopSpawn(context.Background(), "chat-1", SpawnRef{
		ThreadID:   "original-thread",
		WorkflowID: "wf-for-resumption-3",
		ToolCallID: "toolu_resume3",
	}))

	require.Len(t, tc.signals, 1)
	sig, ok := tc.signals[0].arg.(threadcancel.Signal)
	require.True(t, ok)
	require.Equal(t, "original-thread", sig.Thread,
		"the signal addresses the loop, which still runs under the original thread id")
	require.Equal(t, "toolu_resume3", sig.ToolCallID)

	require.Len(t, rec.attempts, 1)
	require.Equal(t, "wf-for-resumption-3", rec.attempts[0].id,
		"the reconcile must target THIS resumption's workflow row, not the thread id")
	require.Equal(t, core.Cancelled(), rec.attempts[0].to)
}

func TestSpawnStopper_ReconcileFallsBackToPaused(t *testing.T) {
	t.Parallel()
	paused := core.Paused()
	rec := &recordingReconciler{swapOn: &paused}
	stopper := NewSpawnStopper(&stopperTemporalClient{}, nil, rec)

	require.NoError(t, stopper.StopSpawn(context.Background(), "chat-1", SpawnRef{
		ThreadID: "child-thread-1", WorkflowID: "child-wf-1", ToolCallID: "toolu_abc"}))

	require.Len(t, rec.attempts, 2)
	require.Equal(t, core.Active(), rec.attempts[0].expectFrom)
	require.Equal(t, core.Paused(), rec.attempts[1].expectFrom)
}

// An undelivered signal means the spawn is still running, so the error must
// propagate and NOTHING may be recorded as cancelled. A cancel that claims it
// worked and didn't is worse than one that admits it couldn't.
func TestSpawnStopper_SignalFails_DoesNotReconcile(t *testing.T) {
	t.Parallel()
	rec := &recordingReconciler{}
	stopper := NewSpawnStopper(
		&stopperTemporalClient{signalErr: errors.New("workflow not found")}, nil, rec)

	err := stopper.StopSpawn(context.Background(), "chat-1", SpawnRef{
		ThreadID: "child-thread-1", WorkflowID: "child-wf-1", ToolCallID: "toolu_abc"})
	require.Error(t, err)
	require.Empty(t, rec.attempts, "a spawn that is still running must not be recorded as cancelled")
}

// No Temporal client is the daemon runtime's situation. It must report that it
// cannot deliver rather than returning success.
func TestSpawnStopper_NilClient_Errors(t *testing.T) {
	t.Parallel()
	stopper := NewSpawnStopper(nil, nil, nil)
	require.Error(t, stopper.StopSpawn(context.Background(), "chat-1", SpawnRef{
		ThreadID: "child-thread-1", WorkflowID: "child-wf-1", ToolCallID: "toolu_abc"}))
}

func TestSpawnStopper_RequiresAnIdentifier(t *testing.T) {
	t.Parallel()
	stopper := NewSpawnStopper(&stopperTemporalClient{}, nil, nil)
	require.Error(t, stopper.StopSpawn(context.Background(), "chat-1", SpawnRef{}))
	require.Error(t, stopper.StopSpawn(context.Background(), "", SpawnRef{ThreadID: "child-thread-1"}))
}
