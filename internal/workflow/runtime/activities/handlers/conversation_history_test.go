// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Test Helpers
// =============================================================================

// setupTestRepoWithCleanup creates an in-memory repository for testing with cleanup
func setupTestRepoWithCleanup(t *testing.T) (db.Repository, func()) {
	t.Helper()

	repo := db.NewTestRepo(t)

	// NewTestRepo already seeds the shared "test-project" row (ON CONFLICT DO
	// NOTHING). Re-creating it here would collide (projects_pkey) with the seed
	// and with every other test in this package that shares the same Postgres DB.

	cleanup := func() {
		repo.Close()
	}

	return repo, cleanup
}

// createTestChatForConversation creates a test chat
func createTestChatForConversation(t *testing.T, repo db.Repository) string {
	t.Helper()
	ctx := context.Background()
	chatID := uuid.New().String()

	err := repo.CreateChat(ctx, &db.Chat{
		ID:        chatID,
		Title:     "Test Chat",
		ProjectID: "test-project",
		UserID:    "test-user",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})
	require.NoError(t, err)

	return chatID
}

// createThreadWithContextWindow creates a thread and an initial context window
// Returns threadID, contextWindowID
// When forkAtContextWindowID is provided, sets up CW lineage (ParentContextWindowID, ForkAtMessageID).
// forkAtOrdinal is a position in forkAtContextWindowID's messages, resolved
// here to the message it names -- callers pass ordinals because fixtures
// already track ordinals for message identity, and the resolution mirrors
// 20260803010000_fork_points_reference_messages.sql's backfill.
func createThreadWithContextWindow(t *testing.T, repo db.Repository, chatID string, parentThreadID *string, forkAtOrdinal *int64, forkAtContextWindowID *string, sequence int) (string, string) {
	t.Helper()
	ctx := context.Background()

	threadID := uuid.New().String()
	contextWindowID := uuid.New().String()

	var forkAtMessageID *string
	if forkAtOrdinal != nil && forkAtContextWindowID != nil {
		msgs, err := repo.GetMessagesByContextWindow(ctx, *forkAtContextWindowID, nil)
		require.NoError(t, err)
		for _, m := range msgs {
			if m.Ordinal == *forkAtOrdinal {
				id := m.ID
				forkAtMessageID = &id
				break
			}
		}
	}

	// Create thread
	_, err := repo.CreateThread(ctx, &db.Thread{
		ID:              threadID,
		ChatID:          chatID,
		ParentThreadID:  parentThreadID,
		ForkAtMessageID: forkAtMessageID,
		CreatedAt:       time.Now(),
	})
	require.NoError(t, err)

	// Create context window with CW lineage for forked threads
	_, err = repo.CreateContextWindow(ctx, &db.ContextWindow{
		ID:                    contextWindowID,
		ThreadID:              threadID,
		Sequence:              sequence,
		ParentContextWindowID: forkAtContextWindowID, // Link to parent CW for resolution
		ForkAtMessageID:       forkAtMessageID,       // Message to inherit from parent up to
	})
	require.NoError(t, err)

	return threadID, contextWindowID
}

// createMessageWithParts creates a message with text content using the new model
func createMessageWithParts(t *testing.T, repo db.Repository, chatID, threadID, contextWindowID string, ordinal int64, role reliantv1.MessageRole, text string) *db.Message {
	t.Helper()
	ctx := context.Background()

	seq, err := repo.GetNextSeq(ctx, chatID, threadID)
	require.NoError(t, err)

	msg := &db.Message{
		ID:              uuid.New().String(),
		ChatID:          chatID,
		Ordinal:         ordinal,
		Seq:             seq,
		ThreadID:        threadID,
		ContextWindowID: contextWindowID,
		Role:            role,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}

	err = repo.CreateMessage(ctx, msg)
	require.NoError(t, err)

	// Create text content block
	block := &db.MessageContentBlock{
		ID:        uuid.New().String(),
		MessageID: msg.ID,
		BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
		Content:   &text,
		Position:  0,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	err = repo.CreateContentBlock(ctx, block)
	require.NoError(t, err)

	return msg
}

// createMessageWithToolCall creates an assistant message with a tool call
func createMessageWithToolCall(t *testing.T, repo db.Repository, chatID, threadID, contextWindowID string, ordinal int64, toolCallID, toolName, toolInput string) *db.Message {
	t.Helper()
	ctx := context.Background()

	seq, err := repo.GetNextSeq(ctx, chatID, threadID)
	require.NoError(t, err)

	msg := &db.Message{
		ID:              uuid.New().String(),
		ChatID:          chatID,
		Ordinal:         ordinal,
		Seq:             seq,
		ThreadID:        threadID,
		ContextWindowID: contextWindowID,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}

	err = repo.CreateMessage(ctx, msg)
	require.NoError(t, err)

	// Create tool_call content block
	block := &db.MessageContentBlock{
		ID:         uuid.New().String(),
		MessageID:  msg.ID,
		BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
		ToolName:   &toolName,
		ToolInput:  &toolInput,
		ToolCallID: &toolCallID,
		Position:   0,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	err = repo.CreateContentBlock(ctx, block)
	require.NoError(t, err)

	return msg
}

// createMessageWithToolResult creates a tool message with a result
func createMessageWithToolResult(t *testing.T, repo db.Repository, chatID, threadID, contextWindowID string, ordinal int64, toolCallID, content string) *db.Message {
	t.Helper()
	ctx := context.Background()

	seq, err := repo.GetNextSeq(ctx, chatID, threadID)
	require.NoError(t, err)

	msg := &db.Message{
		ID:              uuid.New().String(),
		ChatID:          chatID,
		Ordinal:         ordinal,
		Seq:             seq,
		ThreadID:        threadID,
		ContextWindowID: contextWindowID,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_TOOL,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}

	err = repo.CreateMessage(ctx, msg)
	require.NoError(t, err)

	// Create tool_result content block
	block := &db.MessageContentBlock{
		ID:         uuid.New().String(),
		MessageID:  msg.ID,
		BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT,
		Content:    &content,
		ToolCallID: &toolCallID,
		Position:   0,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	err = repo.CreateContentBlock(ctx, block)
	require.NoError(t, err)

	return msg
}

// =============================================================================
// Table-Driven Tests: LoadMessagesForLLM
// =============================================================================

func TestLoadMessagesForLLM_TableDriven(t *testing.T) {
	tests := []struct {
		name                 string
		setup                func(t *testing.T, repo db.Repository) (chatID, thread string, explicitContextSeq *int)
		expectedMsgCount     int
		expectedFirstMsgRole string // Role of first message
		expectedLastMsgRole  string // Role of last message
		wantErr              bool
		errContains          string
	}{
		{
			name: "non-forked thread loads local messages only",
			setup: func(t *testing.T, repo db.Repository) (string, string, *int) {
				chatID := createTestChatForConversation(t, repo)
				threadID, contextWindowID := createThreadWithContextWindow(t, repo, chatID, nil, nil, nil, 0)
				createMessageWithParts(t, repo, chatID, threadID, contextWindowID, 1, reliantv1.MessageRole_MESSAGE_ROLE_USER, "Hello")
				createMessageWithParts(t, repo, chatID, threadID, contextWindowID, 2, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, "Hi there!")
				createMessageWithParts(t, repo, chatID, threadID, contextWindowID, 3, reliantv1.MessageRole_MESSAGE_ROLE_USER, "How are you?")
				return chatID, threadID, nil
			},
			expectedMsgCount:     3,
			expectedFirstMsgRole: "user",
			expectedLastMsgRole:  "user",
		},
		{
			name: "forked thread includes parent messages up to fork point",
			setup: func(t *testing.T, repo db.Repository) (string, string, *int) {
				chatID := createTestChatForConversation(t, repo)

				// Parent thread with messages
				parentThreadID, parentContextWindowID := createThreadWithContextWindow(t, repo, chatID, nil, nil, nil, 0)
				createMessageWithParts(t, repo, chatID, parentThreadID, parentContextWindowID, 1, reliantv1.MessageRole_MESSAGE_ROLE_USER, "Parent msg 1")
				createMessageWithParts(t, repo, chatID, parentThreadID, parentContextWindowID, 2, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, "Parent msg 2")
				createMessageWithParts(t, repo, chatID, parentThreadID, parentContextWindowID, 3, reliantv1.MessageRole_MESSAGE_ROLE_USER, "Parent msg 3")      // After fork
				createMessageWithParts(t, repo, chatID, parentThreadID, parentContextWindowID, 4, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, "Parent msg 4") // After fork

				// Child thread forked at ordinal 2
				forkOrdinal := int64(2)
				childThreadID, childContextWindowID := createThreadWithContextWindow(t, repo, chatID, &parentThreadID, &forkOrdinal, &parentContextWindowID, 0)
				createMessageWithParts(t, repo, chatID, childThreadID, childContextWindowID, 5, reliantv1.MessageRole_MESSAGE_ROLE_USER, "Child msg 1")
				createMessageWithParts(t, repo, chatID, childThreadID, childContextWindowID, 6, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, "Child msg 2")

				return chatID, childThreadID, nil
			},
			expectedMsgCount:     4, // 2 parent + 2 child
			expectedFirstMsgRole: "user",
			expectedLastMsgRole:  "assistant",
		},
		{
			name: "fork chain (A→B→C) resolves correctly",
			setup: func(t *testing.T, repo db.Repository) (string, string, *int) {
				chatID := createTestChatForConversation(t, repo)

				// Thread A: messages 1, 2
				threadA, contextWindowA := createThreadWithContextWindow(t, repo, chatID, nil, nil, nil, 0)
				createMessageWithParts(t, repo, chatID, threadA, contextWindowA, 1, reliantv1.MessageRole_MESSAGE_ROLE_USER, "A1")
				createMessageWithParts(t, repo, chatID, threadA, contextWindowA, 2, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, "A2")

				// Thread B forked from A at ordinal 2
				forkOrdinalAtoB := int64(2)
				threadB, contextWindowB := createThreadWithContextWindow(t, repo, chatID, &threadA, &forkOrdinalAtoB, &contextWindowA, 0)
				createMessageWithParts(t, repo, chatID, threadB, contextWindowB, 3, reliantv1.MessageRole_MESSAGE_ROLE_USER, "B1")

				// Thread C forked from B at ordinal 3
				forkOrdinalBtoC := int64(3)
				threadC, contextWindowC := createThreadWithContextWindow(t, repo, chatID, &threadB, &forkOrdinalBtoC, &contextWindowB, 0)
				createMessageWithParts(t, repo, chatID, threadC, contextWindowC, 4, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, "C1")

				return chatID, threadC, nil
			},
			expectedMsgCount:     4, // 2 from A + 1 from B + 1 from C
			expectedFirstMsgRole: "user",
			expectedLastMsgRole:  "assistant",
		},
		{
			name: "latest context sequence is used when explicit not provided",
			setup: func(t *testing.T, repo db.Repository) (string, string, *int) {
				chatID := createTestChatForConversation(t, repo)
				threadID, _ := createThreadWithContextWindow(t, repo, chatID, nil, nil, nil, 0)

				// Create second context window (post-compaction)
				ctx := context.Background()
				contextWindowID1 := uuid.New().String()
				_, err := repo.CreateContextWindow(ctx, &db.ContextWindow{
					ID:       contextWindowID1,
					ThreadID: threadID,
					Sequence: 1,
				})
				require.NoError(t, err)

				// Messages at context_sequence=1 (post-compaction)
				createMessageWithParts(t, repo, chatID, threadID, contextWindowID1, 3, reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM, "Compaction summary")
				createMessageWithParts(t, repo, chatID, threadID, contextWindowID1, 4, reliantv1.MessageRole_MESSAGE_ROLE_USER, "New message")

				// Don't pass explicit context seq - should use latest
				return chatID, threadID, nil
			},
			expectedMsgCount:     2, // Only seq=1 messages (latest)
			expectedFirstMsgRole: "system",
			expectedLastMsgRole:  "user",
		},
		{
			name: "empty thread returns empty slice",
			setup: func(t *testing.T, repo db.Repository) (string, string, *int) {
				chatID := createTestChatForConversation(t, repo)
				threadID, _ := createThreadWithContextWindow(t, repo, chatID, nil, nil, nil, 0)
				return chatID, threadID, nil
			},
			expectedMsgCount: 0,
		},
		{
			name: "fork with no parent messages works",
			setup: func(t *testing.T, repo db.Repository) (string, string, *int) {
				chatID := createTestChatForConversation(t, repo)

				// Parent thread with no messages
				parentThreadID, parentContextWindowID := createThreadWithContextWindow(t, repo, chatID, nil, nil, nil, 0)

				// Child thread forked at ordinal 0
				forkOrdinal := int64(0)
				childThreadID, childContextWindowID := createThreadWithContextWindow(t, repo, chatID, &parentThreadID, &forkOrdinal, &parentContextWindowID, 0)
				createMessageWithParts(t, repo, chatID, childThreadID, childContextWindowID, 1, reliantv1.MessageRole_MESSAGE_ROLE_USER, "First in child")

				return chatID, childThreadID, nil
			},
			expectedMsgCount:     1,
			expectedFirstMsgRole: "user",
			expectedLastMsgRole:  "user",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, cleanup := setupTestRepoWithCleanup(t)
			defer cleanup()
			ctx := context.Background()

			chatID, thread, explicitContextSeq := tt.setup(t, repo)

			messages, err := LoadMessagesForLLM(ctx, repo, chatID, thread, explicitContextSeq)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
				return
			}

			require.NoError(t, err)
			assert.Len(t, messages, tt.expectedMsgCount,
				"Expected %d messages, got %d", tt.expectedMsgCount, len(messages))

			if len(messages) > 0 {
				assert.Equal(t, tt.expectedFirstMsgRole, string(messages[0].Role),
					"First message role mismatch")
				assert.Equal(t, tt.expectedLastMsgRole, string(messages[len(messages)-1].Role),
					"Last message role mismatch")
			}
		})
	}
}

// =============================================================================
// Table-Driven Tests: Orphaned Tool Call Repair
// =============================================================================

func TestLoadMessagesForLLM_OrphanedToolCallRepair(t *testing.T) {
	tests := []struct {
		name             string
		setup            func(t *testing.T, repo db.Repository) (chatID, thread string)
		expectedMsgCount int
		validateRepair   func(t *testing.T, messages []message.Message) // Validate repair was applied
	}{
		{
			name: "repairs orphaned tool call with synthetic result",
			setup: func(t *testing.T, repo db.Repository) (string, string) {
				chatID := createTestChatForConversation(t, repo)
				threadID, contextWindowID := createThreadWithContextWindow(t, repo, chatID, nil, nil, nil, 0)

				// User message
				createMessageWithParts(t, repo, chatID, threadID, contextWindowID, 1, reliantv1.MessageRole_MESSAGE_ROLE_USER, "List files")

				// Assistant with tool call (no matching result)
				createMessageWithToolCall(t, repo, chatID, threadID, contextWindowID, 2, "call_orphan", "bash", `{"command":"ls"}`)

				return chatID, threadID
			},
			expectedMsgCount: 3, // user + assistant with tool_call + synthetic tool_result
		},
		{
			name: "no repair needed when tool result exists",
			setup: func(t *testing.T, repo db.Repository) (string, string) {
				chatID := createTestChatForConversation(t, repo)
				threadID, contextWindowID := createThreadWithContextWindow(t, repo, chatID, nil, nil, nil, 0)

				// User message
				createMessageWithParts(t, repo, chatID, threadID, contextWindowID, 1, reliantv1.MessageRole_MESSAGE_ROLE_USER, "List files")

				// Assistant with tool call
				createMessageWithToolCall(t, repo, chatID, threadID, contextWindowID, 2, "call_123", "bash", `{"command":"ls"}`)

				// Tool result
				createMessageWithToolResult(t, repo, chatID, threadID, contextWindowID, 3, "call_123", "file1.txt\nfile2.txt")

				return chatID, threadID
			},
			expectedMsgCount: 3, // No repair needed
		},
		{
			name: "repairs multiple orphaned tool calls",
			setup: func(t *testing.T, repo db.Repository) (string, string) {
				chatID := createTestChatForConversation(t, repo)
				threadID, contextWindowID := createThreadWithContextWindow(t, repo, chatID, nil, nil, nil, 0)

				// User message
				createMessageWithParts(t, repo, chatID, threadID, contextWindowID, 1, reliantv1.MessageRole_MESSAGE_ROLE_USER, "Do tasks")

				// Assistant with first tool call
				createMessageWithToolCall(t, repo, chatID, threadID, contextWindowID, 2, "call_1", "task1", `{}`)

				// Tool result for first
				createMessageWithToolResult(t, repo, chatID, threadID, contextWindowID, 3, "call_1", "done1")

				// Assistant with second tool call (orphaned)
				createMessageWithToolCall(t, repo, chatID, threadID, contextWindowID, 4, "call_2", "task2", `{}`)

				return chatID, threadID
			},
			expectedMsgCount: 5, // user + tool_call + tool_result + tool_call + synthetic result
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, cleanup := setupTestRepoWithCleanup(t)
			defer cleanup()
			ctx := context.Background()

			chatID, thread := tt.setup(t, repo)

			messages, err := LoadMessagesForLLM(ctx, repo, chatID, thread, nil)

			require.NoError(t, err)
			assert.Len(t, messages, tt.expectedMsgCount,
				"Expected %d messages, got %d", tt.expectedMsgCount, len(messages))

			if tt.validateRepair != nil {
				tt.validateRepair(t, messages)
			}
		})
	}
}

// =============================================================================
// Test: Message Ordering
// =============================================================================

func TestLoadMessagesForLLM_MessageOrdering(t *testing.T) {
	repo, cleanup := setupTestRepoWithCleanup(t)
	defer cleanup()
	ctx := context.Background()

	chatID := createTestChatForConversation(t, repo)
	threadID, contextWindowID := createThreadWithContextWindow(t, repo, chatID, nil, nil, nil, 0)

	// Ordinals deliberately disagree with insertion order. seq is assigned at
	// persistence time (GetNextSeq), so the load order must follow insertion,
	// not the ordinal values.
	createMessageWithParts(t, repo, chatID, threadID, contextWindowID, 3, reliantv1.MessageRole_MESSAGE_ROLE_USER, "First")
	createMessageWithParts(t, repo, chatID, threadID, contextWindowID, 1, reliantv1.MessageRole_MESSAGE_ROLE_USER, "Second")
	createMessageWithParts(t, repo, chatID, threadID, contextWindowID, 2, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, "Third")

	messages, err := LoadMessagesForLLM(ctx, repo, chatID, threadID, nil)
	require.NoError(t, err)
	require.Len(t, messages, 3)

	// Ordered by seq, the chat-global total order — which here is insertion
	// order, in spite of the ordinals saying otherwise.
	got := make([]string, len(messages))
	for i, msg := range messages {
		got[i] = msg.Content().Text
	}
	assert.Equal(t, []string{"First", "Second", "Third"}, got,
		"Messages should be ordered by seq (insertion order), not by ordinal")
}
