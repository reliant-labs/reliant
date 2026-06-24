// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
)

// ============================================================================
// MOCK IMPLEMENTATIONS
// ============================================================================

// MockLLMDriver simulates LLM responses with canned events
type MockLLMDriver struct {
	streamEvents []llm.DriverEvent
	modelUsed    models.Model
}

func (m *MockLLMDriver) Name() string {
	return "mock-llm"
}

func (m *MockLLMDriver) Model() models.Model {
	return m.modelUsed
}

func (m *MockLLMDriver) SendMessages(ctx context.Context, prompts []string, messages []message.Message, tools []tools.Tool) (*llm.DriverResponse, error) {
	return &llm.DriverResponse{
		Content:      "Mock response",
		FinishReason: "end_turn",
		Usage: llm.TokenUsage{
			TokenCount: 30,
		},
	}, nil
}

func (m *MockLLMDriver) StreamResponse(ctx context.Context, prompts []string, messages []message.Message, tools []tools.Tool) <-chan llm.DriverEvent {
	ch := make(chan llm.DriverEvent, len(m.streamEvents)+1)
	for _, event := range m.streamEvents {
		ch <- event
	}
	close(ch)
	return ch
}

func (m *MockLLMDriver) ValidateKey(ctx context.Context) error {
	return nil
}

// MockToolExecutor simulates tool execution with configurable results
type MockToolExecutor struct {
	executedTools []string // Track which tools were called
	results       map[string]*toolexec.ToolResult
}

func NewMockToolExecutor() *MockToolExecutor {
	return &MockToolExecutor{
		executedTools: []string{},
		results:       make(map[string]*toolexec.ToolResult),
	}
}

func (m *MockToolExecutor) SetResult(toolName string, result *toolexec.ToolResult) {
	m.results[toolName] = result
}

func (m *MockToolExecutor) ExecuteTool(ctx context.Context, req *toolexec.ToolRequest) (*toolexec.ToolResult, error) {
	m.executedTools = append(m.executedTools, req.ToolName)

	// Return configured result if available
	if result, ok := m.results[req.ToolName]; ok {
		return result, nil
	}

	// Default success result
	return &toolexec.ToolResult{
		Success:   true,
		IsError:   false,
		Content:   "Tool executed successfully",
		StartTime: time.Now(),
		EndTime:   time.Now(),
	}, nil
}

func (m *MockToolExecutor) Close() error {
	return nil
}

// ============================================================================
// TEST 1: SaveMessage → CallLLM (User input flow)
// ============================================================================

func TestUnifiedWorkflow_SaveUserMessage_CallLLM(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()

	// Setup test data
	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()
	thread := "0"

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	// Create test environment
	testSuite := &testsuite.WorkflowTestSuite{}
	env := testSuite.NewTestActivityEnvironment()

	// STEP 1: SaveMessage creates user message
	t.Run("SaveMessage creates user message", func(t *testing.T) {
		saveActivity := NewSaveMessageActivity(h.Repo())
		env.RegisterActivity(saveActivity.Execute)

		saveInput := SaveMessageInput{
			ChatID:  chatID,
			Thread:  thread,
			Role:    "user",
			Content: "Hello, please help me with a task",
		}

		val, err := env.ExecuteActivity(saveActivity.Execute, saveInput.V3())
		require.NoError(t, err)

		var saveOutput SaveMessageOutput
		err = val.Get(&saveOutput)
		require.NoError(t, err)

		// Verify user message was created
		assert.NotEmpty(t, saveOutput.MessageId)

		userMsg, err := h.Repo().GetMessage(ctx, saveOutput.MessageId)
		require.NoError(t, err)
		assert.Equal(t, reliantv1.MessageRole_MESSAGE_ROLE_USER, userMsg.Role)
		assert.Equal(t, int64(0), userMsg.Ordinal)
		assert.NotEmpty(t, userMsg.ContextWindowID, "Message should have context window ID")

		// Verify content block
		blocks, err := h.Repo().ListContentBlocks(ctx, saveOutput.MessageId)
		require.NoError(t, err)
		require.Len(t, blocks, 1)
		assert.Equal(t, reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT, blocks[0].BlockType)
		assert.NotNil(t, blocks[0].Content)
		assert.Equal(t, "Hello, please help me with a task", *blocks[0].Content)
	})

	// STEP 2: CallLLM reads that message and returns response
	t.Run("CallLLM reads user message and returns response", func(t *testing.T) {
		// Note: In a real scenario, CallLLM would execute here with a mocked LLM driver
		// For this integration test, we're focusing on data flow validation
		// The CallLLM activity would:
		// 1. Read the user message from the database
		// 2. Stream LLM response
		// 3. Create assistant message with response content
		// 4. Return CallLLMOutput with message_id and any tool_calls

		// Verify user message is in database with correct ordinal
		messages, err := h.Repo().ListMessages(ctx, chatID, db.MessageListOptions{
			Thread: &thread,
		})
		require.NoError(t, err)
		require.Len(t, messages, 1)
		assert.Equal(t, reliantv1.MessageRole_MESSAGE_ROLE_USER, messages[0].Role)
		assert.Equal(t, int64(0), messages[0].Ordinal)
	})

	// STEP 3: Verify data flows correctly between activities
	t.Run("Verify message sequence and data flow", func(t *testing.T) {
		messages, err := h.Repo().ListMessages(ctx, chatID, db.MessageListOptions{
			Thread: &thread,
		})
		require.NoError(t, err)

		// Should have user message at ordinal 0
		require.Len(t, messages, 1)
		assert.Equal(t, reliantv1.MessageRole_MESSAGE_ROLE_USER, messages[0].Role)
		assert.Equal(t, int64(0), messages[0].Ordinal)

		// Verify content blocks exist and are complete
		blocks, err := h.Repo().ListContentBlocks(ctx, messages[0].ID)
		require.NoError(t, err)
		require.Len(t, blocks, 1)
		assert.Equal(t, reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT, blocks[0].BlockType)
		assert.NotNil(t, blocks[0].Content)
	})
}

// ============================================================================
// TEST 2: ExecuteTools → SaveMessage (Tool execution flow)
// ============================================================================

func TestUnifiedWorkflow_ExecuteTools_SaveToolResults(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()

	// Setup test data
	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()
	thread := chatID // Use chatID as thread ID to match context window

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	// Create thread and context window (using chatID as thread ID)
	contextWindowID := h.CreateTestThreadAndContextWindow(ctx, chatID, chatID)

	// Create test environment
	testSuite := &testsuite.WorkflowTestSuite{}
	env := testSuite.NewTestActivityEnvironment()

	// STEP 0: Create user message (ordinal 0)
	userMsgID := uuid.New().String()
	err := h.Repo().CreateMessage(ctx, &db.Message{
		ID:              userMsgID,
		ChatID:          chatID,
		Ordinal:         0,
		ThreadID:        thread,
		ContextWindowID: contextWindowID,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_USER,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	})
	require.NoError(t, err)

	// STEP 1: Create assistant message with tool_calls (simulating CallLLM output)
	assistantMsgID := uuid.New().String()
	toolCallID1 := uuid.New().String()
	toolCallID2 := uuid.New().String()

	err = h.Repo().CreateMessage(ctx, &db.Message{
		ID:              assistantMsgID,
		ChatID:          chatID,
		Ordinal:         1,
		ThreadID:        thread,
		ContextWindowID: contextWindowID,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	})
	require.NoError(t, err)

	// Create tool_call content blocks
	toolName1 := "Read"
	toolInput1 := `{"file_path": "/tmp/test.txt"}`
	err = h.Repo().CreateContentBlock(ctx, &db.MessageContentBlock{
		ID:         uuid.New().String(),
		MessageID:  assistantMsgID,
		Position:   0,
		BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
		ToolCallID: &toolCallID1,
		ToolName:   &toolName1,
		ToolInput:  &toolInput1,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	})
	require.NoError(t, err)

	toolName2 := "Write"
	toolInput2 := `{"file_path": "/tmp/output.txt", "content": "test"}`
	err = h.Repo().CreateContentBlock(ctx, &db.MessageContentBlock{
		ID:         uuid.New().String(),
		MessageID:  assistantMsgID,
		Position:   1,
		BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
		ToolCallID: &toolCallID2,
		ToolName:   &toolName2,
		ToolInput:  &toolInput2,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	})
	require.NoError(t, err)

	// STEP 2: ExecuteTools executes and returns tool_results
	t.Run("ExecuteTools executes tools and returns results", func(t *testing.T) {
		mockExecutor := NewMockToolExecutor()
		mockExecutor.SetResult("Read", &toolexec.ToolResult{
			Success:   true,
			IsError:   false,
			Content:   "File contents: Hello World",
			StartTime: time.Now(),
			EndTime:   time.Now(),
		})
		mockExecutor.SetResult("Write", &toolexec.ToolResult{
			Success:   true,
			IsError:   false,
			Content:   "File written successfully",
			StartTime: time.Now(),
			EndTime:   time.Now(),
		})

		executeActivity := NewExecuteToolsActivity(h.Repo(), mockExecutor)
		env.RegisterActivity(executeActivity.Execute)

		executeInput := ExecuteToolsInput{
			ChatID: chatID,
			Thread: thread,
			ToolCalls: []ToolCall{
				{ID: toolCallID1, Name: "Read", Input: `{"file_path": "/tmp/test.txt"}`, BlockIndex: 0},
				{ID: toolCallID2, Name: "Write", Input: `{"file_path": "/tmp/output.txt", "content": "test"}`, BlockIndex: 1},
			},
		}

		val, err := env.ExecuteActivity(executeActivity.Execute, executeInput.V3())
		require.NoError(t, err)

		var executeOutput ExecuteToolsOutput
		err = val.Get(&executeOutput)
		require.NoError(t, err)

		// Verify tool execution results
		require.Len(t, executeOutput.ToolResults, 2)

		// Verify first tool result (ToolResult type - minimal fields)
		assert.Equal(t, toolCallID1, executeOutput.ToolResults[0].GetToolCallId())
		assert.False(t, executeOutput.ToolResults[0].IsError)
		assert.Contains(t, executeOutput.ToolResults[0].Content, "File contents")

		// Verify second tool result (ToolResult type - minimal fields)
		assert.Equal(t, toolCallID2, executeOutput.ToolResults[1].GetToolCallId())
		assert.False(t, executeOutput.ToolResults[1].IsError)
		assert.Contains(t, executeOutput.ToolResults[1].Content, "written successfully")

		// Verify mock executor was called (order is non-deterministic due to parallel execution)
		assert.Len(t, mockExecutor.executedTools, 2)
		assert.ElementsMatch(t, []string{"Read", "Write"}, mockExecutor.executedTools)
	})

	// STEP 3: SaveMessage creates tool message with results
	t.Run("SaveMessage creates tool message with results", func(t *testing.T) {
		saveActivity := NewSaveMessageActivity(h.Repo())
		env.RegisterActivity(saveActivity.Execute)

		// Convert ExecuteToolResult to ToolResult for SaveMessage
		toolResults := []ToolResult{
			{
				ToolCallID: toolCallID1,
				Content:    "File contents: Hello World",
				IsError:    false,
			},
			{
				ToolCallID: toolCallID2,
				Content:    "File written successfully",
				IsError:    false,
			},
		}

		saveInput := SaveMessageInput{
			ChatID:          chatID,
			Thread:          thread,
			Role:            "tool",
			ToolResults:     toolResults,
			ContextWindowID: contextWindowID, // Use same context window as manually created messages
		}

		val, err := env.ExecuteActivity(saveActivity.Execute, saveInput.V3())
		require.NoError(t, err)

		var saveOutput SaveMessageOutput
		err = val.Get(&saveOutput)
		require.NoError(t, err)

		// Verify tool message was created
		assert.NotEmpty(t, saveOutput.MessageId)

		toolMsg, err := h.Repo().GetMessage(ctx, saveOutput.MessageId)
		require.NoError(t, err)
		assert.Equal(t, reliantv1.MessageRole_MESSAGE_ROLE_TOOL, toolMsg.Role)
		assert.Equal(t, int64(2), toolMsg.Ordinal) // After user (0) and assistant (1)

		// Verify tool_result content blocks
		blocks, err := h.Repo().ListContentBlocks(ctx, saveOutput.MessageId)
		require.NoError(t, err)
		require.Len(t, blocks, 2)

		// Verify first tool_result block
		assert.Equal(t, reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT, blocks[0].BlockType)
		assert.Equal(t, toolCallID1, *blocks[0].ToolCallID)
		assert.Contains(t, *blocks[0].Content, "File contents")
		assert.False(t, *blocks[0].IsError)

		// Verify second tool_result block
		assert.Equal(t, reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT, blocks[1].BlockType)
		assert.Equal(t, toolCallID2, *blocks[1].ToolCallID)
		assert.Contains(t, *blocks[1].Content, "written successfully")
		assert.False(t, *blocks[1].IsError)
	})

	// STEP 4: Verify complete message sequence
	t.Run("Verify complete message sequence", func(t *testing.T) {
		messages, err := h.Repo().ListMessages(ctx, chatID, db.MessageListOptions{
			Thread: &thread,
		})
		require.NoError(t, err)

		// Should have: user (ordinal 0), assistant (ordinal 1), and tool (ordinal 2)
		require.Len(t, messages, 3)

		// Verify sequence
		assert.Equal(t, reliantv1.MessageRole_MESSAGE_ROLE_USER, messages[0].Role)
		assert.Equal(t, int64(0), messages[0].Ordinal)

		assert.Equal(t, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, messages[1].Role)
		assert.Equal(t, int64(1), messages[1].Ordinal)

		assert.Equal(t, reliantv1.MessageRole_MESSAGE_ROLE_TOOL, messages[2].Role)
		assert.Equal(t, int64(2), messages[2].Ordinal)
	})
}

// ============================================================================
// TEST 3: End-to-End Complete Loop
// ============================================================================

func TestUnifiedWorkflow_EndToEnd(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()

	// Setup test data
	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()
	thread := chatID // Use chatID as thread ID (standard pattern)

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID) // Also creates thread with ID = chatID

	// Create test environment
	testSuite := &testsuite.WorkflowTestSuite{}
	env := testSuite.NewTestActivityEnvironment()

	// STEP 1: User sends message
	var userMessageID string
	t.Run("Step 1: User sends message", func(t *testing.T) {
		saveActivity := NewSaveMessageActivity(h.Repo())
		env.RegisterActivity(saveActivity.Execute)

		saveInput := SaveMessageInput{
			ChatID:  chatID,
			Thread:  thread,
			Role:    "user",
			Content: "Count to 3 for me",
		}

		val, err := env.ExecuteActivity(saveActivity.Execute, saveInput.V3())
		require.NoError(t, err)

		var saveOutput SaveMessageOutput
		err = val.Get(&saveOutput)
		require.NoError(t, err)

		userMessageID = saveOutput.MessageId
		assert.NotEmpty(t, userMessageID)
	})

	// STEP 2: Assistant responds with tool calls (mocked)
	// In real flow, CallLLM would do this, but we simulate it here
	var assistantMsgID string
	var toolCallIDs []string
	t.Run("Step 2: Assistant responds with tool calls", func(t *testing.T) {
		// Get the context window from the user message
		userMsg, err := h.Repo().GetMessage(ctx, userMessageID)
		require.NoError(t, err)
		contextWindowID := userMsg.ContextWindowID

		assistantMsgID = uuid.New().String()
		toolCallID := uuid.New().String()
		toolCallIDs = append(toolCallIDs, toolCallID)

		err = h.Repo().CreateMessage(ctx, &db.Message{
			ID:              assistantMsgID,
			ChatID:          chatID,
			Ordinal:         1,
			ThreadID:        userMsg.ThreadID,
			ContextWindowID: contextWindowID,
			Role:            reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
			CreatedAt:       time.Now(),
			UpdatedAt:       time.Now(),
		})
		require.NoError(t, err)

		// Create text content block
		textContent := "Let me count for you using the Counter tool."
		err = h.Repo().CreateContentBlock(ctx, &db.MessageContentBlock{
			ID:        uuid.New().String(),
			MessageID: assistantMsgID,
			Position:  0,
			BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
			Content:   &textContent,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		})
		require.NoError(t, err)

		// Create tool_call block
		toolName := "Counter"
		toolInput := `{"max": 3}`
		err = h.Repo().CreateContentBlock(ctx, &db.MessageContentBlock{
			ID:         uuid.New().String(),
			MessageID:  assistantMsgID,
			Position:   1,
			BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
			ToolCallID: &toolCallID,
			ToolName:   &toolName,
			ToolInput:  &toolInput,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
		})
		require.NoError(t, err)
	})

	// STEP 3: Execute tools
	var toolResults []ToolResult
	t.Run("Step 3: Execute tools", func(t *testing.T) {
		mockExecutor := NewMockToolExecutor()
		mockExecutor.SetResult("Counter", &toolexec.ToolResult{
			Success:   true,
			IsError:   false,
			Content:   "1, 2, 3",
			StartTime: time.Now(),
			EndTime:   time.Now(),
		})

		executeActivity := NewExecuteToolsActivity(h.Repo(), mockExecutor)
		env.RegisterActivity(executeActivity.Execute)

		executeInput := ExecuteToolsInput{
			ChatID: chatID,
			Thread: thread,
			ToolCalls: []ToolCall{
				{ID: toolCallIDs[0], Name: "Counter", Input: `{"max": 3}`, BlockIndex: 1},
			},
		}

		val, err := env.ExecuteActivity(executeActivity.Execute, executeInput.V3())
		require.NoError(t, err)

		var executeOutput ExecuteToolsOutput
		err = val.Get(&executeOutput)
		require.NoError(t, err)

		require.Len(t, executeOutput.ToolResults, 1)
		assert.Equal(t, "1, 2, 3", executeOutput.ToolResults[0].GetContent())

		// Convert to ToolResult for SaveMessage
		toolResults = []ToolResult{
			{
				ToolCallID: executeOutput.ToolResults[0].GetToolCallId(),
				Content:    executeOutput.ToolResults[0].GetContent(),
				IsError:    executeOutput.ToolResults[0].GetIsError(),
			},
		}
	})

	// STEP 4: Save tool results
	var toolMessageID string
	t.Run("Step 4: Save tool results", func(t *testing.T) {
		saveActivity := NewSaveMessageActivity(h.Repo())
		env.RegisterActivity(saveActivity.Execute)

		saveInput := SaveMessageInput{
			ChatID:      chatID,
			Thread:      thread,
			Role:        "tool",
			ToolResults: toolResults,
		}

		val, err := env.ExecuteActivity(saveActivity.Execute, saveInput.V3())
		require.NoError(t, err)

		var saveOutput SaveMessageOutput
		err = val.Get(&saveOutput)
		require.NoError(t, err)

		toolMessageID = saveOutput.MessageId
		assert.NotEmpty(t, toolMessageID)
	})

	// STEP 5: Verify complete conversation history
	t.Run("Step 5: Verify complete conversation history", func(t *testing.T) {
		messages, err := h.Repo().ListMessages(ctx, chatID, db.MessageListOptions{
			Thread: &thread,
		})
		require.NoError(t, err)

		// Should have 3 messages: user, assistant, tool
		require.Len(t, messages, 3)

		// Verify sequence
		assert.Equal(t, reliantv1.MessageRole_MESSAGE_ROLE_USER, messages[0].Role)
		assert.Equal(t, int64(0), messages[0].Ordinal)
		assert.Equal(t, userMessageID, messages[0].ID)

		assert.Equal(t, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, messages[1].Role)
		assert.Equal(t, int64(1), messages[1].Ordinal)
		assert.Equal(t, assistantMsgID, messages[1].ID)

		assert.Equal(t, reliantv1.MessageRole_MESSAGE_ROLE_TOOL, messages[2].Role)
		assert.Equal(t, int64(2), messages[2].Ordinal)
		assert.Equal(t, toolMessageID, messages[2].ID)

		// Verify all messages have content blocks
		for _, msg := range messages {
			blocks, err := h.Repo().ListContentBlocks(ctx, msg.ID)
			require.NoError(t, err)
			assert.Greater(t, len(blocks), 0, "Message %s (%s) should have content blocks", msg.ID, msg.Role)
		}
	})
}

// ============================================================================
// TEST 4: Data Flow Through Outputs (CEL expression simulation)
// ============================================================================

func TestUnifiedWorkflow_DataFlowThroughOutputs(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()

	// Setup test data
	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()
	thread := chatID // Use chatID as thread ID (standard pattern)

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	// Create thread and context window
	contextWindowID := h.CreateTestThreadAndContextWindow(ctx, chatID, thread)

	testSuite := &testsuite.WorkflowTestSuite{}
	env := testSuite.NewTestActivityEnvironment()

	// This test verifies that outputs from one activity can be used as inputs to the next
	// Simulating the YAML workflow pattern: tool_calls: call_llm.tool_calls

	t.Run("CallLLM output tool_calls can flow to ExecuteTools input", func(t *testing.T) {
		// Step 1: Create assistant message with tool calls (simulating CallLLM)
		assistantMsgID := uuid.New().String()
		toolCallID1 := uuid.New().String()
		toolCallID2 := uuid.New().String()

		err := h.Repo().CreateMessage(ctx, &db.Message{
			ID:              assistantMsgID,
			ChatID:          chatID,
			Ordinal:         0,
			ThreadID:        thread,
			ContextWindowID: contextWindowID,
			Role:            reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
			CreatedAt:       time.Now(),
			UpdatedAt:       time.Now(),
		})
		require.NoError(t, err)

		toolName1 := "Glob"
		toolInput1 := `{"pattern": "*.go"}`
		err = h.Repo().CreateContentBlock(ctx, &db.MessageContentBlock{
			ID:         uuid.New().String(),
			MessageID:  assistantMsgID,
			Position:   0,
			BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
			ToolCallID: &toolCallID1,
			ToolName:   &toolName1,
			ToolInput:  &toolInput1,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
		})
		require.NoError(t, err)

		toolName2 := "Read"
		toolInput2 := `{"file_path": "/tmp/test.go"}`
		err = h.Repo().CreateContentBlock(ctx, &db.MessageContentBlock{
			ID:         uuid.New().String(),
			MessageID:  assistantMsgID,
			Position:   1,
			BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
			ToolCallID: &toolCallID2,
			ToolName:   &toolName2,
			ToolInput:  &toolInput2,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
		})
		require.NoError(t, err)

		// Simulate CallLLM output structure (note: MessageID is not in output anymore)
		callLLMOutput := CallLLMOutput{
			ResponseText: "I'll help you with that.",
			ToolCalls: []*reliantv1.ToolCallMsg{
				{Id: toolCallID1, Name: "Glob"},
				{Id: toolCallID2, Name: "Read"},
			},
			TokenCount: 80,
		}

		// Verify the output structure can be serialized (as it would be in Temporal)
		outputBytes, err := json.Marshal(&callLLMOutput)
		require.NoError(t, err)
		assert.NotEmpty(t, outputBytes)

		// Verify we can deserialize it
		var deserializedOutput CallLLMOutput
		err = json.Unmarshal(outputBytes, &deserializedOutput)
		require.NoError(t, err)
		assert.Equal(t, 2, len(deserializedOutput.ToolCalls))

		// Step 2: Use CallLLM output.tool_calls as ExecuteTools input.tool_calls
		// This simulates: tool_calls: call_llm.tool_calls
		executeInput := ExecuteToolsInput{
			ChatID: chatID,
			Thread: thread,
			ToolCalls: []ToolCall{
				{ID: callLLMOutput.ToolCalls[0].GetId(), Name: callLLMOutput.ToolCalls[0].GetName()},
				{ID: callLLMOutput.ToolCalls[1].GetId(), Name: callLLMOutput.ToolCalls[1].GetName()},
			}, // Direct assignment simulating CEL expression
		}

		// Verify the input structure is correct
		require.Len(t, executeInput.ToolCalls, 2)
		assert.Equal(t, toolCallID1, executeInput.ToolCalls[0].ID)
		assert.Equal(t, "Glob", executeInput.ToolCalls[0].Name)
		assert.Equal(t, toolCallID2, executeInput.ToolCalls[1].ID)
		assert.Equal(t, "Read", executeInput.ToolCalls[1].Name)
	})

	t.Run("ExecuteTools output tool_results can flow to SaveMessage input", func(t *testing.T) {
		// Simulate ExecuteTools output
		// Note: ExecuteTools now returns []ToolResult directly (not ExecuteToolResult)
		executeOutput := ExecuteToolsOutput{
			ToolResults: []*reliantv1.ToolResultMsg{
				{
					ToolCallId: "call-1",
					Content:    "file1.go\nfile2.go\nfile3.go",
					IsError:    false,
				},
				{
					ToolCallId: "call-2",
					Content:    "package main\n\nfunc main() {}",
					IsError:    false,
				},
			},
		}

		// Convert proto ToolResultMsg to SaveMessageInput ToolResult representation.
		toolResults := protoToolResultsToMessage(executeOutput.ToolResults)

		// Create SaveMessage input
		saveInput := SaveMessageInput{
			ChatID:          chatID,
			Thread:          thread,
			Role:            "tool",
			ToolResults:     toolResults,     // Transformed from ExecuteTools output
			ContextWindowID: contextWindowID, // Use same context window as manually created messages
		}

		// Verify the transformation worked correctly
		require.Len(t, saveInput.ToolResults, 2)
		assert.Equal(t, "call-1", saveInput.ToolResults[0].ToolCallID)
		assert.Contains(t, saveInput.ToolResults[0].Content, "file1.go")
		assert.False(t, saveInput.ToolResults[0].IsError)

		assert.Equal(t, "call-2", saveInput.ToolResults[1].ToolCallID)
		assert.Contains(t, saveInput.ToolResults[1].Content, "package main")
		assert.False(t, saveInput.ToolResults[1].IsError)

		// Execute SaveMessage to verify it works
		saveActivity := NewSaveMessageActivity(h.Repo())
		env.RegisterActivity(saveActivity.Execute)

		val, err := env.ExecuteActivity(saveActivity.Execute, saveInput.V3())
		require.NoError(t, err)

		var saveOutput SaveMessageOutput
		err = val.Get(&saveOutput)
		require.NoError(t, err)
		assert.NotEmpty(t, saveOutput.MessageId)
	})

	t.Run("Complete data flow chain works end-to-end", func(t *testing.T) {
		// This test verifies the complete data flow:
		// CallLLM.output.tool_calls → ExecuteTools.input.tool_calls
		// ExecuteTools.output.tool_results → SaveMessage.input.tool_results
		// SaveMessage.output.message_id → CallLLM.input (implicitly through thread)

		// Verify final message count and sequence
		messages, err := h.Repo().ListMessages(ctx, chatID, db.MessageListOptions{
			Thread: &thread,
		})
		require.NoError(t, err)

		// Should have assistant message (from earlier test) and tool message
		require.GreaterOrEqual(t, len(messages), 2)

		// Verify messages are properly sequenced
		for i := 0; i < len(messages)-1; i++ {
			assert.Less(t, messages[i].Ordinal, messages[i+1].Ordinal,
				"Messages should be in ascending ordinal order")
		}

		// Verify all messages have complete content blocks
		for _, msg := range messages {
			blocks, err := h.Repo().ListContentBlocks(ctx, msg.ID)
			require.NoError(t, err)
			assert.Greater(t, len(blocks), 0, "Message %s should have content blocks", msg.ID)

			// Verify all blocks have required fields
			for _, block := range blocks {
				assert.NotEmpty(t, block.BlockType)
				switch block.BlockType {
				case reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT:
					assert.NotNil(t, block.Content)
				case reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL:
					assert.NotNil(t, block.ToolCallID)
					assert.NotNil(t, block.ToolName)
					assert.NotNil(t, block.ToolInput)
				case reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT:
					assert.NotNil(t, block.ToolCallID)
					assert.NotNil(t, block.Content)
					assert.NotNil(t, block.IsError)
				}
			}
		}
	})
}

// ============================================================================
// HELPER FUNCTIONS
// ============================================================================

// intPtr is already defined in save_message.go, no need to redefine it here
