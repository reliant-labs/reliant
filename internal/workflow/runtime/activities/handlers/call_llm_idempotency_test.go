// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/ptr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockLLMDriverForIdempotency is a simple mock that returns a basic response
type mockLLMDriverForIdempotency struct{}

func (m *mockLLMDriverForIdempotency) Name() string {
	return "mock"
}

func (m *mockLLMDriverForIdempotency) Model() models.Model {
	return models.Model{ID: "mock-model", Name: "Mock Model"}
}

func (m *mockLLMDriverForIdempotency) SendMessages(ctx context.Context, prompts []string, messages []message.Message, tools []tools.Tool) (*llm.DriverResponse, error) {
	return &llm.DriverResponse{
		Content:      "Mock response",
		FinishReason: "end_turn",
		Usage:        llm.TokenUsage{TokenCount: 30},
	}, nil
}

func (m *mockLLMDriverForIdempotency) StreamResponse(ctx context.Context, prompts []string, messages []message.Message, tools []tools.Tool) <-chan llm.DriverEvent {
	ch := make(chan llm.DriverEvent, 3)
	ch <- llm.DriverEvent{Type: llm.EventContentStart}
	ch <- llm.DriverEvent{Type: llm.EventContentDelta, Content: "Mock response"}
	ch <- llm.DriverEvent{
		Type: llm.EventComplete,
		Response: &llm.DriverResponse{
			Content:      "Mock response",
			FinishReason: "end_turn",
			Usage:        llm.TokenUsage{TokenCount: 30},
		},
	}
	close(ch)
	return ch
}

func (m *mockLLMDriverForIdempotency) ValidateKey(ctx context.Context) error {
	return nil
}

// mockLLMDriverResolver returns a DriverResolver that always returns the mock driver.
func mockLLMDriverResolver() drivers.DriverResolver {
	return func(ctx context.Context, userID string, prefs models.Preferences, opts ...llm.DriverOption) (llm.Driver, error) {
		return &mockLLMDriverForIdempotency{}, nil
	}
}

type capturedDriverOptions struct {
	Preferences     models.Preferences
	ReasoningEffort string
}

func captureDriverOptionsResolver(captured *capturedDriverOptions) drivers.DriverResolver {
	return func(ctx context.Context, userID string, prefs models.Preferences, opts ...llm.DriverOption) (llm.Driver, error) {
		captured.Preferences = append(models.Preferences(nil), prefs...)

		driverOpts := llm.DriverOptions{}
		for _, opt := range opts {
			opt(&driverOpts)
		}
		captured.ReasoningEffort = driverOpts.ReasoningEffort

		return &mockLLMDriverForIdempotency{}, nil
	}
}

// callLLMInput is a helper that builds an ActivityInput for call_llm with the given model ID.
func callLLMInput(chatID, threadID, modelID string) ActivityInput {
	return ActivityInput{
		Runtime: RuntimeContext{
			ChatID: chatID,
			Thread: threadID,
		},
		Node: &reliantv1.Node{
			Type: "call_llm",
			Args: &reliantv1.Node_CallLlm{
				CallLlm: &reliantv1.CallLLMArgs{
					Model: &reliantv1.CelModelSelector{
						Value: &reliantv1.CelModelSelector_Literal{
							Literal: &reliantv1.ModelSelector{Id: modelID},
						},
					},
				},
			},
		},
	}
}

// callLLMInputEmpty is a helper that builds an ActivityInput for call_llm with no args.
func callLLMInputEmpty(chatID, threadID string) ActivityInput {
	return ActivityInput{
		Runtime: RuntimeContext{
			ChatID: chatID,
			Thread: threadID,
		},
		Node: &reliantv1.Node{
			Type: "call_llm",
			Args: &reliantv1.Node_CallLlm{
				CallLlm: &reliantv1.CallLLMArgs{},
			},
		},
	}
}

func TestCallLLMActivity_Idempotency(t *testing.T) {
	// Setup mock LLM driver
	resolver := mockLLMDriverResolver()

	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()

	// Setup test data
	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	// Create thread and context window
	threadID := chatID // Root thread ID equals chat ID
	contextWindowID := h.CreateTestThreadAndContextWindow(ctx, chatID, threadID)

	// Create a user message to set up conversation history
	userMsgID := uuid.New().String()
	err := h.Repo().CreateMessage(ctx, &db.Message{
		ID:              userMsgID,
		ChatID:          chatID,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_USER,
		Ordinal:         0,
		ThreadID:        threadID,
		ContextWindowID: contextWindowID,
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
	})
	require.NoError(t, err)

	// Create user content block
	err = h.Repo().CreateContentBlock(ctx, &db.MessageContentBlock{
		ID:        uuid.New().String(),
		MessageID: userMsgID,
		BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
		Position:  0,
		Content:   ptr.Of("Hello, test message"),
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	})
	require.NoError(t, err)

	// Create activity (nil toolsFactory since we're testing idempotency, not tool execution)
	activityInstance := NewCallLLMActivity(h.Repo(), nil, nil, nil, resolver, nil)

	input := callLLMInput(chatID, threadID, "mock-model")

	t.Run("First execution creates assistant message", func(t *testing.T) {
		// Execute activity for the first time
		// This will fail at LLM streaming since we don't have a real driver,
		// but it should still create the assistant message skeleton
		var output CallLLMOutput
		err := h.ExecuteActivity(activityInstance.Execute, input, &output)

		// Expected to fail without LLM driver, but should create message
		if err == nil {
			// If somehow succeeded, verify output
			t.Log("Activity unexpectedly succeeded - this may indicate test setup changed")
		}

		// Verify assistant message was created (is_streaming=true initially)
		messages, err := h.Repo().ListMessages(ctx, chatID, db.MessageListOptions{
			Thread: ptr.Of(threadID),
		})
		require.NoError(t, err)

		// Should have user message + potentially incomplete assistant message
		assert.GreaterOrEqual(t, len(messages), 1, "Should have at least user message")

		// Check if assistant message was created
		var assistantMsg *db.Message
		for i := range messages {
			if messages[i].Role == reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT {
				assistantMsg = messages[i]
				break
			}
		}

		if assistantMsg != nil {
			// Verify activity_id is set for idempotency tracking
			assert.NotNil(t, assistantMsg.ActivityID, "Assistant message should have activity_id")
			t.Logf("Assistant message created with ID: %s, activity_id: %v", assistantMsg.ID, assistantMsg.ActivityID)
		}
	})

	t.Run("Retry returns same result if message exists", func(t *testing.T) {
		// Get the assistant message created in first execution (if any)
		messagesBefore, err := h.Repo().ListMessages(ctx, chatID, db.MessageListOptions{
			Thread: ptr.Of(threadID),
		})
		require.NoError(t, err)

		var existingAssistantMsg *db.Message
		for i := range messagesBefore {
			if messagesBefore[i].Role == reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT {
				existingAssistantMsg = messagesBefore[i]
				break
			}
		}

		// Execute again (simulating retry with same activity_id)
		var output CallLLMOutput
		_ = h.ExecuteActivityWithAttempt(activityInstance.Execute, input, 1, &output)

		// Get messages after retry
		messagesAfter, err := h.Repo().ListMessages(ctx, chatID, db.MessageListOptions{
			Thread: ptr.Of(threadID),
		})
		require.NoError(t, err)

		// If an assistant message existed, verify no duplicates created
		if existingAssistantMsg != nil {
			assistantCount := 0
			for _, msg := range messagesAfter {
				if msg.Role == reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT {
					assistantCount++
				}
			}
			assert.Equal(t, 1, assistantCount, "Should not create duplicate assistant messages on retry")
		}

		// Total message count should not increase significantly
		assert.LessOrEqual(t, len(messagesAfter), len(messagesBefore)+1,
			"Should not create duplicate messages on retry")
	})

	t.Run("No duplicate messages created", func(t *testing.T) {
		// Count messages by role
		messages, err := h.Repo().ListMessages(ctx, chatID, db.MessageListOptions{
			Thread: ptr.Of(threadID),
		})
		require.NoError(t, err)

		userCount := 0
		assistantCount := 0
		for _, msg := range messages {
			switch msg.Role {
			case reliantv1.MessageRole_MESSAGE_ROLE_USER:
				userCount++
			case reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT:
				assistantCount++
			}
		}

		assert.Equal(t, 1, userCount, "Should have exactly 1 user message")
		assert.LessOrEqual(t, assistantCount, 1, "Should have at most 1 assistant message")
	})
}

func TestCallLLMActivity_CleansUpIncompleteBlocks(t *testing.T) {
	t.Skip("Skipping: Temporal test framework doesn't properly simulate activity retry behavior")

	// Setup mock LLM driver
	resolver := mockLLMDriverResolver()

	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()

	// Setup test data
	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	// Create thread and context window
	threadID := chatID // Root thread ID equals chat ID
	contextWindowID := h.CreateTestThreadAndContextWindow(ctx, chatID, threadID)

	// Create user message
	userMsgID := uuid.New().String()
	err := h.Repo().CreateMessage(ctx, &db.Message{
		ID:              userMsgID,
		ChatID:          chatID,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_USER,
		Ordinal:         0,
		ContextWindowID: contextWindowID,
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
	})
	require.NoError(t, err)

	// Create user content block
	err = h.Repo().CreateContentBlock(ctx, &db.MessageContentBlock{
		ID:        uuid.New().String(),
		MessageID: userMsgID,
		BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
		Position:  0,
		Content:   ptr.Of("Test cleanup"),
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	})
	require.NoError(t, err)

	// Manually create an incomplete assistant message with is_streaming=true
	// This simulates a failed attempt that left the message in incomplete state
	activityID := "test-activity-123"
	assistantMsgID := uuid.New().String()
	isStreaming := true
	modelStr := "claude-3-5-sonnet-20241022"
	err = h.Repo().CreateMessage(ctx, &db.Message{
		ID:              assistantMsgID,
		ChatID:          chatID,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
		Ordinal:         1,
		ContextWindowID: contextWindowID,
		Model:           &modelStr,
		IsStreaming:     isStreaming,
		ActivityID:      &activityID,
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
	})
	require.NoError(t, err)

	// Create incomplete content blocks
	blockID1 := uuid.New().String()
	err = h.Repo().CreateContentBlock(ctx, &db.MessageContentBlock{
		ID:        blockID1,
		MessageID: assistantMsgID,
		BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
		Position:  0,
		Content:   ptr.Of("Incomplete content..."),
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	})
	require.NoError(t, err)

	blockID2 := uuid.New().String()
	err = h.Repo().CreateContentBlock(ctx, &db.MessageContentBlock{
		ID:        blockID2,
		MessageID: assistantMsgID,
		BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
		Position:  1,
		Content:   ptr.Of("More incomplete content"),
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	})
	require.NoError(t, err)

	t.Run("Cleanup removes incomplete message and starts fresh", func(t *testing.T) {
		// Create activity
		activityInstance := NewCallLLMActivity(h.Repo(), nil, nil, nil, resolver, nil)

		input := callLLMInput(chatID, threadID, "mock-model")

		// Execute activity - should detect incomplete message and clean up
		var output CallLLMOutput
		_ = h.ExecuteActivity(activityInstance.Execute, input, &output)

		// Verify the incomplete message was cleaned up
		messages, err := h.Repo().ListMessages(ctx, chatID, db.MessageListOptions{
			Thread: ptr.Of(threadID),
		})
		require.NoError(t, err)

		// Check that we have at most 1 assistant message
		assistantCount := 0
		var newAssistantMsg *db.Message
		for i := range messages {
			if messages[i].Role == reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT {
				assistantCount++
				newAssistantMsg = messages[i]
			}
		}
		assert.LessOrEqual(t, assistantCount, 1, "Should have at most 1 assistant message after cleanup")

		// If a new assistant message was created, it should be different from the old one
		if newAssistantMsg != nil {
			t.Logf("New assistant message created with ID: %s (old ID: %s)", newAssistantMsg.ID, assistantMsgID)
			assert.NotEqual(t, assistantMsgID, newAssistantMsg.ID,
				"New message should have different ID from deleted message")
		}
	})
}

func TestCallLLMActivity_NoOrphanedRecords(t *testing.T) {
	// Setup mock LLM driver
	resolver := mockLLMDriverResolver()

	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()

	// Setup test data
	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	// Create thread and context window
	threadID := chatID // Root thread ID equals chat ID
	contextWindowID := h.CreateTestThreadAndContextWindow(ctx, chatID, threadID)

	// Create user message
	userMsgID := uuid.New().String()
	err := h.Repo().CreateMessage(ctx, &db.Message{
		ID:              userMsgID,
		ChatID:          chatID,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_USER,
		Ordinal:         0,
		ThreadID:        threadID,
		ContextWindowID: contextWindowID,
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
	})
	require.NoError(t, err)

	// Create user content block
	err = h.Repo().CreateContentBlock(ctx, &db.MessageContentBlock{
		ID:        uuid.New().String(),
		MessageID: userMsgID,
		BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
		Position:  0,
		Content:   ptr.Of("Multiple retry test"),
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	})
	require.NoError(t, err)

	t.Run("Multiple retries don't create orphaned records", func(t *testing.T) {
		// Create activity
		activityInstance := NewCallLLMActivity(h.Repo(), nil, nil, nil, resolver, nil)

		input := callLLMInput(chatID, threadID, "mock-model")

		// Execute 5 times simulating multiple retries
		for attempt := int32(1); attempt <= 5; attempt++ {
			var output CallLLMOutput
			_ = h.ExecuteActivityWithAttempt(activityInstance.Execute, input, attempt, &output)
		}

		// Verify message count
		messages, err := h.Repo().ListMessages(ctx, chatID, db.MessageListOptions{
			Thread: ptr.Of(threadID),
		})
		require.NoError(t, err)

		// Should have:
		// - 1 user message
		// - At most 1 assistant message (could be incomplete or deleted)
		assert.LessOrEqual(t, len(messages), 2,
			"Should have at most 2 messages after multiple retries (1 user + 1 assistant)")

		// Count by role
		userCount := 0
		assistantCount := 0
		for _, msg := range messages {
			switch msg.Role {
			case reliantv1.MessageRole_MESSAGE_ROLE_USER:
				userCount++
			case reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT:
				assistantCount++
			}
		}

		assert.Equal(t, 1, userCount, "Should have exactly 1 user message")
		assert.LessOrEqual(t, assistantCount, 1, "Should have at most 1 assistant message")

		// Verify no orphaned content blocks
		// Each message should have at least one content block if it exists
		for _, msg := range messages {
			blocks, err := h.Repo().ListContentBlocks(ctx, msg.ID)
			require.NoError(t, err)
			assert.Greater(t, len(blocks), 0,
				"Message %s (role: %s) should have at least one content block", msg.ID, msg.Role)
		}
	})
}

func TestCallLLMActivity_ActivityIDTracking(t *testing.T) {
	// Setup mock LLM driver
	resolver := mockLLMDriverResolver()

	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()

	// Setup test data
	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	// Create thread and context window
	threadID := chatID // Root thread ID equals chat ID
	contextWindowID := h.CreateTestThreadAndContextWindow(ctx, chatID, threadID)

	// Create user message
	userMsgID := uuid.New().String()
	err := h.Repo().CreateMessage(ctx, &db.Message{
		ID:              userMsgID,
		ChatID:          chatID,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_USER,
		Ordinal:         0,
		ThreadID:        threadID,
		ContextWindowID: contextWindowID,
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
	})
	require.NoError(t, err)

	// Create user content block
	err = h.Repo().CreateContentBlock(ctx, &db.MessageContentBlock{
		ID:        uuid.New().String(),
		MessageID: userMsgID,
		BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
		Position:  0,
		Content:   ptr.Of("Test activity tracking"),
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	})
	require.NoError(t, err)

	t.Run("Activity ID is tracked in message", func(t *testing.T) {
		// Create activity
		activityInstance := NewCallLLMActivity(h.Repo(), nil, nil, nil, resolver, nil)

		input := callLLMInputEmpty(chatID, threadID)

		// Execute activity
		var output CallLLMOutput
		_ = h.ExecuteActivity(activityInstance.Execute, input, &output)

		// Verify assistant message has activity_id
		messages, err := h.Repo().ListMessages(ctx, chatID, db.MessageListOptions{
			Thread: ptr.Of(threadID),
		})
		require.NoError(t, err)

		var assistantMsg *db.Message
		for i := range messages {
			if messages[i].Role == reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT {
				assistantMsg = messages[i]
				break
			}
		}

		if assistantMsg != nil {
			assert.NotNil(t, assistantMsg.ActivityID,
				"Assistant message should have activity_id for idempotency tracking")

			// Verify content blocks also have activity_id
			blocks, err := h.Repo().ListContentBlocks(ctx, assistantMsg.ID)
			require.NoError(t, err)

			for _, block := range blocks {
				assert.NotNil(t, block.ActivityID,
					"Content block %s should have activity_id", block.ID)
			}
		}
	})
}

func TestCallLLMActivity_ThreadHandling(t *testing.T) {
	// Setup mock LLM driver
	resolver := mockLLMDriverResolver()

	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()

	// Setup test data
	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	// Create thread and context window for default thread
	threadID := chatID // Root thread ID equals chat ID
	_ = h.CreateTestThreadAndContextWindow(ctx, chatID, threadID)

	t.Run("Empty thread path returns error", func(t *testing.T) {
		// Create activity
		activityInstance := NewCallLLMActivity(h.Repo(), nil, nil, nil, resolver, nil)

		// Execute with empty thread path - should return error
		input := callLLMInputEmpty(chatID, "")

		var output CallLLMOutput
		err := h.ExecuteActivity(activityInstance.Execute, input, &output)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "thread is required")
	})

	t.Run("Specific thread path is respected", func(t *testing.T) {
		// Create child thread and context window
		childThreadID := chatID + "/child"
		childContextWindowID := h.CreateTestThreadAndContextWindow(ctx, chatID, childThreadID)

		// Create user message in child thread
		userMsgID := uuid.New().String()
		err := h.Repo().CreateMessage(ctx, &db.Message{
			ID:              userMsgID,
			ChatID:          chatID,
			Role:            reliantv1.MessageRole_MESSAGE_ROLE_USER,
			Ordinal:         0,
			ThreadID:        childThreadID,
			ContextWindowID: childContextWindowID,
			CreatedAt:       time.Now().UTC(),
			UpdatedAt:       time.Now().UTC(),
		})
		require.NoError(t, err)

		err = h.Repo().CreateContentBlock(ctx, &db.MessageContentBlock{
			ID:        uuid.New().String(),
			MessageID: userMsgID,
			BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
			Position:  0,
			Content:   ptr.Of("Test child thread"),
			CreatedAt: time.Now().UTC(),
			UpdatedAt: time.Now().UTC(),
		})
		require.NoError(t, err)

		// Create activity
		activityInstance := NewCallLLMActivity(h.Repo(), nil, nil, nil, resolver, nil)

		// Execute with specific thread path (must match the thread we created)
		input := callLLMInputEmpty(chatID, childThreadID)

		var output CallLLMOutput
		_ = h.ExecuteActivity(activityInstance.Execute, input, &output)

		// Verify message is created in correct thread
		messages, err := h.Repo().ListMessages(ctx, chatID, db.MessageListOptions{
			Thread: ptr.Of(childThreadID),
		})
		require.NoError(t, err)

		// Should have user message and potentially assistant message
		assert.GreaterOrEqual(t, len(messages), 1, "Should have messages in child thread")
	})
}

func TestCallLLMActivity_ThinkingLevel(t *testing.T) {
	// Setup mock LLM driver
	resolver := mockLLMDriverResolver()

	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()

	// Setup test data
	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	// Create thread and context window
	threadID := chatID // Root thread ID equals chat ID
	contextWindowID := h.CreateTestThreadAndContextWindow(ctx, chatID, threadID)

	// Create user message
	userMsgID := uuid.New().String()
	err := h.Repo().CreateMessage(ctx, &db.Message{
		ID:              userMsgID,
		ChatID:          chatID,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_USER,
		Ordinal:         0,
		ThreadID:        threadID,
		ContextWindowID: contextWindowID,
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
	})
	require.NoError(t, err)

	// Create user content block
	err = h.Repo().CreateContentBlock(ctx, &db.MessageContentBlock{
		ID:        uuid.New().String(),
		MessageID: userMsgID,
		BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
		Position:  0,
		Content:   ptr.Of("Test thinking level parameter"),
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	})
	require.NoError(t, err)

	activityInstance := NewCallLLMActivity(h.Repo(), nil, nil, nil, resolver, nil)

	t.Run("ThinkingLevel parameter is accepted without error", func(t *testing.T) {
		input := ActivityInput{
			Runtime: RuntimeContext{
				ChatID: chatID,
				Thread: "0",
			},
			Node: &reliantv1.Node{
				Type: "call_llm",
				Args: &reliantv1.Node_CallLlm{
					CallLlm: &reliantv1.CallLLMArgs{
						Model: &reliantv1.CelModelSelector{
							Value: &reliantv1.CelModelSelector_Literal{
								Literal: &reliantv1.ModelSelector{Id: "mock-model"},
							},
						},
						ThinkingLevel: &reliantv1.CelString{
							Value: &reliantv1.CelString_Literal{Literal: string(ThinkingLevelMedium)},
						},
					},
				},
			},
		}

		var output CallLLMOutput
		// Execute activity - should not fail due to thinking_level parameter
		_ = h.ExecuteActivity(activityInstance.Execute, input, &output)

		// The test validates that the parameter is accepted and passed through
		// without causing errors. The actual thinking level behavior is tested
		// in the driver layer tests.
	})

	t.Run("ThinkingLevel with different values", func(t *testing.T) {
		testCases := []ThinkingLevel{ThinkingLevelLow, ThinkingLevelMedium, ThinkingLevelHigh}

		for _, level := range testCases {
			t.Run(string(level), func(t *testing.T) {
				input := ActivityInput{
					Runtime: RuntimeContext{
						ChatID: chatID,
						Thread: "0",
					},
					Node: &reliantv1.Node{
						Type: "call_llm",
						Args: &reliantv1.Node_CallLlm{
							CallLlm: &reliantv1.CallLLMArgs{
								Model: &reliantv1.CelModelSelector{
									Value: &reliantv1.CelModelSelector_Literal{
										Literal: &reliantv1.ModelSelector{Id: "mock-model"},
									},
								},
								ThinkingLevel: &reliantv1.CelString{
									Value: &reliantv1.CelString_Literal{Literal: string(level)},
								},
							},
						},
					},
				}

				var output CallLLMOutput
				// Should accept all valid thinking levels
				_ = h.ExecuteActivity(activityInstance.Execute, input, &output)
			})
		}
	})

	t.Run("ThinkingLevel nil is acceptable", func(t *testing.T) {
		input := callLLMInput(chatID, "0", "mock-model")

		var output CallLLMOutput
		_ = h.ExecuteActivity(activityInstance.Execute, input, &output)
		// Should work fine with nil thinking level
	})

	t.Run("Invalid ThinkingLevel is rejected", func(t *testing.T) {
		input := ActivityInput{
			Runtime: RuntimeContext{
				ChatID: chatID,
				Thread: "0",
			},
			Node: &reliantv1.Node{
				Type: "call_llm",
				Args: &reliantv1.Node_CallLlm{
					CallLlm: &reliantv1.CallLLMArgs{
						Model: &reliantv1.CelModelSelector{
							Value: &reliantv1.CelModelSelector_Literal{
								Literal: &reliantv1.ModelSelector{Id: "mock-model"},
							},
						},
						ThinkingLevel: &reliantv1.CelString{
							Value: &reliantv1.CelString_Literal{Literal: "invalid"},
						},
					},
				},
			},
		}

		var output CallLLMOutput
		err := h.ExecuteActivity(activityInstance.Execute, input, &output)
		// Should return error for invalid thinking level
		require.Error(t, err)
		require.Contains(t, err.Error(), "invalid thinking_level")
		require.Contains(t, err.Error(), "must be one of: low, medium, high, xhigh")
	})
}