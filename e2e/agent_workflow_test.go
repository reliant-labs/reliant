// Copyright (c) 2025 Reliant Labs
package e2e

import (
	"context"
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	roleUser      = reliantv1.MessageRole_MESSAGE_ROLE_USER
	roleAssistant = reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT
	roleTool      = reliantv1.MessageRole_MESSAGE_ROLE_TOOL
)

// ============================================================================
// AGENT WORKFLOW E2E TESTS
//
// Tests for the core agent workflow (builtin://agent) defined in:
// ./internal/workflow/builtin/agent.yaml
//
// Test scenarios:
// 1. Simple prompt -> response flow
// 2. Tool call -> execution -> response flow
// 3. Multi-turn conversation
// 4. Multiple tool calls in sequence
// 5. Approval gates (when auto_approve=false)
// 6. Error handling and recovery
// 7. Cancellation handling
// ============================================================================

// ============================================================================
// SIMPLE PROMPT -> RESPONSE FLOW
// ============================================================================

// TestAgent_SimplePromptResponse tests the most basic agent flow:
// User sends prompt -> Agent responds with text only.
func TestAgent_SimplePromptResponse(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Cleanup()

	// Configure mock to return simple text
	h.MockLLM.SetResponse("Hello! I understand your request. Here's my response.")

	chatID := h.StartAgentWorkflowViaGRPC(t, "Hello, can you help me?")

	// Wait for workflow to complete (with error logging)
	h.WaitForWorkflowComplete(t, chatID)

	// Wait for exactly 2 messages: user + assistant
	messages := h.WaitForMessages(t, chatID, 2)

	// Verify structure
	AssertMessageRolesInOrder(t, messages, roleUser, roleAssistant)

	// Verify content was saved correctly
	AssertTextContentContains(t, h.DB, messages[0].ID, "Hello, can you help me?")
	AssertTextContentContains(t, h.DB, messages[1].ID, "Here's my response")

	// Verify no tool calls
	AssertNoToolCalls(t, h.DB, messages[1].ID)

	// Verify LLM was called exactly once
	assert.Equal(t, 1, h.MockLLM.CallCount(), "LLM should be called once for simple response")

	t.Logf("✓ Simple prompt->response: user='%v', assistant='%v'",
		messages[0].Role, messages[1].Role)
}

// TestAgent_EmptyResponse tests handling of empty LLM responses.
func TestAgent_EmptyResponse(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Cleanup()

	// Configure mock to return empty text (edge case)
	h.MockLLM.SetResponse("")

	chatID := h.StartAgentWorkflowViaGRPC(t, "Test empty response")

	// Should still create 2 messages (user + assistant with empty content)
	messages := h.WaitForMessages(t, chatID, 2)
	AssertMessageRolesInOrder(t, messages, roleUser, roleAssistant)

	t.Logf("✓ Empty response handled: %d messages created", len(messages))
}

// ============================================================================
// TOOL CALL -> EXECUTION -> RESPONSE FLOW
// ============================================================================

// TestAgent_SingleToolCall tests the flow when agent makes a single tool call.
func TestAgent_SingleToolCall(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Cleanup()

	// First response: tool call
	h.MockLLM.SetResponseWithToolCall(
		"I'll run that command for you.",
		"Bash",
		map[string]interface{}{"command": "echo 'test output'"},
	)

	// Second response: after receiving tool result
	h.MockLLM.AddResponse(MockResponse{
		Text: "The command executed successfully and returned 'test output'.",
	})

	chatID := h.StartAgentWorkflowViaGRPC(t, "Run: echo 'test output'")

	// Wait for workflow to complete first - this will log activity details on error
	h.WaitForWorkflowComplete(t, chatID)

	// DEBUG: dump workflow history
	history := h.GetWorkflowHistory(t, chatID)
	t.Logf("ALL ACTIVITIES (%d):", len(history.GetActivities()))
	for _, a := range history.GetActivities() {
		if a.Failed {
			t.Logf("  FAILED: type=%s id=%s error=%s", a.ActivityType, a.ActivityID, a.FailureMessage)
		} else if a.Completed {
			t.Logf("  OK: type=%s id=%s", a.ActivityType, a.ActivityID)
		} else {
			t.Logf("  ???: type=%s id=%s", a.ActivityType, a.ActivityID)
		}
	}
	// Also dump LLM call count
	t.Logf("LLM call count: %d", h.MockLLM.CallCount())
	// Dump WorkflowError input to see the actual error
	errAct := history.GetFirstActivity("WorkflowError")
	if errAct != nil {
		errInput := errAct.MustParseInput(t)
		t.Logf("WorkflowError input: %+v", errInput)
	}

	// Expected flow: user -> assistant (tool call) -> tool -> assistant (final)
	messages := h.WaitForMessages(t, chatID, 4)
	AssertMessageRolesInOrder(t, messages, roleUser, roleAssistant, roleTool, roleAssistant)

	// Verify tool call details
	AssertToolCallExists(t, h.DB, messages[1].ID, "Bash")

	// Verify final response
	AssertTextContentContains(t, h.DB, messages[3].ID, "executed successfully")

	// Verify LLM was called twice (initial + after tool result)
	assert.Equal(t, 2, h.MockLLM.CallCount(), "LLM should be called twice")

	t.Logf("✓ Single tool call: tool='Bash', messages=%d, llm_calls=%d",
		len(messages), h.MockLLM.CallCount())
}

// TestAgent_ExecuteToolsFailureDoesNotTriggerCELNoSuchKey verifies loop failure
// propagation: when a loop step fails, the workflow should report the underlying
// step error directly instead of masking it behind edge-routing CEL "no such key"
// errors from evaluating conditions on failed step outputs.
func TestAgent_ExecuteToolsFailureDoesNotTriggerCELNoSuchKey(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Cleanup()

	workflowYAML := `
name: loop_failure_routing_guard
entry: [agent_loop]

nodes:
  - id: agent_loop
    type: loop
    while: iter.iteration < 1
    inline:
      entry: [bad_step]
      nodes:
        - id: bad_step
          type: run
          command: "{{nodes.bad_step.stdout}}"

        - id: next_step
          type: save_message
          role: assistant
          content: "should never be reached"

      edges:
        - from: bad_step
          cases:
            - to: next_step
              condition: nodes.bad_step.exit_code > 0
`
	h.WriteWorkflowFile(t, "loop_failure_routing_guard.yaml", workflowYAML)

	chatID := h.StartWorkflowViaGRPC(t, "loop_failure_routing_guard", map[string]interface{}{}, "trigger loop failure")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var latestErrorUpdateData string
	for {
		updates, err := h.DB.GetUpdatesSince(context.Background(), chatID, 0, 200)
		require.NoError(t, err)

		for _, update := range updates {
			if update.UpdateType != reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_ERROR {
				continue
			}
			latestErrorUpdateData = string(update.Data)
		}

		if latestErrorUpdateData != "" {
			break
		}

		select {
		case <-ctx.Done():
			t.Fatalf("timeout waiting for error update for chat %s", chatID)
		case <-time.After(100 * time.Millisecond):
		}
	}

	assert.NotContains(t, latestErrorUpdateData, "no such key: exit_code",
		"loop failure should not be masked as edge-condition missing-key error")
	assert.Contains(t, latestErrorUpdateData, "CEL evaluation failed for step bad_step",
		"error update should preserve underlying bad_step failure")
	assert.Contains(t, latestErrorUpdateData, "no such key: bad_step",
		"error update should reflect the original bad step evaluation failure")
}

// TestAgent_MultipleToolCallsInSequence tests chained tool calls.
func TestAgent_MultipleToolCallsInSequence(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Cleanup()

	// First LLM call: first tool call
	h.MockLLM.SetResponseWithToolCall(
		"First, I'll check the file.",
		"Bash",
		map[string]interface{}{"command": "cat file.txt"},
	)

	// Second LLM call: another tool call
	h.MockLLM.AddResponse(MockResponse{
		Text:      "Now I'll modify it.",
		ToolCalls: []MockToolCall{{Name: "Bash", Input: map[string]interface{}{"command": "echo 'modified' > file.txt"}}},
	})

	// Third LLM call: final response
	h.MockLLM.AddResponse(MockResponse{
		Text: "I've read and modified the file successfully.",
	})

	chatID := h.StartAgentWorkflowViaGRPC(t, "Read and modify file.txt")

	// Expected: user -> assistant (tool) -> tool -> assistant (tool) -> tool -> assistant (final)
	messages := h.WaitForMessages(t, chatID, 6)
	AssertMessageRolesInOrder(t, messages, roleUser, roleAssistant, roleTool, roleAssistant, roleTool, roleAssistant)

	// Verify tool calls
	AssertToolCallExists(t, h.DB, messages[1].ID, "Bash")
	AssertToolCallExists(t, h.DB, messages[3].ID, "Bash")

	// Verify final response has no tool calls
	AssertNoToolCalls(t, h.DB, messages[5].ID)

	assert.Equal(t, 3, h.MockLLM.CallCount(), "LLM should be called 3 times")

	t.Logf("✓ Multiple tool calls in sequence: %d messages, %d LLM calls",
		len(messages), h.MockLLM.CallCount())
}

// TestAgent_ParallelToolCalls tests when agent requests multiple tools at once.
func TestAgent_ParallelToolCalls(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Cleanup()

	// First response: multiple tool calls at once
	h.MockLLM.SetResponseWithToolCalls(
		"I'll check both files.",
		MockToolCall{Name: "Bash", Input: map[string]interface{}{"command": "cat file1.txt"}},
		MockToolCall{Name: "Bash", Input: map[string]interface{}{"command": "cat file2.txt"}},
	)

	// Second response: after both tool results
	h.MockLLM.AddResponse(MockResponse{
		Text: "I've read both files. File1 contains X and file2 contains Y.",
	})

	chatID := h.StartAgentWorkflowViaGRPC(t, "Read file1.txt and file2.txt")

	// Expected: user -> assistant (2 tool calls) -> tool (results) -> assistant
	// Tool results are grouped into a single "tool" message
	messages := h.WaitForMessages(t, chatID, 4)
	AssertMessageRolesInOrder(t, messages, roleUser, roleAssistant, roleTool, roleAssistant)

	// Verify assistant message has multiple tool calls
	blocks := h.GetContentBlocks(t, messages[1].ID)
	toolCallCount := 0
	for _, block := range blocks {
		if block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL {
			toolCallCount++
		}
	}
	assert.GreaterOrEqual(t, toolCallCount, 2, "should have at least 2 tool calls")

	t.Logf("✓ Parallel tool calls: %d tool calls, %d messages",
		toolCallCount, len(messages))
}

// ============================================================================
// MULTI-TURN CONVERSATION
// ============================================================================

// TestAgent_MultiTurnConversation tests multiple back-and-forth exchanges.
func TestAgent_MultiTurnConversation(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Cleanup()

	// Configure responses for each turn
	h.MockLLM.SetResponses(
		"Turn 1: Hello! How can I help you?",
		"Turn 2: Sure, I can help with that.",
		"Turn 3: Here's the final answer.",
	)

	chatID := h.StartAgentWorkflowViaGRPC(t, "Hello")

	// Turn 1
	messages := h.WaitForMessages(t, chatID, 2)
	h.WaitForWorkflowComplete(t, chatID)
	require.Len(t, messages, 2)

	// Turn 2
	h.SendMessageViaGRPC(t, chatID, "Can you help me?")
	messages = h.WaitForMessages(t, chatID, 4)
	h.WaitForWorkflowComplete(t, chatID)
	require.Len(t, messages, 4)

	// Turn 3
	h.SendMessageViaGRPC(t, chatID, "What's the answer?")
	messages = h.WaitForMessages(t, chatID, 6)
	require.Len(t, messages, 6)

	// Verify all roles alternate correctly
	AssertMessageRolesInOrder(t, messages, roleUser, roleAssistant, roleUser, roleAssistant, roleUser, roleAssistant)

	// Verify ordinals are sequential
	AssertMessageOrdinalsSequential(t, messages)

	// Verify each turn's content
	AssertTextContentContains(t, h.DB, messages[1].ID, "Turn 1")
	AssertTextContentContains(t, h.DB, messages[3].ID, "Turn 2")
	AssertTextContentContains(t, h.DB, messages[5].ID, "Turn 3")

	t.Logf("✓ Multi-turn conversation: %d turns, %d total messages", 3, len(messages))
}

// TestAgent_MultiTurnWithTools tests multi-turn conversation with tool use.
func TestAgent_MultiTurnWithTools(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Cleanup()

	// Turn 1: simple response
	h.MockLLM.SetResponse("Hello! What would you like me to do?")

	chatID := h.StartAgentWorkflowViaGRPC(t, "Hi")

	// Turn 1
	h.WaitForMessages(t, chatID, 2)
	h.WaitForWorkflowComplete(t, chatID)

	// Turn 2: tool call
	h.MockLLM.Reset()
	h.MockLLM.SetResponseWithToolCall(
		"I'll list the files.",
		"Bash",
		map[string]interface{}{"command": "ls"},
	)
	h.MockLLM.AddResponse(MockResponse{
		Text: "Here are the files in the directory.",
	})

	h.SendMessageViaGRPC(t, chatID, "List files")
	messages := h.WaitForMessages(t, chatID, 6)

	// Turn 2 adds: user, assistant (tool), tool, assistant
	AssertMessageRolesInOrder(t, messages, roleUser, roleAssistant, roleUser, roleAssistant, roleTool, roleAssistant)

	t.Logf("✓ Multi-turn with tools: %d messages across 2 turns", len(messages))
}

// ============================================================================
// APPROVAL GATES
// ============================================================================

// TestAgent_ApprovalGate_AutoApprove tests that auto_approve=true skips approval.
func TestAgent_ApprovalGate_AutoApprove(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Cleanup()

	h.MockLLM.SetResponseWithToolCall(
		"I'll execute that command.",
		"Bash",
		map[string]interface{}{"command": "echo test"},
	)
	h.MockLLM.AddResponse(MockResponse{Text: "Done!"})

	// Create chat - mode is controlled via workflow inputs, not chat struct
	chatID := h.StartAgentWorkflowViaGRPC(t, "Run test")

	// Should complete without waiting for approval
	messages := h.WaitForMessages(t, chatID, 4)
	AssertMessageRolesInOrder(t, messages, roleUser, roleAssistant, roleTool, roleAssistant)

	t.Logf("✓ Auto approve: tool executed without approval gate")
}

// ============================================================================
// ERROR HANDLING AND RECOVERY
// ============================================================================

// TestAgent_ToolExecutionError tests handling of tool execution errors.
func TestAgent_ToolExecutionError(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Cleanup()

	// Tool call that will fail (nonexistent command)
	h.MockLLM.SetResponseWithToolCall(
		"I'll run that command.",
		"Bash",
		map[string]interface{}{"command": "this-command-does-not-exist-12345"},
	)

	// Response after tool error
	h.MockLLM.AddResponse(MockResponse{
		Text: "The command failed. Let me try something else.",
	})

	chatID := h.StartAgentWorkflowViaGRPC(t, "Run nonexistent command")

	// Flow should still complete: user -> assistant (tool) -> tool (error result) -> assistant
	messages := h.WaitForMessages(t, chatID, 4)
	AssertMessageRolesInOrder(t, messages, roleUser, roleAssistant, roleTool, roleAssistant)

	// Verify tool result contains error information
	blocks := h.GetContentBlocks(t, messages[2].ID)
	hasToolResult := false
	for _, block := range blocks {
		if block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT {
			hasToolResult = true
		}
	}
	assert.True(t, hasToolResult, "tool message should have tool_result block")

	t.Logf("✓ Tool error handling: workflow recovered from tool error")
}

// TestAgent_LLMContextPreserved tests that context is maintained across tool calls.
func TestAgent_LLMContextPreserved(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Cleanup()

	h.MockLLM.SetResponseWithToolCall(
		"Checking...",
		"Bash",
		map[string]interface{}{"command": "echo test"},
	)
	h.MockLLM.AddResponse(MockResponse{Text: "Done"})

	chatID := h.StartAgentWorkflowViaGRPC(t, "Original user message")

	h.WaitForMessages(t, chatID, 4)

	// Verify the second LLM call received the full context
	calls := h.MockLLM.GetCalls()
	require.Equal(t, 2, len(calls), "should have 2 LLM calls")

	// Second call should have more messages than first
	assert.Greater(t, len(calls[1].Messages), len(calls[0].Messages),
		"second LLM call should have more context")

	t.Logf("✓ Context preserved: call 1 had %d msgs, call 2 had %d msgs",
		len(calls[0].Messages), len(calls[1].Messages))
}

// ============================================================================
// TOKEN TRACKING
// ============================================================================

// TestAgent_TokenUsageTracked tests that token usage is recorded in messages.
func TestAgent_TokenUsageTracked(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Cleanup()

	h.MockLLM.SetResponse("Response with token tracking")

	chatID := h.StartAgentWorkflowViaGRPC(t, "Test tokens")

	messages := h.WaitForMessages(t, chatID, 2)

	// Assistant message should have token count
	assistantMsg := messages[1]
	require.NotNil(t, assistantMsg.TokenCount, "should have token count pointer")
	assert.Greater(t, *assistantMsg.TokenCount, 0, "should have token count > 0")

	t.Logf("✓ Token tracking: token_count=%d", *assistantMsg.TokenCount)
}

// ============================================================================
// THREAD PATH HANDLING
// ============================================================================

// TestAgent_ThreadDefault tests that the root thread ID equals the chat ID.
func TestAgent_ThreadDefault(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Cleanup()

	h.MockLLM.SetResponse("Response in default thread")

	chatID := h.StartAgentWorkflowViaGRPC(t, "Test thread")

	messages := h.WaitForMessages(t, chatID, 2)

	// In the new thread model, root thread ID equals chat ID - verify via context_window
	for _, msg := range messages {
		cw, err := h.DB.GetContextWindow(context.Background(), msg.ContextWindowID)
		require.NoError(t, err, "should get context window")
		assert.Equal(t, chatID, cw.ThreadID, "messages should be in root thread (equals chat ID)")
	}

	cw, _ := h.DB.GetContextWindow(context.Background(), messages[0].ContextWindowID)
	t.Logf("✓ Thread path: all messages in thread '%s'", cw.ThreadID)
}
