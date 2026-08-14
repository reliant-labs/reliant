// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

// These tests pin spec §6.1's loop-lifetime rule in isolation: the loop must
// not abandon a background spawn still in flight, must NOT burn a call_llm
// turn while waiting (§6.2's rejected approach), and must react to the FIRST
// finisher rather than waiting for every one.

type SpawnLifetimeSuite struct {
	suite.Suite
	testsuite.WorkflowTestSuite
}

func TestSpawnLifetime(t *testing.T) {
	suite.Run(t, new(SpawnLifetimeSuite))
}

// awaitLiveDetachedSpawnsWorkflow drives InlineLoopExecutor.awaitLiveDetachedSpawns
// directly against a childTracker whose registration/completion is controlled
// by the test, without needing a full spawn/InlineWorkflowExecutor round trip.
//
// A closure over tracker, NOT a workflow argument: workflow arguments are
// round-tripped through the DataConverter (even in the test environment,
// for determinism parity with production), so a *ChildWorkflowTracker passed
// as an argument would arrive as a deserialized COPY — mutations the test
// makes via RegisterDelayedCallback would then be invisible to the workflow.
// Capturing the same pointer directly is what keeps them the same object.
func awaitLiveDetachedSpawnsWorkflow(thread string, tracker *ChildWorkflowTracker) func(workflow.Context) (bool, error) {
	return func(ctx workflow.Context) (bool, error) {
		e := &InlineLoopExecutor{
			ctx:          ctx,
			loopID:       "agent_loop",
			logger:       workflow.GetLogger(ctx),
			childTracker: tracker,
			execContext:  &ExecutionContext{Thread: thread},
		}
		return e.awaitLiveDetachedSpawns(), nil
	}
}

// TestAwaitLiveDetachedSpawns_ReturnsFalseImmediately_WhenNoneLive is the
// hot path: a normal turn with no background spawns costs nothing extra.
func (s *SpawnLifetimeSuite) TestAwaitLiveDetachedSpawns_ReturnsFalseImmediately_WhenNoneLive() {
	env := s.NewTestWorkflowEnvironment()
	tracker := &ChildWorkflowTracker{}

	env.ExecuteWorkflow(awaitLiveDetachedSpawnsWorkflow("thread-a", tracker))

	s.True(env.IsWorkflowCompleted())
	s.NoError(env.GetWorkflowError())
	var reenter bool
	s.NoError(env.GetWorkflowResult(&reenter))
	s.False(reenter, "nothing to wait on must not ask the loop to re-enter")
}

// TestAwaitLiveDetachedSpawns_WakesOnFirstCompletion is the core lifetime
// regression: with a live detached spawn, the loop blocks until it
// completes, then re-enters — it does not exit early and abandon it.
func (s *SpawnLifetimeSuite) TestAwaitLiveDetachedSpawns_WakesOnFirstCompletion() {
	env := s.NewTestWorkflowEnvironment()
	tracker := &ChildWorkflowTracker{}
	tracker.registerDetachedSpawn(&detachedSpawnRecord{ToolCallID: "tc1", ParentThread: "thread-a", ChildThread: "child-1"})

	// Simulate the detached goroutine completing shortly after the loop
	// starts waiting — the test-env clock auto-advances while everything is
	// blocked, so this "shortly after" costs no real wall-clock time here.
	env.RegisterDelayedCallback(func() {
		tracker.completeDetachedSpawn("tc1", "thread-a")
	}, 5*time.Second)

	env.ExecuteWorkflow(awaitLiveDetachedSpawnsWorkflow("thread-a", tracker))

	s.True(env.IsWorkflowCompleted())
	s.NoError(env.GetWorkflowError())
	var reenter bool
	s.NoError(env.GetWorkflowResult(&reenter))
	s.True(reenter, "the loop must re-enter once its thread's spawn completes, not exit and abandon it")
}

// TestAwaitLiveDetachedSpawns_TwoSpawns_WakesOnFirstNotBoth pins spec §6.3's
// "not wait for all agents" behavior (implemented here without the CEL wait
// node): with two live spawns, the wait resolves as soon as ONE completes.
func (s *SpawnLifetimeSuite) TestAwaitLiveDetachedSpawns_TwoSpawns_WakesOnFirstNotBoth() {
	env := s.NewTestWorkflowEnvironment()
	tracker := &ChildWorkflowTracker{}
	tracker.registerDetachedSpawn(&detachedSpawnRecord{ToolCallID: "tc1", ParentThread: "thread-a", ChildThread: "child-1"})
	tracker.registerDetachedSpawn(&detachedSpawnRecord{ToolCallID: "tc2", ParentThread: "thread-a", ChildThread: "child-2"})

	env.RegisterDelayedCallback(func() {
		tracker.completeDetachedSpawn("tc1", "thread-a")
	}, 5*time.Second)
	// tc2 deliberately never completes in this test.

	env.ExecuteWorkflow(awaitLiveDetachedSpawnsWorkflow("thread-a", tracker))

	s.True(env.IsWorkflowCompleted())
	s.NoError(env.GetWorkflowError())
	var reenter bool
	s.NoError(env.GetWorkflowResult(&reenter))
	s.True(reenter, "the first finisher must wake the wait even with a second spawn still running")
	s.True(tracker.hasLiveDetachedSpawns("thread-a"), "the still-running second spawn must remain tracked")
}

// TestAwaitLiveDetachedSpawns_WaitsPastOldCeiling pins the wait as UNBOUNDED.
//
// This replaces a test that asserted the opposite — that an await ceiling
// fires and the loop stops waiting. That ceiling was 4 minutes, and it fired
// on healthy runs: a background agent doing real work outlives it, so the
// parent abandoned live children, exited, and cascaded them to COMPLETED,
// leaving the chat IDLE while six agents were still running. A background
// agent's runtime is unbounded in the normal case, so the deadline was
// removed rather than retuned.
//
// The child here stays live well past the old 4-minute ceiling and only then
// completes. Under the old behavior the wait would have given up at 4:00 and
// returned false; the assertion is that it instead waits and reports the
// completion, so the parent loop re-enters and can react to the result.
func (s *SpawnLifetimeSuite) TestAwaitLiveDetachedSpawns_WaitsPastOldCeiling() {
	env := s.NewTestWorkflowEnvironment()
	tracker := &ChildWorkflowTracker{}
	tracker.registerDetachedSpawn(&detachedSpawnRecord{ToolCallID: "tc1", ParentThread: "thread-a", ChildThread: "child-1"})

	// Well beyond the removed 4-minute ceiling. The test env runs on a
	// simulated clock, so this costs no real time.
	env.RegisterDelayedCallback(func() {
		tracker.completeDetachedSpawn("tc1", "thread-a")
	}, 30*time.Minute)

	env.ExecuteWorkflow(awaitLiveDetachedSpawnsWorkflow("thread-a", tracker))

	s.True(env.IsWorkflowCompleted())
	s.NoError(env.GetWorkflowError())
	var reenter bool
	s.NoError(env.GetWorkflowResult(&reenter))
	s.True(reenter, "a spawn finishing long after the old ceiling must still wake the wait and re-enter the loop")
}

// TestAwaitLiveDetachedSpawns_WakesOnQueuedAgentMessage is the mailbox
// deadlock regression.
//
// A parent that has fanned work out to sub-agents parks here with its
// children still live. Delivery of a queued message happens ONLY in
// drainAgentMessagesAtBoundary, at the top of the loop body — which a parked
// loop never reaches. Before this fix the wait predicate watched child
// completions and nothing else, so a message queued into a waiting parent was
// undeliverable until some child happened to finish for unrelated reasons.
//
// Observed on chat 0f80b069-c77e-41a5-a86d-403ab3eb9410: a human "continue"
// queued at 01:30:45 was delivered at 01:59:02 — 28 minutes later, and 303ms
// after a sub-agent completed.
//
// The child here NEVER completes. The only thing that can end this wait is
// the mailbox notification, which is exactly the property under test.
func (s *SpawnLifetimeSuite) TestAwaitLiveDetachedSpawns_WakesOnQueuedAgentMessage() {
	env := s.NewTestWorkflowEnvironment()
	tracker := &ChildWorkflowTracker{}
	tracker.registerDetachedSpawn(&detachedSpawnRecord{ToolCallID: "tc1", ParentThread: "thread-a", ChildThread: "child-1"})

	env.RegisterDelayedCallback(func() {
		tracker.notifyAgentMessageQueued("thread-a")
	}, 5*time.Second)

	env.ExecuteWorkflow(awaitLiveDetachedSpawnsWorkflow("thread-a", tracker))

	s.True(env.IsWorkflowCompleted())
	s.NoError(env.GetWorkflowError())
	var reenter bool
	s.NoError(env.GetWorkflowResult(&reenter))
	s.True(reenter,
		"a message queued into a thread parked on its sub-agents must wake the loop so it drains, "+
			"not wait for a child to finish")
	s.True(tracker.hasLiveDetachedSpawns("thread-a"),
		"waking to read a message must not abandon the still-running child")
}

// TestAwaitLiveDetachedSpawns_IgnoresOtherThreadsMailbox: the doorbell is
// thread-scoped like everything else here. A message for a sibling thread
// must not wake this thread's gate, or a busy chat would re-enter every
// parked loop on every send.
func (s *SpawnLifetimeSuite) TestAwaitLiveDetachedSpawns_IgnoresOtherThreadsMailbox() {
	env := s.NewTestWorkflowEnvironment()
	tracker := &ChildWorkflowTracker{}
	tracker.registerDetachedSpawn(&detachedSpawnRecord{ToolCallID: "tc1", ParentThread: "thread-a", ChildThread: "child-1"})

	// A message for someone else, then the child finishes. Only the
	// completion should be reported as the reason to re-enter.
	env.RegisterDelayedCallback(func() {
		tracker.notifyAgentMessageQueued("thread-b")
	}, 5*time.Second)
	env.RegisterDelayedCallback(func() {
		tracker.completeDetachedSpawn("tc1", "thread-a")
	}, 10*time.Second)

	env.ExecuteWorkflow(awaitLiveDetachedSpawnsWorkflow("thread-a", tracker))

	s.True(env.IsWorkflowCompleted())
	s.NoError(env.GetWorkflowError())
	var reenter bool
	s.NoError(env.GetWorkflowResult(&reenter))
	s.True(reenter)
	s.Equal(0, tracker.agentMessageWakeCount("thread-a"),
		"another thread's mailbox must not register against this one")
}

// A mailbox notification that arrives when nothing is live must not, on its
// own, hold a loop open — there is no wait to wake, and the ordinary drain at
// the top of the next iteration already covers it.
func (s *SpawnLifetimeSuite) TestAwaitLiveDetachedSpawns_QueuedMessageAloneDoesNotBlock() {
	env := s.NewTestWorkflowEnvironment()
	tracker := &ChildWorkflowTracker{}
	tracker.notifyAgentMessageQueued("thread-a")

	env.ExecuteWorkflow(awaitLiveDetachedSpawnsWorkflow("thread-a", tracker))

	s.True(env.IsWorkflowCompleted())
	s.NoError(env.GetWorkflowError())
	var reenter bool
	s.NoError(env.GetWorkflowResult(&reenter))
	s.False(reenter, "with no live spawns there is nothing to wait on; the normal boundary drain applies")
}

// parkedThreadWorkflow wires the REAL signal handler to the REAL wait gate,
// the way DynamicWorkflow does: setupAgentMessageQueuedHandler feeds the
// tracker, awaitLiveDetachedSpawns reads it.
//
// The tests above drive the tracker directly, which proves the gate's
// predicate but says nothing about whether a signal actually reaches it. This
// one closes that gap — it fails if the signal name is wrong, if the payload
// does not round-trip through the DataConverter, or if the handler never
// registers.
func parkedThreadWorkflow(thread string, tracker *ChildWorkflowTracker) func(workflow.Context) (bool, error) {
	return func(ctx workflow.Context) (bool, error) {
		setupAgentMessageQueuedHandler(ctx, tracker, "test-workflow")
		e := &InlineLoopExecutor{
			ctx:          ctx,
			loopID:       "agent_loop",
			logger:       workflow.GetLogger(ctx),
			childTracker: tracker,
			execContext:  &ExecutionContext{Thread: thread},
		}
		return e.awaitLiveDetachedSpawns(), nil
	}
}

// TestParkedThread_WokenByRealSignal is the full-path regression: a parent
// parked on a sub-agent that NEVER finishes is woken by an actual
// AgentMessageQueuedSignal delivered through Temporal's signal machinery.
//
// This is the exact production shape of the bug. The child stays live for the
// whole test, so under the old behavior the workflow would block until the
// test deadline.
func (s *SpawnLifetimeSuite) TestParkedThread_WokenByRealSignal() {
	env := s.NewTestWorkflowEnvironment()
	tracker := &ChildWorkflowTracker{}
	tracker.registerDetachedSpawn(&detachedSpawnRecord{ToolCallID: "tc1", ParentThread: "thread-a", ChildThread: "child-1"})

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(AgentMessageQueuedSignalName, AgentMessageQueuedSignal{Thread: "thread-a"})
	}, 5*time.Second)

	env.ExecuteWorkflow(parkedThreadWorkflow("thread-a", tracker))

	s.True(env.IsWorkflowCompleted())
	s.NoError(env.GetWorkflowError())
	var reenter bool
	s.NoError(env.GetWorkflowResult(&reenter))
	s.True(reenter,
		"a real agent_message_queued signal must wake a parent parked on a still-running sub-agent")
	s.True(tracker.hasLiveDetachedSpawns("thread-a"),
		"the child never finished; waking to read the message must not abandon it")
}

// The signal must be thread-addressed end to end: a real signal naming a
// different thread must leave this thread parked.
func (s *SpawnLifetimeSuite) TestParkedThread_RealSignalForOtherThreadDoesNotWake() {
	env := s.NewTestWorkflowEnvironment()
	tracker := &ChildWorkflowTracker{}
	tracker.registerDetachedSpawn(&detachedSpawnRecord{ToolCallID: "tc1", ParentThread: "thread-a", ChildThread: "child-1"})

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(AgentMessageQueuedSignalName, AgentMessageQueuedSignal{Thread: "thread-b"})
	}, 5*time.Second)
	// Only this releases thread-a, proving the sibling's signal did not.
	env.RegisterDelayedCallback(func() {
		tracker.completeDetachedSpawn("tc1", "thread-a")
	}, 10*time.Second)

	env.ExecuteWorkflow(parkedThreadWorkflow("thread-a", tracker))

	s.True(env.IsWorkflowCompleted())
	s.NoError(env.GetWorkflowError())
	s.Equal(1, tracker.agentMessageWakeCount("thread-b"), "the signal must land on the thread it named")
	s.Equal(0, tracker.agentMessageWakeCount("thread-a"))
}

// TestAwaitLiveDetachedSpawns_IgnoresOtherThreads: a background spawn
// belongs to the thread that launched it. A different thread's loop must not
// block on it.
func (s *SpawnLifetimeSuite) TestAwaitLiveDetachedSpawns_IgnoresOtherThreads() {
	env := s.NewTestWorkflowEnvironment()
	tracker := &ChildWorkflowTracker{}
	tracker.registerDetachedSpawn(&detachedSpawnRecord{ToolCallID: "tc1", ParentThread: "thread-other", ChildThread: "child-1"})

	env.ExecuteWorkflow(awaitLiveDetachedSpawnsWorkflow("thread-a", tracker))

	s.True(env.IsWorkflowCompleted())
	s.NoError(env.GetWorkflowError())
	var reenter bool
	s.NoError(env.GetWorkflowResult(&reenter))
	s.False(reenter)
}

func TestChildWorkflowTracker_DetachedSpawnRegistry(t *testing.T) {
	tracker := &ChildWorkflowTracker{}
	assert.False(t, tracker.hasLiveDetachedSpawns("thread-a"))
	assert.Equal(t, 0, tracker.detachedCompletionCount("thread-a"))
	assert.Empty(t, tracker.listLiveDetachedSpawns())

	tracker.registerDetachedSpawn(&detachedSpawnRecord{ToolCallID: "tc1", ChatID: "chat-1", ParentThread: "thread-a", ChildThread: "child-1"})
	assert.True(t, tracker.hasLiveDetachedSpawns("thread-a"))
	require.Len(t, tracker.listLiveDetachedSpawns(), 1)
	assert.Equal(t, "tc1", tracker.listLiveDetachedSpawns()[0].ToolCallID)

	tracker.completeDetachedSpawn("tc1", "thread-a")
	assert.False(t, tracker.hasLiveDetachedSpawns("thread-a"))
	assert.Equal(t, 1, tracker.detachedCompletionCount("thread-a"))
	assert.Empty(t, tracker.listLiveDetachedSpawns())

	// Double-report is safe (defensive against a race in the caller).
	tracker.completeDetachedSpawn("tc1", "thread-a")
	assert.Equal(t, 2, tracker.detachedCompletionCount("thread-a"))
}

func TestChildWorkflowTracker_ListLiveDetachedSpawns_SortedByToolCallID(t *testing.T) {
	// Regression for replay-safety: map iteration order is randomized, so
	// listLiveDetachedSpawns must return a stable order.
	tracker := &ChildWorkflowTracker{}
	tracker.registerDetachedSpawn(&detachedSpawnRecord{ToolCallID: "tc-b", ParentThread: "thread-a"})
	tracker.registerDetachedSpawn(&detachedSpawnRecord{ToolCallID: "tc-a", ParentThread: "thread-a"})
	tracker.registerDetachedSpawn(&detachedSpawnRecord{ToolCallID: "tc-c", ParentThread: "thread-a"})

	for i := 0; i < 10; i++ {
		records := tracker.listLiveDetachedSpawns()
		require.Len(t, records, 3)
		assert.Equal(t, "tc-a", records[0].ToolCallID)
		assert.Equal(t, "tc-b", records[1].ToolCallID)
		assert.Equal(t, "tc-c", records[2].ToolCallID)
	}
}

// ============================================================================
// terminalDrainDetachedSpawns (spec §6.7) — the abnormal-termination path
// ============================================================================

// terminalDrainTestWorkflow drives handleWorkflowCompletion via the same
// deferred pattern DynamicWorkflow uses, with a childTracker pre-populated
// with a live detached spawn the workflow itself never reaps — modeling a
// cancellation/panic/error landing while a background spawn is still running.
func terminalDrainTestWorkflow(mode string, tracker *ChildWorkflowTracker) func(workflow.Context) (result *WorkflowResult, retErr error) {
	return func(ctx workflow.Context) (result *WorkflowResult, retErr error) {
		workflowID := workflow.GetInfo(ctx).WorkflowExecution.ID
		defer func() {
			handleWorkflowCompletion(ctx, workflowID, "chat-1", "test-workflow", "", "thread-1", "", retErr, "", tracker)
		}()

		switch mode {
		case "error":
			return nil, assertionError("something went wrong")
		case "cancel":
			_ = workflow.Sleep(ctx, time.Hour)
			return nil, ctx.Err()
		default:
			return &WorkflowResult{}, nil
		}
	}
}

type assertionError string

func (e assertionError) Error() string { return string(e) }

func (s *SpawnLifetimeSuite) TestTerminalDrain_CancelsLiveDetachedSpawnOnError() {
	env := s.NewTestWorkflowEnvironment()

	// Must register BEFORE setupCompletionEnv, which switches the env into
	// mock (OnActivity) mode — RegisterActivityWithOptions cannot follow that.
	var emittedStatuses []string
	env.RegisterActivityWithOptions(
		func(_ context.Context, input map[string]interface{}) (map[string]interface{}, error) {
			status, _ := input["status"].(string)
			emittedStatuses = append(emittedStatuses, status)
			return map[string]interface{}{"success": true}, nil
		},
		activity.RegisterOptions{Name: "EmitToolCallStatus"},
	)
	captured := setupCompletionEnv(env)

	tracker := &ChildWorkflowTracker{}
	tracker.registerDetachedSpawn(&detachedSpawnRecord{
		ToolCallID: "tc-live", ChatID: "chat-1", ParentThread: "thread-1", ChildThread: "child-1",
	})

	env.ExecuteWorkflow(terminalDrainTestWorkflow("error", tracker))

	s.True(env.IsWorkflowCompleted())
	s.Error(env.GetWorkflowError())
	s.Contains(*captured, "failed")
	s.Contains(emittedStatuses, "cancelled",
		"a detached spawn still live when an abnormal termination hits must be drained (cancelled status emitted), not abandoned")
	s.False(tracker.hasLiveDetachedSpawns("thread-1"), "the drain must remove the entry from the live registry")
}

func (s *SpawnLifetimeSuite) TestTerminalDrain_NoOpWhenNothingLive() {
	env := s.NewTestWorkflowEnvironment()

	var emitToolCallStatusCalls int
	env.RegisterActivityWithOptions(
		func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
			emitToolCallStatusCalls++
			return map[string]interface{}{"success": true}, nil
		},
		activity.RegisterOptions{Name: "EmitToolCallStatus"},
	)
	captured := setupCompletionEnv(env)

	tracker := &ChildWorkflowTracker{}

	env.ExecuteWorkflow(terminalDrainTestWorkflow("error", tracker))

	s.True(env.IsWorkflowCompleted())
	s.Contains(*captured, "failed")
	s.Equal(0, emitToolCallStatusCalls, "no live detached spawns means no drain activity at all")
}
