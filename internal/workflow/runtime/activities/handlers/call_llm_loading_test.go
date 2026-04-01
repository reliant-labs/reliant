// Copyright (c) 2025 Reliant Labs
// Tests for call_llm message loading - ensuring correct messages are loaded
// based on thread, context_sequence, and fork relationships.
package handlers

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// MESSAGE LOADING EDGE CASES
// These tests verify that LoadConversationHistory and related functions
// handle various edge cases correctly.
// ============================================================================

// TestLoadMessages_EmptyThread tests loading messages from an empty thread
func TestLoadMessages_EmptyThread(t *testing.T) {
	msgs := []message.Message{}
	result := repairMessageHistory(msgs)
	require.Len(t, result, 0)
}

// TestLoadMessages_OnlySystemMessages tests loading with only system messages
func TestLoadMessages_OnlySystemMessages(t *testing.T) {
	msgs := []message.Message{
		{ID: "1", Role: message.System, Parts: []message.ContentPart{message.TextContent{Text: "You are a helpful assistant"}}},
	}

	result := repairMessageHistory(msgs)
	require.Len(t, result, 1)
	require.Equal(t, message.System, result[0].Role)
}

// TestLoadMessages_AlternatingRoles tests proper user/assistant alternation
func TestLoadMessages_AlternatingRoles(t *testing.T) {
	msgs := []message.Message{
		{ID: "1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "Hello"}}},
		{ID: "2", Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "Hi there"}}},
		{ID: "3", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "How are you?"}}},
		{ID: "4", Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "I'm doing well"}}},
	}

	result := repairMessageHistory(msgs)
	require.Len(t, result, 4)
	require.Equal(t, message.User, result[0].Role)
	require.Equal(t, message.Assistant, result[1].Role)
	require.Equal(t, message.User, result[2].Role)
	require.Equal(t, message.Assistant, result[3].Role)
}

// TestLoadMessages_MultipleToolCallsPerMessage tests messages with many tool calls
func TestLoadMessages_MultipleToolCallsPerMessage(t *testing.T) {
	msgs := []message.Message{
		{ID: "1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "Do multiple things"}}},
		{ID: "2", Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "tc-1", Name: "view", Input: `{"file": "a.go"}`},
			message.ToolCall{ID: "tc-2", Name: "view", Input: `{"file": "b.go"}`},
			message.ToolCall{ID: "tc-3", Name: "view", Input: `{"file": "c.go"}`},
			message.ToolCall{ID: "tc-4", Name: "view", Input: `{"file": "d.go"}`},
			message.ToolCall{ID: "tc-5", Name: "view", Input: `{"file": "e.go"}`},
		}},
		{ID: "3", Role: message.Tool, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "tc-1", Content: "a contents"},
			message.ToolResult{ToolCallID: "tc-2", Content: "b contents"},
			message.ToolResult{ToolCallID: "tc-3", Content: "c contents"},
			message.ToolResult{ToolCallID: "tc-4", Content: "d contents"},
			message.ToolResult{ToolCallID: "tc-5", Content: "e contents"},
		}},
	}

	result := repairMessageHistory(msgs)
	require.Len(t, result, 3)

	// All 5 tool results should be in the tool message
	toolResults := result[2].ToolResults()
	require.Len(t, toolResults, 5)
}

// TestLoadMessages_LargeToolResults tests handling of large tool results
func TestLoadMessages_LargeToolResults(t *testing.T) {
	// Create a large result string (10KB)
	largeContent := make([]byte, 10*1024)
	for i := range largeContent {
		largeContent[i] = 'x'
	}

	msgs := []message.Message{
		{ID: "1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "Read file"}}},
		{ID: "2", Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "tc-1", Name: "view", Input: `{}`},
		}},
		{ID: "3", Role: message.Tool, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "tc-1", Content: string(largeContent)},
		}},
	}

	result := repairMessageHistory(msgs)
	require.Len(t, result, 3)

	// Result should be preserved
	toolResults := result[2].ToolResults()
	require.Len(t, toolResults, 1)
	require.Len(t, toolResults[0].Content, 10*1024)
}

// TestLoadMessages_ErrorToolResults tests handling of error tool results
func TestLoadMessages_ErrorToolResults(t *testing.T) {
	msgs := []message.Message{
		{ID: "1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "Run command"}}},
		{ID: "2", Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "tc-1", Name: "bash", Input: `{"command": "exit 1"}`},
		}},
		{ID: "3", Role: message.Tool, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "tc-1", Content: "exit status 1", IsError: true},
		}},
	}

	result := repairMessageHistory(msgs)
	require.Len(t, result, 3)

	// Error flag should be preserved
	toolResults := result[2].ToolResults()
	require.Len(t, toolResults, 1)
	require.True(t, toolResults[0].IsError)
}

// TestLoadMessages_MixedTextAndToolCalls tests assistant messages with both text and tools
func TestLoadMessages_MixedTextAndToolCalls(t *testing.T) {
	msgs := []message.Message{
		{ID: "1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "Help me"}}},
		{ID: "2", Role: message.Assistant, Parts: []message.ContentPart{
			message.TextContent{Text: "I'll help you with that. First let me check something."},
			message.ToolCall{ID: "tc-1", Name: "view", Input: `{}`},
		}},
		{ID: "3", Role: message.Tool, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "tc-1", Content: "file contents"},
		}},
		{ID: "4", Role: message.Assistant, Parts: []message.ContentPart{
			message.TextContent{Text: "Based on what I found, here's the solution."},
		}},
	}

	result := repairMessageHistory(msgs)
	require.Len(t, result, 4)

	// First assistant message should have both text and tool call
	require.Len(t, result[1].Parts, 2)
	_, isText := result[1].Parts[0].(message.TextContent)
	require.True(t, isText)
	_, isToolCall := result[1].Parts[1].(message.ToolCall)
	require.True(t, isToolCall)
}

// TestLoadMessages_ReasoningContent tests extended thinking preservation
func TestLoadMessages_ReasoningContent(t *testing.T) {
	msgs := []message.Message{
		{ID: "1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "Think about this"}}},
		{ID: "2", Role: message.Assistant, Parts: []message.ContentPart{
			message.ReasoningContent{Thinking: "Let me analyze this carefully...", Signature: "sig123"},
			message.TextContent{Text: "Here's my answer."},
		}},
	}

	result := repairMessageHistory(msgs)
	require.Len(t, result, 2)

	// Reasoning should be preserved
	require.Len(t, result[1].Parts, 2)
	reasoning, isReasoning := result[1].Parts[0].(message.ReasoningContent)
	require.True(t, isReasoning)
	require.Equal(t, "sig123", reasoning.Signature)
}

// TestLoadMessages_ContextSequenceFiltering tests that only current context is used
func TestLoadMessages_ContextSequenceFiltering(t *testing.T) {
	// This is a conceptual test - in practice, the DB layer handles filtering
	// Here we verify the message structure is maintained correctly

	// Simulate messages from different context sequences being loaded
	// (in practice, the DB would filter these)
	currentContextMsgs := []message.Message{
		{ID: "1", Role: message.System, Parts: []message.ContentPart{message.TextContent{Text: "Compaction summary"}}},
		{ID: "2", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "Continue"}}},
		{ID: "3", Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "OK"}}},
	}

	result := repairMessageHistory(currentContextMsgs)
	require.Len(t, result, 3)
}

// TestLoadMessages_AgentRole tests the 'agent' role handling
func TestLoadMessages_AgentRole(t *testing.T) {
	msgs := []message.Message{
		{ID: "1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "Hello"}}},
		{ID: "2", Role: message.Agent, Parts: []message.ContentPart{message.TextContent{Text: "System notification"}}},
		{ID: "3", Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "Hi!"}}},
	}

	result := repairMessageHistory(msgs)
	require.Len(t, result, 3)
	require.Equal(t, message.Agent, result[1].Role)
}

// TestLoadMessages_FinishContent tests finish markers
func TestLoadMessages_FinishContent(t *testing.T) {
	msgs := []message.Message{
		{ID: "1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "Hello"}}},
		{ID: "2", Role: message.Assistant, Parts: []message.ContentPart{
			message.TextContent{Text: "Hi there!"},
			message.Finish{Reason: "end_turn"},
		}},
	}

	result := repairMessageHistory(msgs)
	require.Len(t, result, 2)

	// Finish marker should be preserved
	require.Len(t, result[1].Parts, 2)
}

// TestLoadMessages_ConsecutiveAssistantMessages tests back-to-back assistant messages
func TestLoadMessages_ConsecutiveAssistantMessages(t *testing.T) {
	// This can happen in some edge cases or with streaming
	msgs := []message.Message{
		{ID: "1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "Hello"}}},
		{ID: "2", Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "First response"}}},
		{ID: "3", Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "Second response"}}},
	}

	result := repairMessageHistory(msgs)
	// Should handle gracefully - no tool calls means no repair needed
	require.Len(t, result, 3)
}

// TestLoadMessages_ConsecutiveUserMessages tests back-to-back user messages
func TestLoadMessages_ConsecutiveUserMessages(t *testing.T) {
	msgs := []message.Message{
		{ID: "1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "First question"}}},
		{ID: "2", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "Follow up"}}},
		{ID: "3", Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "Answer"}}},
	}

	result := repairMessageHistory(msgs)
	require.Len(t, result, 3)
}

// TestLoadMessages_ImageContent tests image handling
func TestLoadMessages_ImageContent(t *testing.T) {
	msgs := []message.Message{
		{ID: "1", Role: message.User, Parts: []message.ContentPart{
			message.TextContent{Text: "What's in this image?"},
			message.BinaryContent{MIMEType: "image/png", Data: []byte{0x89, 0x50, 0x4E, 0x47}},
		}},
		{ID: "2", Role: message.Assistant, Parts: []message.ContentPart{
			message.TextContent{Text: "I see a picture of..."},
		}},
	}

	result := repairMessageHistory(msgs)
	require.Len(t, result, 2)

	// Image should be preserved
	require.Len(t, result[0].Parts, 2)
	_, isBinary := result[0].Parts[1].(message.BinaryContent)
	require.True(t, isBinary)
}

// ============================================================================
// TABLE-DRIVEN TESTS FOR MESSAGE ROLE SEQUENCES
// ============================================================================

func TestLoadMessages_RoleSequences(t *testing.T) {
	testCases := []struct {
		name     string
		roles    []message.MessageRole
		hasTools []bool // Whether each assistant message has tool calls
		expected int    // Expected final message count
	}{
		{
			name:     "simple_conversation",
			roles:    []message.MessageRole{message.User, message.Assistant, message.User, message.Assistant},
			hasTools: []bool{false, false},
			expected: 4,
		},
		{
			name:     "tool_usage_complete",
			roles:    []message.MessageRole{message.User, message.Assistant, message.Tool, message.Assistant},
			hasTools: []bool{true, false},
			expected: 4,
		},
		{
			name:     "tool_usage_orphaned",
			roles:    []message.MessageRole{message.User, message.Assistant},
			hasTools: []bool{true},
			expected: 3, // +1 for synthetic tool result
		},
		{
			name:     "multiple_tool_rounds",
			roles:    []message.MessageRole{message.User, message.Assistant, message.Tool, message.Assistant, message.Tool},
			hasTools: []bool{true, true},
			expected: 5,
		},
		{
			name:     "system_prefix",
			roles:    []message.MessageRole{message.System, message.User, message.Assistant},
			hasTools: []bool{false},
			expected: 3,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			msgs := make([]message.Message, 0, len(tc.roles))
			toolIdx := 0

			for i, role := range tc.roles {
				msg := message.Message{
					ID:   string(rune('a' + i)),
					Role: role,
				}

				switch role {
				case message.User:
					msg.Parts = []message.ContentPart{message.TextContent{Text: "user message"}}
				case message.Assistant:
					if toolIdx < len(tc.hasTools) && tc.hasTools[toolIdx] {
						msg.Parts = []message.ContentPart{
							message.ToolCall{ID: "tc-" + string(rune('0'+toolIdx)), Name: "test", Input: "{}"},
						}
					} else {
						msg.Parts = []message.ContentPart{message.TextContent{Text: "assistant message"}}
					}
					toolIdx++
				case message.Tool:
					// Find the previous tool call
					prevToolIdx := toolIdx - 1
					if prevToolIdx >= 0 {
						msg.Parts = []message.ContentPart{
							message.ToolResult{ToolCallID: "tc-" + string(rune('0'+prevToolIdx)), Content: "result"},
						}
					}
				case message.System:
					msg.Parts = []message.ContentPart{message.TextContent{Text: "system message"}}
				}

				msgs = append(msgs, msg)
			}

			result := repairMessageHistory(msgs)
			require.Equal(t, tc.expected, len(result), "message count mismatch for %s", tc.name)
		})
	}
}

// ============================================================================
// EDGE CASES FOR INHERITED MESSAGES (from forked chats)
// ============================================================================

// TestLoadMessages_InheritedWithOrphanedToolCalls tests repair of inherited orphans
func TestLoadMessages_InheritedWithOrphanedToolCalls(t *testing.T) {
	// Simulates messages inherited from parent chat where parent had orphaned tool call
	msgs := []message.Message{
		// Inherited from parent
		{ID: "parent-1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "do something"}}},
		{ID: "parent-2", Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "parent-tc", Name: "bash", Input: `{}`},
		}},
		// Branch point - no tool result for parent-tc
		// Local messages
		{ID: "child-1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "continue"}}},
		{ID: "child-2", Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "OK"}}},
	}

	result := repairMessageHistory(msgs)

	// Should have synthetic result injected
	require.Len(t, result, 5)

	// Synthetic result should be after parent-2
	require.Equal(t, "parent-1", result[0].ID)
	require.Equal(t, "parent-2", result[1].ID)
	require.Equal(t, message.Tool, result[2].Role) // Synthetic
	require.Equal(t, "child-1", result[3].ID)
	require.Equal(t, "child-2", result[4].ID)
}

// TestLoadMessages_InheritedWithExistingRepair tests that already-repaired messages aren't double-repaired
func TestLoadMessages_InheritedWithExistingRepair(t *testing.T) {
	// Parent already had repair message
	msgs := []message.Message{
		{ID: "parent-1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "do"}}},
		{ID: "parent-2", Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "tc-1", Name: "bash", Input: `{}`},
		}},
		{ID: "parent-repair", Role: message.Tool, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "tc-1", Content: "cancelled", IsError: true},
		}},
		{ID: "child-1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "ok"}}},
	}

	result := repairMessageHistory(msgs)

	// Should not add another repair
	require.Len(t, result, 4)

	// Verify no duplicate repairs
	toolMsgCount := 0
	for _, msg := range result {
		if msg.Role == message.Tool {
			toolMsgCount++
		}
	}
	require.Equal(t, 1, toolMsgCount)
}
