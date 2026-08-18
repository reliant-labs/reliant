package runtime

import (
	"context"
	"errors"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/core"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

func TestNormalizeOutput_MergesSnakeCaseDefaults(t *testing.T) {
	executor := &StepExecutor{}
	rawOutput := map[string]interface{}{
		"response_text": "hello",
		"token_count":   float64(42),
		"tool_calls":    []interface{}{},
	}

	normalized := executor.normalizeOutput(rawOutput, "CallLLM")

	assert.Equal(t, "hello", normalized["response_text"])
	assert.Equal(t, float64(42), normalized["token_count"])
	require.Contains(t, normalized, "tool_calls")
	require.Contains(t, normalized, "message")
	// message gets default nested keys (role, text) from withRequiredActivityOutputFields
	assert.Equal(t, map[string]interface{}{"role": "", "text": ""}, normalized["message"])
}

func TestNormalizeOutput_CallLLMAddsMissingToolCallsField(t *testing.T) {
	executor := &StepExecutor{}
	rawOutput := map[string]interface{}{
		"message":      map[string]interface{}{"role": "assistant", "text": "ok"},
		"responseText": "ok",
		"tokenCount":   float64(10),
		"thinking":     map[string]interface{}{},
	}

	normalized := executor.normalizeOutput(rawOutput, "CallLLM")

	require.Contains(t, normalized, "tool_calls")
	assert.Equal(t, []interface{}{}, normalized["tool_calls"])
}

func TestEnsureStepEventRoutable(t *testing.T) {
	t.Run("nil step event returns explicit error", func(t *testing.T) {
		err := EnsureStepEventRoutable(nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "step event is nil")
	})

	t.Run("failed step event returns underlying step error", func(t *testing.T) {
		stepErr := errors.New("execute_tools failed")
		err := EnsureStepEventRoutable(&StepEvent{Error: stepErr})
		require.Error(t, err)
		assert.ErrorIs(t, err, stepErr)
	})

	t.Run("successful step event is routable", func(t *testing.T) {
		err := EnsureStepEventRoutable(&StepEvent{StepID: "execute_tools", Data: map[string]interface{}{"ok": true}})
		require.NoError(t, err)
	})
}

type interruptSettlementResult struct {
	StepHadError bool
	Saved        bool
	SaveRole     string
	SaveContent  string
	PendingInbox bool
}

type canceledDetailsFuture struct {
	err error
}

func (f canceledDetailsFuture) Get(workflow.Context, interface{}) error { return f.err }
func (f canceledDetailsFuture) IsReady() bool                           { return true }

func stepExecutorInterruptSettlesWorkflow(ctx workflow.Context) (interruptSettlementResult, error) {
	workflowID := workflow.GetInfo(ctx).WorkflowExecution.ID
	thread := "thread-interrupt-settle"
	interrupt := &ThreadInterrupt{
		coordinator: &ThreadInterruptCoordinator{states: map[string]*threadInterruptState{
			thread: {epoch: 1},
		}},
		thread: thread,
	}
	executor := NewStepExecutor(
		ctx,
		workflowID,
		"chat-interrupt-settle",
		"interrupt-settle",
		map[string]interface{}{},
		map[string]interface{}{},
		&ChildWorkflowTracker{children: make(map[string]bool)},
	).WithExecContext(&ExecutionContext{
		WorkflowID: workflowID,
		ChatID:     "chat-interrupt-settle",
		Thread:     thread,
	}).WithThreadInterrupts(interrupt)

	node := &reliantv1.Node{
		Id:      "call_llm",
		Type:    model.NodeTypeCallLLM,
		Timeout: celLiteral("10s"),
		Args:    &reliantv1.Node_CallLlm{CallLlm: &reliantv1.CallLLMArgs{}},
		SaveMessage: &reliantv1.SaveMessageConfig{
			Role:    celLiteral("{{output.message.role}}"),
			Content: celLiteral("{{output.message.text}}"),
		},
	}
	details := map[string]interface{}{
		"response_text":   "partial answer",
		"pending_inbox":   true,
		"tool_calls":      []interface{}{},
		"token_count":     0,
		"message_id":      "assistant-msg-1",
		"last_stream_seq": 2,
		"message": map[string]interface{}{
			"role": "assistant",
			"text": "partial answer",
		},
	}
	running := &RunningStep{
		ActivityID:            node.GetId(),
		StepID:                node.GetId(),
		ActivityName:          "CallLLM",
		Node:                  node,
		Event:                 &core.WorkflowEvent{ID: "event-start", WorkflowID: workflowID, ChatID: "chat-interrupt-settle"},
		Future:                canceledDetailsFuture{err: temporal.NewCanceledError(details)},
		ThreadInterruptEpoch:  0,
		PreallocatedMessageID: "",
	}
	stepEvent := executor.HandleCompletion(running)

	result := interruptSettlementResult{StepHadError: stepEvent.Error != nil}
	if stepEvent.Data != nil {
		result.PendingInbox, _ = stepEvent.Data["pending_inbox"].(bool)
	}
	return result, nil
}

func TestStepExecutor_InterruptedCallLLMSurfacesCancellation(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	saved := &interruptSettlementResult{}

	env.RegisterActivityWithOptions(
		func(_ context.Context, input types.ActivityInput) (map[string]interface{}, error) {
			args := input.Node.GetSaveMessageNode()
			saved.Saved = true
			saved.SaveRole = args.GetResolvedRole()
			saved.SaveContent = args.GetResolvedContent()
			return map[string]interface{}{"message_id": "saved-msg-1"}, nil
		},
		activity.RegisterOptions{Name: "SaveMessage"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, _ types.EmitStreamFinalizedInput) (types.EmitStreamFinalizedOutput, error) {
			return types.EmitStreamFinalizedOutput{Success: true}, nil
		},
		activity.RegisterOptions{Name: "EmitStreamFinalized"},
	)
	env.ExecuteWorkflow(stepExecutorInterruptSettlesWorkflow)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var result interruptSettlementResult
	require.NoError(t, env.GetWorkflowResult(&result))
	// The step no longer settles from a cancelled activity's payload. The
	// workflow sees the CanceledError, re-dispatches, and the turn's partial is
	// already durable because call_llm wrote it itself (persistInterruptedTurn)
	// -- which is what let the 1-3s WaitForCancellation stall be removed.
	require.True(t, result.StepHadError,
		"a cancelled call_llm surfaces the cancellation so the workflow can re-dispatch")
	require.False(t, saved.Saved,
		"inline save_message must not run for a step that never produced output")
}
