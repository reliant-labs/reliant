// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/reliant-labs/reliant/internal/workflow/model"
	types "github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// CANCELLING A SPAWN MUST STOP ITS LOOP.
//
// A spawn runs inline in the parent's Temporal execution as a workflow.Go
// goroutine, so Temporal cannot cancel it for us — the spawned thread has to
// notice the user's cancel and stop itself. That is what
// PauseController.Cancelled (fed by the cancel_thread signal, workflow.go
// setupCancelThreadHandler) is for.
//
// InlineWorkflowExecutor checks it at its step boundary
// (inline_workflow_executor.go:660) and that was assumed sufficient. It is not.
// A spawn runs `builtin://agent`, whose body is a LOOP node, so
// InlineLoopExecutor is what actually drives the spawned turns — and it never
// checked. Clicking cancel on an async spawn set the flag and nothing observed
// it, so the spawn ran to completion.
//
// This drives a REAL loop workflow whose cancel flag is already set, and
// asserts the loop refuses to run turns.

const spawnCancelLoopYAML = `
name: spawn-cancel-loop
entry: [agent_loop]
nodes:
  - id: agent_loop
    type: loop
    while: outputs.keep_going == true
    inline:
      outputs:
        keep_going: "{{has(nodes.work) && has(nodes.work.keep_going) ? nodes.work.keep_going : false}}"
      entry: [work]
      nodes:
        - id: work
          type: call_llm
edges: []
`

// TestSpawnCancel_LoopStopsWhenThreadCancelled pins that a cancelled thread's
// loop stops instead of executing turns.
func TestSpawnCancel_LoopStopsWhenThreadCancelled(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()

	wf, err := wfyaml.ParseWorkflow([]byte(spawnCancelLoopYAML))
	require.NoError(t, err)
	wfJSON, err := protojson.Marshal(wf)
	require.NoError(t, err)

	env.RegisterActivityWithOptions(
		func(_ context.Context, _ map[string]string) (LoadedWorkflow, error) {
			return LoadedWorkflow{WorkflowJSON: wfJSON}, nil
		},
		activity.RegisterOptions{Name: "ActivityLoadWorkflow"},
	)
	for _, name := range []string{"WorkflowStatus", "WorkflowCheckpoint", "WorkflowError", "EmitToolCallStatus", "ThreadStatus"} {
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

	// Counts turns the spawned loop actually took. A cancelled thread must take
	// ZERO: the check sits at the top of the loop body, before any dispatch.
	var turns int32
	env.RegisterActivityWithOptions(
		func(_ context.Context, _ types.ActivityInput) (map[string]interface{}, error) {
			atomic.AddInt32(&turns, 1)
			return map[string]interface{}{
				"response_text": "ok",
				"tool_calls":    nil,
				"keep_going":    true, // would loop forever if nothing stopped it
				"message":       map[string]interface{}{"role": "assistant", "text": "ok"},
			}, nil
		},
		activity.RegisterOptions{Name: "CallLLM"},
	)

	// Cancel the thread BEFORE the loop starts, which is the state the loop
	// sees when a user cancels an async spawn that is between turns.
	const chatID = "chat-spawn-cancel"
	const thread = "wf-" + chatID
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(CancelThreadSignalName, CancelThreadSignal{Thread: thread})
	}, 0)

	env.ExecuteWorkflow(DynamicWorkflow, WorkflowInput{
		ChatID:       chatID,
		WorkflowName: "spawn-cancel-loop",
		Inputs:       map[string]interface{}{},
		ExecContext: &ExecutionContext{
			WorkflowID:   thread,
			ChatID:       chatID,
			Thread:       thread,
			ThreadMode:   model.ThreadModeNew,
			WorkflowName: "spawn-cancel-loop",
		},
	})

	require.True(t, env.IsWorkflowCompleted(),
		"a cancelled thread must terminate, not spin forever")
	require.LessOrEqual(t, atomic.LoadInt32(&turns), int32(1),
		"a cancelled thread's loop must stop at the boundary — it kept taking turns, "+
			"which is why clicking cancel on an async spawn did nothing")
}
