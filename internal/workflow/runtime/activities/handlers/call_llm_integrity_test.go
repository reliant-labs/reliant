// Copyright (c) 2025 Reliant Labs
// Tests to ensure call_llm never sends malformed messages to the LLM API.
// Specifically verifies that every tool_call has a matching tool_result.
package handlers

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// TOOL CALL / TOOL RESULT INTEGRITY TESTS
// These tests verify that repairMessageHistory ensures every tool_call
// has a matching tool_result, which is required by the Anthropic API.
// ============================================================================

// validateMessageIntegrity checks that every tool_call has a matching tool_result
// immediately following the assistant message. Returns an error if invalid.
func validateMessageIntegrity(t *testing.T, msgs []message.Message) {
	t.Helper()

	for i, msg := range msgs {
		if msg.Role != message.Assistant {
			continue
		}

		toolCalls := msg.ToolCalls()
		if len(toolCalls) == 0 {
			continue
		}

		// There MUST be a tool message following this assistant message
		require.Less(t, i+1, len(msgs),
			"assistant message with tool_calls at index %d must be followed by a tool message", i)

		nextMsg := msgs[i+1]
		require.Equal(t, message.Tool, nextMsg.Role,
			"message following assistant with tool_calls must be tool role, got %s at index %d", nextMsg.Role, i+1)

		// Collect tool result IDs
		toolResults := nextMsg.ToolResults()
		resultIDs := make(map[string]bool)
		for _, tr := range toolResults {
			resultIDs[tr.ToolCallID] = true
		}

		// Every tool_call must have a matching tool_result
		for _, tc := range toolCalls {
			require.True(t, resultIDs[tc.ID],
				"tool_call %s (%s) at message index %d has no matching tool_result", tc.ID, tc.Name, i)
		}
	}
}

// TestRepairIntegrity_SingleOrphanedToolCall verifies single orphaned tool call gets repaired
func TestRepairIntegrity_SingleOrphanedToolCall(t *testing.T) {
	msgs := []message.Message{
		{ID: "1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "do something"}}},
		{ID: "2", Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "tc-1", Name: "bash", Input: `{"command": "ls"}`},
		}},
		// No tool result - orphaned!
	}

	result := repairMessageHistory(msgs)
	validateMessageIntegrity(t, result)
}

// TestRepairIntegrity_MultipleOrphanedToolCalls verifies multiple orphaned tool calls in one message
func TestRepairIntegrity_MultipleOrphanedToolCalls(t *testing.T) {
	msgs := []message.Message{
		{ID: "1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "do multiple things"}}},
		{ID: "2", Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "tc-1", Name: "bash", Input: `{}`},
			message.ToolCall{ID: "tc-2", Name: "view", Input: `{}`},
			message.ToolCall{ID: "tc-3", Name: "edit", Input: `{}`},
		}},
		// No tool results - all orphaned!
	}

	result := repairMessageHistory(msgs)
	validateMessageIntegrity(t, result)

	// Verify we have exactly 3 tool results
	require.Len(t, result, 3)
	toolResults := result[2].ToolResults()
	require.Len(t, toolResults, 3)
}

// TestRepairIntegrity_PartialToolResults verifies partial results get completed
func TestRepairIntegrity_PartialToolResults(t *testing.T) {
	msgs := []message.Message{
		{ID: "1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "do things"}}},
		{ID: "2", Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "tc-1", Name: "bash", Input: `{}`},
			message.ToolCall{ID: "tc-2", Name: "view", Input: `{}`},
			message.ToolCall{ID: "tc-3", Name: "edit", Input: `{}`},
		}},
		{ID: "3", Role: message.Tool, Parts: []message.ContentPart{
			// Only tc-1 completed
			message.ToolResult{ToolCallID: "tc-1", Content: "done"},
		}},
	}

	result := repairMessageHistory(msgs)
	validateMessageIntegrity(t, result)

	// Should have all 3 tool results
	toolResults := result[2].ToolResults()
	require.Len(t, toolResults, 3)

	// tc-1 should not be an error, tc-2 and tc-3 should be errors
	resultMap := make(map[string]message.ToolResult)
	for _, tr := range toolResults {
		resultMap[tr.ToolCallID] = tr
	}

	require.False(t, resultMap["tc-1"].IsError, "tc-1 completed successfully")
	require.True(t, resultMap["tc-2"].IsError, "tc-2 was cancelled")
	require.True(t, resultMap["tc-3"].IsError, "tc-3 was cancelled")
}

// TestRepairIntegrity_MultipleAssistantMessagesWithToolCalls tests back-to-back assistant+tool sequences
func TestRepairIntegrity_MultipleAssistantMessagesWithToolCalls(t *testing.T) {
	msgs := []message.Message{
		{ID: "1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "start"}}},
		{ID: "2", Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "tc-1", Name: "bash", Input: `{}`},
		}},
		{ID: "3", Role: message.Tool, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "tc-1", Content: "done"},
		}},
		{ID: "4", Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "tc-2", Name: "view", Input: `{}`},
		}},
		// tc-2 has no result - orphaned!
	}

	result := repairMessageHistory(msgs)
	validateMessageIntegrity(t, result)
}

// TestRepairIntegrity_UserInterruptMidExecution tests the critical user interrupt scenario
func TestRepairIntegrity_UserInterruptMidExecution(t *testing.T) {
	msgs := []message.Message{
		{ID: "1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "run tests"}}},
		{ID: "2", Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "tc-1", Name: "bash", Input: `{"command": "go test ./..."}`},
		}},
		// User interrupts before tc-1 completes
		{ID: "3", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "stop! don't run all tests"}}},
		{ID: "4", Role: message.Assistant, Parts: []message.ContentPart{
			message.TextContent{Text: "OK, I'll just run a subset"},
			message.ToolCall{ID: "tc-2", Name: "bash", Input: `{"command": "go test ./internal/..."}`},
		}},
		{ID: "5", Role: message.Tool, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "tc-2", Content: "PASS"},
		}},
	}

	result := repairMessageHistory(msgs)
	validateMessageIntegrity(t, result)

	// Verify correct order: user, assistant(tc-1), tool(tc-1-cancelled), user, assistant(tc-2), tool(tc-2)
	require.Len(t, result, 6)
	require.Equal(t, message.User, result[0].Role)
	require.Equal(t, message.Assistant, result[1].Role)
	require.Equal(t, message.Tool, result[2].Role) // Synthetic for tc-1
	require.Equal(t, message.User, result[3].Role) // User interrupt
	require.Equal(t, message.Assistant, result[4].Role)
	require.Equal(t, message.Tool, result[5].Role) // tc-2 result
}

// TestRepairIntegrity_NestedToolCallSequences tests complex multi-round tool usage
func TestRepairIntegrity_NestedToolCallSequences(t *testing.T) {
	msgs := []message.Message{
		{ID: "1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "help me fix the bug"}}},
		// First round: read files
		{ID: "2", Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "tc-1", Name: "view", Input: `{"file": "main.go"}`},
			message.ToolCall{ID: "tc-2", Name: "view", Input: `{"file": "test.go"}`},
		}},
		{ID: "3", Role: message.Tool, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "tc-1", Content: "package main..."},
			message.ToolResult{ToolCallID: "tc-2", Content: "func TestMain..."},
		}},
		// Second round: search
		{ID: "4", Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "tc-3", Name: "grep", Input: `{"pattern": "error"}`},
		}},
		{ID: "5", Role: message.Tool, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "tc-3", Content: "main.go:10: error handling"},
		}},
		// Third round: edit (cancelled mid-way)
		{ID: "6", Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "tc-4", Name: "edit", Input: `{"file": "main.go"}`},
		}},
		// Workflow cancelled - no result for tc-4
	}

	result := repairMessageHistory(msgs)
	validateMessageIntegrity(t, result)

	// tc-4 should have synthetic result
	require.Len(t, result, 7)
	lastToolMsg := result[6]
	require.Equal(t, message.Tool, lastToolMsg.Role)
	toolResults := lastToolMsg.ToolResults()
	require.Len(t, toolResults, 1)
	require.Equal(t, "tc-4", toolResults[0].ToolCallID)
	require.True(t, toolResults[0].IsError)
}

// TestRepairIntegrity_EmptyConversation tests empty message list
func TestRepairIntegrity_EmptyConversation(t *testing.T) {
	msgs := []message.Message{}
	result := repairMessageHistory(msgs)
	validateMessageIntegrity(t, result)
	require.Len(t, result, 0)
}

// TestRepairIntegrity_NoToolCalls tests conversation without tool calls
func TestRepairIntegrity_NoToolCalls(t *testing.T) {
	msgs := []message.Message{
		{ID: "1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hello"}}},
		{ID: "2", Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "hi there!"}}},
		{ID: "3", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "how are you?"}}},
		{ID: "4", Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "I'm doing well"}}},
	}

	result := repairMessageHistory(msgs)
	validateMessageIntegrity(t, result)
	require.Len(t, result, 4)
}

// TestRepairIntegrity_SystemMessages tests that system messages don't interfere
func TestRepairIntegrity_SystemMessages(t *testing.T) {
	msgs := []message.Message{
		{ID: "1", Role: message.System, Parts: []message.ContentPart{message.TextContent{Text: "You are a helpful assistant"}}},
		{ID: "2", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "help"}}},
		{ID: "3", Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "tc-1", Name: "bash", Input: `{}`},
		}},
		// No tool result
	}

	result := repairMessageHistory(msgs)
	validateMessageIntegrity(t, result)

	// System message should remain, tool call should be repaired
	require.Len(t, result, 4)
	require.Equal(t, message.System, result[0].Role)
}

// TestRepairIntegrity_BranchedChatInheritedOrphans tests orphans from inherited parent messages
func TestRepairIntegrity_BranchedChatInheritedOrphans(t *testing.T) {
	// Simulates: Parent has assistant+tool_call, then user branches before tool completes
	msgs := []message.Message{
		// Inherited from parent
		{ID: "parent-1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "run command"}}},
		{ID: "parent-2", Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "parent-tc", Name: "bash", Input: `{"command": "sleep 10"}`},
		}},
		// Branch point - new messages in child
		{ID: "child-1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "actually, do something else"}}},
		{ID: "child-2", Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "OK, I'll do that instead"}}},
	}

	result := repairMessageHistory(msgs)
	validateMessageIntegrity(t, result)

	// Should inject synthetic result for parent-tc between parent-2 and child-1
	require.Len(t, result, 5)
	require.Equal(t, "parent-1", result[0].ID)
	require.Equal(t, "parent-2", result[1].ID)
	require.Equal(t, message.Tool, result[2].Role) // Synthetic
	require.Equal(t, "child-1", result[3].ID)
	require.Equal(t, "child-2", result[4].ID)
}

// TestRepairIntegrity_MismatchedToolResultOrder tests tool results in wrong order
func TestRepairIntegrity_MismatchedToolResultOrder(t *testing.T) {
	msgs := []message.Message{
		{ID: "1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "do things"}}},
		{ID: "2", Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "tc-1", Name: "bash", Input: `{}`},
			message.ToolCall{ID: "tc-2", Name: "view", Input: `{}`},
		}},
		{ID: "3", Role: message.Tool, Parts: []message.ContentPart{
			// Results in reverse order (tc-2 first, then tc-1)
			message.ToolResult{ToolCallID: "tc-2", Content: "view result"},
			message.ToolResult{ToolCallID: "tc-1", Content: "bash result"},
		}},
	}

	result := repairMessageHistory(msgs)
	validateMessageIntegrity(t, result)

	// Both results should be present regardless of order
	require.Len(t, result, 3)
	toolResults := result[2].ToolResults()
	require.Len(t, toolResults, 2)
}

// TestRepairIntegrity_DuplicateToolResults tests handling of duplicate tool results
func TestRepairIntegrity_DuplicateToolResults(t *testing.T) {
	msgs := []message.Message{
		{ID: "1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "do"}}},
		{ID: "2", Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "tc-1", Name: "bash", Input: `{}`},
		}},
		{ID: "3", Role: message.Tool, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "tc-1", Content: "result 1"},
		}},
		// Duplicate tool result (shouldn't happen, but handle gracefully)
		{ID: "4", Role: message.Tool, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "tc-1", Content: "result 2"},
		}},
	}

	result := repairMessageHistory(msgs)
	validateMessageIntegrity(t, result)
}

// TestRepairIntegrity_AssistantWithTextAndToolCalls tests mixed content
func TestRepairIntegrity_AssistantWithTextAndToolCalls(t *testing.T) {
	msgs := []message.Message{
		{ID: "1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "fix the bug"}}},
		{ID: "2", Role: message.Assistant, Parts: []message.ContentPart{
			message.TextContent{Text: "I'll investigate this issue. Let me first check the logs."},
			message.ToolCall{ID: "tc-1", Name: "bash", Input: `{"command": "tail -f /var/log/app.log"}`},
		}},
		// Cancelled before result
	}

	result := repairMessageHistory(msgs)
	validateMessageIntegrity(t, result)

	// Text content should be preserved, tool call should have synthetic result
	require.Len(t, result, 3)
	assistantParts := result[1].Parts
	require.Len(t, assistantParts, 2) // Text + ToolCall
}

// TestRepairIntegrity_ThinkingContent tests that thinking/reasoning content doesn't interfere
func TestRepairIntegrity_ThinkingContent(t *testing.T) {
	msgs := []message.Message{
		{ID: "1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "solve problem"}}},
		{ID: "2", Role: message.Assistant, Parts: []message.ContentPart{
			message.ReasoningContent{Thinking: "Let me think about this...", Signature: "sig123"},
			message.TextContent{Text: "I'll run a command"},
			message.ToolCall{ID: "tc-1", Name: "bash", Input: `{}`},
		}},
		// No result
	}

	result := repairMessageHistory(msgs)
	validateMessageIntegrity(t, result)

	// Thinking should be preserved
	require.Len(t, result, 3)
	assistantParts := result[1].Parts
	_, hasThinking := assistantParts[0].(message.ReasoningContent)
	require.True(t, hasThinking)
}

// TestRepairIntegrity_LongToolCallChain tests many sequential tool uses
func TestRepairIntegrity_LongToolCallChain(t *testing.T) {
	msgs := []message.Message{
		{ID: "1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "help me refactor"}}},
	}

	// Add 10 rounds of tool calls
	for i := 0; i < 10; i++ {
		assistantID := string(rune('a' + i))
		toolCallID := string(rune('t' + i))

		msgs = append(msgs, message.Message{
			ID:   assistantID,
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.ToolCall{ID: toolCallID, Name: "view", Input: `{}`},
			},
		})

		// Only add results for first 8
		if i < 8 {
			msgs = append(msgs, message.Message{
				ID:   assistantID + "-result",
				Role: message.Tool,
				Parts: []message.ContentPart{
					message.ToolResult{ToolCallID: toolCallID, Content: "done"},
				},
			})
		}
	}

	result := repairMessageHistory(msgs)
	validateMessageIntegrity(t, result)

	// All 10 tool calls should have results
	toolResultCount := 0
	for _, msg := range result {
		if msg.Role == message.Tool {
			toolResultCount++
		}
	}
	require.Equal(t, 10, toolResultCount)
}

// ============================================================================
// TABLE-DRIVEN TESTS FOR COMPREHENSIVE COVERAGE
// ============================================================================

func TestRepairIntegrity_TableDriven(t *testing.T) {
	testCases := []struct {
		name     string
		messages []message.Message
		// Expected counts after repair
		expectedMsgCount       int
		expectedToolMsgCount   int
		expectedSyntheticCount int // Number of synthetic results
	}{
		{
			name: "all_tool_calls_complete",
			messages: []message.Message{
				{ID: "1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "x"}}},
				{ID: "2", Role: message.Assistant, Parts: []message.ContentPart{
					message.ToolCall{ID: "tc-1", Name: "a", Input: "{}"},
					message.ToolCall{ID: "tc-2", Name: "b", Input: "{}"},
				}},
				{ID: "3", Role: message.Tool, Parts: []message.ContentPart{
					message.ToolResult{ToolCallID: "tc-1", Content: "ok"},
					message.ToolResult{ToolCallID: "tc-2", Content: "ok"},
				}},
			},
			expectedMsgCount:       3,
			expectedToolMsgCount:   1,
			expectedSyntheticCount: 0,
		},
		{
			name: "one_missing_of_three",
			messages: []message.Message{
				{ID: "1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "x"}}},
				{ID: "2", Role: message.Assistant, Parts: []message.ContentPart{
					message.ToolCall{ID: "tc-1", Name: "a", Input: "{}"},
					message.ToolCall{ID: "tc-2", Name: "b", Input: "{}"},
					message.ToolCall{ID: "tc-3", Name: "c", Input: "{}"},
				}},
				{ID: "3", Role: message.Tool, Parts: []message.ContentPart{
					message.ToolResult{ToolCallID: "tc-1", Content: "ok"},
					message.ToolResult{ToolCallID: "tc-3", Content: "ok"},
					// tc-2 missing
				}},
			},
			expectedMsgCount:       3,
			expectedToolMsgCount:   1,
			expectedSyntheticCount: 1,
		},
		{
			name: "all_missing",
			messages: []message.Message{
				{ID: "1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "x"}}},
				{ID: "2", Role: message.Assistant, Parts: []message.ContentPart{
					message.ToolCall{ID: "tc-1", Name: "a", Input: "{}"},
				}},
			},
			expectedMsgCount:       3, // user + assistant + synthetic tool
			expectedToolMsgCount:   1,
			expectedSyntheticCount: 1,
		},
		{
			name: "multiple_assistant_rounds_some_orphaned",
			messages: []message.Message{
				{ID: "1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "x"}}},
				{ID: "2", Role: message.Assistant, Parts: []message.ContentPart{
					message.ToolCall{ID: "tc-1", Name: "a", Input: "{}"},
				}},
				{ID: "3", Role: message.Tool, Parts: []message.ContentPart{
					message.ToolResult{ToolCallID: "tc-1", Content: "ok"},
				}},
				{ID: "4", Role: message.Assistant, Parts: []message.ContentPart{
					message.ToolCall{ID: "tc-2", Name: "b", Input: "{}"},
				}},
				// tc-2 missing
				{ID: "5", Role: message.Assistant, Parts: []message.ContentPart{
					message.ToolCall{ID: "tc-3", Name: "c", Input: "{}"},
				}},
				// tc-3 missing
			},
			expectedMsgCount:       7, // 1u + 2a + 2tr(consolidated) + 2a = 7 (but need to account for synthetic)
			expectedToolMsgCount:   3,
			expectedSyntheticCount: 2,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := repairMessageHistory(tc.messages)

			// Always validate integrity
			validateMessageIntegrity(t, result)

			// Count tool messages
			toolMsgCount := 0
			syntheticCount := 0
			for _, msg := range result {
				if msg.Role == message.Tool {
					toolMsgCount++
					// Check if any results are synthetic (is_error with cancelled message)
					for _, tr := range msg.ToolResults() {
						if tr.IsError && (len(tr.Content) == 0 ||
							contains(tr.Content, "cancelled") ||
							contains(tr.Content, "interrupted")) {
							syntheticCount++
						}
					}
				}
			}

			require.Equal(t, tc.expectedMsgCount, len(result), "message count mismatch")
			require.Equal(t, tc.expectedToolMsgCount, toolMsgCount, "tool message count mismatch")
			require.Equal(t, tc.expectedSyntheticCount, syntheticCount, "synthetic result count mismatch")
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
