// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const slackPost = "slack__message_post"

// A call the workflow refused — a mutating integration action the person
// attending did not approve — is answered with the reason and recorded FAILED,
// and never reaches the executor.
func TestExecuteTools_RefusesCallTheUserDidNotApprove(t *testing.T) {
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

func newToolApprovalInput(chatID, workflowID string) ApprovalCreateInput {
	return ApprovalCreateInput{
		ChatID:             chatID,
		WorkflowID:         workflowID,
		TemporalWorkflowID: workflowID,
		StepID:             "execute_tools",
		ToolName:           slackPost,
		ToolCallID:         "toolu_" + uuid.NewString(),
		ToolInput:          `{"channel":"#general","text":"Ship it"}`,
	}
}

// A tool approval is a TOOL row titled from the action and the channel, and
// carries what the card shows: the action, its integration and the params.
func TestApprovalCreateActivity_ToolApprovalAsksAboutTheCall(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()
	userID, projectID, chatID, workflowID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)
	h.CreateTestWorkflow(ctx, workflowID, chatID)

	input := newToolApprovalInput(chatID, workflowID)
	var output ApprovalCreateOutput
	require.NoError(t, h.ExecuteActivity(NewApprovalCreateActivity(h.Repo()).Execute, input, &output))
	require.False(t, output.AlreadyResolved)

	pending, err := h.Repo().ListPendingApprovalsByChat(ctx, chatID)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	row := pending[0]
	assert.Equal(t, output.ApprovalID, row.ID)
	assert.Equal(t, int32(reliantv1.ApprovalType_APPROVAL_TYPE_TOOL), row.ApprovalType)
	assert.Equal(t, "Post message in #general?", row.Title)

	require.NotNil(t, row.Metadata)
	var metadata map[string]any
	require.NoError(t, json.Unmarshal([]byte(*row.Metadata), &metadata))
	assert.Equal(t, slackPost, metadata["tool_name"])
	assert.Equal(t, input.ToolCallID, metadata["tool_call_id"])
	assert.Equal(t, input.ToolInput, metadata["input"])
	assert.Equal(t, "Slack", metadata["integration_name"])
	assert.Equal(t, "slack", metadata["integration_icon"])
}

// "Always allow" is read before asking: with the user's row present the
// approval resolves approved with no card, and once the row is deleted
// (revoked) the next call is asked about again.
func TestApprovalCreateActivity_AlwaysAllowSkipsTheCardUntilRevoked(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()
	userID, projectID, chatID, workflowID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)
	h.CreateTestWorkflow(ctx, workflowID, chatID)

	action, ok := tools.MutatingIntegrationActionInfo(slackPost)
	require.True(t, ok)
	key := tools.ActionApprovalSettingKey(slackPost)
	require.NoError(t, h.Repo().SetString(ctx, userID, nil, key, tools.AlwaysAllowSettingValue(action)))

	var allowed ApprovalCreateOutput
	require.NoError(t, h.ExecuteActivity(NewApprovalCreateActivity(h.Repo()).Execute, newToolApprovalInput(chatID, workflowID), &allowed))
	assert.True(t, allowed.AlreadyResolved)
	assert.Equal(t, "approved", allowed.Status)
	assert.Equal(t, tools.ActionApprovalAlwaysAllow, allowed.ActionTaken)
	pending, err := h.Repo().ListPendingApprovalsByChat(ctx, chatID)
	require.NoError(t, err)
	assert.Empty(t, pending, "an always-allowed action shows no card")

	// Allowing one action does not allow another.
	other := newToolApprovalInput(chatID, workflowID)
	other.ToolName = "gmail__message_send"
	other.ToolInput = `{"to":["ann@example.com"],"subject":"Hi"}`
	var asked ApprovalCreateOutput
	require.NoError(t, h.ExecuteActivity(NewApprovalCreateActivity(h.Repo()).Execute, other, &asked))
	assert.False(t, asked.AlreadyResolved, "always allow is per action")

	// Revoke.
	setting, err := h.Repo().GetSetting(ctx, userID, nil, key)
	require.NoError(t, err)
	require.NoError(t, h.Repo().DeleteSetting(ctx, setting.ID))

	var again ApprovalCreateOutput
	require.NoError(t, h.ExecuteActivity(NewApprovalCreateActivity(h.Repo()).Execute, newToolApprovalInput(chatID, workflowID), &again))
	assert.False(t, again.AlreadyResolved, "revoking brings the card back")
	require.NotEmpty(t, again.ApprovalID)
	assert.NotEqual(t, asked.ApprovalID, again.ApprovalID)
	row, err := h.Repo().GetApproval(ctx, again.ApprovalID)
	require.NoError(t, err)
	assert.Equal(t, "Post message in #general?", row.Title)
}
