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
	signalWorkflowID string
	signalRunID      string
	signalName       string
	signalArg        interface{}
	signalCalls      int
}

func (m *mockTemporalSignalClient) SignalWorkflow(ctx context.Context, workflowID string, runID string, signalName string, arg interface{}) error {
	m.signalWorkflowID = workflowID
	m.signalRunID = runID
	m.signalName = signalName
	m.signalArg = arg
	m.signalCalls++
	return nil
}

// setupTestYieldService creates an in-memory database and yield service for testing
func setupTestYieldService(t *testing.T) (*YieldService, *db.Repo, *sql.DB) {
	t.Helper()

	sqlDB, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)

	err = db.RunMigrations(sqlDB)
	require.NoError(t, err)

	repo := db.NewRepo(sqlDB)
	service := NewYieldService(repo, nil)
	return service, repo, sqlDB
}

// createYieldTestData creates the prerequisite project, chat, and workflow for yield tests
func createYieldTestData(t *testing.T, sqlDB *sql.DB) (chatID, workflowID string) {
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

// createTestYield creates a pending yield in the database
func createTestYield(t *testing.T, repo *db.Repo, chatID, workflowID string, status db.YieldStatus) string {
	t.Helper()
	ctx := context.Background()

	yieldID := uuid.New().String()
	loopNodeID := "agent_loop"
	loopIteration := 0

	yield := &db.Yield{
		ID:                 yieldID,
		ChatID:             chatID,
		WorkflowID:         workflowID,
		TemporalWorkflowID: workflowID,
		StepID:             "agent_loop",
		LoopNodeID:         &loopNodeID,
		LoopIteration:      &loopIteration,
		Status:             status,
		CreatedAt:          time.Now().UTC(),
	}

	if status == db.YieldStatusResolved {
		action := "continue"
		yield.ActionTaken = &action
		now := time.Now().UTC()
		yield.ResolvedAt = &now
	}

	err := repo.CreateYield(ctx, yield)
	require.NoError(t, err)
	return yieldID
}

// TestYieldService_ResolveYield_Success resolves a pending yield and verifies success.
func TestYieldService_ResolveYield_Success(t *testing.T) {
	service, repo, sqlDB := setupTestYieldService(t)
	defer sqlDB.Close()

	chatID, workflowID := createYieldTestData(t, sqlDB)
	yieldID := createTestYield(t, repo, chatID, workflowID, db.YieldStatusPending)

	resp, err := service.ResolveYield(context.Background(), connect.NewRequest(&reliantv1.ResolveYieldRequest{
		YieldId: yieldID,
		Action:  "continue",
	}))

	require.NoError(t, err)
	assert.True(t, resp.Msg.Success)

	// Verify yield is resolved in DB
	yield, err := repo.GetYieldByID(context.Background(), yieldID)
	require.NoError(t, err)
	assert.Equal(t, db.YieldStatusResolved, yield.Status)
	require.NotNil(t, yield.ActionTaken)
	assert.Equal(t, "continue", *yield.ActionTaken)
}

func TestYieldService_ResolveYield_UsesTemporalWorkflowIDForSignalRouting(t *testing.T) {
	sqlDB, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	defer sqlDB.Close()
	require.NoError(t, db.RunMigrations(sqlDB))

	repo := db.NewRepo(sqlDB)
	mockClient := &mockTemporalSignalClient{}
	pauseService := workflowpkg.NewPauseService(mockClient, repo)
	service := NewYieldService(repo, pauseService)

	chatID, workflowID := createYieldTestData(t, sqlDB)
	yieldID := uuid.New().String()
	loopNodeID := "agent_loop"
	loopIteration := 0
	logicalWorkflowID := "logical-inline-child-workflow"
	temporalWorkflowID := workflowID

	require.NoError(t, repo.CreateYield(context.Background(), &db.Yield{
		ID:                 yieldID,
		ChatID:             chatID,
		WorkflowID:         logicalWorkflowID,
		TemporalWorkflowID: temporalWorkflowID,
		ThreadID:           logicalWorkflowID,
		StepID:             "agent_loop",
		LoopNodeID:         &loopNodeID,
		LoopIteration:      &loopIteration,
		Status:             db.YieldStatusPending,
		CreatedAt:          time.Now().UTC(),
	}))

	resp, err := service.ResolveYield(context.Background(), connect.NewRequest(&reliantv1.ResolveYieldRequest{
		YieldId: yieldID,
		Action:  "reply",
	}))
	require.NoError(t, err)
	assert.True(t, resp.Msg.Success)
	assert.Equal(t, 1, mockClient.signalCalls)
	assert.Equal(t, temporalWorkflowID, mockClient.signalWorkflowID)
	assert.Equal(t, "signal.yield."+yieldID, mockClient.signalName)
}

// TestYieldService_ResolveYield_AlreadyResolved returns FailedPrecondition for
// a yield that was already resolved.
func TestYieldService_ResolveYield_AlreadyResolved(t *testing.T) {
	service, repo, sqlDB := setupTestYieldService(t)
	defer sqlDB.Close()

	chatID, workflowID := createYieldTestData(t, sqlDB)
	yieldID := createTestYield(t, repo, chatID, workflowID, db.YieldStatusResolved)

	_, err := service.ResolveYield(context.Background(), connect.NewRequest(&reliantv1.ResolveYieldRequest{
		YieldId: yieldID,
		Action:  "continue",
	}))

	require.Error(t, err)
	connectErr := new(connect.Error)
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, connect.CodeFailedPrecondition, connectErr.Code())
}

// TestYieldService_ResolveYield_NotFound returns NotFound for unknown yield IDs.
func TestYieldService_ResolveYield_NotFound(t *testing.T) {
	service, _, sqlDB := setupTestYieldService(t)
	defer sqlDB.Close()

	_, err := service.ResolveYield(context.Background(), connect.NewRequest(&reliantv1.ResolveYieldRequest{
		YieldId: "nonexistent-yield-id",
		Action:  "continue",
	}))

	require.Error(t, err)
	connectErr := new(connect.Error)
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, connect.CodeNotFound, connectErr.Code())
}

// TestYieldService_ResolveYield_MissingYieldID returns InvalidArgument when yield_id is empty.
func TestYieldService_ResolveYield_MissingYieldID(t *testing.T) {
	service, _, sqlDB := setupTestYieldService(t)
	defer sqlDB.Close()

	_, err := service.ResolveYield(context.Background(), connect.NewRequest(&reliantv1.ResolveYieldRequest{
		YieldId: "",
		Action:  "continue",
	}))

	require.Error(t, err)
	connectErr := new(connect.Error)
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, connect.CodeInvalidArgument, connectErr.Code())
}

// TestYieldService_ResolveYield_MissingAction returns InvalidArgument when action is empty.
func TestYieldService_ResolveYield_MissingAction(t *testing.T) {
	service, _, sqlDB := setupTestYieldService(t)
	defer sqlDB.Close()

	_, err := service.ResolveYield(context.Background(), connect.NewRequest(&reliantv1.ResolveYieldRequest{
		YieldId: "some-id",
		Action:  "",
	}))

	require.Error(t, err)
	connectErr := new(connect.Error)
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, connect.CodeInvalidArgument, connectErr.Code())
}

// TestYieldService_GetPendingYield_Found returns yield info when a pending yield exists.
func TestYieldService_GetPendingYield_Found(t *testing.T) {
	service, repo, sqlDB := setupTestYieldService(t)
	defer sqlDB.Close()

	chatID, workflowID := createYieldTestData(t, sqlDB)
	yieldID := createTestYield(t, repo, chatID, workflowID, db.YieldStatusPending)

	resp, err := service.GetPendingYield(context.Background(), connect.NewRequest(&reliantv1.GetPendingYieldRequest{
		ChatId: chatID,
	}))

	require.NoError(t, err)
	require.NotNil(t, resp.Msg.Yield)
	assert.Equal(t, yieldID, resp.Msg.Yield.YieldId)
	assert.Equal(t, chatID, resp.Msg.Yield.ChatId)
	assert.Equal(t, workflowID, resp.Msg.Yield.WorkflowId)
	assert.Equal(t, "agent_loop", resp.Msg.Yield.StepId)
	assert.Equal(t, reliantv1.YieldStatus_YIELD_STATUS_PENDING, resp.Msg.Yield.Status)
}

// TestYieldService_GetPendingYield_NoneExists returns empty response when no
// pending yield exists for the chat.
func TestYieldService_GetPendingYield_NoneExists(t *testing.T) {
	service, _, sqlDB := setupTestYieldService(t)
	defer sqlDB.Close()

	chatID, _ := createYieldTestData(t, sqlDB)

	resp, err := service.GetPendingYield(context.Background(), connect.NewRequest(&reliantv1.GetPendingYieldRequest{
		ChatId: chatID,
	}))

	require.NoError(t, err)
	assert.Nil(t, resp.Msg.Yield)
}

// TestYieldService_GetPendingYield_ResolvedNotReturned verifies that resolved
// yields are not returned by GetPendingYield.
func TestYieldService_GetPendingYield_ResolvedNotReturned(t *testing.T) {
	service, repo, sqlDB := setupTestYieldService(t)
	defer sqlDB.Close()

	chatID, workflowID := createYieldTestData(t, sqlDB)
	_ = createTestYield(t, repo, chatID, workflowID, db.YieldStatusResolved)

	resp, err := service.GetPendingYield(context.Background(), connect.NewRequest(&reliantv1.GetPendingYieldRequest{
		ChatId: chatID,
	}))

	require.NoError(t, err)
	assert.Nil(t, resp.Msg.Yield, "Resolved yields should not be returned by GetPendingYield")
}

// TestYieldService_GetPendingYield_MissingChatID returns InvalidArgument when chat_id is empty.
func TestYieldService_GetPendingYield_MissingChatID(t *testing.T) {
	service, _, sqlDB := setupTestYieldService(t)
	defer sqlDB.Close()

	_, err := service.GetPendingYield(context.Background(), connect.NewRequest(&reliantv1.GetPendingYieldRequest{
		ChatId: "",
	}))

	require.Error(t, err)
	connectErr := new(connect.Error)
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, connect.CodeInvalidArgument, connectErr.Code())
}

// TestYieldService_GetPendingYield_EmptyTemporalWorkflowID verifies that a pending yield
// with an empty temporal_workflow_id is still returned. This covers the case where old
// yield records were created before the temporal_workflow_id column existed.
func TestYieldService_GetPendingYield_EmptyTemporalWorkflowID(t *testing.T) {
	service, repo, sqlDB := setupTestYieldService(t)
	defer sqlDB.Close()

	chatID, workflowID := createYieldTestData(t, sqlDB)

	// Create yield with empty temporal_workflow_id (simulates old yield records)
	yieldID := uuid.New().String()
	loopNodeID := "agent_loop"
	loopIteration := 0

	err := repo.CreateYield(context.Background(), &db.Yield{
		ID:                 yieldID,
		ChatID:             chatID,
		WorkflowID:         workflowID,
		TemporalWorkflowID: "", // explicitly empty
		ThreadID:           workflowID,
		StepID:             "agent_loop",
		LoopNodeID:         &loopNodeID,
		LoopIteration:      &loopIteration,
		Status:             db.YieldStatusPending,
		CreatedAt:          time.Now().UTC(),
	})
	require.NoError(t, err)

	resp, err := service.GetPendingYield(context.Background(), connect.NewRequest(&reliantv1.GetPendingYieldRequest{
		ChatId: chatID,
	}))

	require.NoError(t, err)
	require.NotNil(t, resp.Msg.Yield, "Pending yield with empty temporal_workflow_id should still be returned")
	assert.Equal(t, yieldID, resp.Msg.Yield.YieldId)
	assert.Equal(t, chatID, resp.Msg.Yield.ChatId)
	assert.Equal(t, reliantv1.YieldStatus_YIELD_STATUS_PENDING, resp.Msg.Yield.Status)
}

// TestYieldService_GetPendingYield_ReturnsLatestWhenMultiplePending verifies that when
// multiple pending yields exist for a chat (edge case), the most recent one is returned.
func TestYieldService_GetPendingYield_ReturnsLatestWhenMultiplePending(t *testing.T) {
	service, repo, sqlDB := setupTestYieldService(t)
	defer sqlDB.Close()

	chatID, workflowID := createYieldTestData(t, sqlDB)

	// Create two pending yields with different timestamps
	loopNodeID := "agent_loop"
	loopIteration0 := 0
	loopIteration1 := 1

	err := repo.CreateYield(context.Background(), &db.Yield{
		ID:                 uuid.New().String(),
		ChatID:             chatID,
		WorkflowID:         workflowID,
		TemporalWorkflowID: workflowID,
		ThreadID:           workflowID,
		StepID:             "agent_loop",
		LoopNodeID:         &loopNodeID,
		LoopIteration:      &loopIteration0,
		Status:             db.YieldStatusPending,
		CreatedAt:          time.Now().UTC().Add(-10 * time.Second), // older
	})
	require.NoError(t, err)

	newerYieldID := uuid.New().String()
	err = repo.CreateYield(context.Background(), &db.Yield{
		ID:                 newerYieldID,
		ChatID:             chatID,
		WorkflowID:         workflowID,
		TemporalWorkflowID: workflowID,
		ThreadID:           workflowID,
		StepID:             "agent_loop",
		LoopNodeID:         &loopNodeID,
		LoopIteration:      &loopIteration1,
		Status:             db.YieldStatusPending,
		CreatedAt:          time.Now().UTC(), // newer
	})
	require.NoError(t, err)

	resp, err := service.GetPendingYield(context.Background(), connect.NewRequest(&reliantv1.GetPendingYieldRequest{
		ChatId: chatID,
	}))

	require.NoError(t, err)
	require.NotNil(t, resp.Msg.Yield)
	assert.Equal(t, newerYieldID, resp.Msg.Yield.YieldId,
		"Should return the most recent pending yield")
}
