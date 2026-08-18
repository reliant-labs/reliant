// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

type threadInterruptIsolationResult struct {
	RootEpochAfterRootSignal    int64
	SpawnEpochAfterRootSignal   int64
	RootCtxCancelled            bool
	SpawnCtxCancelled           bool
	RootEpochAfterSpawnSignal   int64
	SpawnEpochAfterSpawnSignal  int64
	SpawnCtxCancelledAfterSpawn bool
}

func threadInterruptIsolationWorkflow(rootThread, spawnThread string) func(workflow.Context) (threadInterruptIsolationResult, error) {
	return func(ctx workflow.Context) (threadInterruptIsolationResult, error) {
		coordinator := NewThreadInterruptCoordinator(ctx, "wf-interrupt-isolation")
		rootInterrupt := coordinator.ForThread(rootThread)
		spawnInterrupt := coordinator.ForThread(spawnThread)

		rootActivityCtx := rootInterrupt.ActivityContext(ctx)
		spawnActivityCtx := spawnInterrupt.ActivityContext(ctx)

		startRootEpoch := rootInterrupt.Epoch()
		_ = workflow.Await(ctx, func() bool {
			return rootInterrupt.Epoch() > startRootEpoch
		})

		result := threadInterruptIsolationResult{
			RootEpochAfterRootSignal:  rootInterrupt.Epoch(),
			SpawnEpochAfterRootSignal: spawnInterrupt.Epoch(),
			RootCtxCancelled:          rootActivityCtx.Err() != nil,
			SpawnCtxCancelled:         spawnActivityCtx.Err() != nil,
		}

		startSpawnEpoch := spawnInterrupt.Epoch()
		_ = workflow.Await(ctx, func() bool {
			return spawnInterrupt.Epoch() > startSpawnEpoch
		})

		result.RootEpochAfterSpawnSignal = rootInterrupt.Epoch()
		result.SpawnEpochAfterSpawnSignal = spawnInterrupt.Epoch()
		result.SpawnCtxCancelledAfterSpawn = spawnActivityCtx.Err() != nil
		return result, nil
	}
}

func TestThreadInterrupt_RootAndInlineSpawnContextsAreIsolated(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()

	const rootThread = "thread-root"
	const spawnThread = "thread-spawn"

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(InterruptThreadSignalName, InterruptThreadSignal{ThreadID: rootThread, Epoch: 1})
	}, time.Second)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(InterruptThreadSignalName, InterruptThreadSignal{ThreadID: spawnThread, Epoch: 1})
	}, 2*time.Second)

	env.ExecuteWorkflow(threadInterruptIsolationWorkflow(rootThread, spawnThread))

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var result threadInterruptIsolationResult
	require.NoError(t, env.GetWorkflowResult(&result))
	require.Equal(t, int64(1), result.RootEpochAfterRootSignal)
	require.Equal(t, int64(0), result.SpawnEpochAfterRootSignal,
		"interrupting the root thread must not advance a spawned thread's epoch")
	require.True(t, result.RootCtxCancelled,
		"interrupting a thread must cancel that thread's local activity context")
	require.False(t, result.SpawnCtxCancelled,
		"interrupting the root thread must not cancel an inline spawn's local activity context")
	require.Equal(t, int64(1), result.RootEpochAfterSpawnSignal)
	require.Equal(t, int64(1), result.SpawnEpochAfterSpawnSignal)
	require.True(t, result.SpawnCtxCancelledAfterSpawn,
		"the spawned thread's own interrupt must cancel its local activity context")
}

func parkedInterruptWorkflow(thread string, tracker *ChildWorkflowTracker) func(workflow.Context) (bool, error) {
	return func(ctx workflow.Context) (bool, error) {
		coordinator := NewThreadInterruptCoordinator(ctx, "wf-interrupt-parked")
		e := &InlineLoopExecutor{
			ctx:          ctx,
			loopID:       "agent_loop",
			logger:       workflow.GetLogger(ctx),
			childTracker: tracker,
			execContext:  &ExecutionContext{Thread: thread},
		}
		e = e.WithThreadInterrupts(coordinator.ForThread(thread))
		return e.awaitLiveDetachedSpawns(), nil
	}
}

func TestThreadInterrupt_WakesParkedDetachedWaitAndForcesNextTurn(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	tracker := &ChildWorkflowTracker{}
	tracker.registerDetachedSpawn(&detachedSpawnRecord{
		ToolCallID: "tc1", ParentThread: "thread-a", ChildThread: "child-1",
	})

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(InterruptThreadSignalName, InterruptThreadSignal{ThreadID: "thread-a", Epoch: 7})
	}, 5*time.Second)

	env.ExecuteWorkflow(parkedInterruptWorkflow("thread-a", tracker))

	require.True(t, env.IsWorkflowCompleted(),
		"a thread interrupt must wake a loop parked on still-running detached spawns")
	require.NoError(t, env.GetWorkflowError())

	var forceNextTurn bool
	require.NoError(t, env.GetWorkflowResult(&forceNextTurn))
	require.True(t, forceNextTurn,
		"a parked interrupt wake is a forced next-turn intent, not a child completion")
	require.True(t, tracker.hasLiveDetachedSpawns("thread-a"),
		"waking for interrupt must not abandon or complete the detached spawn")
}

func TestThreadInterrupt_ParkedWaitIgnoresOtherThreads(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	tracker := &ChildWorkflowTracker{}
	tracker.registerDetachedSpawn(&detachedSpawnRecord{
		ToolCallID: "tc1", ParentThread: "thread-a", ChildThread: "child-1",
	})

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(InterruptThreadSignalName, InterruptThreadSignal{ThreadID: "thread-b", Epoch: 1})
	}, 5*time.Second)
	env.RegisterDelayedCallback(func() {
		tracker.completeDetachedSpawn("tc1", "thread-a")
	}, 10*time.Second)

	env.ExecuteWorkflow(parkedInterruptWorkflow("thread-a", tracker))

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var reenter bool
	require.NoError(t, env.GetWorkflowResult(&reenter))
	require.True(t, reenter,
		"the wait should wake only when thread-a's child completes; thread-b's interrupt must not wake it")
}
