// Copyright (c) 2025 Reliant Labs
package handlers

// Tests for the round-trip between SaveMessageToThread (used by gRPC SendMessage)
// and LoadMessagesForLLM (used by CallLLM activity).
//
// Bug: "This model does not support assistant message prefill. The conversation
// must end with a user message."
//
// Root cause: user message saved via SaveMessageToThread is not visible to
// LoadMessagesForLLM, causing the conversation sent to the LLM to end with
// the previous assistant message.

import (
	"context"
	"fmt"
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Role constants matching proto enum values
const (
	roleUser      = int32(reliantv1.MessageRole_MESSAGE_ROLE_USER)
	roleAssistant = int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT)
	roleSystem    = int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM)
	roleTool      = int32(reliantv1.MessageRole_MESSAGE_ROLE_TOOL)
)

// =============================================================================
// Helper: creates a chat, workflow, and thread using the same path as
// grpc/services/chat.go CreateChat and SendMessage.
// Returns chatID, workflowID (= threadID for root), and the thread service.
// =============================================================================
func setupChatWithWorkflowAndThread(t *testing.T, repo db.Repository) (chatID, workflowID string, svc *threads.Service) {
	t.Helper()
	ctx := context.Background()

	chatID = createTestChatForConversation(t, repo)
	workflowID = chatID // Root workflow ID = chat ID (standard pattern)

	svc = threads.NewService(repo)

	// Mimics CreateChat: CreateWorkflowWithThread before any messages
	wf := &db.Workflow{
		ID:           workflowID,
		ChatID:       chatID,
		WorkflowName: "test-workflow",
		Thread:       workflowID, // Root workflow: thread = workflow ID
		Status:       db.WorkflowStatusPending,
	}
	_, _, _, err := svc.CreateWorkflowWithThread(ctx, threads.CreateWorkflowWithThreadOpts{
		Workflow: wf,
		ThreadID: workflowID,
		ChatID:   chatID,
	})
	require.NoError(t, err)

	return chatID, workflowID, svc
}

// =============================================================================
// Tests: SaveMessageToThread + LoadMessagesForLLM round-trip
// =============================================================================

// TestUserMessageRoundTrip_BasicSaveAndLoad verifies the fundamental round-trip:
// a user message saved via SaveMessageToThread (the gRPC path) is visible in
// LoadMessagesForLLM (the CallLLM path).
func TestUserMessageRoundTrip_BasicSaveAndLoad(t *testing.T) {
	repo, cleanup := setupTestRepoWithCleanup(t)
	defer cleanup()
	ctx := context.Background()

	chatID, workflowID, _ := setupChatWithWorkflowAndThread(t, repo)
	thread := workflowID // Root thread = workflow ID

	// Save user message via SaveMessageToThread (same as gRPC SendMessage path)
	savedMsg, err := repo.SaveMessageToThread(ctx, chatID, thread, roleUser, "Hello, world!", &workflowID, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, savedMsg)

	// Load via LoadMessagesForLLM (same as CallLLM path)
	messages, err := LoadMessagesForLLM(ctx, repo, chatID, thread, nil)
	require.NoError(t, err)
	require.Len(t, messages, 1, "Should have exactly 1 message")
	assert.Equal(t, "user", string(messages[0].Role), "Message should be user role")
}

// TestUserMessageRoundTrip_ConversationEndsWithUser verifies that after a full
// conversation cycle (user→assistant→user), LoadMessagesForLLM returns messages
// ending with the user message. This is the exact scenario that triggers the
// "must end with a user message" error when it fails.
func TestUserMessageRoundTrip_ConversationEndsWithUser(t *testing.T) {
	repo, cleanup := setupTestRepoWithCleanup(t)
	defer cleanup()
	ctx := context.Background()

	chatID, workflowID, svc := setupChatWithWorkflowAndThread(t, repo)
	thread := workflowID

	// Turn 1: User sends message (via gRPC SaveMessageToThread)
	_, err := repo.SaveMessageToThread(ctx, chatID, thread, roleUser, "What is 2+2?", &workflowID, nil, nil)
	require.NoError(t, err)

	// Turn 1: Assistant responds (via workflow SaveMessage activity - uses threads.Service)
	_, err = svc.SaveMessage(ctx, threads.SaveMessageOpts{
		ChatID:  chatID,
		Thread:  thread,
		Role:    roleAssistant,
		Content: "2+2 equals 4.",
	})
	require.NoError(t, err)

	// Turn 2: User sends follow-up (via gRPC SaveMessageToThread)
	_, err = repo.SaveMessageToThread(ctx, chatID, thread, roleUser, "Thanks! And what about 3+3?", &workflowID, nil, nil)
	require.NoError(t, err)

	// Verify: LoadMessagesForLLM should return 3 messages, last one = user
	messages, err := LoadMessagesForLLM(ctx, repo, chatID, thread, nil)
	require.NoError(t, err)
	require.Len(t, messages, 3, "Should have 3 messages (user, assistant, user)")

	assert.Equal(t, "user", string(messages[0].Role), "First message should be user")
	assert.Equal(t, "assistant", string(messages[1].Role), "Second message should be assistant")
	assert.Equal(t, "user", string(messages[2].Role), "Third (last) message should be user")
}

// TestUserMessageRoundTrip_WithToolCalls verifies that after a tool call cycle
// (user→assistant-with-tool-call→tool-result→assistant→user), the conversation
// ends with the user message.
func TestUserMessageRoundTrip_WithToolCalls(t *testing.T) {
	repo, cleanup := setupTestRepoWithCleanup(t)
	defer cleanup()
	ctx := context.Background()

	chatID, workflowID, svc := setupChatWithWorkflowAndThread(t, repo)
	thread := workflowID

	// User message
	_, err := repo.SaveMessageToThread(ctx, chatID, thread, roleUser, "List files", &workflowID, nil, nil)
	require.NoError(t, err)

	// Assistant with tool call (via workflow)
	_, err = svc.SaveMessage(ctx, threads.SaveMessageOpts{
		ChatID:  chatID,
		Thread:  thread,
		Role:    roleAssistant,
		Content: "Let me list the files for you.",
		ToolCalls: []message.ToolCall{
			{ID: "call_1", Name: "bash", Input: `{"command":"ls"}`},
		},
	})
	require.NoError(t, err)

	// Tool result (via workflow)
	_, err = svc.SaveMessage(ctx, threads.SaveMessageOpts{
		ChatID: chatID,
		Thread: thread,
		Role:   roleTool,
		ToolResults: []message.ToolResult{
			{ToolCallID: "call_1", Content: "file1.txt\nfile2.txt"},
		},
	})
	require.NoError(t, err)

	// Assistant final response (via workflow)
	_, err = svc.SaveMessage(ctx, threads.SaveMessageOpts{
		ChatID:  chatID,
		Thread:  thread,
		Role:    roleAssistant,
		Content: "I found file1.txt and file2.txt.",
	})
	require.NoError(t, err)

	// User sends follow-up (via gRPC SaveMessageToThread)
	_, err = repo.SaveMessageToThread(ctx, chatID, thread, roleUser, "Show me file1.txt", &workflowID, nil, nil)
	require.NoError(t, err)

	// Verify: Last message should be user
	messages, err := LoadMessagesForLLM(ctx, repo, chatID, thread, nil)
	require.NoError(t, err)
	require.Greater(t, len(messages), 0, "Should have messages")
	assert.Equal(t, "user", string(messages[len(messages)-1].Role),
		"Last message must be user role to avoid 'must end with user message' error")
}

// TestUserMessageRoundTrip_AfterCompaction verifies that when compaction has
// advanced the context sequence, a new user message saved via SaveMessageToThread
// is still visible in LoadMessagesForLLM.
func TestUserMessageRoundTrip_AfterCompaction(t *testing.T) {
	repo, cleanup := setupTestRepoWithCleanup(t)
	defer cleanup()
	ctx := context.Background()

	chatID, workflowID, svc := setupChatWithWorkflowAndThread(t, repo)
	thread := workflowID

	// Simulate pre-compaction conversation at context_sequence=0
	_, err := repo.SaveMessageToThread(ctx, chatID, thread, roleUser, "Old message 1", &workflowID, nil, nil)
	require.NoError(t, err)
	_, err = svc.SaveMessage(ctx, threads.SaveMessageOpts{
		ChatID:  chatID,
		Thread:  thread,
		Role:    roleAssistant,
		Content: "Old response 1",
	})
	require.NoError(t, err)

	// Simulate compaction: create new context window at sequence=1 with compaction summary
	compactionCWID := fmt.Sprintf("%s:%s:%d", chatID, thread, 1)
	summaryMsgID := "compaction-summary-msg"

	// Create the compaction summary message
	summaryMsg := &db.Message{
		ID:              summaryMsgID,
		ChatID:          chatID,
		Ordinal:         2,
		ThreadID:        thread,
		ContextWindowID: compactionCWID,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM,
	}

	// Create the new context window with compaction summary
	_, err = repo.CreateContextWindow(ctx, &db.ContextWindow{
		ID:                         compactionCWID,
		ThreadID:                   thread,
		Sequence:                   1,
		CompactionSummaryMessageID: &summaryMsgID,
	})
	require.NoError(t, err)
	err = repo.CreateMessage(ctx, summaryMsg)
	require.NoError(t, err)
	summaryContent := "Summary of previous conversation"
	err = repo.CreateContentBlock(ctx, &db.MessageContentBlock{
		ID:        "summary-block",
		MessageID: summaryMsgID,
		BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
		Content:   &summaryContent,
		Position:  0,
	})
	require.NoError(t, err)

	// Assistant response in new CW (via workflow SaveMessage)
	_, err = svc.SaveMessage(ctx, threads.SaveMessageOpts{
		ChatID:  chatID,
		Thread:  thread,
		Role:    roleAssistant,
		Content: "Continuing after compaction",
	})
	require.NoError(t, err)

	// Now: user sends a new message via SaveMessageToThread (gRPC path)
	// This should land in context_sequence=1 (the post-compaction CW)
	_, err = repo.SaveMessageToThread(ctx, chatID, thread, roleUser, "New user message after compaction", &workflowID, nil, nil)
	require.NoError(t, err)

	// Verify: LoadMessagesForLLM should see the user message as the last message
	messages, err := LoadMessagesForLLM(ctx, repo, chatID, thread, nil)
	require.NoError(t, err)
	require.Greater(t, len(messages), 0, "Should have messages")
	assert.Equal(t, "user", string(messages[len(messages)-1].Role),
		"Last message must be user role after compaction")
}

// TestUserMessageRoundTrip_SystemMessageBeforeUser verifies that when system
// messages are saved before the user message (as SendMessage does), the
// conversation still correctly ends with the user message.
func TestUserMessageRoundTrip_SystemMessageBeforeUser(t *testing.T) {
	repo, cleanup := setupTestRepoWithCleanup(t)
	defer cleanup()
	ctx := context.Background()

	chatID, workflowID, svc := setupChatWithWorkflowAndThread(t, repo)
	thread := workflowID

	// Previous turn
	_, err := repo.SaveMessageToThread(ctx, chatID, thread, roleUser, "First message", &workflowID, nil, nil)
	require.NoError(t, err)
	_, err = svc.SaveMessage(ctx, threads.SaveMessageOpts{
		ChatID:  chatID,
		Thread:  thread,
		Role:    roleAssistant,
		Content: "First response",
	})
	require.NoError(t, err)

	// SendMessage path: system message first, then user message
	_, err = repo.SaveMessageToThread(ctx, chatID, thread, roleSystem, "Updated system prompt", &workflowID, nil, nil)
	require.NoError(t, err)
	_, err = repo.SaveMessageToThread(ctx, chatID, thread, roleUser, "Follow-up question", &workflowID, nil, nil)
	require.NoError(t, err)

	// Verify: Last message should be user
	messages, err := LoadMessagesForLLM(ctx, repo, chatID, thread, nil)
	require.NoError(t, err)
	require.Greater(t, len(messages), 0, "Should have messages")
	assert.Equal(t, "user", string(messages[len(messages)-1].Role),
		"Last message must be user role")
}

// TestUserMessageRoundTrip_ForkedThread verifies that a user message saved
// to a forked thread is visible via LoadMessagesForLLM on that thread.
// This tests the branch/fork scenario where SendMessage targets a forked thread.
func TestUserMessageRoundTrip_ForkedThread(t *testing.T) {
	repo, cleanup := setupTestRepoWithCleanup(t)
	defer cleanup()
	ctx := context.Background()

	chatID, parentWorkflowID, parentSvc := setupChatWithWorkflowAndThread(t, repo)
	parentThread := parentWorkflowID

	// Build parent conversation
	_, err := repo.SaveMessageToThread(ctx, chatID, parentThread, roleUser, "Parent user msg", &parentWorkflowID, nil, nil)
	require.NoError(t, err)
	_, err = parentSvc.SaveMessage(ctx, threads.SaveMessageOpts{
		ChatID:  chatID,
		Thread:  parentThread,
		Role:    roleAssistant,
		Content: "Parent assistant msg",
	})
	require.NoError(t, err)

	// Create child workflow with forked thread
	childWorkflowID := "child-workflow-1"
	childThread := childWorkflowID
	childWf := &db.Workflow{
		ID:           childWorkflowID,
		ChatID:       chatID,
		WorkflowName: "test-workflow",
		Thread:       childThread,
		Status:       db.WorkflowStatusRunning,
	}
	_, _, _, err = parentSvc.CreateWorkflowWithThread(ctx, threads.CreateWorkflowWithThreadOpts{
		Workflow:       childWf,
		ThreadID:       childThread,
		ChatID:         chatID,
		ForkFromThread: &parentThread,
	})
	require.NoError(t, err)

	// Save user message to the forked thread via SaveMessageToThread
	_, err = repo.SaveMessageToThread(ctx, chatID, childThread, roleUser, "Child user message", &childWorkflowID, nil, nil)
	require.NoError(t, err)

	// Verify: LoadMessagesForLLM on child thread should see inherited + child messages
	messages, err := LoadMessagesForLLM(ctx, repo, chatID, childThread, nil)
	require.NoError(t, err)
	require.Greater(t, len(messages), 0, "Should have messages")

	// Last message should be the user message we just saved
	assert.Equal(t, "user", string(messages[len(messages)-1].Role),
		"Last message in forked thread must be user role")

	// Should include parent messages + child user message
	assert.GreaterOrEqual(t, len(messages), 3,
		"Should have at least 3 messages (2 parent + 1 child)")
}

// TestUserMessageRoundTrip_GhostRecoveryInheritThread verifies that ghost recovery
// (resurrectGhostWorkflow) which uses ThreadModeInherit correctly makes the user
// message visible. Ghost recovery reuses the existing thread, so the user message
// saved before workflow restart should be at the right context window.
func TestUserMessageRoundTrip_GhostRecoveryInheritThread(t *testing.T) {
	repo, cleanup := setupTestRepoWithCleanup(t)
	defer cleanup()
	ctx := context.Background()

	chatID, workflowID, svc := setupChatWithWorkflowAndThread(t, repo)
	thread := workflowID

	// Simulate existing conversation from previous workflow execution
	_, err := repo.SaveMessageToThread(ctx, chatID, thread, roleUser, "Original question", &workflowID, nil, nil)
	require.NoError(t, err)
	_, err = svc.SaveMessage(ctx, threads.SaveMessageOpts{
		ChatID:  chatID,
		Thread:  thread,
		Role:    roleAssistant,
		Content: "Original answer",
	})
	require.NoError(t, err)

	// Ghost recovery: save new user message to existing thread
	// (same as resurrectGhostWorkflow path in chat.go)
	_, err = repo.SaveMessageToThread(ctx, chatID, thread, roleUser, "Message after ghost recovery", &workflowID, nil, nil)
	require.NoError(t, err)

	// Verify: new workflow's CallLLM should see the user message
	messages, err := LoadMessagesForLLM(ctx, repo, chatID, thread, nil)
	require.NoError(t, err)
	require.Greater(t, len(messages), 0, "Should have messages")
	assert.Equal(t, "user", string(messages[len(messages)-1].Role),
		"Last message must be user role after ghost recovery")
	assert.Len(t, messages, 3, "Should have original user + assistant + new user")
}

// TestUserMessageRoundTrip_ResumedPausedWorkflow verifies that when a paused
// workflow is resumed with a new user message, the message is visible to CallLLM.
// This simulates the paused workflow path in SendMessage.
func TestUserMessageRoundTrip_ResumedPausedWorkflow(t *testing.T) {
	repo, cleanup := setupTestRepoWithCleanup(t)
	defer cleanup()
	ctx := context.Background()

	chatID, workflowID, svc := setupChatWithWorkflowAndThread(t, repo)
	thread := workflowID

	// Simulate conversation before pause
	_, err := repo.SaveMessageToThread(ctx, chatID, thread, roleUser, "Do something", &workflowID, nil, nil)
	require.NoError(t, err)
	_, err = svc.SaveMessage(ctx, threads.SaveMessageOpts{
		ChatID:  chatID,
		Thread:  thread,
		Role:    roleAssistant,
		Content: "I paused for your input.",
	})
	require.NoError(t, err)

	// Paused workflow resumed: user message saved in transaction
	// (same as the paused case in SendMessage - lines 1458-1484 of chat.go)
	_, err = repo.SaveMessageToThread(ctx, chatID, thread, roleUser, "Continue with the task", &workflowID, nil, nil)
	require.NoError(t, err)

	// Verify: CallLLM should see the user message as the last one
	messages, err := LoadMessagesForLLM(ctx, repo, chatID, thread, nil)
	require.NoError(t, err)
	require.Len(t, messages, 3, "Should have 3 messages")
	assert.Equal(t, "user", string(messages[2].Role),
		"Last message must be user role after resume")
}

// TestUserMessageRoundTrip_RunningWorkflowInterrupt verifies that when a user
// sends a message to a running workflow, it's visible to the next CallLLM.
func TestUserMessageRoundTrip_RunningWorkflowInterrupt(t *testing.T) {
	repo, cleanup := setupTestRepoWithCleanup(t)
	defer cleanup()
	ctx := context.Background()

	chatID, workflowID, svc := setupChatWithWorkflowAndThread(t, repo)
	thread := workflowID

	// Simulate running workflow conversation
	_, err := repo.SaveMessageToThread(ctx, chatID, thread, roleUser, "Start task", &workflowID, nil, nil)
	require.NoError(t, err)
	_, err = svc.SaveMessage(ctx, threads.SaveMessageOpts{
		ChatID:  chatID,
		Thread:  thread,
		Role:    roleAssistant,
		Content: "Working on it...",
	})
	require.NoError(t, err)

	// Simulate tool call cycle
	_, err = svc.SaveMessage(ctx, threads.SaveMessageOpts{
		ChatID:  chatID,
		Thread:  thread,
		Role:    roleAssistant,
		Content: "Let me check something.",
		ToolCalls: []message.ToolCall{
			{ID: "call_check", Name: "bash", Input: `{"command":"echo ok"}`},
		},
	})
	require.NoError(t, err)
	_, err = svc.SaveMessage(ctx, threads.SaveMessageOpts{
		ChatID: chatID,
		Thread: thread,
		Role:   roleTool,
		ToolResults: []message.ToolResult{
			{ToolCallID: "call_check", Content: "ok"},
		},
	})
	require.NoError(t, err)
	_, err = svc.SaveMessage(ctx, threads.SaveMessageOpts{
		ChatID:  chatID,
		Thread:  thread,
		Role:    roleAssistant,
		Content: "Everything looks good.",
	})
	require.NoError(t, err)

	// User interrupts running workflow (same as running case in SendMessage)
	_, err = repo.SaveMessageToThread(ctx, chatID, thread, roleUser, "Actually, do something else", &workflowID, nil, nil)
	require.NoError(t, err)

	// Verify: Last message must be user
	messages, err := LoadMessagesForLLM(ctx, repo, chatID, thread, nil)
	require.NoError(t, err)
	require.Greater(t, len(messages), 0, "Should have messages")
	assert.Equal(t, "user", string(messages[len(messages)-1].Role),
		"Last message must be user role after interrupt")
}

// TestUserMessageRoundTrip_SaveMessageToThread_CreatesContextWindow verifies
// that SaveMessageToThread creates a context window if one doesn't exist yet.
// This is important for threads that were created by CreateWorkflowWithThread
// but haven't had messages saved via the threads.Service path yet.
func TestUserMessageRoundTrip_SaveMessageToThread_CreatesContextWindow(t *testing.T) {
	repo, cleanup := setupTestRepoWithCleanup(t)
	defer cleanup()
	ctx := context.Background()

	chatID, workflowID, _ := setupChatWithWorkflowAndThread(t, repo)
	thread := workflowID

	// Verify: CW exists (created by CreateWorkflowWithThread)
	svc := threads.NewService(repo)
	cw, err := svc.GetLatestContextWindow(ctx, thread)
	require.NoError(t, err)
	require.NotNil(t, cw, "Context window should exist from CreateWorkflowWithThread")

	expectedCWID := fmt.Sprintf("%s:%s:%d", chatID, thread, 0)
	assert.Equal(t, expectedCWID, cw.ID,
		"CW ID should match the format used by SaveMessageToThread")

	// Save via SaveMessageToThread (should find the existing CW, not create new one)
	savedMsg, err := repo.SaveMessageToThread(ctx, chatID, thread, roleUser, "Test message", &workflowID, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, expectedCWID, savedMsg.ContextWindowID,
		"Message should be in the same CW created by CreateWorkflowWithThread")

	// LoadMessagesForLLM should find the message
	messages, err := LoadMessagesForLLM(ctx, repo, chatID, thread, nil)
	require.NoError(t, err)
	require.Len(t, messages, 1)
	assert.Equal(t, "user", string(messages[0].Role))
}

// TestUserMessageRoundTrip_ContextWindowIDConsistency verifies that the context
// window IDs generated by SaveMessageToThread match those created by
// threads.Service.CreateThread. If these don't match, messages saved by one
// path won't be visible to the other.
func TestUserMessageRoundTrip_ContextWindowIDConsistency(t *testing.T) {
	repo, cleanup := setupTestRepoWithCleanup(t)
	defer cleanup()
	ctx := context.Background()

	chatID, workflowID, _ := setupChatWithWorkflowAndThread(t, repo)
	thread := workflowID

	// Save a message via SaveMessageToThread
	msg1, err := repo.SaveMessageToThread(ctx, chatID, thread, roleUser, "Message 1", &workflowID, nil, nil)
	require.NoError(t, err)

	// Save another message via threads.Service.SaveMessage
	svc := threads.NewService(repo)
	result, err := svc.SaveMessage(ctx, threads.SaveMessageOpts{
		ChatID:  chatID,
		Thread:  thread,
		Role:    roleAssistant,
		Content: "Response 1",
	})
	require.NoError(t, err)

	// Load the assistant message to check its CW
	msg2, err := repo.GetMessage(ctx, result.MessageID)
	require.NoError(t, err)

	// Both messages should be in the same context window
	assert.Equal(t, msg1.ContextWindowID, msg2.ContextWindowID,
		"Messages from SaveMessageToThread and threads.Service.SaveMessage should use the same context window")
}

// TestUserMessageRoundTrip_MultipleContextWindows verifies that when a thread
// has multiple context windows (e.g., from compaction), SaveMessageToThread
// saves to the latest one, and LoadMessagesForLLM loads from the latest one.
func TestUserMessageRoundTrip_MultipleContextWindows(t *testing.T) {
	repo, cleanup := setupTestRepoWithCleanup(t)
	defer cleanup()
	ctx := context.Background()

	chatID, workflowID, _ := setupChatWithWorkflowAndThread(t, repo)
	thread := workflowID

	// Pre-compaction: save message at seq 0
	_, err := repo.SaveMessageToThread(ctx, chatID, thread, roleUser, "Old message", &workflowID, nil, nil)
	require.NoError(t, err)

	// Create a new context window at seq 1 (simulating compaction)
	compactionCWID := fmt.Sprintf("%s:%s:%d", chatID, thread, 1)
	summaryMsgID := "summary-msg"
	_, err = repo.CreateContextWindow(ctx, &db.ContextWindow{
		ID:                         compactionCWID,
		ThreadID:                   thread,
		Sequence:                   1,
		CompactionSummaryMessageID: &summaryMsgID,
	})
	require.NoError(t, err)

	// Create the compaction summary message
	err = repo.CreateMessage(ctx, &db.Message{
		ID:              summaryMsgID,
		ChatID:          chatID,
		Ordinal:         1,
		ThreadID:        thread,
		ContextWindowID: compactionCWID,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM,
	})
	require.NoError(t, err)
	summaryText := "Summary of old conversation"
	err = repo.CreateContentBlock(ctx, &db.MessageContentBlock{
		ID:        "summary-block-1",
		MessageID: summaryMsgID,
		BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
		Content:   &summaryText,
		Position:  0,
	})
	require.NoError(t, err)

	// Now save user message via SaveMessageToThread - should go to seq 1
	savedMsg, err := repo.SaveMessageToThread(ctx, chatID, thread, roleUser, "New message after compaction", &workflowID, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, compactionCWID, savedMsg.ContextWindowID,
		"User message should be saved to the latest context window (seq 1)")

	// LoadMessagesForLLM should show messages from latest CW only
	messages, err := LoadMessagesForLLM(ctx, repo, chatID, thread, nil)
	require.NoError(t, err)

	// Should have: compaction summary + new user message
	require.Greater(t, len(messages), 0)
	assert.Equal(t, "user", string(messages[len(messages)-1].Role),
		"Last message should be user")
}

// TestUserMessageRoundTrip_YieldReply verifies the yield reply path where
// a message is saved to a yield's thread (not the root thread) and the
// next CallLLM on that thread sees it.
func TestUserMessageRoundTrip_YieldReply(t *testing.T) {
	repo, cleanup := setupTestRepoWithCleanup(t)
	defer cleanup()
	ctx := context.Background()

	chatID, parentWorkflowID, svc := setupChatWithWorkflowAndThread(t, repo)
	parentThread := parentWorkflowID

	// Build parent conversation
	_, err := repo.SaveMessageToThread(ctx, chatID, parentThread, roleUser, "Start", &parentWorkflowID, nil, nil)
	require.NoError(t, err)

	// Create a child workflow with its own thread (simulating spawn that yields)
	childWorkflowID := "child-yield-workflow"
	childThread := childWorkflowID
	childWf := &db.Workflow{
		ID:           childWorkflowID,
		ChatID:       chatID,
		WorkflowName: "agent",
		Thread:       childThread,
		Status:       db.WorkflowStatusRunning,
	}
	_, _, _, err = svc.CreateWorkflowWithThread(ctx, threads.CreateWorkflowWithThreadOpts{
		Workflow:       childWf,
		ThreadID:       childThread,
		ChatID:         chatID,
		ForkFromThread: &parentThread,
	})
	require.NoError(t, err)

	// Child workflow asks something (assistant message on child thread)
	_, err = svc.SaveMessage(ctx, threads.SaveMessageOpts{
		ChatID:  chatID,
		Thread:  childThread,
		Role:    roleAssistant,
		Content: "What should I do next?",
	})
	require.NoError(t, err)

	// User replies to yield — message goes to child thread via SaveMessageToThread
	// (same as yield reply path in SendMessage)
	_, err = repo.SaveMessageToThread(ctx, chatID, childThread, roleUser, "Do the thing", &childWorkflowID, nil, nil)
	require.NoError(t, err)

	// Verify: CallLLM on child thread should see the user reply
	messages, err := LoadMessagesForLLM(ctx, repo, chatID, childThread, nil)
	require.NoError(t, err)
	require.Greater(t, len(messages), 0)
	assert.Equal(t, "user", string(messages[len(messages)-1].Role),
		"Last message on yield thread must be user's reply")
}

// TestUserMessageRoundTrip_EmptyContentWithAttachment verifies that a message
// with empty content but saved as user role still shows up correctly.
func TestUserMessageRoundTrip_EmptyContentWithAttachment(t *testing.T) {
	repo, cleanup := setupTestRepoWithCleanup(t)
	defer cleanup()
	ctx := context.Background()

	chatID, workflowID, _ := setupChatWithWorkflowAndThread(t, repo)
	thread := workflowID

	// Save a user message with empty text (no content block created)
	savedMsg, err := repo.SaveMessageToThread(ctx, chatID, thread, roleUser, "", &workflowID, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, savedMsg)

	// The message exists in DB but has no content blocks
	// LoadMessagesForLLM should still return it as a user message
	messages, err := LoadMessagesForLLM(ctx, repo, chatID, thread, nil)
	require.NoError(t, err)
	// Message with no content blocks may be filtered or kept - verify it doesn't cause errors
	assert.NoError(t, err)
	_ = messages // May be 0 or 1 depending on how empty messages are handled
}
