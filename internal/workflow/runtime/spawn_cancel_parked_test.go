// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

// CANCELLING A PARENT PARKED ON ITS BACKGROUND SPAWNS MUST RELEASE IT.
//
// The existing spawn-cancel coverage (spawn_cancel_loop_test.go) sets the
// cancel flag BEFORE the loop starts and asserts the loop takes no turns. That
// pins the step-boundary check, which is only reachable by a goroutine that is
// still going around the loop.
//
// A thread that has fanned work out to background spawns is NOT going around
// the loop. It parks in awaitLiveDetachedSpawns (loop_executor.go) on a
// workflow.Await whose predicate watches exactly three things: a child
// completing, a mailbox doorbell, and the live-spawn set emptying.
// Cancellation is not among them, so the flag is set and nothing observes it —
// the boundary check that would notice sits on the far side of a wait that the
// cancel itself never ends.
//
// These tests drive that wait site directly with the cancel flag set, which is
// the state a user creates by clicking cancel on a parent whose sub-agents are
// still running.

type SpawnCancelParkedSuite struct {
	suite.Suite
	testsuite.WorkflowTestSuite
}

func TestSpawnCancelParked(t *testing.T) {
	suite.Run(t, new(SpawnCancelParkedSuite))
}

// cancellableParkedWorkflow drives awaitLiveDetachedSpawns with a real
// PauseController whose Cancelled flag is fed by the REAL cancel_thread signal
// handler, exactly as DynamicWorkflow wires it: setupCancelThreadHandler
// writes the map, makeThreadPauseCtrl closes over it.
//
// Closures over tracker and the cancelled map, not workflow arguments: an
// argument would be round-tripped through the DataConverter and arrive as a
// copy, so mutations from the signal handler would be invisible here.
func cancellableParkedWorkflow(thread string, tracker *ChildWorkflowTracker) func(workflow.Context) (bool, error) {
	return func(ctx workflow.Context) (bool, error) {
		cancelled := map[string]bool{}
		setupCancelThreadHandler(ctx, cancelled, "test-workflow")

		e := &InlineLoopExecutor{
			ctx:          ctx,
			loopID:       "agent_loop",
			logger:       workflow.GetLogger(ctx),
			childTracker: tracker,
			execContext:  &ExecutionContext{Thread: thread},
			pauseCtrl: &PauseController{
				Cancelled: func() bool { return cancelled[thread] },
			},
		}
		return e.awaitLiveDetachedSpawns(), nil
	}
}

// TestParkedParent_CancelReleasesTheWait is the regression.
//
// The child NEVER completes and no mailbox doorbell is ever rung, so the only
// thing that can end this wait is the cancellation itself. Before the fix the
// workflow blocks until the test environment's deadline: the flag is set, the
// parked goroutine is not watching it, and the step-boundary check that would
// see it is unreachable.
func (s *SpawnCancelParkedSuite) TestParkedParent_CancelReleasesTheWait() {
	env := s.NewTestWorkflowEnvironment()
	tracker := &ChildWorkflowTracker{}
	tracker.registerDetachedSpawn(&detachedSpawnRecord{
		ToolCallID: "tc1", ParentThread: "thread-a", ChildThread: "child-1",
	})

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(CancelThreadSignalName, CancelThreadSignal{Thread: "thread-a"})
	}, 5*time.Second)

	env.ExecuteWorkflow(cancellableParkedWorkflow("thread-a", tracker))

	s.True(env.IsWorkflowCompleted(),
		"a cancelled thread parked on its background spawns must be released; it stayed parked, "+
			"which is why clicking cancel on an async spawn did nothing")
	s.NoError(env.GetWorkflowError())

	var reenter bool
	s.NoError(env.GetWorkflowResult(&reenter))
	s.False(reenter,
		"a cancelled thread must not re-enter the loop to take another turn — it must fall "+
			"through to the boundary check that stops it")
}

// TestParkedParent_CancelForOtherThreadDoesNotWake is the blast-radius
// assertion, and it is the known failure mode in this area: a cancel aimed at
// one thread must not release a sibling's wait.
//
// Only the sibling's own child completing releases this thread, which is what
// proves the foreign cancel did not.
func (s *SpawnCancelParkedSuite) TestParkedParent_CancelForOtherThreadDoesNotWake() {
	env := s.NewTestWorkflowEnvironment()
	tracker := &ChildWorkflowTracker{}
	tracker.registerDetachedSpawn(&detachedSpawnRecord{
		ToolCallID: "tc1", ParentThread: "thread-a", ChildThread: "child-1",
	})

	// A cancel for someone else must leave thread-a parked...
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(CancelThreadSignalName, CancelThreadSignal{Thread: "thread-b"})
	}, 5*time.Second)
	// ...and only this may release it.
	env.RegisterDelayedCallback(func() {
		tracker.completeDetachedSpawn("tc1", "thread-a")
	}, 10*time.Second)

	env.ExecuteWorkflow(cancellableParkedWorkflow("thread-a", tracker))

	s.True(env.IsWorkflowCompleted())
	s.NoError(env.GetWorkflowError())

	var reenter bool
	s.NoError(env.GetWorkflowResult(&reenter))
	s.True(reenter,
		"another thread's cancel must not release this thread's wait; it must be the child "+
			"completion that wakes it, and that means re-entering the loop")
}

// A spawn is cancelled from the UI by its TOOL CALL id — the only id a user
// has. prepareSpawnInline widens the thread's Cancelled check to match either
// id, so a tool-call-addressed cancel must release the wait too.
func (s *SpawnCancelParkedSuite) TestParkedParent_CancelByToolCallIDReleasesTheWait() {
	env := s.NewTestWorkflowEnvironment()
	tracker := &ChildWorkflowTracker{}
	tracker.registerDetachedSpawn(&detachedSpawnRecord{
		ToolCallID: "tc1", ParentThread: "thread-a", ChildThread: "child-1",
	})

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(CancelThreadSignalName, CancelThreadSignal{ToolCallID: "tc-parent"})
	}, 5*time.Second)

	env.ExecuteWorkflow(func(ctx workflow.Context) (bool, error) {
		cancelled := map[string]bool{}
		setupCancelThreadHandler(ctx, cancelled, "test-workflow")

		// Mirror prepareSpawnInline: this thread answers to both its thread id
		// and the tool call that created it.
		e := &InlineLoopExecutor{
			ctx:          ctx,
			loopID:       "agent_loop",
			logger:       workflow.GetLogger(ctx),
			childTracker: tracker,
			execContext:  &ExecutionContext{Thread: "thread-a"},
			pauseCtrl: &PauseController{
				Cancelled: func() bool { return cancelled["thread-a"] || cancelled["tc-parent"] },
			},
		}
		return e.awaitLiveDetachedSpawns(), nil
	})

	s.True(env.IsWorkflowCompleted(),
		"a cancel naming the spawn's tool call id must release the parked wait")
	s.NoError(env.GetWorkflowError())

	var reenter bool
	s.NoError(env.GetWorkflowResult(&reenter))
	s.False(reenter, "a cancelled thread must not re-enter the loop")
}
