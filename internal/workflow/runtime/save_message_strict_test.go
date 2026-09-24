// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/encoding/protojson"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	types "github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// A save_message the workflow performs itself is as strict as one the
// ActivityWrapper performs: a save that cannot be resolved fails the node
// instead of being logged and swallowed (which silently dropped the message
// while the run carried on as if it had been written).

// StepExecutor path: an execute_tools batch with ask_user is assembled — and
// therefore saved — by the workflow.
func TestWorkflowSideSave_ResolutionErrorFailsStep(t *testing.T) {
	t.Parallel()
	h := newDelegationHarness(t)

	var stepErr error
	var retryExhausted bool
	h.env.ExecuteWorkflow(func(ctx workflow.Context) error {
		executor := newDelegationExecutor(ctx, map[string]interface{}{InputKeyUnattended: true})
		executor.nodeOutputs["call_llm"] = map[string]interface{}{
			"tool_calls": []interface{}{
				map[string]interface{}{"id": "tc1", "name": "view", "input": `{"file_path":"a.go"}`},
				map[string]interface{}{"id": "tc2", "name": "ask_user", "input": `{"questions":[]}`},
			},
		}
		tools := &reliantv1.Node{
			Id:   "execute_tools",
			Type: model.NodeTypeExecuteTools,
			SaveMessage: &reliantv1.SaveMessageConfig{
				Role:    celLit("user"),
				Content: celExpr("{{output.no_such_field.x}}"),
			},
			Args: &reliantv1.Node_ExecuteTools{ExecuteTools: &reliantv1.ExecuteToolsArgs{
				ToolCalls: celExpr("{{nodes.call_llm.tool_calls}}"),
			}},
		}
		ev := runStep(executor, tools)
		stepErr = ev.Error
		retryExhausted = ev.RetryExhausted
		return nil
	})

	require.True(t, h.env.IsWorkflowCompleted())
	require.NoError(t, h.env.GetWorkflowError())
	require.Error(t, stepErr, "a workflow-side save that cannot resolve must fail the step")
	require.Contains(t, stepErr.Error(), "save_message")
	require.True(t, retryExhausted, "surfaces through the retry-exhaustion pause path, like any failed step")
	require.Zero(t, atomic.LoadInt32(&h.saveActivity), "nothing is written")
}

// Loop node path: a loop's own save_message (evaluated against the loop's
// output, by the workflow) that fails must fail the run.
const loopSaveFailsYAML = `
name: loop-save-fails
entry: [outer_loop]
nodes:
  - id: outer_loop
    type: loop
    while: "false"
    save_message:
      role: user
      content: "{{output.no_such_field.x}}"
    inline:
      entry: [work]
      nodes:
        - id: work
          type: call_llm
edges: []
`

func TestWorkflowSideSave_LoopNodeSaveErrorFailsRun(t *testing.T) {
	t.Parallel()
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()

	wf, err := wfyaml.ParseWorkflow([]byte(loopSaveFailsYAML))
	require.NoError(t, err)
	wfJSON, err := protojson.Marshal(wf)
	require.NoError(t, err)

	env.RegisterActivityWithOptions(
		func(_ context.Context, _ map[string]string) (LoadedWorkflow, error) {
			return LoadedWorkflow{WorkflowJSON: wfJSON}, nil
		},
		activity.RegisterOptions{Name: "ActivityLoadWorkflow"},
	)
	for _, name := range []string{"WorkflowStatus", "WorkflowCheckpoint", "WorkflowError", "EmitToolCallStatus", "Cleanup"} {
		env.RegisterActivityWithOptions(
			func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
				return map[string]interface{}{"success": true}, nil
			},
			activity.RegisterOptions{Name: name},
		)
	}
	var saves int32
	env.RegisterActivityWithOptions(
		func(_ context.Context, _ types.ActivityInput) (map[string]interface{}, error) {
			atomic.AddInt32(&saves, 1)
			return map[string]interface{}{"message_id": "m"}, nil
		},
		activity.RegisterOptions{Name: "SaveMessage"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, _ types.ActivityInput) (map[string]interface{}, error) {
			return map[string]interface{}{
				"response_text": "done",
				"message":       map[string]interface{}{"role": "assistant", "text": "done"},
			}, nil
		},
		activity.RegisterOptions{Name: "CallLLM"},
	)

	env.ExecuteWorkflow(DynamicWorkflow, nestedPauseInput("chat-loop-save-fails"))

	require.True(t, env.IsWorkflowCompleted())
	require.Error(t, env.GetWorkflowError(), "a loop node's failed save_message must fail the run, not be swallowed")
	require.Contains(t, env.GetWorkflowError().Error(), "save_message")
	require.Zero(t, atomic.LoadInt32(&saves))
}
