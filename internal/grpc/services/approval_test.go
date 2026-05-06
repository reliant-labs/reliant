// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/client"

	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	workflowpkg "github.com/reliant-labs/reliant/internal/workflow"
)

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

// setupTestApprovalService creates an in-memory database and approval service for testing.
func setupTestApprovalService(t *testing.T) (*ApprovalService, *db.Repo, *sql.DB) {
	t.Helper()

	sqlDB, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)

	err = db.RunMigrations(sqlDB)
	require.NoError(t, err)

	repo := db.NewRepo(sqlDB)
	service := NewApprovalService(repo, nil)
	return service, repo, sqlDB
}

// setupTestApprovalServiceWithMockClient creates an approval service wired to a mock Temporal client
// so we can verify signal routing.
func setupTestApprovalServiceWithMockClient(t *testing.T) (*ApprovalService, *db.Repo, *sql.DB, *mockTemporalSignalClient) {
	t.Helper()

	sqlDB, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)

	err = db.RunMigrations(sqlDB)
	require.NoError(t, err)

	repo := db.NewRepo(sqlDB)
	mockClient := &mockTemporalSignalClient{}
	pauseService := workflowpkg.NewPauseService(mockClient, repo)
	service := NewApprovalService(repo, pauseService)
	return service, repo, sqlDB, mockClient
}

// createApprovalTestData creates the prerequisite project, chat, and workflow for approval tests.
func createApprovalTestData(t *testing.T, sqlDB *sql.DB) (chatID, workflowID string) {
	t.Helper()

	projectID := uuid.New().String()
	chatID = uuid.New().String()
	workflowID = uuid.New().String()

	_, err := sqlDB.Exec(
		`INSERT INTO projects (id, user_id, name, path, created_at, updated_at) VALUES (?, ?, ?, ?, datetime('now'), datetime('now'))`,
		projectID, "test-user", "Test Project", "/tmp/test")
	require.NoError(t, err)

	_, err = sqlDB.Exec(
		`INSERT INTO chats (id, title, project_id, user_id, created_at, updated_at, last_active) VALUES (?, ?, ?, ?, datetime('now'), datetime('now'), datetime('now'))`,
		chatID, "Test Chat", projectID, "test-user")
	require.NoError(t, err)

	_, err = sqlDB.Exec(
		`INSERT INTO workflows (id, chat_id, workflow_name, thread, status, created_at) VALUES (?, ?, ?, ?, ?, datetime('now'))`,
		workflowID, chatID, "test-workflow", "/test", "running")
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
	service, repo, sqlDB, mockClient := setupTestApprovalServiceWithMockClient(t)
	defer sqlDB.Close()

	chatID, workflowID := createApprovalTestData(t, sqlDB)
	approvalID := createTestApproval(t, repo, chatID, workflowID, int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING))

	resp, err := service.Approve(context.Background(), connect.NewRequest(&reliantv1.ApproveRequest{
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
	service, repo, sqlDB, mockClient := setupTestApprovalServiceWithMockClient(t)
	defer sqlDB.Close()

	chatID, workflowID := createApprovalTestData(t, sqlDB)
	approvalID := createTestApproval(t, repo, chatID, workflowID, int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING))

	denialReason := "Not ready for production"
	resp, err := service.Deny(context.Background(), connect.NewRequest(&reliantv1.DenyRequest{
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
	service, repo, sqlDB := setupTestApprovalService(t)
	defer sqlDB.Close()

	chatID, workflowID := createApprovalTestData(t, sqlDB)
	approvalID := createTestApproval(t, repo, chatID, workflowID, int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_APPROVED))

	_, err := service.Approve(context.Background(), connect.NewRequest(&reliantv1.ApproveRequest{
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
	service, repo, sqlDB := setupTestApprovalService(t)
	defer sqlDB.Close()

	chatID, workflowID := createApprovalTestData(t, sqlDB)
	approvalID := createTestApproval(t, repo, chatID, workflowID, int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_DENIED))

	_, err := service.Deny(context.Background(), connect.NewRequest(&reliantv1.DenyRequest{
		RequestId: approvalID,
	}))

	require.Error(t, err)
	connectErr := new(connect.Error)
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, connect.CodeFailedPrecondition, connectErr.Code())
}

// TestApprovalService_Approve_NotFound returns NotFound for unknown approval IDs.
func TestApprovalService_Approve_NotFound(t *testing.T) {
	service, _, sqlDB := setupTestApprovalService(t)
	defer sqlDB.Close()

	_, err := service.Approve(context.Background(), connect.NewRequest(&reliantv1.ApproveRequest{
		RequestId: "nonexistent-approval-id",
	}))

	require.Error(t, err)
	connectErr := new(connect.Error)
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, connect.CodeNotFound, connectErr.Code())
}

// TestApprovalService_Approve_MissingRequestID returns InvalidArgument when request_id is empty.
func TestApprovalService_Approve_MissingRequestID(t *testing.T) {
	service, _, sqlDB := setupTestApprovalService(t)
	defer sqlDB.Close()

	_, err := service.Approve(context.Background(), connect.NewRequest(&reliantv1.ApproveRequest{
		RequestId: "",
	}))

	require.Error(t, err)
	connectErr := new(connect.Error)
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, connect.CodeInvalidArgument, connectErr.Code())
}
