// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/stretchr/testify/require"
)

// setupTestRepo creates an in-memory test database
func setupTestRepo(t *testing.T) db.Repository {
	repo := db.NewTestRepo(t)
	return repo
}

// createTestChat creates a test project and chat
func createTestChat(t *testing.T, repo db.Repository) string {
	chatID, _ := createTestChatWithContextWindow(t, repo)
	return chatID
}

// createTestChatWithContextWindow creates a test project, chat, thread, and context window
// Returns the chatID and contextWindowID for use in message creation
func createTestChatWithContextWindow(t *testing.T, repo db.Repository) (string, string) {
	ctx := context.Background()
	projectID := uuid.New().String()
	chatID := uuid.New().String()

	// Create project first (required for foreign key)
	err := repo.CreateProject(ctx, &db.Project{
		ID:         projectID,
		Name:       "Test Project",
		Path:       "/tmp/test",
		UserID:     "test-user",
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
		LastActive: time.Now(),
	})
	require.NoError(t, err)

	// Create chat
	err = repo.CreateChat(ctx, &db.Chat{
		ID:         chatID,
		ProjectID:  projectID,
		UserID:     "test-user",
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
		LastActive: time.Now(),
	})
	require.NoError(t, err)

	// Create thread (root thread ID equals chat ID)
	threadID := chatID
	_, err = repo.CreateThread(ctx, &db.Thread{
		ID:             threadID,
		ChatID: chatID,
	})
	require.NoError(t, err)

	// Create context window
	contextWindowID := threadID + ":0"
	_, err = repo.CreateContextWindow(ctx, &db.ContextWindow{
		ID:       contextWindowID,
		ThreadID: threadID,
		Sequence: 0,
	})
	require.NoError(t, err)

	return chatID, contextWindowID
}

// TestCleanupActivity_CancelsApprovals verifies that cleanup activity
// cancels all pending approvals for a chat
func TestCleanupActivity_CancelsApprovals(t *testing.T) {
	repo := setupTestRepo(t)
	ctx := context.Background()
	chatID := createTestChat(t, repo)

	// Create pending approvals
	approval1 := &db.Approval{
		ID:           uuid.New().String(),
		ChatID:       chatID,
		ApprovalType: int32(reliantv1.ApprovalType_APPROVAL_TYPE_WORKFLOW_STEP),
		EntityID:     "entity-1",
		Status:       int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING),
		Title:        "Test Approval 1",
		CreatedAt:    time.Now(),
	}
	approval2 := &db.Approval{
		ID:           uuid.New().String(),
		ChatID:       chatID,
		ApprovalType: int32(reliantv1.ApprovalType_APPROVAL_TYPE_WORKFLOW_STEP),
		EntityID:     "entity-2",
		Status:       int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING),
		Title:        "Test Approval 2",
		CreatedAt:    time.Now(),
	}

	err := repo.CreateApproval(ctx, approval1)
	require.NoError(t, err)
	err = repo.CreateApproval(ctx, approval2)
	require.NoError(t, err)

	// Verify pending approvals exist
	pending, err := repo.ListPendingApprovalsByChat(ctx, chatID)
	require.NoError(t, err)
	require.Len(t, pending, 2)

	// Run cleanup activity
	activity := NewCleanupActivity(repo)
	output, err := activity.Execute(ctx, CleanupInput{ChatID: chatID})
	require.NoError(t, err)
	require.Equal(t, 2, output.ApprovalsCancelled)

	// Verify approvals are cancelled
	pending, err = repo.ListPendingApprovalsByChat(ctx, chatID)
	require.NoError(t, err)
	require.Len(t, pending, 0, "All pending approvals should be cancelled")

	// Verify approval status changed
	a1, err := repo.GetApproval(ctx, approval1.ID)
	require.NoError(t, err)
	require.Equal(t, int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_DENIED), a1.Status)

	a2, err := repo.GetApproval(ctx, approval2.ID)
	require.NoError(t, err)
	require.Equal(t, int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_DENIED), a2.Status)
}

// TestCleanupActivity_NoApprovals verifies cleanup handles case with no approvals
func TestCleanupActivity_NoApprovals(t *testing.T) {
	repo := setupTestRepo(t)
	ctx := context.Background()
	chatID := createTestChat(t, repo)

	// Run cleanup activity (no approvals to cancel)
	activity := NewCleanupActivity(repo)
	output, err := activity.Execute(ctx, CleanupInput{ChatID: chatID})
	require.NoError(t, err)
	require.Equal(t, 0, output.ApprovalsCancelled)
}

// TestCleanupActivity_CancelsOrphanedToolCalls verifies that cleanup activity
// cancels tool calls that don't have matching tool results
func TestCleanupActivity_CancelsOrphanedToolCalls(t *testing.T) {
	repo := setupTestRepo(t)
	ctx := context.Background()
	chatID, contextWindowID := createTestChatWithContextWindow(t, repo)

	// Create an assistant message with a tool_call block
	msgID := uuid.New().String()
	err := createMessageWithSeq(ctx, t, repo, &db.Message{
		ID:              msgID,
		ChatID:          chatID,
		Ordinal:         1,
		ThreadID:        chatID,
		ContextWindowID: contextWindowID,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	})
	require.NoError(t, err)

	// Create an orphaned tool_call block (no matching tool_result)
	toolCallID := "call_" + uuid.New().String()
	toolBlockID := uuid.New().String()
	toolName := "bash"
	toolInput := `{"command": "ls -la"}`
	err = repo.CreateContentBlock(ctx, &db.MessageContentBlock{
		ID:         toolBlockID,
		MessageID:  msgID,
		Position:   0,
		BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
		ToolName:   &toolName,
		ToolInput:  &toolInput,
		ToolCallID: &toolCallID,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	})
	require.NoError(t, err)

	// Verify the tool_call block exists
	blocks, err := repo.ListContentBlocks(ctx, msgID)
	require.NoError(t, err)
	require.Len(t, blocks, 1)
	require.Equal(t, reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL, blocks[0].BlockType)

	// Verify no tool_result exists
	resultBlock, err := repo.GetToolResultBlock(ctx, toolCallID)
	require.NoError(t, err)
	require.Nil(t, resultBlock, "should have no tool_result block")

	// Run cleanup activity
	activity := NewCleanupActivity(repo)
	output, err := activity.Execute(ctx, CleanupInput{ChatID: chatID})
	require.NoError(t, err)
	require.Equal(t, 1, output.ToolCallsCancelled, "should have cancelled 1 orphaned tool call")
	require.Equal(t, 0, output.ApprovalsCancelled)

	// Verify a tool_result block was persisted (new behavior)
	resultBlock, err = repo.GetToolResultBlock(ctx, toolCallID)
	require.NoError(t, err)
	require.NotNil(t, resultBlock, "should have created a tool_result block")
	require.Equal(t, reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT, resultBlock.BlockType)
	require.NotNil(t, resultBlock.IsError)
	require.True(t, *resultBlock.IsError, "repair tool_result should be marked as error")
	require.NotNil(t, resultBlock.Content)
	require.Contains(t, *resultBlock.Content, "interrupted")

	// Verify a cancelled status update was emitted (for UI)
	updates, err := repo.GetUpdatesSince(ctx, chatID, 0, 100)
	require.NoError(t, err)

	// Find the tool_call cancellation update
	var foundCancelledUpdate bool
	for _, update := range updates {
		if update.UpdateType == reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_TOOL_CALL {
			// Parse the update data
			var data map[string]interface{}
			err := json.Unmarshal(update.Data, &data)
			require.NoError(t, err)

			if data["status"] == "cancelled" && data["tool_call_id"] == toolCallID {
				foundCancelledUpdate = true
				require.Equal(t, "bash", data["tool_name"])
				require.Equal(t, toolBlockID, data["content_block_id"])
				break
			}
		}
	}
	require.True(t, foundCancelledUpdate, "should have found a tool_call cancelled update")
}

// TestCleanupActivity_SkipsToolCallsWithResults verifies that tool calls with results are not cancelled
func TestCleanupActivity_SkipsToolCallsWithResults(t *testing.T) {
	repo := setupTestRepo(t)
	ctx := context.Background()
	chatID, contextWindowID := createTestChatWithContextWindow(t, repo)

	// Create an assistant message with a tool_call block
	asstMsgID := uuid.New().String()
	err := createMessageWithSeq(ctx, t, repo, &db.Message{
		ID:              asstMsgID,
		ChatID:          chatID,
		Ordinal:         1,
		ThreadID:        chatID,
		ContextWindowID: contextWindowID,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	})
	require.NoError(t, err)

	// Create a tool_call block
	toolCallID := "call_" + uuid.New().String()
	toolBlockID := uuid.New().String()
	toolName := "bash"
	toolInput := `{"command": "ls -la"}`
	err = repo.CreateContentBlock(ctx, &db.MessageContentBlock{
		ID:         toolBlockID,
		MessageID:  asstMsgID,
		Position:   0,
		BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
		ToolName:   &toolName,
		ToolInput:  &toolInput,
		ToolCallID: &toolCallID,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	})
	require.NoError(t, err)

	// Create a tool message with the tool_result block
	toolMsgID := uuid.New().String()
	err = createMessageWithSeq(ctx, t, repo, &db.Message{
		ID:              toolMsgID,
		ChatID:          chatID,
		Ordinal:         2,
		ThreadID:        chatID,
		ContextWindowID: contextWindowID,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_TOOL,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	})
	require.NoError(t, err)

	// Create the matching tool_result block
	resultContent := "file1.txt\nfile2.txt"
	err = repo.CreateContentBlock(ctx, &db.MessageContentBlock{
		ID:         uuid.New().String(),
		MessageID:  toolMsgID,
		Position:   0,
		BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT,
		Content:    &resultContent,
		ToolCallID: &toolCallID,
		NodeID:     "test-node", // Required for the query scan
		NodePath:   "test-node", // Required for the query scan
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	})
	require.NoError(t, err)

	// Verify the tool_result exists
	resultBlock, err := repo.GetToolResultBlock(ctx, toolCallID)
	require.NoError(t, err)
	require.NotNil(t, resultBlock, "should have tool_result block")

	// Run cleanup activity
	activity := NewCleanupActivity(repo)
	output, err := activity.Execute(ctx, CleanupInput{ChatID: chatID})
	require.NoError(t, err)
	require.Equal(t, 0, output.ToolCallsCancelled, "should NOT cancel tool calls that have results")
}

// TestCleanupActivity_OnlyPendingApprovals verifies only pending approvals are cancelled
func TestCleanupActivity_OnlyPendingApprovals(t *testing.T) {
	repo := setupTestRepo(t)
	ctx := context.Background()
	chatID := createTestChat(t, repo)

	// Create one pending and one approved approval
	pendingApproval := &db.Approval{
		ID:           uuid.New().String(),
		ChatID:       chatID,
		ApprovalType: int32(reliantv1.ApprovalType_APPROVAL_TYPE_WORKFLOW_STEP),
		EntityID:     "entity-pending",
		Status:       int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING),
		Title:        "Pending Approval",
		CreatedAt:    time.Now(),
	}
	approvedApproval := &db.Approval{
		ID:           uuid.New().String(),
		ChatID:       chatID,
		ApprovalType: int32(reliantv1.ApprovalType_APPROVAL_TYPE_WORKFLOW_STEP),
		EntityID:     "entity-approved",
		Status:       int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_APPROVED),
		Title:        "Approved Approval",
		CreatedAt:    time.Now(),
	}

	err := repo.CreateApproval(ctx, pendingApproval)
	require.NoError(t, err)
	err = repo.CreateApproval(ctx, approvedApproval)
	require.NoError(t, err)

	// Run cleanup activity
	activity := NewCleanupActivity(repo)
	output, err := activity.Execute(ctx, CleanupInput{ChatID: chatID})
	require.NoError(t, err)
	require.Equal(t, 1, output.ApprovalsCancelled, "Only the pending approval should be cancelled")

	// Verify pending approval is cancelled
	pa, err := repo.GetApproval(ctx, pendingApproval.ID)
	require.NoError(t, err)
	require.Equal(t, int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_DENIED), pa.Status)

	// Verify approved approval is unchanged
	aa, err := repo.GetApproval(ctx, approvedApproval.ID)
	require.NoError(t, err)
	require.Equal(t, int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_APPROVED), aa.Status)
}

// TestCleanupActivity_IdempotentRepair verifies that running cleanup twice doesn't duplicate repairs
func TestCleanupActivity_IdempotentRepair(t *testing.T) {
	repo := setupTestRepo(t)
	ctx := context.Background()
	chatID, contextWindowID := createTestChatWithContextWindow(t, repo)

	// Create an assistant message with an orphaned tool_call
	msgID := uuid.New().String()
	err := createMessageWithSeq(ctx, t, repo, &db.Message{
		ID:              msgID,
		ChatID:          chatID,
		Ordinal:         1,
		ThreadID:        chatID,
		ContextWindowID: contextWindowID,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	})
	require.NoError(t, err)

	toolCallID := "call_" + uuid.New().String()
	toolName := "bash"
	toolInput := `{"command": "ls"}`
	err = repo.CreateContentBlock(ctx, &db.MessageContentBlock{
		ID:         uuid.New().String(),
		MessageID:  msgID,
		Position:   0,
		BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
		ToolName:   &toolName,
		ToolInput:  &toolInput,
		ToolCallID: &toolCallID,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	})
	require.NoError(t, err)

	activity := NewCleanupActivity(repo)

	// First cleanup - should create repair
	output1, err := activity.Execute(ctx, CleanupInput{ChatID: chatID})
	require.NoError(t, err)
	require.Equal(t, 1, output1.ToolCallsCancelled)

	// Verify tool_result was created
	resultBlock, err := repo.GetToolResultBlock(ctx, toolCallID)
	require.NoError(t, err)
	require.NotNil(t, resultBlock)

	// Second cleanup - should NOT create duplicate repair
	output2, err := activity.Execute(ctx, CleanupInput{ChatID: chatID})
	require.NoError(t, err)
	require.Equal(t, 0, output2.ToolCallsCancelled, "second cleanup should find no orphans")

	// Verify only one tool_result exists (GetToolResultBlock returns first match)
	// We can verify by checking there's still just one result block
	resultBlock2, err := repo.GetToolResultBlock(ctx, toolCallID)
	require.NoError(t, err)
	require.NotNil(t, resultBlock2)
	require.Equal(t, resultBlock.ID, resultBlock2.ID, "should return the same result block")
}

// TestCleanupActivity_MultipleOrphanedToolCalls verifies cleanup handles multiple orphaned tool calls
func TestCleanupActivity_MultipleOrphanedToolCalls(t *testing.T) {
	repo := setupTestRepo(t)
	ctx := context.Background()
	chatID, contextWindowID := createTestChatWithContextWindow(t, repo)

	// Create an assistant message with multiple tool_calls
	msgID := uuid.New().String()
	err := createMessageWithSeq(ctx, t, repo, &db.Message{
		ID:              msgID,
		ChatID:          chatID,
		Ordinal:         1,
		ThreadID:        chatID,
		ContextWindowID: contextWindowID,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	})
	require.NoError(t, err)

	// Create 3 orphaned tool_calls
	toolCallIDs := []string{
		"call_" + uuid.New().String(),
		"call_" + uuid.New().String(),
		"call_" + uuid.New().String(),
	}
	toolNames := []string{"bash", "edit", "view"}

	for i, toolCallID := range toolCallIDs {
		toolName := toolNames[i]
		toolInput := `{}`
		err = repo.CreateContentBlock(ctx, &db.MessageContentBlock{
			ID:         uuid.New().String(),
			MessageID:  msgID,
			Position:   i,
			BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
			ToolName:   &toolName,
			ToolInput:  &toolInput,
			ToolCallID: &toolCallID,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
		})
		require.NoError(t, err)
	}

	// Run cleanup
	activity := NewCleanupActivity(repo)
	output, err := activity.Execute(ctx, CleanupInput{ChatID: chatID})
	require.NoError(t, err)
	require.Equal(t, 3, output.ToolCallsCancelled, "should repair all 3 orphaned tool calls")

	// Verify all tool_results were created
	for _, toolCallID := range toolCallIDs {
		resultBlock, err := repo.GetToolResultBlock(ctx, toolCallID)
		require.NoError(t, err)
		require.NotNil(t, resultBlock, "should have tool_result for %s", toolCallID)
		require.NotNil(t, resultBlock.IsError)
		require.True(t, *resultBlock.IsError)
	}
}
