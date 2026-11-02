// Copyright (c) 2025 Reliant Labs
package approval

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/client"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/dbtest"
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
func setupTestApprovalService(t *testing.T) (*Service, *db.Repo) {
	t.Helper()

	repo := db.NewTestRepo(t)
	// The harness stubs Signals: these tests resolve approvals no run waits on.
	service := NewTestApproval(t, WithApprovalDeps(Deps{Repo: repo}))
	return service, repo
}

// setupTestApprovalServiceWithMockClient creates an approval service wired to a mock Temporal client
// so we can verify signal routing.
func setupTestApprovalServiceWithMockClient(t *testing.T) (*Service, *db.Repo, *mockTemporalSignalClient) {
	t.Helper()

	repo := db.NewTestRepo(t)
	mockClient := &mockTemporalSignalClient{}
	pauseService := workflowpkg.NewPauseService(mockClient, repo)
	service := NewTestApproval(t, WithApprovalDeps(Deps{Repo: repo, Signals: pauseService}))
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
	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
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
	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
}

// TestApprovalService_Approve_NotFound returns NotFound for unknown approval IDs.
func TestApprovalService_Approve_NotFound(t *testing.T) {
	service, _ := setupTestApprovalService(t)

	_, err := service.Approve(approvalTestCtx(), connect.NewRequest(&reliantv1.ApproveRequest{
		RequestId: "nonexistent-approval-id",
	}))

	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, svcerr.Code(err))
}

// TestApprovalService_Approve_MissingRequestID returns InvalidArgument when request_id is empty.
func TestApprovalService_Approve_MissingRequestID(t *testing.T) {
	service, _ := setupTestApprovalService(t)

	_, err := service.Approve(approvalTestCtx(), connect.NewRequest(&reliantv1.ApproveRequest{
		RequestId: "",
	}))

	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
}

// createRetiredToolApproval creates a pending approval as the removed
// integration action approval prompt recorded it: type 1, the reserved
// APPROVAL_TYPE_TOOL, asking about one call in the turn.
func createRetiredToolApproval(t *testing.T, repo *db.Repo, chatID, workflowID string) string {
	t.Helper()
	approvalID := uuid.NewString()
	require.NoError(t, repo.CreateApproval(context.Background(), &db.Approval{
		ID:                 approvalID,
		ChatID:             chatID,
		ApprovalType:       retiredToolApprovalType,
		EntityID:           workflowID + ":" + uuid.NewString(),
		Status:             int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING),
		Title:              "Post message in #general?",
		TemporalWorkflowID: workflowID,
		CreatedAt:          time.Now().UTC(),
	}))
	return approvalID
}

// Denying an approval the removed prompt recorded writes no denial message:
// the run still waiting on it refuses that one call in ExecuteTools, so a
// tool_result for every call in the turn would duplicate that result. Denying
// a workflow step approval in the same turn still writes one.
func TestApprovalService_DenyRetiredToolApprovalWritesNoDenialMessage(t *testing.T) {
	testDenyAnsweredByItsRun(t, createRetiredToolApproval)
}

// A tool-call approval (an activation change, a machine start) is answered by
// its run too: denying it must not write a denied tool_result for every call
// in the turn, the others of which still run.
func TestApprovalService_DenyToolCallApprovalWritesNoDenialMessage(t *testing.T) {
	testDenyAnsweredByItsRun(t, func(t *testing.T, repo *db.Repo, chatID, workflowID string) string {
		return createBatchedApproval(t, repo, chatID, workflowID, "batch-1", int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING))
	})
}

func testDenyAnsweredByItsRun(t *testing.T, create func(t *testing.T, repo *db.Repo, chatID, workflowID string) string) {
	ctx := approvalTestCtx()

	for name, deny := range map[string]func(*Service, string) error{
		"Deny": func(s *Service, id string) error {
			_, err := s.Deny(ctx, connect.NewRequest(&reliantv1.DenyRequest{RequestId: id}))
			return err
		},
		"BatchDeny": func(s *Service, id string) error {
			_, err := s.BatchDeny(ctx, connect.NewRequest(&reliantv1.BatchDenyRequest{RequestIds: []string{id}}))
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			service, repo, mockClient := setupTestApprovalServiceWithMockClient(t)
			f := dbtest.SeedToolCall(t, ctx, repo, `{"channel":"#general","text":"Ship it"}`)
			workflowID := uuid.NewString()
			require.NoError(t, repo.CreateWorkflow(ctx, &db.Workflow{ID: workflowID, ChatID: f.ChatID, WorkflowName: "agent", Thread: f.ThreadID, Status: db.Active()}))

			retired := create(t, repo, f.ChatID, workflowID)
			before, err := repo.ListMessages(ctx, f.ChatID, db.MessageListOptions{})
			require.NoError(t, err)
			require.NoError(t, deny(service, retired))
			after, err := repo.ListMessages(ctx, f.ChatID, db.MessageListOptions{})
			require.NoError(t, err)
			assert.Len(t, after, len(before), "the run answers the call; no denial message")
			signal, ok := mockClient.signalArg.(map[string]interface{})
			require.True(t, ok)
			assert.Equal(t, "denied", signal["status"], "the waiting run is still told")

			step := createTestApproval(t, repo, f.ChatID, workflowID, int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING))
			require.NoError(t, deny(service, step))
			afterStep, err := repo.ListMessages(ctx, f.ChatID, db.MessageListOptions{})
			require.NoError(t, err)
			assert.Len(t, afterStep, len(after)+1, "a step approval's denial still writes its message")
		})
	}
}

// createBatchedApproval creates an approval about one of a run's own tool
// calls, raised in batch.
func createBatchedApproval(t *testing.T, repo *db.Repo, chatID, workflowID, batchID string, status int32) string {
	t.Helper()
	approvalID := uuid.NewString()
	require.NoError(t, repo.CreateApproval(context.Background(), &db.Approval{
		ID:                 approvalID,
		ChatID:             chatID,
		ApprovalType:       int32(reliantv1.ApprovalType_APPROVAL_TYPE_TOOL_CALL),
		EntityID:           workflowID + ":" + uuid.NewString(),
		Status:             status,
		Title:              "Approval: enable_trigger",
		TemporalWorkflowID: workflowID,
		CreatedAt:          time.Now().UTC(),
		BatchID:            &batchID,
	}))
	return approvalID
}

// A batch approves what is still pending of itself: one of its approvals
// decided elsewhere first is skipped, not an error, and each approved one's
// run is signalled. Only a batch with nothing left pending is refused.
func TestApprovalService_BatchApproveSkipsDecided(t *testing.T) {
	service, repo, mockClient := setupTestApprovalServiceWithMockClient(t)
	chatID, workflowID := createApprovalTestData(t, repo)
	pending := int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING)
	a := createBatchedApproval(t, repo, chatID, workflowID, "batch-1", pending)
	b := createBatchedApproval(t, repo, chatID, workflowID, "batch-1", pending)
	c := createBatchedApproval(t, repo, chatID, workflowID, "batch-1", pending)

	_, err := service.Deny(approvalTestCtx(), connect.NewRequest(&reliantv1.DenyRequest{RequestId: b}))
	require.NoError(t, err)
	resp, err := service.BatchApprove(approvalTestCtx(), connect.NewRequest(&reliantv1.BatchApproveRequest{RequestIds: []string{a, b, c}}))
	require.NoError(t, err)
	assert.Equal(t, int32(2), resp.Msg.Approved)
	assert.Equal(t, chatID, resp.Msg.ChatId)
	assert.Equal(t, 3, mockClient.signalCalls, "one denial, then two approvals")

	for id, want := range map[string]reliantv1.ApprovalStatus{a: reliantv1.ApprovalStatus_APPROVAL_STATUS_APPROVED, b: reliantv1.ApprovalStatus_APPROVAL_STATUS_DENIED, c: reliantv1.ApprovalStatus_APPROVAL_STATUS_APPROVED} {
		got, err := repo.GetApproval(context.Background(), id)
		require.NoError(t, err)
		assert.Equal(t, int32(want), got.Status, id)
	}

	_, err = service.BatchDeny(approvalTestCtx(), connect.NewRequest(&reliantv1.BatchDenyRequest{RequestIds: []string{a, b, c}}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
}

// The chat's list carries each approval's batch.
func TestApprovalService_ListCarriesBatch(t *testing.T) {
	service, repo := setupTestApprovalService(t)
	chatID, workflowID := createApprovalTestData(t, repo)
	createBatchedApproval(t, repo, chatID, workflowID, "batch-1", int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING))
	createTestApproval(t, repo, chatID, workflowID, int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING))
	resp, err := service.ListApprovalsByChat(approvalTestCtx(), connect.NewRequest(&reliantv1.ListApprovalsByChatRequest{ChatId: chatID}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.Approvals, 2)
	batches := map[string]bool{}
	for _, a := range resp.Msg.Approvals {
		batches[a.GetBatchId()] = true
	}
	assert.Equal(t, map[string]bool{"batch-1": true, "": true}, batches)
}
