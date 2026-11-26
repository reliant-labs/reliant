// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/store"
)

// A call the workflow refused is answered with the reason and recorded
// FAILED, and never reaches the executor: an activation change the person
// attending did not approve (runtime/activation_approval.go).
func TestExecuteTools_RefusesCallTheWorkflowRefused(t *testing.T) {
	t.Parallel()

	const slackPost = "slack__message_post"
	h, chatID := newValidationChat(t)
	mockExecutor := newMockToolExecutor()
	activity := NewExecuteToolsActivity(ExecuteToolsDeps{Store: tools.WithChats(h.Repo()), Rows: workspaceRowsOf(h.Repo()), ToolCalls: toolCallsOf(h.Repo()), ToolExecutor: mockExecutor, Preemption: store.NewAgentMessageExtStore(h.Repo().ORM())})
	refusedID := "toolu_" + uuid.NewString()
	readID := "toolu_" + uuid.NewString()
	reason := "The user did not approve slack__message_post, so it was not run. Do not attempt it another way."

	var output ExecuteToolsOutput
	require.NoError(t, h.ExecuteActivity(activity.Execute, ExecuteToolsInput{
		ChatID: chatID,
		Thread: chatID,
		Capabilities: (&tools.Capabilities{
			Offered:    []string{slackPost, tools.ToolView},
			Permission: tools.PermissionMutating,
		}).Proto(),
		ToolCalls: []message.ToolCall{
			{ID: refusedID, Name: slackPost, Input: `{"channel":"#general","text":"hi"}`},
			{ID: readID, Name: tools.ToolView, Input: `{"file_path":"/tmp/x"}`},
		},
		ActivationRefusals: map[string]string{refusedID: reason},
	}, &output))

	require.Len(t, output.ToolResults, 2)
	refused := output.ToolResults[0]
	assert.Equal(t, refusedID, refused.ToolCallId)
	assert.True(t, refused.IsError)
	assert.Equal(t, reason, refused.Content)
	assert.Equal(t, 0, mockExecutor.GetExecutionCount(refusedID), "a refused call is never executed")
	assert.Equal(t, 1, mockExecutor.GetExecutionCount(readID), "the rest of the batch still runs")

	call, err := h.Repo().GetToolCall(context.Background(), refusedID)
	require.NoError(t, err)
	assert.Equal(t, core.ToolCallStatusFailed, call.Status)
	require.NotNil(t, call.ErrorMessage)
	assert.Equal(t, reason, *call.ErrorMessage)
}

// An ExecuteTools activity an earlier release scheduled, carrying a call the
// person denied at the removed integration action approval prompt in the
// removed refused_tool_calls field, still never runs that call on this worker.
func TestExecuteTools_RefusesCallAnEarlierReleaseRefused(t *testing.T) {
	t.Parallel()

	const slackPost = "slack__message_post"
	h, chatID := newValidationChat(t)
	mockExecutor := newMockToolExecutor()
	activity := NewExecuteToolsActivity(tools.WithChats(h.Repo()), workspaceRowsOf(h.Repo()), toolCallsOf(h.Repo()), mockExecutor)
	refusedID := "toolu_" + uuid.NewString()
	readID := "toolu_" + uuid.NewString()
	reason := "The user did not approve slack__message_post, so it was not run. Do not attempt it another way."

	raw, err := json.Marshal(ExecuteToolsInput{
		ChatID: chatID,
		Thread: chatID,
		Capabilities: (&tools.Capabilities{
			Offered:    []string{slackPost, tools.ToolView},
			Permission: tools.PermissionMutating,
		}).Proto(),
		ToolCalls: []message.ToolCall{
			{ID: refusedID, Name: slackPost, Input: `{"channel":"#general","text":"hi"}`},
			{ID: readID, Name: tools.ToolView, Input: `{"file_path":"/tmp/x"}`},
		},
	})
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(raw, &payload))
	args := payload["node"].(map[string]any)["execute_tools"].(map[string]any)
	args["refused_tool_calls"] = map[string]string{refusedID: reason}
	raw, err = json.Marshal(payload)
	require.NoError(t, err)

	// Decoded as Temporal's JSON payload converter decodes the activity's
	// argument: json.Unmarshal into ActivityInput.
	var input ActivityInput
	require.NoError(t, json.Unmarshal(raw, &input))
	assert.Equal(t, reason, input.Runtime.ActivationRefusals[refusedID], "the earlier release's refusal was dropped in decoding")

	var output ExecuteToolsOutput
	require.NoError(t, h.ExecuteActivity(activity.Execute, input, &output))

	require.Len(t, output.ToolResults, 2)
	refused := output.ToolResults[0]
	assert.Equal(t, refusedID, refused.ToolCallId)
	assert.True(t, refused.IsError)
	assert.Equal(t, reason, refused.Content)
	assert.Equal(t, 0, mockExecutor.GetExecutionCount(refusedID), "a call an earlier release refused is never executed")
	assert.Equal(t, 1, mockExecutor.GetExecutionCount(readID), "the rest of the batch still runs")
}
