// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/workflow"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/threadwake"
)

// deliverWakeLikeTemporal puts a thread-wake signal on the workflow's signal
// channel exactly the way the SDK delivers one (HandleSignal → SendAsync): a
// receiver blocked on the channel is handed the value directly, and only
// counts it when it next runs.
func deliverWakeLikeTemporal(ctx workflow.Context, thread string) {
	ch := workflow.GetSignalChannel(ctx, ThreadWakeSignalName).(workflow.Channel)
	ch.SendAsync(ThreadWakeSignal{Thread: thread, Reason: threadwake.ReasonUserMessage})
}

type lateWakeDecision struct {
	CountedBeforeCheck int
	ContinuedAsNew     bool
	SuccessorResume    bool
}

// runLateWakeCheck runs the end-of-run check in a real workflow environment,
// with the real wake handler, after setup has arranged the thread's turns and
// wakes.
func runLateWakeCheck(t *testing.T, setup func(ctx workflow.Context, tracker *ChildWorkflowTracker)) lateWakeDecision {
	t.Helper()
	wf := func(ctx workflow.Context) (lateWakeDecision, error) {
		tracker := &ChildWorkflowTracker{}
		setupThreadWakeHandler(ctx, tracker, "wf")
		// Let the handler reach its blocking Receive, as it long has by the
		// time a run finishes.
		yieldToRunnableCoroutines(ctx)
		setup(ctx, tracker)

		var d lateWakeDecision
		d.CountedBeforeCheck = tracker.threadWakeCount("t")
		input := WorkflowInput{ChatID: "c", ExecContext: &ExecutionContext{Thread: "t"}, Inputs: map[string]interface{}{}}
		err := continueAsNewForLateWake(ctx, input, "t", tracker)
		var contErr *workflow.ContinueAsNewError
		if errors.As(err, &contErr) {
			d.ContinuedAsNew = true
			var next WorkflowInput
			require.NoError(t, converter.GetDefaultDataConverter().FromPayloads(contErr.Input, &next))
			d.SuccessorResume = next.Resume != nil
		}
		return d, nil
	}
	var s temporaltest.WorkflowTestSuite
	env := s.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(wf)
	env.ExecuteWorkflow(wf)
	require.True(t, env.IsWorkflowCompleted())
	var d lateWakeDecision
	require.NoError(t, env.GetWorkflowResult(&d))
	return d
}

// The retried final workflow task after UNHANDLED_COMMAND carries the signal
// in the same batch as the activity the run was awaiting. The SDK hands the
// signal to the blocked wake handler and resumes the root coroutine FIRST, so
// at the moment the end-of-run check runs the wake is delivered but not yet
// counted. The check must still see it — or the run completes a second time,
// and this time the server accepts.
func TestLateWakeCheck_SeesAWakeTheHandlerHasNotCountedYet(t *testing.T) {
	t.Parallel()
	d := runLateWakeCheck(t, func(ctx workflow.Context, tracker *ChildWorkflowTracker) {
		tracker.recordTurnStart("t")
		deliverWakeLikeTemporal(ctx, "t")
	})
	require.Zero(t, d.CountedBeforeCheck, "precondition: the handler has not run, so nothing is counted yet")
	require.True(t, d.ContinuedAsNew, "the check must let the handler count the wake and continue as a run for it")
	require.False(t, d.SuccessorResume, "the successor starts at graph entry")
}

// A wake that a turn has since seen is not a reason to go round again.
func TestLateWakeCheck_AWakeTheLastTurnSawCompletes(t *testing.T) {
	t.Parallel()
	d := runLateWakeCheck(t, func(ctx workflow.Context, tracker *ChildWorkflowTracker) {
		deliverWakeLikeTemporal(ctx, "t")
		yieldToRunnableCoroutines(ctx)
		tracker.recordTurnStart("t") // a turn began after it, and read it
	})
	require.False(t, d.ContinuedAsNew)
}

// A thread parked on its background spawns hands off to a successor that
// waits on the relaunched spawns before its first turn. If the thread was
// woken since its last turn — the wake and a ready handoff landing together,
// which the handoff wins — that successor must take its turn first, or the
// user's message waits for a sub-agent to finish.
func TestContinueAsNewWhileParked_AWokenThreadTakesItsTurnFirst(t *testing.T) {
	t.Parallel()
	type decision struct{ Woken, Unwoken bool }
	wf := func(ctx workflow.Context) (decision, error) {
		awaitFirst := func(tracker *ChildWorkflowTracker) bool {
			err := newContinueAsNewError(ctx, WorkflowInput{ChatID: "c", ExecContext: &ExecutionContext{Thread: "t"}}, "agent_loop", 3, tracker, true)
			var contErr *workflow.ContinueAsNewError
			require.True(t, errors.As(err, &contErr))
			var next WorkflowInput
			require.NoError(t, converter.GetDefaultDataConverter().FromPayloads(contErr.Input, &next))
			return next.Resume.AwaitSpawnsFirst
		}
		woken := &ChildWorkflowTracker{}
		woken.recordTurnStart("t")
		woken.notifyThreadWake("t")
		unwoken := &ChildWorkflowTracker{}
		unwoken.notifyThreadWake("t")
		unwoken.recordTurnStart("t")
		return decision{Woken: awaitFirst(woken), Unwoken: awaitFirst(unwoken)}, nil
	}
	var s temporaltest.WorkflowTestSuite
	env := s.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(wf)
	env.ExecuteWorkflow(wf)
	var d decision
	require.NoError(t, env.GetWorkflowResult(&d))
	require.False(t, d.Woken, "a woken thread's successor takes its turn before waiting on its spawns")
	require.True(t, d.Unwoken, "with nothing unread, the successor re-parks on its spawns as before")
}
