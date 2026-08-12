// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

// =========================================================================
// THRESHOLD DECISION
// =========================================================================

const oneMB = 1024 * 1024

func TestHistoryNeedsContinueAsNew(t *testing.T) {
	tests := []struct {
		name      string
		length    int
		sizeBytes int
		want      bool
	}{
		{"fresh run", 12, 8 * 1024, false},
		{"busy but well short on both axes", continueAsNewEventThreshold - 1000, 10 * oneMB, false},
		{"one event short", continueAsNewEventThreshold - 1, 10 * oneMB, false},
		{"exactly at count threshold", continueAsNewEventThreshold, 10 * oneMB, true},
		{"past count threshold", continueAsNewEventThreshold + 5000, 10 * oneMB, true},

		// The size axis binds independently. A payload-heavy run reaches
		// Temporal's 50 MB limit at ~45k events (measured 1,157 bytes/event),
		// i.e. BEFORE the 51,200 count cap — so a count-only check would let
		// exactly the runs this exists for die.
		{"one byte short on size", 100, continueAsNewSizeThreshold - 1, false},
		{"exactly at size threshold", 100, continueAsNewSizeThreshold, true},
		{"heavy payloads trip size long before count", 20000, 44 * oneMB, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := historyNeedsContinueAsNew(tc.length, tc.sizeBytes)
			require.Equal(t, tc.want, got)
		})
	}
}

// The thresholds are only safe because the gap between each one and Temporal's
// corresponding hard limit dwarfs any single agent turn: the boundary check is
// SKIPPED whenever a background spawn is in flight, so a run must be able to
// keep going for many more turns after first crossing a threshold.
//
// Both numbers are measured, not assumed. Turn cost: median 37 events, max 64,
// over 73 turns of real history. Event density: 517–1,157 bytes/event across
// sampled production runs.
func TestThresholdsLeaveHeadroomForWorstCaseTurns(t *testing.T) {
	const (
		countCap          = 51200      // limit.historyCount.error
		sizeCap           = 50 * oneMB // limit.historySize.error
		worstObservedTurn = 64         // events per turn, p100
		heaviestDensity   = 1157       // bytes per event, p100
		wantHeadroomTurns = 100
	)

	countHeadroom := countCap - continueAsNewEventThreshold
	require.Greater(t, countHeadroom/worstObservedTurn, wantHeadroomTurns,
		"count threshold %d leaves only %d events (%d worst-case turns) before the %d cap",
		continueAsNewEventThreshold, countHeadroom, countHeadroom/worstObservedTurn, countCap)

	sizeHeadroom := sizeCap - continueAsNewSizeThreshold
	sizeHeadroomTurns := sizeHeadroom / (worstObservedTurn * heaviestDensity)
	require.Greater(t, sizeHeadroomTurns, wantHeadroomTurns,
		"size threshold %d MB leaves only %d MB (%d worst-case turns) before the %d MB cap",
		continueAsNewSizeThreshold/oneMB, sizeHeadroom/oneMB, sizeHeadroomTurns, sizeCap/oneMB)
}

// Neither threshold may sit at or above the limit it protects against — that
// would mean handing off only once the run is already dead.
func TestThresholdsStayBelowTemporalLimits(t *testing.T) {
	require.Less(t, continueAsNewEventThreshold, 51200,
		"count threshold must be below limit.historyCount.error")
	require.Less(t, continueAsNewSizeThreshold, 50*oneMB,
		"size threshold must be below limit.historySize.error")
}

// The server raises GetContinueAsNewSuggested() at 4,096 events / 4 MB by
// default — about a tenth of the real cap. Honoring it would hand off roughly
// every 110 turns and make our own thresholds dead code, since an OR-ed hint
// always trips first. This pins the decision to ignore it: a history well past
// the server's hint but short of ours must NOT trigger a handoff.
func TestServerSuggestionThresholdIsNotUsed(t *testing.T) {
	const (
		serverCountHint = 4096
		serverSizeHint  = 4 * oneMB
	)

	require.False(t, historyNeedsContinueAsNew(serverCountHint+1, serverSizeHint+1),
		"a run past Temporal's default suggest-continue-as-new hint but short of our "+
			"thresholds must keep running; handing off here would be ~10x too eager")
}

// =========================================================================
// QUIESCENCE
// =========================================================================

func TestQuiescentForContinueAsNew(t *testing.T) {
	trackerWithSpawn := func() *ChildWorkflowTracker {
		tracker := &ChildWorkflowTracker{}
		tracker.registerDetachedSpawn(&detachedSpawnRecord{
			ToolCallID:   "tool-1",
			ChatID:       "chat-1",
			ParentThread: "thread-1",
			ChildThread:  "thread-2",
		})
		return tracker
	}

	t.Run("idle boundary is quiescent", func(t *testing.T) {
		require.True(t, quiescentForContinueAsNew(&ChildWorkflowTracker{}, false))
	})

	t.Run("nil tracker is quiescent", func(t *testing.T) {
		require.True(t, quiescentForContinueAsNew(nil, false))
	})

	// Continuing as new ends the execution, and a background spawn is a
	// goroutine INSIDE it — not a child workflow that would survive. Killing
	// one mid-flight strands its tool_calls row at "backgrounded" forever.
	t.Run("live detached spawn blocks", func(t *testing.T) {
		require.False(t, quiescentForContinueAsNew(trackerWithSpawn(), false))
	})

	// A spawn that has landed no longer pins the run.
	t.Run("completed detached spawn does not block", func(t *testing.T) {
		tracker := trackerWithSpawn()
		tracker.completeDetachedSpawn("tool-1", "thread-1")
		require.True(t, quiescentForContinueAsNew(tracker, false))
	})

	// A continuation started under an armed pause would drop the resume
	// signal and come back running, silently undoing the pause.
	t.Run("armed pause blocks", func(t *testing.T) {
		require.False(t, quiescentForContinueAsNew(&ChildWorkflowTracker{}, true))
	})
}

// =========================================================================
// CONTINUATION INPUT
// =========================================================================

// continueAsNewCarryWorkflow returns a continuation built the same way the
// agent loop builds one, so the test can inspect what crosses the boundary.
func continueAsNewCarryWorkflow(ctx workflow.Context, input WorkflowInput) (*WorkflowResult, error) {
	return nil, newContinueAsNewError(ctx, input, "agent_loop", 7)
}

type ContinueAsNewInputTestSuite struct {
	suite.Suite
	testsuite.WorkflowTestSuite
}

func TestContinueAsNewInput(t *testing.T) {
	suite.Run(t, new(ContinueAsNewInputTestSuite))
}

// The continuation must carry the position it resumes at, and the CURRENT
// inputs map — values applied by update_workflow_state signals live there, and
// dropping them would silently revert mid-run configuration changes.
func (s *ContinueAsNewInputTestSuite) TestCarriesResumePositionAndInputs() {
	env := s.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(continueAsNewCarryWorkflow)

	env.ExecuteWorkflow(continueAsNewCarryWorkflow, WorkflowInput{
		ChatID:       "chat-1",
		WorkflowName: "agent",
		Inputs:       map[string]interface{}{"model": "updated-by-signal"},
		ExecContext:  &ExecutionContext{Thread: "thread-1"},
	})

	s.True(env.IsWorkflowCompleted())

	var contErr *workflow.ContinueAsNewError
	s.Require().True(errors.As(env.GetWorkflowError(), &contErr),
		"expected a ContinueAsNewError, got %v", env.GetWorkflowError())

	var carried WorkflowInput
	s.Require().NoError(converter.GetDefaultDataConverter().
		FromPayloads(contErr.Input, &carried))

	s.Equal("chat-1", carried.ChatID)
	s.Equal("agent", carried.WorkflowName)
	s.Equal("updated-by-signal", carried.Inputs["model"])
	s.Require().NotNil(carried.ExecContext)
	s.Equal("thread-1", carried.ExecContext.Thread)

	// The position is what makes the handoff exact: the successor re-enters
	// at the iteration whose checkpoint was just written, so nothing is
	// repeated and nothing is skipped.
	s.Require().NotNil(carried.Resume)
	s.Equal("agent_loop", carried.Resume.NodeID)
	s.Equal(7, carried.Resume.LoopIteration)
}

// The carried position must resolve back to the same loop and iteration the
// predecessor stopped at. This is the round trip that makes the continuation
// correct, and it goes through the same resolver the coarse restart uses.
func TestContinuationPositionResolvesBackToLoop(t *testing.T) {
	wf := resumeTestWorkflow(t, `
name: single-loop
entry: [agent_loop]
nodes:
  - id: agent_loop
    type: loop
    while: iter.iteration < 3
    inline:
      entry: [work]
      nodes:
        - id: work
          type: call_llm
`)

	carried := &ResumeInput{NodeID: "agent_loop", LoopIteration: 7}
	target, iteration := resolveResumeTarget(wf, carried, &resumeTestLogger{})

	require.NotNil(t, target)
	require.Equal(t, "agent_loop", target.GetId())
	require.Equal(t, 7, iteration)
}

// =========================================================================
// ERROR CLASSIFICATION
// =========================================================================

// classifyErrWorkflow reports how isContinueAsNew classifies each error kind.
// It runs inside a workflow because a ContinueAsNewError can only be built
// from a workflow context.
func classifyErrWorkflow(ctx workflow.Context) ([]bool, error) {
	contErr := workflow.NewContinueAsNewError(ctx, classifyErrWorkflow)
	return []bool{
		isContinueAsNew(contErr),
		// The loop path wraps errors on the way out (%w), so the check has to
		// see through wrapping or the handoff is misread as a loop failure.
		isContinueAsNew(fmt.Errorf("loop step %s failed: %w", "agent_loop", contErr)),
		isContinueAsNew(errors.New("something went wrong")),
		isContinueAsNew(nil),
	}, nil
}

type ContinueAsNewClassifyTestSuite struct {
	suite.Suite
	testsuite.WorkflowTestSuite
}

func TestContinueAsNewClassify(t *testing.T) {
	suite.Run(t, new(ContinueAsNewClassifyTestSuite))
}

func (s *ContinueAsNewClassifyTestSuite) TestIsContinueAsNew() {
	env := s.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(classifyErrWorkflow)

	env.ExecuteWorkflow(classifyErrWorkflow)
	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())

	var got []bool
	s.Require().NoError(env.GetWorkflowResult(&got))
	s.Equal([]bool{true, true, false, false}, got,
		"expected [bare, wrapped, ordinary error, nil] to classify as [true, true, false, false]")
}

// =========================================================================
// LOOP BOUNDARY WIRING
// =========================================================================

// The check fires at the TOP of an iteration, after the position checkpoint
// and before any of the iteration's work, and aborts the loop with the error
// verbatim. Anything less exact means the successor either redoes work or
// skips it.
func TestLoopBoundaryCheckOrdering(t *testing.T) {
	var events []string

	executor := &InlineLoopExecutor{}
	executor.iterationCheckpoint = func(iteration int) {
		events = append(events, "checkpoint")
	}
	sentinel := errors.New("continue-as-new sentinel")
	executor.continueAsNewCheck = func(iteration int) error {
		events = append(events, "check")
		return sentinel
	}

	// Mirror the boundary sequence from Execute()'s main loop.
	if executor.iterationCheckpoint != nil {
		executor.iterationCheckpoint(0)
	}
	var got error
	if executor.continueAsNewCheck != nil {
		got = executor.continueAsNewCheck(0)
	}

	require.Equal(t, []string{"checkpoint", "check"}, events,
		"the checkpoint must be written before the check, so the persisted position is the one resumed at")
	require.ErrorIs(t, got, sentinel, "the error must propagate unwrapped")
}

// A nested loop must never be able to end the whole execution, so only
// DynamicWorkflow's top-level wiring sets the check. An executor without it
// runs its boundary unchanged.
func TestNestedLoopHasNoContinueAsNewCheck(t *testing.T) {
	executor := &InlineLoopExecutor{}
	require.Nil(t, executor.continueAsNewCheck,
		"a bare executor must not continue as new; only top-level loops opt in")

	executor = executor.WithContinueAsNewCheck(func(int) error { return nil })
	require.NotNil(t, executor.continueAsNewCheck)
}

// =========================================================================
// COMPLETION HANDLING
// =========================================================================

type ContinueAsNewCompletionTestSuite struct {
	suite.Suite
	testsuite.WorkflowTestSuite
}

func TestContinueAsNewCompletion(t *testing.T) {
	suite.Run(t, new(ContinueAsNewCompletionTestSuite))
}

// handleWorkflowCompletion must not treat a ContinueAsNew error as a failure.
// Doing so would mark the chat failed in the UI while its successor is
// starting, and it would run cleanup activities that cancel pending approvals
// and orphan tool calls the successor still needs. The successor reports its
// own status.
func (s *ContinueAsNewCompletionTestSuite) TestDoesNotReportTerminalStatus() {
	env := s.NewTestWorkflowEnvironment()
	captured := setupCompletionEnv(env)

	env.ExecuteWorkflow(completionTestWorkflow, "continue_as_new")

	s.True(env.IsWorkflowCompleted())

	// The error must reach the SDK intact — that is what makes it a
	// CONTINUE_AS_NEW command instead of a workflow failure.
	var contErr *workflow.ContinueAsNewError
	s.Require().True(errors.As(env.GetWorkflowError(), &contErr),
		"expected a ContinueAsNewError, got %v", env.GetWorkflowError())

	s.NotContains(*captured, "failed",
		"a continue-as-new handoff must not mark the chat failed — its successor is about to run")
	s.NotContains(*captured, "completed",
		"a continue-as-new handoff is not a completion either; the successor reports its own status")
	s.Empty(*captured,
		"a continue-as-new handoff should announce no terminal status at all")
}

// The guard must be specific to ContinueAsNew: a genuine failure still has to
// be reported. Without this, a bug that widened the check would silence every
// workflow failure and the test above would still pass.
func (s *ContinueAsNewCompletionTestSuite) TestOrdinaryErrorStillReportsFailed() {
	env := s.NewTestWorkflowEnvironment()
	captured := setupCompletionEnv(env)

	env.ExecuteWorkflow(completionTestWorkflow, "error")

	s.True(env.IsWorkflowCompleted())
	s.Error(env.GetWorkflowError())
	s.Contains(*captured, "failed",
		"a real error must still be reported as a failure")
}
