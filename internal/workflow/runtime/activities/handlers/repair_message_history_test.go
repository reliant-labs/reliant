// Copyright (c) 2025 Reliant Labs
package handlers

// Tests for repairMessageHistory: verifying it preserves message ordering
// such that conversations ending with a user message retain that property.
//
// Bug context: "This model does not support assistant message prefill. The
// conversation must end with a user message." The repair function must preserve
// message ordering such that the conversation still ends with a user message.

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Tests: repairMessageHistory preserves conversation ending with user message
// =============================================================================

func TestRepairMessageHistory_PreservesLastUserMessage(t *testing.T) {
	tests := []struct {
		name             string
		messages         []message.Message
		expectedLastRole message.MessageRole
		expectMinMsgs    int
	}{
		{
			name: "simple user-assistant-user stays in order",
			messages: []message.Message{
				{ID: "1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hello"}}},
				{ID: "2", Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "hi"}}},
				{ID: "3", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "bye"}}},
			},
			expectedLastRole: message.User,
			expectMinMsgs:    3,
		},
		{
			name: "user after tool call cycle preserved",
			messages: []message.Message{
				{ID: "1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "list files"}}},
				{ID: "2", Role: message.Assistant, Parts: []message.ContentPart{
					message.ToolCall{ID: "tc1", Name: "bash", Input: `{"cmd":"ls"}`},
				}},
				{ID: "3", Role: message.Tool, Parts: []message.ContentPart{
					message.ToolResult{ToolCallID: "tc1", Content: "file1.txt"},
				}},
				{ID: "4", Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "found files"}}},
				{ID: "5", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "show me"}}},
			},
			expectedLastRole: message.User,
			expectMinMsgs:    5,
		},
		{
			name: "orphaned tool call with user message after it - user preserved",
			messages: []message.Message{
				{ID: "1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "do stuff"}}},
				{ID: "2", Role: message.Assistant, Parts: []message.ContentPart{
					message.TextContent{Text: "let me help"},
					message.ToolCall{ID: "tc_orphan", Name: "bash", Input: `{"cmd":"whoami"}`},
				}},
				// No tool result - orphaned!
				{ID: "3", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "actually nevermind"}}},
			},
			expectedLastRole: message.User,
			expectMinMsgs:    3, // user + assistant + synthetic tool result + user (at minimum 3 non-tool)
		},
		{
			name: "user message after orphaned tool call not reordered",
			messages: []message.Message{
				{ID: "1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "start"}}},
				{ID: "2", Role: message.Assistant, Parts: []message.ContentPart{
					message.ToolCall{ID: "tc1", Name: "read", Input: `{}`},
				}},
				{ID: "3", Role: message.Tool, Parts: []message.ContentPart{
					message.ToolResult{ToolCallID: "tc1", Content: "data"},
				}},
				{ID: "4", Role: message.Assistant, Parts: []message.ContentPart{
					message.ToolCall{ID: "tc2", Name: "write", Input: `{}`},
				}},
				// tc2 orphaned - no tool result
				{ID: "5", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "continue"}}},
			},
			expectedLastRole: message.User,
			expectMinMsgs:    4,
		},
		{
			name: "multiple orphaned tool calls then user - user is last",
			messages: []message.Message{
				{ID: "1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "multi"}}},
				{ID: "2", Role: message.Assistant, Parts: []message.ContentPart{
					message.ToolCall{ID: "tc1", Name: "a", Input: `{}`},
					message.ToolCall{ID: "tc2", Name: "b", Input: `{}`},
				}},
				// Both orphaned
				{ID: "3", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "user msg"}}},
			},
			expectedLastRole: message.User,
			expectMinMsgs:    3,
		},
		{
			name: "tool result before user not dropped when assistant exists",
			messages: []message.Message{
				{ID: "1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "do task"}}},
				{ID: "2", Role: message.Assistant, Parts: []message.ContentPart{
					message.ToolCall{ID: "tc1", Name: "bash", Input: `{}`},
				}},
				{ID: "3", Role: message.Tool, Parts: []message.ContentPart{
					message.ToolResult{ToolCallID: "tc1", Content: "done"},
				}},
				{ID: "4", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "thanks"}}},
			},
			expectedLastRole: message.User,
			expectMinMsgs:    4,
		},
		{
			name: "conversation ending with only user message",
			messages: []message.Message{
				{ID: "1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hello"}}},
			},
			expectedLastRole: message.User,
			expectMinMsgs:    1,
		},
		{
			name: "system then user stays in order",
			messages: []message.Message{
				{ID: "1", Role: message.System, Parts: []message.ContentPart{message.TextContent{Text: "system prompt"}}},
				{ID: "2", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hello"}}},
			},
			expectedLastRole: message.User,
			expectMinMsgs:    2,
		},
		{
			name: "complex cycle: tool results split across messages then user",
			messages: []message.Message{
				{ID: "1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "start"}}},
				{ID: "2", Role: message.Assistant, Parts: []message.ContentPart{
					message.ToolCall{ID: "tc1", Name: "a", Input: `{}`},
					message.ToolCall{ID: "tc2", Name: "b", Input: `{}`},
				}},
				{ID: "3", Role: message.Tool, Parts: []message.ContentPart{
					message.ToolResult{ToolCallID: "tc1", Content: "result1"},
				}},
				{ID: "4", Role: message.Tool, Parts: []message.ContentPart{
					message.ToolResult{ToolCallID: "tc2", Content: "result2"},
				}},
				{ID: "5", Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "done"}}},
				{ID: "6", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "next"}}},
			},
			expectedLastRole: message.User,
			expectMinMsgs:    4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := repairMessageHistory(tt.messages)
			require.GreaterOrEqual(t, len(result), tt.expectMinMsgs,
				"Expected at least %d messages, got %d", tt.expectMinMsgs, len(result))

			if len(result) > 0 {
				lastMsg := result[len(result)-1]
				assert.Equal(t, tt.expectedLastRole, lastMsg.Role,
					"Last message role should be %s, got %s", tt.expectedLastRole, lastMsg.Role)
			}
		})
	}
}

// TestRepairMessageHistory_OrphanedToolCallBeforeUserMessage specifically tests
// the scenario that causes the "must end with user message" error:
// assistant message with tool call → (no tool result) → user message
//
// The repair function must insert a synthetic tool result BETWEEN the assistant
// and user messages, NOT after the user message.
func TestRepairMessageHistory_OrphanedToolCallBeforeUserMessage(t *testing.T) {
	msgs := []message.Message{
		{ID: "1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "do something"}}},
		{ID: "2", Role: message.Assistant, Parts: []message.ContentPart{
			message.TextContent{Text: "working on it"},
			message.ToolCall{ID: "tc_orphan", Name: "bash", Input: `{"cmd":"ls"}`},
		}},
		// Orphaned: no tool result for tc_orphan
		{ID: "3", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "new question"}}},
	}

	result := repairMessageHistory(msgs)

	// Should have: user, assistant, synthetic-tool-result, user
	require.GreaterOrEqual(t, len(result), 4,
		"Should have at least 4 messages: user + assistant + synthetic tool result + user")

	// Verify ordering: the user message must be LAST
	assert.Equal(t, message.User, result[len(result)-1].Role,
		"Last message MUST be user to avoid 'must end with user message' API error")
	assert.Equal(t, "3", result[len(result)-1].ID,
		"The last user message should be the one sent after the interruption")

	// Verify the synthetic tool result is BEFORE the user message
	foundToolResult := false
	foundUserAfterRepair := false
	for _, msg := range result {
		if msg.Role == message.Tool {
			foundToolResult = true
		}
		if foundToolResult && msg.Role == message.User && msg.ID == "3" {
			foundUserAfterRepair = true
		}
	}
	assert.True(t, foundToolResult, "Should have a synthetic tool result")
	assert.True(t, foundUserAfterRepair, "User message should come after synthetic tool result")
}

// TestRepairMessageHistory_DoesNotDropUserMessages verifies that the repair
// function never drops user messages, even in complex repair scenarios.
func TestRepairMessageHistory_DoesNotDropUserMessages(t *testing.T) {
	msgs := []message.Message{
		{ID: "u1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "first"}}},
		{ID: "a1", Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "tc1", Name: "x", Input: `{}`},
		}},
		{ID: "t1", Role: message.Tool, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "tc1", Content: "ok"},
		}},
		{ID: "a2", Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "tc2", Name: "y", Input: `{}`},
		}},
		// tc2 is orphaned
		{ID: "u2", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "interrupt"}}},
		{ID: "a3", Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "response"}}},
		{ID: "u3", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "final"}}},
	}

	result := repairMessageHistory(msgs)

	// Count user messages - should have all 3
	userCount := 0
	for _, msg := range result {
		if msg.Role == message.User {
			userCount++
		}
	}
	assert.Equal(t, 3, userCount,
		"All 3 user messages should be preserved (not dropped by repair)")

	// Last message should be user
	assert.Equal(t, message.User, result[len(result)-1].Role,
		"Last message must be user")
}

// TestRepairMessageHistory_OrphanedToolResultsDroppedButUserPreserved verifies
// that orphaned tool results (whose assistant message was lost) are dropped,
// but this doesn't affect user messages.
func TestRepairMessageHistory_OrphanedToolResultsDroppedButUserPreserved(t *testing.T) {
	msgs := []message.Message{
		// Orphaned tool result - no assistant message with matching tool call
		{ID: "t1", Role: message.Tool, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "missing_tc", Content: "orphan result"},
		}},
		{ID: "u1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hello"}}},
		{ID: "a1", Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "hi"}}},
		{ID: "u2", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "bye"}}},
	}

	result := repairMessageHistory(msgs)

	// The orphaned tool result should be dropped
	// But user messages should be preserved
	userCount := 0
	for _, msg := range result {
		if msg.Role == message.User {
			userCount++
		}
	}
	assert.Equal(t, 2, userCount, "Both user messages should be preserved")
	assert.Equal(t, message.User, result[len(result)-1].Role,
		"Last message must be user")
}

// TestRepairMessageHistory_ToolCallThenImmediateUserWithoutResult tests the
// exact sequence that causes the API error: the assistant makes a tool call,
// the tool result is missing (maybe workflow was interrupted), and the user
// sends a new message. After repair, the conversation must still end with user.
func TestRepairMessageHistory_ToolCallThenImmediateUserWithoutResult(t *testing.T) {
	// This is the exact scenario from the bug report
	msgs := []message.Message{
		{ID: "u1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "help me"}}},
		{ID: "a1", Role: message.Assistant, Parts: []message.ContentPart{
			message.TextContent{Text: "Sure, let me check."},
			message.ToolCall{ID: "tc_bash", Name: "bash", Input: `{"command":"echo hello"}`},
		}},
		// Workflow was cancelled/interrupted here - no tool result saved
		// User sends a new message (via SendMessage → SaveMessageToThread)
		{ID: "u2", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "actually, do something else"}}},
	}

	result := repairMessageHistory(msgs)

	// Must end with user message
	require.Greater(t, len(result), 0)
	lastMsg := result[len(result)-1]
	assert.Equal(t, message.User, lastMsg.Role,
		"CRITICAL: Last message MUST be user. Got %s. This causes 'must end with user message' API error", lastMsg.Role)
	assert.Equal(t, "u2", lastMsg.ID,
		"The last user message should be the one sent after the interruption")

	// Verify synthetic tool result was inserted between assistant and user
	roles := make([]message.MessageRole, len(result))
	for i, m := range result {
		roles[i] = m.Role
	}
	t.Logf("Repaired message roles: %v", roles)

	// Expected: [user, assistant, tool(synthetic), user]
	require.Len(t, result, 4, "Should have exactly 4 messages")
	assert.Equal(t, message.User, result[0].Role)
	assert.Equal(t, message.Assistant, result[1].Role)
	assert.Equal(t, message.Tool, result[2].Role, "Synthetic tool result should be at position 2")
	assert.Equal(t, message.User, result[3].Role, "User message should be at position 3 (last)")
}
