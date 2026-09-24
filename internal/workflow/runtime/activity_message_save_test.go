// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	rtemporal "github.com/reliant-labs/reliant/internal/temporal"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// ============================================================================
// Rule 1 at the wrapper: a node's save_message is written by the worker that
// executes the node, right after the activity returns.
// Rule 2 at the wrapper: message-only fields are persisted, then stripped.
// ============================================================================

// agentAssistantSave is agent.yaml's call_llm save_message.
func agentAssistantSave() *reliantv1.SaveMessageConfig {
	return &reliantv1.SaveMessageConfig{
		Role:      celExpr("{{output.message.role}}"),
		Content:   celExpr("{{output.message.text}}"),
		ToolCalls: celExpr("{{output.tool_calls}}"),
	}
}

func callLLMInput(save *types.SaveMessageRequest) types.ActivityInput {
	return types.ActivityInput{
		Runtime: types.RuntimeContext{
			ChatID:             "chat-1",
			Thread:             "thread-1",
			WorkflowID:         "wf-1",
			StepID:             "call_llm",
			AssistantMessageID: "msg-prealloc",
			LoopNodeID:         "agent_loop",
			LoopIteration:      2,
			SaveMessage:        save,
		},
		Node: &reliantv1.Node{Id: "call_llm", Type: model.NodeTypeCallLLM},
	}
}

func saveRequest(config *reliantv1.SaveMessageConfig, inputs map[string]interface{}) *types.SaveMessageRequest {
	if inputs == nil {
		inputs = map[string]interface{}{}
	}
	inputs["thread"] = "thread-1"
	return &types.SaveMessageRequest{
		Config:    config,
		Inputs:    inputs,
		Workflow:  model.WorkflowContext{ID: "wf-1", Name: "agent"},
		AgentName: "builtin://agent",
	}
}

// thinkingCallLLMOutput is a CallLLM result carrying a (large) thinking block.
func thinkingCallLLMOutput() *reliantv1.CallLLMOutput {
	return &reliantv1.CallLLMOutput{
		Message:      &reliantv1.MessageOutput{Role: "assistant", Text: "Looking at it."},
		ResponseText: "Looking at it.",
		ToolCalls:    []*reliantv1.ToolCallMsg{{Id: "tc1", Name: "view", Input: `{"file_path":"a.go"}`}},
		TokenCount:   1234,
		Cost:         0.5,
		Model:        "claude-test",
		Thinking: &reliantv1.ThinkingOutput{
			Content:   "reasoning",
			Signature: strings.Repeat("s", 4096),
		},
	}
}

type wrapperHarness struct {
	env    *testsuite.TestActivityEnvironment
	repo   *wrapperTestRepo
	writer *recordingMessageWriter
}

func newWrapperHarness(t *testing.T, name string, fn func(context.Context, types.ActivityInput) (*reliantv1.CallLLMOutput, error)) *wrapperHarness {
	t.Helper()
	var suite testsuite.WorkflowTestSuite
	h := &wrapperHarness{
		env:    suite.NewTestActivityEnvironment(),
		repo:   &wrapperTestRepo{},
		writer: &recordingMessageWriter{},
	}
	h.env.SetDataConverter(rtemporal.NewFlexibleDataConverter())
	registry := NewActivityRegistry(h.repo)
	registry.SetMessageWriter(h.writer)
	registerWrapped(h.env, registry, name, fn)
	return h
}

func (h *wrapperHarness) run(t *testing.T, name string, input types.ActivityInput) (map[string]interface{}, error) {
	t.Helper()
	val, err := h.env.ExecuteActivity(name, input)
	if err != nil {
		return nil, err
	}
	var out map[string]interface{}
	require.NoError(t, val.Get(&out))
	return out, nil
}

func TestWrapperSave_WritesMessageAndStripsThinking(t *testing.T) {
	t.Parallel()
	h := newWrapperHarness(t, "CallLLM", func(context.Context, types.ActivityInput) (*reliantv1.CallLLMOutput, error) {
		return thinkingCallLLMOutput(), nil
	})

	out, err := h.run(t, "CallLLM", callLLMInput(saveRequest(agentAssistantSave(), nil)))
	require.NoError(t, err)

	// Rule 1: the worker wrote the message, through the injected writer.
	msgs := h.writer.messages()
	require.Len(t, msgs, 1, "the wrapper must write the node's message itself")
	m := msgs[0]
	assert.Equal(t, "assistant", m.Args.GetResolvedRole())
	assert.Equal(t, "Looking at it.", m.Args.GetResolvedContent())
	require.Len(t, m.Args.GetResolvedToolCalls(), 1)
	assert.Equal(t, "tc1", m.Args.GetResolvedToolCalls()[0].GetId())
	assert.Equal(t, int32(1234), m.Args.GetTokenCount())
	assert.Equal(t, "claude-test", m.Args.GetResolvedModel())
	assert.Equal(t, "builtin://agent", m.Args.GetResolvedAgent())
	// Rule 2, persisted half: thinking reached the message.
	require.NotNil(t, m.Args.GetResolvedThinking())
	assert.Equal(t, "reasoning", m.Args.GetResolvedThinking().GetContent())
	assert.Len(t, m.Args.GetResolvedThinking().GetSignature(), 4096)

	// Identity: same runtime identifiers the SaveMessage activity used.
	assert.Equal(t, "chat-1", m.Runtime.ChatID)
	assert.Equal(t, "thread-1", m.Runtime.Thread)
	assert.Equal(t, "wf-1", m.Runtime.WorkflowID)
	assert.Equal(t, "call_llm-save", m.Runtime.StepID)
	assert.Equal(t, "msg-prealloc", m.Runtime.AssistantMessageID, "assistant rows converge on the pre-allocated id")
	assert.Equal(t, "agent_loop", m.Runtime.LoopNodeID)
	assert.Equal(t, 2, m.Runtime.LoopIteration)
	assert.True(t, strings.HasSuffix(m.IdempotencyKey, "-save"), "key %q", m.IdempotencyKey)
	assert.True(t, strings.HasPrefix(m.IdempotencyKey, "wf-1-"), "key %q", m.IdempotencyKey)
	assert.Equal(t, int32(1), m.Attempt)

	// Rule 2, stripped half: the workflow's view has no thinking.
	_, hasThinking := out["thinking"]
	assert.False(t, hasThinking, "message-only thinking must not be returned to the workflow: %v", out)
	assert.Equal(t, "Looking at it.", out["response_text"], "other fields are untouched")

	// The UI keys on a "<node>-save" step row carrying message_id.
	var saveRow bool
	for _, row := range h.repo.stepRows() {
		if row.StepID == "call_llm-save" {
			saveRow = true
			assert.Equal(t, "SaveMessage", row.ActivityName)
			assert.Contains(t, row.OutputJSON.String, `"message_id":"msg-call_llm-save"`)
		}
	}
	assert.True(t, saveRow, "a <node>-save step_executions row must be written")
}

// An interrupted turn (pause, thread interrupt, cancel) returns its partial
// with a nil error on a cancelled context — CallLLM persists that partial
// itself (persistInterruptedTurn) and the workflow discards a cancelled
// activity's result. The wrapper must not save it again: the write would run
// on a dead context, fail, and surface as a chat error on a user's own pause.
func TestWrapperSave_CancelledActivityDoesNotSave(t *testing.T) {
	t.Parallel()
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestActivityEnvironment()
	env.SetDataConverter(rtemporal.NewFlexibleDataConverter())
	repo := &wrapperTestRepo{}
	writer := &recordingMessageWriter{}
	registry := NewActivityRegistry(repo)
	registry.SetMessageWriter(writer)

	ctx, cancel := context.WithCancel(context.Background())
	env.SetTestTimeout(10 * time.Second)
	env.SetWorkerOptions(worker.Options{BackgroundActivityContext: ctx})
	registerWrapped(env, registry, "CallLLM", func(context.Context, types.ActivityInput) (*reliantv1.CallLLMOutput, error) {
		cancel() // the interrupt lands mid-stream
		out := thinkingCallLLMOutput()
		out.Aborted = true
		return out, nil
	})

	_, _ = env.ExecuteActivity("CallLLM", callLLMInput(saveRequest(agentAssistantSave(), nil)))
	assert.Empty(t, writer.messages(), "a cancelled activity's partial is persisted by the activity, not re-saved by the wrapper")
	for _, row := range repo.stepRows() {
		assert.NotEqual(t, "call_llm-save", row.StepID, "no <node>-save row for a cancelled activity")
	}
}

func TestWrapperSave_NoRequestWritesNothingButStillStripsThinking(t *testing.T) {
	t.Parallel()
	h := newWrapperHarness(t, "CallLLM", func(context.Context, types.ActivityInput) (*reliantv1.CallLLMOutput, error) {
		return thinkingCallLLMOutput(), nil
	})

	out, err := h.run(t, "CallLLM", callLLMInput(nil))
	require.NoError(t, err)
	assert.Empty(t, h.writer.messages(), "no request, no write")
	_, hasThinking := out["thinking"]
	assert.False(t, hasThinking, "thinking is stripped even for a node with no save_message")
}

func TestWrapperSave_Skips(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		config *reliantv1.SaveMessageConfig
		output *reliantv1.CallLLMOutput
		inputs map[string]interface{}
	}{
		"condition false": {
			config: &reliantv1.SaveMessageConfig{
				Condition: &reliantv1.DirectCelBool{Expr: "inputs.context_bridge != 'none'"},
				Role:      celLit("assistant"),
				Content:   celExpr("{{output.response_text}}"),
			},
			output: thinkingCallLLMOutput(),
			inputs: map[string]interface{}{"context_bridge": "none"},
		},
		"empty role": {
			config: &reliantv1.SaveMessageConfig{Content: celExpr("{{output.response_text}}")},
			output: thinkingCallLLMOutput(),
		},
		"content-free assistant": {
			config: agentAssistantSave(),
			output: &reliantv1.CallLLMOutput{Message: &reliantv1.MessageOutput{Role: "assistant"}},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newWrapperHarness(t, "CallLLM", func(context.Context, types.ActivityInput) (*reliantv1.CallLLMOutput, error) {
				return tc.output, nil
			})
			_, err := h.run(t, "CallLLM", callLLMInput(saveRequest(tc.config, tc.inputs)))
			require.NoError(t, err, "a declined save is not a failure")
			assert.Empty(t, h.writer.messages())
			for _, row := range h.repo.stepRows() {
				assert.NotEqual(t, "call_llm-save", row.StepID, "no save row for a declined save")
			}
		})
	}
}

func TestWrapperSave_WriteErrorRetriedThenSucceeds(t *testing.T) {
	t.Parallel()
	restore := messageWriteBackoff
	messageWriteBackoff = time.Millisecond
	t.Cleanup(func() { messageWriteBackoff = restore })

	h := newWrapperHarness(t, "CallLLM", func(context.Context, types.ActivityInput) (*reliantv1.CallLLMOutput, error) {
		return thinkingCallLLMOutput(), nil
	})
	h.writer.failures = 2
	h.writer.failErr = errors.New("connection reset by peer")

	_, err := h.run(t, "CallLLM", callLLMInput(saveRequest(agentAssistantSave(), nil)))
	require.NoError(t, err, "transient write failures are retried inside the wrapper")
	assert.Len(t, h.writer.messages(), 1)
	assert.Equal(t, 3, h.writer.calls)
}

func TestWrapperSave_WriteErrorSurfacesAfterBudget(t *testing.T) {
	// Not parallel: shrinks the package-level retry budget.
	restoreBackoff := messageWriteBackoff
	messageWriteBackoff = time.Millisecond
	t.Cleanup(func() { messageWriteBackoff = restoreBackoff })

	h := newWrapperHarness(t, "CallLLM", func(context.Context, types.ActivityInput) (*reliantv1.CallLLMOutput, error) {
		return thinkingCallLLMOutput(), nil
	})
	h.writer.failures = 1 << 30
	h.writer.failErr = errors.New("connection reset by peer")

	start := time.Now()
	_, err := h.run(t, "CallLLM", callLLMInput(saveRequest(agentAssistantSave(), nil)))
	require.Error(t, err, "a message that could not be written must fail the activity")
	assert.Contains(t, err.Error(), "connection reset by peer")
	assert.Greater(t, h.writer.calls, 1, "the write is retried before giving up")
	assert.Less(t, time.Since(start), messageWriteBudget+5*time.Second)
}

func TestWrapperSave_ResolutionErrorIsNonRetryable(t *testing.T) {
	t.Parallel()
	h := newWrapperHarness(t, "CallLLM", func(context.Context, types.ActivityInput) (*reliantv1.CallLLMOutput, error) {
		return thinkingCallLLMOutput(), nil
	})
	bad := &reliantv1.SaveMessageConfig{Role: celLit("assistant"), Content: celExpr("{{output.no_such_field.x}}")}
	_, err := h.run(t, "CallLLM", callLLMInput(saveRequest(bad, nil)))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SaveMessageResolution")
	assert.Empty(t, h.writer.messages())
}

// The map the wrapper evaluates save_message against must be exactly the map
// the workflow sees for the same result — otherwise a template means
// something different depending on who performs the save.
func TestActivityResultMap_ParityWithWorkflowView(t *testing.T) {
	t.Parallel()
	result := &reliantv1.CallLLMOutput{
		Message:      &reliantv1.MessageOutput{Role: "assistant"},
		ResponseText: "hi",
		ToolCalls:    []*reliantv1.ToolCallMsg{{Id: "tc1", Name: "view"}},
		TokenCount:   7,
	}

	// The workflow's view: the production data converter's payload, decoded
	// into a map (StepExecutor.getRawOutput), then normalized.
	dc := rtemporal.NewFlexibleDataConverter()
	payload, err := dc.ToPayload(result)
	require.NoError(t, err)
	var decoded map[string]interface{}
	require.NoError(t, dc.FromPayload(payload, &decoded))
	workflowView := normalizeActivityOutput(decoded, "CallLLM")

	wrapperRaw, err := activityResultMap(result)
	require.NoError(t, err)
	wrapperView := normalizeActivityOutput(wrapperRaw, "CallLLM")

	assert.Equal(t, workflowView, wrapperView)
}

func TestSaveMessageRequestFrom_RunStepFlatMap(t *testing.T) {
	t.Parallel()
	req := saveRequest(&reliantv1.SaveMessageConfig{Role: celLit("tool")}, nil)
	input := map[string]interface{}{"step_id": "lint", types.RunStepSaveMessageKey: req}
	got, err := saveMessageRequestFrom(input)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "tool", got.Config.GetRole().GetLiteral())
	assert.Equal(t, "thread-1", got.Inputs["thread"])
}

func TestClearMessageOnlyFields(t *testing.T) {
	t.Parallel()
	out := thinkingCallLLMOutput()
	clearMessageOnlyFields(&out)
	assert.Nil(t, out.GetThinking())
	assert.Equal(t, "Looking at it.", out.GetResponseText())

	// Non-proto results are left alone.
	m := map[string]interface{}{"thinking": "x"}
	clearMessageOnlyFields(&m)
	assert.Equal(t, "x", m["thinking"])
}
