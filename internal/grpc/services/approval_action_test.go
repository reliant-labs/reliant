// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/tools"
)

const approvalSlackPost = "slack__message_post"

// createToolApproval creates a pending TOOL approval, as the action approval
// gate's ApprovalCreate writes it.
func createToolApproval(t *testing.T, repo *db.Repo, chatID, workflowID, toolCallID string) string {
	t.Helper()
	metadata, err := json.Marshal(map[string]any{
		"workflow_id":      workflowID,
		"tool_name":        approvalSlackPost,
		"tool_call_id":     toolCallID,
		"input":            `{"channel":"#general","text":"Ship it"}`,
		"integration_name": "Slack",
		"integration_icon": "slack",
	})
	require.NoError(t, err)
	metadataStr := string(metadata)
	approvalID := uuid.NewString()
	require.NoError(t, repo.CreateApproval(context.Background(), &db.Approval{
		ID:                 approvalID,
		ChatID:             chatID,
		ApprovalType:       int32(reliantv1.ApprovalType_APPROVAL_TYPE_TOOL),
		EntityID:           workflowID + ":" + uuid.NewString(),
		Status:             int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING),
		Title:              "Post message in #general?",
		Metadata:           &metadataStr,
		TemporalWorkflowID: workflowID,
		CreatedAt:          time.Now().UTC(),
	}))
	return approvalID
}

// The card's three answers: "always allow" also records the caller's standing
// decision for the action; "allow once" does not.
func TestApprovalService_ApproveAlwaysAllowRemembersTheAction(t *testing.T) {
	service, repo, mockClient := setupTestApprovalServiceWithMockClient(t)
	chatID, workflowID := createApprovalTestData(t, repo)
	key := tools.ActionApprovalSettingKey(approvalSlackPost)

	once := createToolApproval(t, repo, chatID, workflowID, "toolu_once")
	allowOnce := "allow_once"
	_, err := service.Approve(approvalTestCtx(), connect.NewRequest(&reliantv1.ApproveRequest{RequestId: once, ActionTaken: &allowOnce}))
	require.NoError(t, err)
	_, err = repo.GetSetting(context.Background(), "test-user", nil, key)
	require.Error(t, err, "allow once remembers nothing")

	always := createToolApproval(t, repo, chatID, workflowID, "toolu_always")
	alwaysAllow := tools.ActionApprovalAlwaysAllow
	_, err = service.Approve(approvalTestCtx(), connect.NewRequest(&reliantv1.ApproveRequest{RequestId: always, ActionTaken: &alwaysAllow}))
	require.NoError(t, err)

	setting, err := repo.GetSetting(context.Background(), "test-user", nil, key)
	require.NoError(t, err)
	assert.True(t, tools.AllowsAlways(setting.Value))
	var stored tools.ActionApprovalSetting
	require.NoError(t, json.Unmarshal([]byte(setting.Value), &stored))
	assert.Equal(t, "Post message", stored.DisplayName)
	assert.Equal(t, "Slack", stored.Integration)

	signal, ok := mockClient.signalArg.(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "approved", signal["status"], "the call itself runs")
}

// A workflow-step approval cannot plant an "always allow".
func TestApprovalService_AlwaysAllowOnAStepApprovalRemembersNothing(t *testing.T) {
	service, repo, _ := setupTestApprovalServiceWithMockClient(t)
	chatID, workflowID := createApprovalTestData(t, repo)
	approvalID := createTestApproval(t, repo, chatID, workflowID, int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING))

	alwaysAllow := tools.ActionApprovalAlwaysAllow
	_, err := service.Approve(approvalTestCtx(), connect.NewRequest(&reliantv1.ApproveRequest{RequestId: approvalID, ActionTaken: &alwaysAllow}))
	require.NoError(t, err)
	settings, err := repo.ListSettingsByKey(context.Background(), "test-user", tools.ActionApprovalSettingPrefix+"%")
	require.NoError(t, err)
	assert.Empty(t, settings)
}

// Denying a tool approval writes no denial message: the ExecuteTools
// activity answers the one call it asked about, and a second tool_result for
// every call in the turn would duplicate those results.
func TestApprovalService_DenyToolApprovalWritesNoDenialMessage(t *testing.T) {
	service, repo, mockClient := setupTestApprovalServiceWithMockClient(t)
	ctx := approvalTestCtx()
	f := setupToolCallDurableFixture(t, ctx, repo, `{"channel":"#general","text":"Ship it"}`)
	workflowID := uuid.NewString()
	require.NoError(t, repo.CreateWorkflow(ctx, &db.Workflow{ID: workflowID, ChatID: f.chatID, WorkflowName: "agent", Thread: f.threadID, Status: db.Active()}))
	approvalID := createToolApproval(t, repo, f.chatID, workflowID, f.toolCallID)

	before, err := repo.ListMessages(ctx, f.chatID, db.MessageListOptions{})
	require.NoError(t, err)
	_, err = service.Deny(ctx, connect.NewRequest(&reliantv1.DenyRequest{RequestId: approvalID}))
	require.NoError(t, err)
	after, err := repo.ListMessages(ctx, f.chatID, db.MessageListOptions{})
	require.NoError(t, err)
	assert.Len(t, after, len(before))

	signal, ok := mockClient.signalArg.(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "denied", signal["status"])
}

// What the card renders arrives on the Approval message.
func TestApprovalToProto_ToolApproval(t *testing.T) {
	_, repo, _ := setupTestApprovalServiceWithMockClient(t)
	chatID, workflowID := createApprovalTestData(t, repo)
	approvalID := createToolApproval(t, repo, chatID, workflowID, "toolu_1")
	approval, err := repo.GetApproval(context.Background(), approvalID)
	require.NoError(t, err)

	p := approvalToProto(approval)
	assert.Equal(t, reliantv1.ApprovalType_APPROVAL_TYPE_TOOL, p.GetApprovalType())
	assert.Equal(t, "Post message in #general?", p.GetTitle())
	assert.Equal(t, approvalSlackPost, p.GetToolName())
	assert.Equal(t, "toolu_1", p.GetToolCallId())
	assert.JSONEq(t, `{"channel":"#general","text":"Ship it"}`, p.GetToolInput())
	assert.Equal(t, "Slack", p.GetIntegrationName())
	assert.Equal(t, "slack", p.GetIntegrationIcon())
}
