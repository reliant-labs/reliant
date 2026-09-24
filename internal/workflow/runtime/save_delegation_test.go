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

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	rtemporal "github.com/reliant-labs/reliant/internal/temporal"
	"github.com/reliant-labs/reliant/internal/workflow/core"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// Rule 1 through the real StepExecutor and the real ActivityWrapper: who
// executes a node writes its save_message.
//
//   - call_llm and a regular-tools execute_tools are executed by an activity,
//     so the wrapper writes the message and the workflow dispatches no
//     SaveMessage activity.
//   - an execute_tools batch containing ask_user (or spawn) is assembled by
//     the workflow, so the workflow writes the message via SaveMessage.

type delegationHarness struct {
	env           *testsuite.TestWorkflowEnvironment
	writer        *recordingMessageWriter
	saveActivity  int32
	savedToolRows int32
}

func newDelegationHarness(t *testing.T) *delegationHarness {
	t.Helper()
	var suite testsuite.WorkflowTestSuite
	h := &delegationHarness{env: suite.NewTestWorkflowEnvironment(), writer: &recordingMessageWriter{}}
	h.env.SetDataConverter(rtemporal.NewFlexibleDataConverter())
	registerFinalizeCapture(h.env)

	registry := NewActivityRegistry(&wrapperTestRepo{})
	registry.SetMessageWriter(h.writer)
	registerWrapped(h.env, registry, "CallLLM",
		func(context.Context, types.ActivityInput) (*reliantv1.CallLLMOutput, error) {
			return thinkingCallLLMOutput(), nil
		})
	registerWrapped(h.env, registry, "ExecuteTools",
		func(_ context.Context, in types.ActivityInput) (*reliantv1.ExecuteToolsOutput, error) {
			var results []*reliantv1.ToolResultMsg
			for _, tc := range in.Node.GetExecuteTools().GetResolvedToolCalls() {
				results = append(results, &reliantv1.ToolResultMsg{ToolCallId: tc.GetId(), Name: tc.GetName(), Content: "file contents"})
			}
			return &reliantv1.ExecuteToolsOutput{ToolResults: results}, nil
		})

	h.env.RegisterActivityWithOptions(
		func(_ context.Context, in types.ActivityInput) (*reliantv1.SaveMessageOutput, error) {
			atomic.AddInt32(&h.saveActivity, 1)
			if len(in.Node.GetSaveMessageNode().GetResolvedToolResults()) > 0 {
				atomic.AddInt32(&h.savedToolRows, 1)
			}
			return &reliantv1.SaveMessageOutput{MessageId: "msg-wf-save"}, nil
		},
		activity.RegisterOptions{Name: "SaveMessage"},
	)
	return h
}

func newDelegationExecutor(ctx workflow.Context, inputs map[string]interface{}) *StepExecutor {
	workflowID := workflow.GetInfo(ctx).WorkflowExecution.ID
	return NewStepExecutor(ctx, workflowID, "chat-1", "agent", inputs, map[string]interface{}{},
		&ChildWorkflowTracker{children: make(map[string]bool)},
	).WithExecContext(&ExecutionContext{WorkflowID: workflowID, ChatID: "chat-1", Thread: "thread-1"})
}

func runStep(executor *StepExecutor, node *reliantv1.Node) *StepEvent {
	running := executor.Start(&core.TriggeredNode{
		Node:  node,
		Event: &core.WorkflowEvent{ID: "evt", WorkflowID: executor.workflowID, ChatID: "chat-1"},
	})
	return executor.HandleCompletion(running)
}

func toolSaveConfig() *reliantv1.SaveMessageConfig {
	return &reliantv1.SaveMessageConfig{
		Role:        celLit("tool"),
		ToolResults: celExpr("{{output.tool_results}}"),
	}
}

func TestSaveDelegation_AgentTurnSavesInWorker(t *testing.T) {
	t.Parallel()
	h := newDelegationHarness(t)

	h.env.ExecuteWorkflow(func(ctx workflow.Context) (map[string]interface{}, error) {
		ctx = WithStreamIDTracker(ctx, NewStreamIDTracker())
		executor := newDelegationExecutor(ctx, map[string]interface{}{})

		llm := callLLMTestNode("call_llm")
		llm.SaveMessage = agentAssistantSave()
		llmEvent := runStep(executor, llm)
		if llmEvent.Error != nil {
			return nil, llmEvent.Error
		}

		tools := &reliantv1.Node{
			Id:          "execute_tools",
			Type:        model.NodeTypeExecuteTools,
			SaveMessage: toolSaveConfig(),
			Args: &reliantv1.Node_ExecuteTools{ExecuteTools: &reliantv1.ExecuteToolsArgs{
				ToolCalls: celExpr("{{nodes.call_llm.tool_calls}}"),
			}},
		}
		toolsEvent := runStep(executor, tools)
		if toolsEvent.Error != nil {
			return nil, toolsEvent.Error
		}
		return llmEvent.Data, nil
	})

	require.True(t, h.env.IsWorkflowCompleted())
	require.NoError(t, h.env.GetWorkflowError())

	require.Zero(t, atomic.LoadInt32(&h.saveActivity),
		"call_llm and regular-tools execute_tools must not dispatch a SaveMessage activity")

	msgs := h.writer.messages()
	require.Len(t, msgs, 2, "the worker writes both the assistant row and the tool row")
	require.Equal(t, "assistant", msgs[0].Args.GetResolvedRole())
	require.Equal(t, "call_llm-save", msgs[0].Runtime.StepID)
	require.NotNil(t, msgs[0].Args.GetResolvedThinking(), "thinking is persisted with the message")
	require.Equal(t, "tool", msgs[1].Args.GetResolvedRole())
	require.Equal(t, "execute_tools-save", msgs[1].Runtime.StepID)
	require.Len(t, msgs[1].Args.GetResolvedToolResults(), 1)
	require.Equal(t, "file contents", msgs[1].Args.GetResolvedToolResults()[0].GetContent())

	var llmOutput map[string]interface{}
	require.NoError(t, h.env.GetWorkflowResult(&llmOutput))
	thinking, _ := llmOutput["thinking"].(map[string]interface{})
	require.Empty(t, thinking["signature"], "the workflow never receives the thinking signature")
	require.Empty(t, thinking["content"], "the workflow never receives the thinking content")
}

func TestSaveDelegation_MixedBatchSavesInWorkflow(t *testing.T) {
	t.Parallel()
	h := newDelegationHarness(t)

	h.env.ExecuteWorkflow(func(ctx workflow.Context) error {
		// Unattended: ask_user resolves inline with no QuestionCreate, so the
		// batch needs no question machinery — it is still assembled
		// workflow-side, which is what decides who saves.
		executor := newDelegationExecutor(ctx, map[string]interface{}{InputKeyUnattended: true})
		executor.nodeOutputs["call_llm"] = map[string]interface{}{
			"tool_calls": []interface{}{
				map[string]interface{}{"id": "tc1", "name": "view", "input": `{"file_path":"a.go"}`},
				map[string]interface{}{"id": "tc2", "name": "ask_user", "input": `{"questions":[]}`},
			},
		}
		tools := &reliantv1.Node{
			Id:          "execute_tools",
			Type:        model.NodeTypeExecuteTools,
			SaveMessage: toolSaveConfig(),
			Args: &reliantv1.Node_ExecuteTools{ExecuteTools: &reliantv1.ExecuteToolsArgs{
				ToolCalls: celExpr("{{nodes.call_llm.tool_calls}}"),
			}},
		}
		ev := runStep(executor, tools)
		return ev.Error
	})

	require.True(t, h.env.IsWorkflowCompleted())
	require.NoError(t, h.env.GetWorkflowError())
	require.Empty(t, h.writer.messages(),
		"the regular-tools half of a mixed batch must not save on its own — its result is only part of the node's output")
	require.Equal(t, int32(1), atomic.LoadInt32(&h.saveActivity),
		"a batch assembled workflow-side is saved by the workflow, once")
	require.Equal(t, int32(1), atomic.LoadInt32(&h.savedToolRows))
}
