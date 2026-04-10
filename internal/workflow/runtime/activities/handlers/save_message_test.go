// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// COMPREHENSIVE UNIT TESTS FOR SAVEMESSAGEACTIVITY
// ============================================================================
//
// These tests verify the core functionality of SaveMessageActivity, focusing
// on the new assistant message capabilities including tool calls, text content,
// and token tracking.
//
// Test Coverage:
// 1. Basic user message creation
// 2. Assistant messages with text only
// 3. Assistant messages with tool calls only
// 4. Assistant messages with both text and tool calls
// 5. Tool messages with results
// 6. Idempotency behavior
// 7. Tool calls passthrough for routing
// 8. Token tracking for assistant messages
// 9. Error cases and validation
//
// ============================================================================

func TestSaveUserMessage(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()

	// Setup test data
	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	// Create activity
	activity := NewSaveMessageActivity(h.Repo())

	input := SaveMessageInput{
		ChatID:  chatID,
		Thread:  "0",
		Role:    "user",
		Content: "Hello, this is a user message",
	}

	t.Run("Creates user message with text content block", func(t *testing.T) {
		var output SaveMessageOutput
		err := h.ExecuteActivity(activity.Execute, &input, &output)
		require.NoError(t, err, "Should successfully create user message")

		assert.NotEmpty(t, output.MessageId, "Should return message_id")
		assert.Empty(t, output.ToolCalls, "User messages should not have tool_calls in output")

		// Verify message was created correctly
		msg, err := h.Repo().GetMessage(ctx, output.MessageId)
		require.NoError(t, err)
		assert.Equal(t, chatID, msg.ChatID)
		assert.Equal(t, reliantv1.MessageRole_MESSAGE_ROLE_USER, msg.Role)
		// Thread is now accessed via context window
		assert.NotEmpty(t, msg.ContextWindowID, "Message should have context window ID")
		cw, err := h.Repo().GetContextWindow(ctx, msg.ContextWindowID)
		require.NoError(t, err)
		assert.Equal(t, "0", cw.ThreadID, "Message should be in the thread specified in input")
		assert.Equal(t, int64(0), msg.Ordinal, "First message should have ordinal 0")

		// Token field should be nil for user messages
		assert.Nil(t, msg.TokenCount, "User messages should not have token count")
	})

	t.Run("Creates single text content block", func(t *testing.T) {
		var output SaveMessageOutput
		err := h.ExecuteActivity(activity.Execute, &input, &output)
		require.NoError(t, err)

		blocks, err := h.Repo().ListContentBlocks(ctx, output.MessageId)
		require.NoError(t, err)
		require.Len(t, blocks, 1, "Should have exactly 1 content block")

		block := blocks[0]
		assert.Equal(t, reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT, block.BlockType)
		assert.Equal(t, 0, block.Position)
		require.NotNil(t, block.Content)
		assert.Equal(t, "Hello, this is a user message", *block.Content)
		assert.NotNil(t, block.Version)
		assert.Equal(t, 1, *block.Version)
	})
}

func TestSaveAssistantMessageWithText(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()

	// Setup test data
	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	// Create activity
	activity := NewSaveMessageActivity(h.Repo())

	input := SaveMessageInput{
		ChatID:  chatID,
		Thread:  "0",
		Role:    "assistant",
		Content: "This is an assistant response with only text.",
	}

	t.Run("Creates assistant message with text content", func(t *testing.T) {
		var output SaveMessageOutput
		err := h.ExecuteActivity(activity.Execute, &input, &output)
		require.NoError(t, err, "Should successfully create assistant message")

		assert.NotEmpty(t, output.MessageId, "Should return message_id")
		assert.Empty(t, output.ToolCalls, "No tool calls should be returned when none provided")

		// Verify message was created correctly
		msg, err := h.Repo().GetMessage(ctx, output.MessageId)
		require.NoError(t, err)
		assert.Equal(t, chatID, msg.ChatID)
		assert.Equal(t, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, msg.Role)
		// Thread is accessed via context window
		assert.NotEmpty(t, msg.ContextWindowID, "Message should have context window ID")
	})

	t.Run("Creates single text content block at position 0", func(t *testing.T) {
		var output SaveMessageOutput
		err := h.ExecuteActivity(activity.Execute, &input, &output)
		require.NoError(t, err)

		blocks, err := h.Repo().ListContentBlocks(ctx, output.MessageId)
		require.NoError(t, err)
		require.Len(t, blocks, 1, "Should have exactly 1 text block")

		block := blocks[0]
		assert.Equal(t, reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT, block.BlockType)
		assert.Equal(t, 0, block.Position, "Text block should be at position 0")
		require.NotNil(t, block.Content)
		assert.Equal(t, "This is an assistant response with only text.", *block.Content)
		assert.NotNil(t, block.Version)
		assert.Equal(t, 1, *block.Version)
	})
}

func TestSaveAssistantMessageWithToolCalls(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()

	// Setup test data
	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	// Create activity
	activity := NewSaveMessageActivity(h.Repo())

	input := SaveMessageInput{
		ChatID: chatID,
		Thread: "0",
		Role:   "assistant",
		ToolCalls: []ToolCall{
			{
				ID:    "call_abc123",
				Name:  "ReadFile",
				Input: `{"file_path": "/path/to/file.txt"}`,
			},
			{
				ID:    "call_def456",
				Name:  "WriteFile",
				Input: `{"file_path": "/path/to/output.txt", "content": "Hello"}`,
			},
		},
	}

	t.Run("Creates assistant message with tool calls only", func(t *testing.T) {
		var output SaveMessageOutput
		err := h.ExecuteActivity(activity.Execute, &input, &output)
		require.NoError(t, err, "Should successfully create assistant message with tool calls")

		assert.NotEmpty(t, output.MessageId, "Should return message_id")
		require.Len(t, output.ToolCalls, 2, "Should passthrough tool calls for routing")

		// Verify message was created correctly
		msg, err := h.Repo().GetMessage(ctx, output.MessageId)
		require.NoError(t, err)
		assert.Equal(t, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, msg.Role)
	})

	t.Run("Creates tool_call content blocks at correct positions", func(t *testing.T) {
		var output SaveMessageOutput
		err := h.ExecuteActivity(activity.Execute, &input, &output)
		require.NoError(t, err)

		blocks, err := h.Repo().ListContentBlocks(ctx, output.MessageId)
		require.NoError(t, err)
		require.Len(t, blocks, 2, "Should have 2 tool_call blocks")

		// First tool call block
		block0 := blocks[0]
		assert.Equal(t, reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL, block0.BlockType)
		assert.Equal(t, 0, block0.Position, "First tool_call should be at position 0")
		require.NotNil(t, block0.ToolName)
		assert.Equal(t, "ReadFile", *block0.ToolName)
		require.NotNil(t, block0.ToolCallID)
		assert.Equal(t, "call_abc123", *block0.ToolCallID)
		require.NotNil(t, block0.ToolInput)
		assert.Equal(t, `{"file_path": "/path/to/file.txt"}`, *block0.ToolInput)
		assert.NotNil(t, block0.Version)
		assert.Equal(t, 1, *block0.Version)

		// Second tool call block
		block1 := blocks[1]
		assert.Equal(t, reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL, block1.BlockType)
		assert.Equal(t, 1, block1.Position, "Second tool_call should be at position 1")
		require.NotNil(t, block1.ToolName)
		assert.Equal(t, "WriteFile", *block1.ToolName)
		require.NotNil(t, block1.ToolCallID)
		assert.Equal(t, "call_def456", *block1.ToolCallID)
		require.NotNil(t, block1.ToolInput)
		assert.Equal(t, `{"file_path": "/path/to/output.txt", "content": "Hello"}`, *block1.ToolInput)
	})
}

func TestSaveAssistantMessageWithTextAndToolCalls(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()

	// Setup test data
	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	// Create activity
	activity := NewSaveMessageActivity(h.Repo())

	input := SaveMessageInput{
		ChatID:  chatID,
		Thread:  "0",
		Role:    "assistant",
		Content: "I'll help you with that. Let me read the file first.",
		ToolCalls: []ToolCall{
			{
				ID:    "call_xyz789",
				Name:  "ReadFile",
				Input: `{"file_path": "/config/settings.json"}`,
			},
		},
	}

	t.Run("Creates assistant message with both text and tool calls", func(t *testing.T) {
		var output SaveMessageOutput
		err := h.ExecuteActivity(activity.Execute, &input, &output)
		require.NoError(t, err, "Should successfully create assistant message with text and tool calls")

		assert.NotEmpty(t, output.MessageId, "Should return message_id")
		require.Len(t, output.ToolCalls, 1, "Should passthrough tool calls for routing")
		assert.Equal(t, "call_xyz789", output.ToolCalls[0].GetId())
		assert.Equal(t, "ReadFile", output.ToolCalls[0].Name)

		// Verify message was created correctly
		msg, err := h.Repo().GetMessage(ctx, output.MessageId)
		require.NoError(t, err)
		assert.Equal(t, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, msg.Role)
	})

	t.Run("Creates text block at position 0, tool_call at position 1", func(t *testing.T) {
		var output SaveMessageOutput
		err := h.ExecuteActivity(activity.Execute, &input, &output)
		require.NoError(t, err)

		blocks, err := h.Repo().ListContentBlocks(ctx, output.MessageId)
		require.NoError(t, err)
		require.Len(t, blocks, 2, "Should have 2 content blocks (1 text + 1 tool_call)")

		// Text block should be first (position 0)
		textBlock := blocks[0]
		assert.Equal(t, reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT, textBlock.BlockType)
		assert.Equal(t, 0, textBlock.Position, "Text block should be at position 0")
		require.NotNil(t, textBlock.Content)
		assert.Equal(t, "I'll help you with that. Let me read the file first.", *textBlock.Content)

		// Tool call block should be second (position 1)
		toolBlock := blocks[1]
		assert.Equal(t, reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL, toolBlock.BlockType)
		assert.Equal(t, 1, toolBlock.Position, "Tool call block should be at position 1")
		require.NotNil(t, toolBlock.ToolName)
		assert.Equal(t, "ReadFile", *toolBlock.ToolName)
		require.NotNil(t, toolBlock.ToolCallID)
		assert.Equal(t, "call_xyz789", *toolBlock.ToolCallID)
	})
}

func TestSaveToolMessage(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()

	// Setup test data
	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	// Create activity
	activity := NewSaveMessageActivity(h.Repo())

	input := SaveMessageInput{
		ChatID: chatID,
		Thread: "0",
		Role:   "tool",
		ToolResults: []ToolResult{
			{
				ToolCallID: "call_abc123",
				Content:    "File contents: Hello World",
				IsError:    false,
			},
			{
				ToolCallID: "call_def456",
				Content:    "Error: File not found",
				IsError:    true,
			},
		},
	}

	t.Run("Creates tool message with multiple results", func(t *testing.T) {
		var output SaveMessageOutput
		err := h.ExecuteActivity(activity.Execute, &input, &output)
		require.NoError(t, err, "Should successfully create tool message")

		assert.NotEmpty(t, output.MessageId, "Should return message_id")
		assert.Empty(t, output.ToolCalls, "Tool messages should not return tool_calls")

		// Verify message was created correctly
		msg, err := h.Repo().GetMessage(ctx, output.MessageId)
		require.NoError(t, err)
		assert.Equal(t, reliantv1.MessageRole_MESSAGE_ROLE_TOOL, msg.Role)
	})

	t.Run("Creates tool_result blocks with correct data", func(t *testing.T) {
		var output SaveMessageOutput
		err := h.ExecuteActivity(activity.Execute, &input, &output)
		require.NoError(t, err)

		blocks, err := h.Repo().ListContentBlocks(ctx, output.MessageId)
		require.NoError(t, err)
		require.Len(t, blocks, 2, "Should have 2 tool_result blocks")

		// First result (success)
		block0 := blocks[0]
		assert.Equal(t, reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT, block0.BlockType)
		assert.Equal(t, 0, block0.Position)
		require.NotNil(t, block0.ToolCallID)
		assert.Equal(t, "call_abc123", *block0.ToolCallID)
		require.NotNil(t, block0.Content)
		assert.Equal(t, "File contents: Hello World", *block0.Content)
		require.NotNil(t, block0.IsError)
		assert.False(t, *block0.IsError, "First result should not be an error")

		// Second result (error)
		block1 := blocks[1]
		assert.Equal(t, reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT, block1.BlockType)
		assert.Equal(t, 1, block1.Position)
		require.NotNil(t, block1.ToolCallID)
		assert.Equal(t, "call_def456", *block1.ToolCallID)
		require.NotNil(t, block1.Content)
		assert.Equal(t, "Error: File not found", *block1.Content)
		require.NotNil(t, block1.IsError)
		assert.True(t, *block1.IsError, "Second result should be marked as error")
	})
}

func TestSaveMessageIdempotency(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()

	// Setup test data
	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	// Create activity
	activity := NewSaveMessageActivity(h.Repo())

	input := SaveMessageInput{
		ChatID:  chatID,
		Thread:  "0",
		Role:    "assistant",
		Content: "Test idempotency",
		ToolCalls: []ToolCall{
			{
				ID:    "call_test123",
				Name:  "TestTool",
				Input: `{"param": "value"}`,
			},
		},
	}

	var firstOutput SaveMessageOutput

	t.Run("First execution creates message", func(t *testing.T) {
		err := h.ExecuteActivity(activity.Execute, &input, &firstOutput)
		require.NoError(t, err)
		assert.NotEmpty(t, firstOutput.MessageId)
		require.Len(t, firstOutput.ToolCalls, 1)

		// Verify message and blocks were created
		msg, err := h.Repo().GetMessage(ctx, firstOutput.MessageId)
		require.NoError(t, err)
		assert.Equal(t, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, msg.Role)

		blocks, err := h.Repo().ListContentBlocks(ctx, firstOutput.MessageId)
		require.NoError(t, err)
		assert.Equal(t, 2, len(blocks), "Should have 2 blocks (1 text + 1 tool_call)")
	})

	// Note: The Temporal test framework increments activity_id on each call,
	// so true idempotency testing (same activity_id returns same message) is not
	// possible in the test environment. This is documented in save_message_idempotency_test.go.
	// The idempotency logic is still present and tested in production-like scenarios
	// in the separate idempotency test file.

	t.Run("Tool calls are passed through in output", func(t *testing.T) {
		// Execute again to verify tool calls passthrough works consistently
		var output SaveMessageOutput
		err := h.ExecuteActivity(activity.Execute, &input, &output)
		require.NoError(t, err)

		// Tool calls should be passed through
		require.Len(t, output.ToolCalls, 1)
		assert.Equal(t, "call_test123", output.ToolCalls[0].GetId())
		assert.Equal(t, "TestTool", output.ToolCalls[0].Name)
		assert.Equal(t, `{"param": "value"}`, output.ToolCalls[0].Input)
	})
}

func TestSaveMessagePassesThroughToolCalls(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()

	// Setup test data
	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	// Create activity
	activity := NewSaveMessageActivity(h.Repo())

	t.Run("Tool calls are passed through in output for workflow routing", func(t *testing.T) {
		input := SaveMessageInput{
			ChatID:  chatID,
			Thread:  "0",
			Role:    "assistant",
			Content: "Running multiple tools",
			ToolCalls: []ToolCall{
				{
					ID:    "call_1",
					Name:  "Tool1",
					Input: `{"arg1": "value1"}`,
				},
				{
					ID:    "call_2",
					Name:  "Tool2",
					Input: `{"arg2": "value2"}`,
				},
				{
					ID:    "call_3",
					Name:  "Tool3",
					Input: `{"arg3": "value3"}`,
				},
			},
		}

		var output SaveMessageOutput
		err := h.ExecuteActivity(activity.Execute, &input, &output)
		require.NoError(t, err)

		// Verify tool calls are passed through exactly as provided
		require.Len(t, output.ToolCalls, 3, "Should passthrough all 3 tool calls")

		assert.Equal(t, "call_1", output.ToolCalls[0].GetId())
		assert.Equal(t, "Tool1", output.ToolCalls[0].Name)
		assert.Equal(t, `{"arg1": "value1"}`, output.ToolCalls[0].Input)

		assert.Equal(t, "call_2", output.ToolCalls[1].GetId())
		assert.Equal(t, "Tool2", output.ToolCalls[1].Name)
		assert.Equal(t, `{"arg2": "value2"}`, output.ToolCalls[1].Input)

		assert.Equal(t, "call_3", output.ToolCalls[2].GetId())
		assert.Equal(t, "Tool3", output.ToolCalls[2].Name)
		assert.Equal(t, `{"arg3": "value3"}`, output.ToolCalls[2].Input)
	})

	t.Run("Empty tool calls array when no tool calls provided", func(t *testing.T) {
		input := SaveMessageInput{
			ChatID:  chatID,
			Thread:  "0",
			Role:    "assistant",
			Content: "No tools needed",
		}

		var output SaveMessageOutput
		err := h.ExecuteActivity(activity.Execute, &input, &output)
		require.NoError(t, err)

		assert.Empty(t, output.ToolCalls, "Should have empty tool_calls array when none provided")
	})
}

func TestSaveAssistantMessageWithTokens(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()

	// Setup test data
	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	// Create activity
	activity := NewSaveMessageActivity(h.Repo())

	t.Run("Stores token count for assistant message", func(t *testing.T) {
		input := SaveMessageInput{
			ChatID:     chatID,
			Thread:     "0",
			Role:       "assistant",
			Content:    "Response with token counts",
			TokenCount: 1750,
		}

		var output SaveMessageOutput
		err := h.ExecuteActivity(activity.Execute, &input, &output)
		require.NoError(t, err)

		// Verify token count was stored in the message
		msg, err := h.Repo().GetMessage(ctx, output.MessageId)
		require.NoError(t, err)

		require.NotNil(t, msg.TokenCount, "TokenCount should be set")
		assert.Equal(t, 1750, *msg.TokenCount)
	})

	t.Run("Handles zero token count", func(t *testing.T) {
		input := SaveMessageInput{
			ChatID:     chatID,
			Thread:     "0",
			Role:       "assistant",
			Content:    "Response with zero tokens",
			TokenCount: 0,
		}

		var output SaveMessageOutput
		err := h.ExecuteActivity(activity.Execute, &input, &output)
		require.NoError(t, err)

		// Zero value should not be stored (nil instead)
		msg, err := h.Repo().GetMessage(ctx, output.MessageId)
		require.NoError(t, err)

		assert.Nil(t, msg.TokenCount, "Zero TokenCount should not be stored")
	})

	t.Run("Stores token count for all message types", func(t *testing.T) {
		// Token count is now stored for all message types
		// The caller is responsible for only providing tokens when appropriate
		input := SaveMessageInput{
			ChatID:     chatID,
			Thread:     "0",
			Role:       "user",
			Content:    "User message with tokens",
			TokenCount: 1500,
		}

		var output SaveMessageOutput
		err := h.ExecuteActivity(activity.Execute, &input, &output)
		require.NoError(t, err)

		msg, err := h.Repo().GetMessage(ctx, output.MessageId)
		require.NoError(t, err)

		// Token count is stored for all roles (caller decides what to pass)
		assert.NotNil(t, msg.TokenCount, "TokenCount should be stored if provided")
		assert.Equal(t, 1500, *msg.TokenCount)
	})
}

func TestSaveMessageValidation(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()

	// Setup test data
	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	// Create activity
	activity := NewSaveMessageActivity(h.Repo())

	t.Run("Missing chat_id returns error", func(t *testing.T) {
		input := SaveMessageInput{
			ChatID:  "", // Missing
			Thread:  "0",
			Role:    "user",
			Content: "Test",
		}

		var output SaveMessageOutput
		err := h.ExecuteActivity(activity.Execute, &input, &output)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "chat_id is required")
	})

	t.Run("Missing thread returns error", func(t *testing.T) {
		input := SaveMessageInput{
			ChatID:  chatID,
			Thread:  "", // Missing
			Role:    "user",
			Content: "Test",
		}

		var output SaveMessageOutput
		err := h.ExecuteActivity(activity.Execute, &input, &output)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "thread is required")
	})
	t.Run("System role is allowed", func(t *testing.T) {
		input := SaveMessageInput{
			ChatID:  chatID,
			Thread:  "0",
			Role:    "system", // Valid - used for compact summaries
			Content: "Test",
		}

		var output SaveMessageOutput
		err := h.ExecuteActivity(activity.Execute, &input, &output)
		require.NoError(t, err)
		assert.NotEmpty(t, output.MessageId, "Should create a system message")
	})

	t.Run("Invalid role returns error", func(t *testing.T) {
		input := SaveMessageInput{
			ChatID:  chatID,
			Thread:  "0",
			Role:    "invalid_role", // Actually invalid
			Content: "Test",
		}

		var output SaveMessageOutput
		err := h.ExecuteActivity(activity.Execute, &input, &output)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "role cannot be unspecified")
	})

	t.Run("User message without content or attachments returns error", func(t *testing.T) {
		input := SaveMessageInput{
			ChatID:  chatID,
			Thread:  "0",
			Role:    "user",
			Content: "", // Missing
		}

		var output SaveMessageOutput
		err := h.ExecuteActivity(activity.Execute, &input, &output)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "content or attachments are required for user messages")
	})

	t.Run("Assistant message without content or tool_calls is allowed", func(t *testing.T) {
		input := SaveMessageInput{
			ChatID:    chatID,
			Thread:    "0",
			Role:      "assistant",
			Content:   "",           // Empty - allowed for custom workflow use cases
			ToolCalls: []ToolCall{}, // Empty - allowed for custom workflow use cases
		}

		var output SaveMessageOutput
		err := h.ExecuteActivity(activity.Execute, &input, &output)
		require.NoError(t, err)
		assert.NotEmpty(t, output.MessageId, "Should create a message even with no content")
	})

	t.Run("Tool message without tool_results returns error", func(t *testing.T) {
		input := SaveMessageInput{
			ChatID:      chatID,
			Thread:      "0",
			Role:        "tool",
			ToolResults: []ToolResult{}, // Empty
		}

		var output SaveMessageOutput
		err := h.ExecuteActivity(activity.Execute, &input, &output)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "tool_results is required for tool messages")
	})
}

func TestSaveMessageOrdinalSequencing(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()

	// Setup test data
	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	// Create thread "0.0" for the independent ordinals subtest
	parentThread := "0"
	_, err := h.Repo().CreateThread(ctx, &db.Thread{
		ID:             "0.0",
		ConversationID: chatID,
		ParentThreadID: &parentThread,
	})
	require.NoError(t, err)

	// Create context window for thread "0.0"
	_, err = h.Repo().CreateContextWindow(ctx, &db.ContextWindow{
		ID:       chatID + ":0.0:0",
		ThreadID: "0.0",
		Sequence: 0,
	})
	require.NoError(t, err)

	// Create activity
	activity := NewSaveMessageActivity(h.Repo())

	t.Run("Messages in same thread have sequential ordinals", func(t *testing.T) {
		// Create first message
		input1 := SaveMessageInput{
			ChatID:  chatID,
			Thread:  "0",
			Role:    "user",
			Content: "First message",
		}

		var output1 SaveMessageOutput
		err := h.ExecuteActivity(activity.Execute, &input1, &output1)
		require.NoError(t, err)

		msg1, err := h.Repo().GetMessage(ctx, output1.MessageId)
		require.NoError(t, err)
		assert.Equal(t, int64(0), msg1.Ordinal, "First message should have ordinal 0")

		// Create second message
		input2 := SaveMessageInput{
			ChatID:  chatID,
			Thread:  "0",
			Role:    "assistant",
			Content: "Second message",
		}

		var output2 SaveMessageOutput
		err = h.ExecuteActivity(activity.Execute, &input2, &output2)
		require.NoError(t, err)

		msg2, err := h.Repo().GetMessage(ctx, output2.MessageId)
		require.NoError(t, err)
		assert.Equal(t, int64(1), msg2.Ordinal, "Second message should have ordinal 1")

		// Create third message
		input3 := SaveMessageInput{
			ChatID:  chatID,
			Thread:  "0",
			Role:    "user",
			Content: "Third message",
		}

		var output3 SaveMessageOutput
		err = h.ExecuteActivity(activity.Execute, &input3, &output3)
		require.NoError(t, err)

		msg3, err := h.Repo().GetMessage(ctx, output3.MessageId)
		require.NoError(t, err)
		assert.Equal(t, int64(2), msg3.Ordinal, "Third message should have ordinal 2")
	})

	t.Run("Messages in different threads have independent ordinals", func(t *testing.T) {
		// Create message in child thread
		input := SaveMessageInput{
			ChatID:  chatID,
			Thread:  "0.0", // Different thread
			Role:    "user",
			Content: "First message in child thread",
		}

		var output SaveMessageOutput
		err := h.ExecuteActivity(activity.Execute, &input, &output)
		require.NoError(t, err)

		msg, err := h.Repo().GetMessage(ctx, output.MessageId)
		require.NoError(t, err)
		assert.Equal(t, int64(0), msg.Ordinal,
			"First message in new thread should have ordinal 0")
	})
}

func TestSaveMessageTransactionAtomicity(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()

	// Setup test data
	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	// Create activity
	activity := NewSaveMessageActivity(h.Repo())

	t.Run("Message and content blocks created atomically", func(t *testing.T) {
		input := SaveMessageInput{
			ChatID:  chatID,
			Thread:  "0",
			Role:    "assistant",
			Content: "Atomic operation test",
			ToolCalls: []ToolCall{
				{
					ID:    "call_atom1",
					Name:  "Tool1",
					Input: `{"test": "data"}`,
				},
			},
		}

		var output SaveMessageOutput
		err := h.ExecuteActivity(activity.Execute, &input, &output)
		require.NoError(t, err)

		// Verify message exists
		msg, err := h.Repo().GetMessage(ctx, output.MessageId)
		require.NoError(t, err)
		assert.NotNil(t, msg, "Message should exist")

		// Verify all content blocks exist
		blocks, err := h.Repo().ListContentBlocks(ctx, output.MessageId)
		require.NoError(t, err)
		require.Len(t, blocks, 2, "Should have 2 content blocks (1 text + 1 tool_call)")

		// Both message and blocks should exist together (atomicity)
		// If transaction failed, neither would exist
	})
}

// ============================================================================
// INJECT FILES INTEGRATION TESTS
// ============================================================================

// buildInjectFileInput constructs an ActivityInput with ResolvedInjectFiles.
// The test helper SaveMessageInput doesn't support InjectFiles, so we build
// the proto Node directly.
func buildInjectFileInput(chatID, thread, role, content string, injectFiles []*reliantv1.InjectFileMsg, attachments []string) ActivityInput {
	return ActivityInput{
		Runtime: RuntimeContext{ChatID: chatID, Thread: thread},
		Node: &reliantv1.Node{
			Type: "save_message",
			Args: &reliantv1.Node_SaveMessageNode{
				SaveMessageNode: &reliantv1.SaveMessageNodeArgs{
					ResolvedRole:        role,
					ResolvedContent:     content,
					ResolvedInjectFiles: injectFiles,
					ResolvedAttachments: attachments,
				},
			},
		},
	}
}

func TestSaveMessageWithInjectImageFile(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()

	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	activity := NewSaveMessageActivity(h.Repo())

	pngData := []byte("fake-png-binary-data")
	input := buildInjectFileInput(chatID, "0", "user", "Check this image", []*reliantv1.InjectFileMsg{
		{Filename: "screenshot.png", MimeType: "image/png", Data: pngData},
	}, nil)

	t.Run("Creates DB attachment with type image", func(t *testing.T) {
		var output SaveMessageOutput
		err := h.ExecuteActivity(activity.Execute, input, &output)
		require.NoError(t, err)
		assert.NotEmpty(t, output.MessageId)

		// Verify content blocks: text at 0, image at 1
		blocks, err := h.Repo().ListContentBlocks(ctx, output.MessageId)
		require.NoError(t, err)
		require.Len(t, blocks, 2, "Should have text block + image block")

		textBlock := blocks[0]
		assert.Equal(t, reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT, textBlock.BlockType)
		assert.Equal(t, 0, textBlock.Position)
		require.NotNil(t, textBlock.Content)
		assert.Equal(t, "Check this image", *textBlock.Content)

		imgBlock := blocks[1]
		assert.Equal(t, reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_IMAGE, imgBlock.BlockType)
		assert.Equal(t, 1, imgBlock.Position)

		// The content of an image block is the attachment ID
		require.NotNil(t, imgBlock.Content)
		attachmentID := *imgBlock.Content
		assert.NotEmpty(t, attachmentID)

		// Verify the attachment in the DB
		att, err := h.Repo().GetAttachment(ctx, attachmentID)
		require.NoError(t, err)
		assert.Equal(t, "screenshot.png", att.Filename)
		assert.Equal(t, "image/png", att.MimeType)
		assert.Equal(t, "image", att.AttachmentType)
		assert.Equal(t, pngData, att.Content)
		assert.Equal(t, int64(len(pngData)), att.Size)
	})
}

func TestSaveMessageWithInjectDocumentFile(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()

	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	activity := NewSaveMessageActivity(h.Repo())

	pdfData := []byte("%PDF-1.4 fake pdf content")
	input := buildInjectFileInput(chatID, "0", "user", "Review this PDF", []*reliantv1.InjectFileMsg{
		{Filename: "report.pdf", MimeType: "application/pdf", Data: pdfData},
	}, nil)

	t.Run("Creates DB attachment with type document", func(t *testing.T) {
		var output SaveMessageOutput
		err := h.ExecuteActivity(activity.Execute, input, &output)
		require.NoError(t, err)
		assert.NotEmpty(t, output.MessageId)

		// Verify content blocks: text at 0, document at 1
		blocks, err := h.Repo().ListContentBlocks(ctx, output.MessageId)
		require.NoError(t, err)
		require.Len(t, blocks, 2, "Should have text block + document block")

		textBlock := blocks[0]
		assert.Equal(t, reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT, textBlock.BlockType)
		assert.Equal(t, 0, textBlock.Position)

		docBlock := blocks[1]
		assert.Equal(t, reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_DOCUMENT, docBlock.BlockType)
		assert.Equal(t, 1, docBlock.Position)

		// The content of a document block is the attachment ID
		require.NotNil(t, docBlock.Content)
		attachmentID := *docBlock.Content
		assert.NotEmpty(t, attachmentID)

		// Verify the attachment in the DB
		att, err := h.Repo().GetAttachment(ctx, attachmentID)
		require.NoError(t, err)
		assert.Equal(t, "report.pdf", att.Filename)
		assert.Equal(t, "application/pdf", att.MimeType)
		assert.Equal(t, "document", att.AttachmentType)
		assert.Equal(t, pdfData, att.Content)
		assert.Equal(t, int64(len(pdfData)), att.Size)
	})
}

func TestSaveMessageWithInjectFilesAndText(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()

	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	activity := NewSaveMessageActivity(h.Repo())

	input := buildInjectFileInput(chatID, "0", "user", "Here are two files", []*reliantv1.InjectFileMsg{
		{Filename: "photo.jpg", MimeType: "image/jpeg", Data: []byte("jpeg-data")},
		{Filename: "spec.pdf", MimeType: "application/pdf", Data: []byte("pdf-data")},
	}, nil)

	t.Run("Text block at position 0 then attachment blocks", func(t *testing.T) {
		var output SaveMessageOutput
		err := h.ExecuteActivity(activity.Execute, input, &output)
		require.NoError(t, err)
		assert.NotEmpty(t, output.MessageId)

		blocks, err := h.Repo().ListContentBlocks(ctx, output.MessageId)
		require.NoError(t, err)
		require.Len(t, blocks, 3, "Should have 1 text + 2 attachment blocks")

		// Position 0: text
		assert.Equal(t, reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT, blocks[0].BlockType)
		assert.Equal(t, 0, blocks[0].Position)
		require.NotNil(t, blocks[0].Content)
		assert.Equal(t, "Here are two files", *blocks[0].Content)

		// Position 1: image (photo.jpg)
		assert.Equal(t, reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_IMAGE, blocks[1].BlockType)
		assert.Equal(t, 1, blocks[1].Position)
		require.NotNil(t, blocks[1].Content)
		att1, err := h.Repo().GetAttachment(ctx, *blocks[1].Content)
		require.NoError(t, err)
		assert.Equal(t, "photo.jpg", att1.Filename)
		assert.Equal(t, "image", att1.AttachmentType)

		// Position 2: document (spec.pdf)
		assert.Equal(t, reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_DOCUMENT, blocks[2].BlockType)
		assert.Equal(t, 2, blocks[2].Position)
		require.NotNil(t, blocks[2].Content)
		att2, err := h.Repo().GetAttachment(ctx, *blocks[2].Content)
		require.NoError(t, err)
		assert.Equal(t, "spec.pdf", att2.Filename)
		assert.Equal(t, "document", att2.AttachmentType)
	})
}

func TestSaveMessageWithInjectFileMixed(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()

	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	activity := NewSaveMessageActivity(h.Repo())

	// Pre-create a regular attachment (image) to use alongside inject files
	regularAttID := uuid.New().String()
	err := h.Repo().CreateAttachment(ctx, &db.Attachment{
		ID:             regularAttID,
		Filename:       "existing.png",
		Size:           100,
		MimeType:       "image/png",
		AttachmentType: "image",
		Content:        []byte("existing-image-data"),
	})
	require.NoError(t, err)

	// Build input with text + regular attachment + inject file
	input := buildInjectFileInput(chatID, "0", "user", "Mixed attachments", []*reliantv1.InjectFileMsg{
		{Filename: "injected.png", MimeType: "image/png", Data: []byte("injected-png")},
	}, []string{regularAttID})

	t.Run("All content blocks created in correct order", func(t *testing.T) {
		var output SaveMessageOutput
		err := h.ExecuteActivity(activity.Execute, input, &output)
		require.NoError(t, err)
		assert.NotEmpty(t, output.MessageId)

		blocks, err := h.Repo().ListContentBlocks(ctx, output.MessageId)
		require.NoError(t, err)
		require.Len(t, blocks, 3, "Should have 1 text + 1 regular attachment + 1 inject file")

		// Position 0: text
		assert.Equal(t, reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT, blocks[0].BlockType)
		assert.Equal(t, 0, blocks[0].Position)
		require.NotNil(t, blocks[0].Content)
		assert.Equal(t, "Mixed attachments", *blocks[0].Content)

		// Position 1: regular attachment (existing.png)
		assert.Equal(t, reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_IMAGE, blocks[1].BlockType)
		assert.Equal(t, 1, blocks[1].Position)
		require.NotNil(t, blocks[1].Content)
		assert.Equal(t, regularAttID, *blocks[1].Content, "Regular attachment should come first")

		// Position 2: injected attachment (injected.png)
		assert.Equal(t, reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_IMAGE, blocks[2].BlockType)
		assert.Equal(t, 2, blocks[2].Position)
		require.NotNil(t, blocks[2].Content)

		// Verify the injected attachment was created in the DB
		injectedAttID := *blocks[2].Content
		assert.NotEqual(t, regularAttID, injectedAttID, "Injected file should get a new attachment ID")
		att, err := h.Repo().GetAttachment(ctx, injectedAttID)
		require.NoError(t, err)
		assert.Equal(t, "injected.png", att.Filename)
		assert.Equal(t, "image", att.AttachmentType)
		assert.Equal(t, []byte("injected-png"), att.Content)
	})
}
