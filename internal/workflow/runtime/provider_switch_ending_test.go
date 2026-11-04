// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/workflow"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/threadwake"
)

// A whole-chat revival looks up the chat's stopped agents durably. When the
// run is already ending, that lookup is still in flight as the end-of-run
// check starts, and what it finds is only known after the check has already
// snapshotted its candidates. A sibling discovered that way must be resumed
// or carried by the successor — never dropped because the run was ending.

// endingRunWithDurableSibling runs a root with nothing to do while a stopped
// sibling sits in the database, and rings `ring` at the moment the root writes
// its own "completed" status (the run has left its graph and is ending).
func endingRunWithDurableSibling(t *testing.T, chatID string, ring func(e *subAgentResumeEnv)) (successor WorkflowInput, e *subAgentResumeEnv, completedNormally bool) {
	t.Helper()
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.SetTestTimeout(20 * time.Second)
	e = newSubAgentResumeEnv(t, env, false)

	input := spawnE2EWorkflowInput(chatID)
	input.ExecContext.ThreadMode = model.ThreadModeInherit
	e.durableStopped["sibling"] = stoppedBefore(chatID, "sibling", "tc-sibling", 1)
	e.parentToolCalls[1] = func(string) []map[string]interface{} { return nil }
	e.onRootFinishing = func(string) { ring(e) }
	e.mu.Lock()
	e.parentThread = input.ExecContext.Thread
	e.mu.Unlock()

	env.ExecuteWorkflow(DynamicWorkflow, input)
	require.True(t, env.IsWorkflowCompleted())

	var contErr *workflow.ContinueAsNewError
	if !errors.As(env.GetWorkflowError(), &contErr) {
		require.NoError(t, env.GetWorkflowError())
		return WorkflowInput{}, e, true
	}
	require.NoError(t, converter.GetDefaultDataConverter().FromPayloads(contErr.Input, &successor))
	return successor, e, false
}

func requireSiblingKept(t *testing.T, successor WorkflowInput) {
	t.Helper()
	require.NotNil(t, successor.Resume, "the successor must resume the chat, not start it over and forget the sibling")
	var carried []string
	for _, h := range successor.Resume.ResumeStopped {
		carried = append(carried, h.ChildThread)
	}
	for _, h := range successor.Resume.StoppedSpawns {
		carried = append(carried, h.ChildThread)
	}
	require.Contains(t, carried, "sibling", "the revived sibling was dropped as the run ended: resume=%v remembered=%v",
		successor.Resume.ResumeStopped, successor.Resume.StoppedSpawns)
}

// A capacity wake that resumes everything (the Resume button) lands while the
// root is ending. Nobody woke the root, so without the sibling the run would
// simply complete.
func TestEndingRunKeepsSiblingRevivedByAllStoppedCapacityWake(t *testing.T) {
	t.Parallel()
	successor, e, completed := endingRunWithDurableSibling(t, "chat-ending-capacity-wake", func(e *subAgentResumeEnv) {
		e.env.SignalWorkflow(CapacityWakeSignalName, CapacityWakeSignal{AllStopped: true})
	})
	require.False(t, completed, "STRANDED: the run completed and dropped the sibling its own lookup revived")
	requireSiblingKept(t, successor)
	require.Zero(t, countTurns(e, "sibling"), "the ending run must not start the sibling itself")
}

// A user message to the root while it ends also rings the whole-chat revival.
// The root's own late wake already forces a successor, and that successor must
// still carry the sibling the revival found.
func TestEndingRunKeepsSiblingRevivedByUserMessage(t *testing.T) {
	t.Parallel()
	successor, e, completed := endingRunWithDurableSibling(t, "chat-ending-user-message", func(e *subAgentResumeEnv) {
		e.env.SignalWorkflow(ThreadWakeSignalName, ThreadWakeSignal{
			Thread: "thread-chat-ending-user-message", Reason: threadwake.ReasonUserMessage, FromUser: true,
		})
	})
	require.False(t, completed, "STRANDED: the run completed with a user message and a stopped sibling waiting")
	requireSiblingKept(t, successor)
	require.Zero(t, countTurns(e, "sibling"), "the ending run must not start the sibling itself")
}

// Control: with no revival at the end, the run completes and a sibling that
// stayed stopped is left alone. Pins that the tests above discriminate on the
// revival, not on the sibling merely existing in the database.
func TestEndingRunWithoutRevivalCompletesAndLeavesStoppedSiblingAlone(t *testing.T) {
	t.Parallel()
	_, e, completed := endingRunWithDurableSibling(t, "chat-ending-no-revival", func(*subAgentResumeEnv) {})
	require.True(t, completed, "a run nobody woke must complete")
	require.Zero(t, countTurns(e, "sibling"))
}

func countTurns(e *subAgentResumeEnv, thread string) int {
	turns, _ := e.threadTurns(thread)
	return turns
}
