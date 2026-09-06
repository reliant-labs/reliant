// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/client"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	workflowpkg "github.com/reliant-labs/reliant/internal/workflow"
)

// approvalTestCtx returns a context authenticated as the user who owns the
// chats createApprovalTestData creates. Approve/Deny/Batch* now verify chat
// ownership, so a bare context is Unauthenticated by design.
func approvalTestCtx() context.Context {
	return context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
}

type mockTemporalSignalClient struct {
	client.Client

	signalCalls      int
	signalWorkflowID string
	signalName       string
	signalArg        interface{}
}

func (m *mockTemporalSignalClient) SignalWorkflow(_ context.Context, workflowID, _ string, signalName string, signalArg interface{}) error {
	m.signalCalls++
	m.signalWorkflowID = workflowID
	m.signalName = signalName
	m.signalArg = signalArg
	return nil
}

// setupTestApprovalService creates a database and approval service for testing.
func setupTestApprovalService(t *testing.T) (*ApprovalService, *db.Repo) {
	t.Helper()

	repo := db.NewTestRepo(t)
	service := NewApprovalService(repo, nil)
	return service, repo
}

// setupTestApprovalServiceWithMockClient creates an approval service wired to a mock Temporal client
// so we can verify signal routing.
func setupTestApprovalServiceWithMockClient(t *testing.T) (*ApprovalService, *db.Repo, *mockTemporalSignalClient) {
	t.Helper()

	repo := db.NewTestRepo(t)
	mockClient := &mockTemporalSignalClient{}
	pauseService := workflowpkg.NewPauseService(mockClient, repo)
	service := NewApprovalService(repo, pauseService)
	return service, repo, mockClient
}

// createApprovalTestData creates the prerequisite project, chat, and workflow for approval tests.
func createApprovalTestData(t *testing.T, repo *db.Repo) (chatID, workflowID string) {
	t.Helper()

	projectID := uuid.New().String()
	chatID = uuid.New().String()
	workflowID = uuid.New().String()
	now := time.Now().UTC()

	err := repo.CreateProject(context.Background(), &db.Project{
		ID:         projectID,
		UserID:     "test-user",
		Name:       "Test Project",
		Path:       "/tmp/test",
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	})
	require.NoError(t, err)

	err = repo.CreateChat(context.Background(), &db.Chat{
		ID:        chatID,
		Title:     "Test Chat",
		ProjectID: projectID,
		UserID:    "test-user",
		CreatedAt: now,
		UpdatedAt: now,
	})
	require.NoError(t, err)

	err = repo.CreateWorkflow(context.Background(), &db.Workflow{
		ID:           workflowID,
		ChatID:       chatID,
		WorkflowName: "test-workflow",
		Thread:       "/test",
		Status:       db.Active(),
	})
	require.NoError(t, err)

	return chatID, workflowID
}

// createTestApproval creates a pending or resolved approval in the database.
func createTestApproval(t *testing.T, repo *db.Repo, chatID, workflowID string, status int32) string {
	t.Helper()
	ctx := context.Background()

	approvalID := uuid.New().String()
	entityID := workflowID + ":test-activity"

	approval := &db.Approval{
		ID:                 approvalID,
		ChatID:             chatID,
		ApprovalType:       int32(reliantv1.ApprovalType_APPROVAL_TYPE_WORKFLOW_STEP),
		EntityID:           entityID,
		Status:             status,
		Title:              "Deploy to production?",
		TemporalWorkflowID: workflowID,
		CreatedAt:          time.Now().UTC(),
	}

	if status == int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_APPROVED) ||
		status == int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_DENIED) {
		action := "test-action"
		approval.ActionTaken = &action
		now := time.Now().UTC()
		approval.ResolvedAt = &now
	}

	err := repo.CreateApproval(ctx, approval)
	require.NoError(t, err)
	return approvalID
}

// TestApprovalService_Approve_SignalsApproval verifies that Approve() calls
// pauseService.SignalWithRecovery with signal name "signal.approval.<id>"
// and data containing status "approved".
func TestApprovalService_Approve_SignalsApproval(t *testing.T) {
	service, repo, mockClient := setupTestApprovalServiceWithMockClient(t)

	chatID, workflowID := createApprovalTestData(t, repo)
	approvalID := createTestApproval(t, repo, chatID, workflowID, int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING))

	resp, err := service.Approve(approvalTestCtx(), connect.NewRequest(&reliantv1.ApproveRequest{
		RequestId: approvalID,
	}))
	require.NoError(t, err)
	assert.True(t, resp.Msg.Success)

	// Verify signal was sent
	assert.Equal(t, 1, mockClient.signalCalls, "Expected exactly 1 signal call")
	assert.Equal(t, workflowID, mockClient.signalWorkflowID,
		"Signal should be sent to the temporal workflow ID")
	assert.Equal(t, "signal.approval."+approvalID, mockClient.signalName,
		"Signal name should be signal.approval.<id>")

	// Verify signal data contains status "approved"
	signalData, ok := mockClient.signalArg.(map[string]interface{})
	require.True(t, ok, "Signal arg should be a map")
	assert.Equal(t, "approved", signalData["status"])

	// Verify DB was updated
	approval, err := repo.GetApproval(context.Background(), approvalID)
	require.NoError(t, err)
	assert.Equal(t, int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_APPROVED), approval.Status)
}

// TestApprovalService_Deny_SignalsDenial verifies that Deny() calls
// pauseService.SignalWithRecovery with signal name "signal.approval.<id>"
// and data containing status "denied". It does NOT cancel the workflow.
func TestApprovalService_Deny_SignalsDenial(t *testing.T) {
	service, repo, mockClient := setupTestApprovalServiceWithMockClient(t)

	chatID, workflowID := createApprovalTestData(t, repo)
	approvalID := createTestApproval(t, repo, chatID, workflowID, int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING))

	denialReason := "Not ready for production"
	resp, err := service.Deny(approvalTestCtx(), connect.NewRequest(&reliantv1.DenyRequest{
		RequestId:    approvalID,
		DenialReason: &denialReason,
	}))
	require.NoError(t, err)
	assert.True(t, resp.Msg.Success)

	// Verify signal was sent (denial signals the workflow, does not cancel it)
	assert.Equal(t, 1, mockClient.signalCalls, "Expected exactly 1 signal call")
	assert.Equal(t, workflowID, mockClient.signalWorkflowID,
		"Signal should be sent to the temporal workflow ID")
	assert.Equal(t, "signal.approval."+approvalID, mockClient.signalName,
		"Signal name should be signal.approval.<id>")

	// Verify signal data contains status "denied"
	signalData, ok := mockClient.signalArg.(map[string]interface{})
	require.True(t, ok, "Signal arg should be a map")
	assert.Equal(t, "denied", signalData["status"])
	assert.Equal(t, denialReason, signalData["denial_reason"])

	// Verify DB was updated
	approval, err := repo.GetApproval(context.Background(), approvalID)
	require.NoError(t, err)
	assert.Equal(t, int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_DENIED), approval.Status)
}

// TestApprovalService_Approve_AlreadyProcessed returns FailedPrecondition for
// an approval that was already resolved.
func TestApprovalService_Approve_AlreadyProcessed(t *testing.T) {
	service, repo := setupTestApprovalService(t)

	chatID, workflowID := createApprovalTestData(t, repo)
	approvalID := createTestApproval(t, repo, chatID, workflowID, int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_APPROVED))

	_, err := service.Approve(approvalTestCtx(), connect.NewRequest(&reliantv1.ApproveRequest{
		RequestId: approvalID,
	}))

	require.Error(t, err)
	connectErr := new(connect.Error)
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, connect.CodeFailedPrecondition, connectErr.Code())
}

// TestApprovalService_Deny_AlreadyProcessed returns FailedPrecondition for
// an approval that was already resolved.
func TestApprovalService_Deny_AlreadyProcessed(t *testing.T) {
	service, repo := setupTestApprovalService(t)

	chatID, workflowID := createApprovalTestData(t, repo)
	approvalID := createTestApproval(t, repo, chatID, workflowID, int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_DENIED))

	_, err := service.Deny(approvalTestCtx(), connect.NewRequest(&reliantv1.DenyRequest{
		RequestId: approvalID,
	}))

	require.Error(t, err)
	connectErr := new(connect.Error)
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, connect.CodeFailedPrecondition, connectErr.Code())
}

// TestApprovalService_Approve_NotFound returns NotFound for unknown approval IDs.
func TestApprovalService_Approve_NotFound(t *testing.T) {
	service, _ := setupTestApprovalService(t)

	_, err := service.Approve(approvalTestCtx(), connect.NewRequest(&reliantv1.ApproveRequest{
		RequestId: "nonexistent-approval-id",
	}))

	require.Error(t, err)
	connectErr := new(connect.Error)
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, connect.CodeNotFound, connectErr.Code())
}

// TestApprovalService_Approve_MissingRequestID returns InvalidArgument when request_id is empty.
func TestApprovalService_Approve_MissingRequestID(t *testing.T) {
	service, _ := setupTestApprovalService(t)

	_, err := service.Approve(approvalTestCtx(), connect.NewRequest(&reliantv1.ApproveRequest{
		RequestId: "",
	}))

	require.Error(t, err)
	connectErr := new(connect.Error)
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, connect.CodeInvalidArgument, connectErr.Code())
}
