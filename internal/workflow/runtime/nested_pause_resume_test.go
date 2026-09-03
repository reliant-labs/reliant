// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/reliant-labs/reliant/internal/workflow/model"
	types "github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// PAUSING A NESTED RUN MUST NOT RE-ENTER THE NODE
//
// Observed on chat 4d92f694: a landing-page run (a loop node whose iteration
// runs an inline sub-workflow) was paused during the implementer's first
// attempt. Resuming re-seeded the sub-workflow's opening message verbatim --
// the same bytes, thirty minutes later -- because resume did not continue where
// it left off. It re-ENTERED the top-level node.
//
// Mechanism: pause cancels the shared activity context, the running activity
// returns a CanceledError, and each nested executor RETURNS that error rather
// than handling it. The stack unwinds to the top-level retryLoop
// (workflow.go), which blocks until resume and then calls
// loopExecutor.Execute() again -- rebuilding every frame below it from scratch.
// Anything a node does on ENTRY (fork a thread, inject a seed message) happens
// a second time.
//
// The top-level executor already does the right thing: on CanceledError it
// blocks in place and re-Start()s just that step (workflow.go:1749). That is
// why pausing a plain agent -- or a spawned sub-agent, which runs as its own
// goroutine off the same shared context -- behaves correctly today, and only
// workflow NODES regress. These tests pin the nested executors to the same
// contract.

// nestedPauseYAML is a loop whose iteration runs an inline sub-workflow, which
// is the shape that breaks: agent_loop -> (inline) prepare -> work.
//
// `prepare` stands in for any on-entry side effect (get-it-right's thread fork
// + inject). Counting its executions is how we detect re-entry: a pause during
// `work` must not run `prepare` again.
const nestedPauseYAML = `
name: nested-pause
entry: [outer_loop]
nodes:
  - id: outer_loop
    type: loop
    while: outputs.keep_going == true
    inline:
      outputs:
        keep_going: "{{has(nodes.work) && has(nodes.work.keep_going) ? nodes.work.keep_going : false}}"
      entry: [prepare]
      nodes:
        - id: prepare
          type: save_message
          args:
            role: user
            content: "seed"
        - id: work
          type: call_llm
      edges:
        - from: prepare
          cases:
            - to: work
              condition: "true"
              label: go
edges: []
`

type nestedPauseEnv struct {
	t *testing.T

	mu sync.Mutex
	// prepareRuns counts entries into the on-entry node. Re-entry shows up
	// here: the defect makes it run twice for a single pause.
	prepareRuns int32
	// workAttempts counts dispatches of the step that gets cancelled.
	workAttempts int32

	// cancelOnAttempt makes the Nth `work` dispatch behave like an activity
	// cancelled by pause, which is exactly what the shared activityCtx does.
	cancelOnAttempt int32

	env *testsuite.TestWorkflowEnvironment
}

func newNestedPauseEnv(t *testing.T, env *testsuite.TestWorkflowEnvironment, cancelOnAttempt int32) *nestedPauseEnv {
	t.Helper()
	e := &nestedPauseEnv{t: t, cancelOnAttempt: cancelOnAttempt, env: env}

	wf, err := wfyaml.ParseWorkflow([]byte(nestedPauseYAML))
	require.NoError(t, err)
	wfJSON, err := protojson.Marshal(wf)
	require.NoError(t, err)

	env.RegisterActivityWithOptions(
		func(_ context.Context, _ map[string]string) (LoadedWorkflow, error) {
			return LoadedWorkflow{WorkflowJSON: wfJSON}, nil
		},
		activity.RegisterOptions{Name: "ActivityLoadWorkflow"},
	)
	for _, name := range []string{"WorkflowStatus", "WorkflowCheckpoint", "WorkflowError", "EmitToolCallStatus"} {
		env.RegisterActivityWithOptions(
			func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
				return map[string]interface{}{"success": true}, nil
			},
			activity.RegisterOptions{Name: name},
		)
	}
	env.RegisterActivityWithOptions(
		func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
			return map[string]interface{}{}, nil
		},
		activity.RegisterOptions{Name: "Cleanup"},
	)

	// The on-entry node. In the real workflow this is the thread fork + inject
	// that re-seeded "Get It Right — Attempt 1 of 4".
	env.RegisterActivityWithOptions(
		func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
			atomic.AddInt32(&e.prepareRuns, 1)
			return map[string]interface{}{"message_id": "seed-msg"}, nil
		},
		activity.RegisterOptions{Name: "SaveMessage"},
	)

	// The step that is in flight when the user pauses.
	env.RegisterActivityWithOptions(
		func(_ context.Context, _ types.ActivityInput) (map[string]interface{}, error) {
			n := atomic.AddInt32(&e.workAttempts, 1)
			if n == e.cancelOnAttempt {
				// What a paused activity returns: the shared activity context
				// was cancelled underneath it.
				return nil, temporal.NewCanceledError("cancelled by pause")
			}
			// Second dispatch (post-resume) finishes the run.
			return map[string]interface{}{
				"response_text": "done",
				"tool_calls":    nil,
				"keep_going":    false,
				"message":       map[string]interface{}{"role": "assistant", "text": "done"},
			}, nil
		},
		activity.RegisterOptions{Name: "CallLLM"},
	)

	return e
}

func nestedPauseInput(chatID string) WorkflowInput {
	return WorkflowInput{
		ChatID:       chatID,
		WorkflowName: "nested-pause",
		Inputs:       map[string]interface{}{},
		ExecContext: &ExecutionContext{
			WorkflowID:   "wf-" + chatID,
			ChatID:       chatID,
			Thread:       "wf-" + chatID,
			ThreadMode:   model.ThreadModeNew,
			WorkflowName: "nested-pause",
		},
	}
}

// THE REGRESSION. A pause during a nested step must resume that step, not
// re-enter the node that dispatched it.
func TestNestedPause_ResumesInPlaceWithoutReEnteringTheNode(t *testing.T) {
	t.Parallel()
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()

	// Cancel the FIRST `work` dispatch, as a pause would.
	e := newNestedPauseEnv(t, env, 1)

	// Resume shortly after the cancellation lands, so the run continues.
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow("signal.resume", nil)
	}, 100*time.Millisecond)

	env.ExecuteWorkflow(DynamicWorkflow, nestedPauseInput("chat-nested-pause"))

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	// The step that was cancelled is retried: two dispatches, one cancelled and
	// one that completes. Without this the test proves nothing.
	require.Equal(t, int32(2), atomic.LoadInt32(&e.workAttempts),
		"the cancelled step must be retried after resume")

	// THE ASSERTION. `prepare` is an ENTRY side effect of the node. Running it
	// twice is the defect: it is what re-seeded the implementer's opening
	// message on chat 4d92f694.
	require.Equal(t, int32(1), atomic.LoadInt32(&e.prepareRuns),
		"pausing a nested step must NOT re-enter the node — re-entry re-runs "+
			"on-entry side effects (thread fork, inject) and re-seeds the "+
			"agent's opening message")
}

// A genuine CancelWorkflow must still unwind rather than retry in place.
// Retrying is right for a PAUSE, which always has a resume coming; a cancel has
// none, so the same path would re-dispatch forever into a dead context.
//
// HONEST SCOPE: this pins that cancellation TERMINATES and does not re-enter
// the node. It does not, on its own, prove the `e.ctx.Err() != nil` guard is
// load-bearing — verified by deleting the guard, this still passes, because
// Temporal's own cancelled root context also stops the run through
// DoCheckPause. The guard remains because relying on that second-order effect
// would be fragile: it makes the intent explicit and bounds the loop directly
// rather than depending on the pause coordinator's behavior under a cancelled
// root. If it is ever removed, the failure mode is a spin, not a wrong answer.
func TestNestedPause_GenuineCancelStillUnwinds(t *testing.T) {
	t.Parallel()
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()

	// Never cancel a dispatch artificially: the real workflow cancellation
	// below is what kills the in-flight activity.
	e := newNestedPauseEnv(t, env, 0)

	// Cancel while the FIRST work step is still in flight. Ordering matters —
	// this must land after `prepare` completes and during `work`.
	env.RegisterDelayedCallback(func() {
		env.CancelWorkflow()
	}, time.Millisecond)

	env.ExecuteWorkflow(DynamicWorkflow, nestedPauseInput("chat-nested-cancel"))

	// The point of the test: it TERMINATES. A missing guard spins the
	// re-dispatch loop and the environment never completes.
	require.True(t, env.IsWorkflowCompleted(),
		"a cancelled workflow must terminate, not spin re-dispatching its cancelled step")

	// And it must not have re-entered the node on the way out.
	require.LessOrEqual(t, atomic.LoadInt32(&e.prepareRuns), int32(1),
		"cancellation must not re-run the node's on-entry side effects")
}
