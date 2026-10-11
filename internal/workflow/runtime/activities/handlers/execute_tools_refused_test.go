// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A call the workflow refused is answered with the reason and recorded
// FAILED, and never reaches the executor. Only a run recorded before the
// integration action approval prompt was removed still refuses one — a call
// the person attending did not approve (runtime/action_approval_legacy.go) —
// and it must keep being honoured until that run is gone.
func TestExecuteTools_RefusesCallTheWorkflowRefused(t *testing.T) {
	const slackPost = "slack__message_post"
	h, chatID := newValidationChat(t)
	mockExecutor := newMockToolExecutor()
	activity := NewExecuteToolsActivity(h.Repo(), mockExecutor)
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
		RefusedToolCalls: map[string]string{refusedID: reason},
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
