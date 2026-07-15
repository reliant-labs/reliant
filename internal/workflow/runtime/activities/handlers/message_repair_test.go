// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/require"
)

// TestRepairMessageHistory_NoOrphanedToolCalls verifies that messages
// without orphaned tool_calls are returned unchanged (results consolidated)
func TestRepairMessageHistory_NoOrphanedToolCalls(t *testing.T) {
	msgs := []message.Message{
		{
			ID:   "msg-1",
			Role: message.User,
			Parts: []message.ContentPart{
				message.TextContent{Text: "Hello"},
			},
		},
		{
			ID:   "msg-2",
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.TextContent{Text: "Hi there!"},
				message.ToolCall{ID: "tc-1", Name: "bash", Input: `{"command": "ls"}`},
			},
		},
		{
			ID:   "msg-3",
			Role: message.Tool,
			Parts: []message.ContentPart{
				message.ToolResult{ToolCallID: "tc-1", Content: "file1.txt\nfile2.txt"},
			},
		},
	}

	result := repairMessageHistory(msgs)

	// Should have 3 messages - the tool result is consolidated right after assistant
	require.Len(t, result, 3)
	require.Equal(t, "msg-1", result[0].ID)
	require.Equal(t, "msg-2", result[1].ID)
	// The synthetic message contains the existing result
	require.Equal(t, "synthetic-repair-1", result[2].ID)
	require.Equal(t, message.Tool, result[2].Role)
	toolResults := result[2].ToolResults()
	require.Len(t, toolResults, 1)
	require.Equal(t, "tc-1", toolResults[0].ToolCallID)
}

// TestRepairMessageHistory_WithOrphanedToolCalls verifies that orphaned
// tool_calls get synthetic tool_results injected
func TestRepairMessageHistory_WithOrphanedToolCalls(t *testing.T) {
	msgs := []message.Message{
		{
			ID:   "msg-1",
			Role: message.User,
			Parts: []message.ContentPart{
				message.TextContent{Text: "Hello"},
			},
		},
		{
			ID:   "msg-2",
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.TextContent{Text: "Let me check that..."},
				message.ToolCall{ID: "tc-1", Name: "bash", Input: `{"command": "ls"}`},
				message.ToolCall{ID: "tc-2", Name: "edit", Input: `{"file": "test.txt"}`},
			},
		},
		// No tool results - workflow was cancelled!
	}

	result := repairMessageHistory(msgs)

	// Should have original messages + synthetic tool message
	require.Len(t, result, 3)
	require.Equal(t, "msg-1", result[0].ID)
	require.Equal(t, "msg-2", result[1].ID)

	// Check synthetic message
	syntheticMsg := result[2]
	require.Equal(t, "synthetic-repair-1", syntheticMsg.ID)
	require.Equal(t, message.Tool, syntheticMsg.Role)

	// Should have 2 tool results
	toolResults := syntheticMsg.ToolResults()
	require.Len(t, toolResults, 2)

	// Verify tool results match orphaned tool calls
	resultIDs := make(map[string]bool)
	for _, tr := range toolResults {
		resultIDs[tr.ToolCallID] = true
		require.True(t, tr.IsError)
		require.Contains(t, tr.Content, "interrupted")
	}
	require.True(t, resultIDs["tc-1"])
	require.True(t, resultIDs["tc-2"])
}

// TestRepairMessageHistory_PartialResults verifies that only missing
// tool_results are added (some tools may have completed)
func TestRepairMessageHistory_PartialResults(t *testing.T) {
	msgs := []message.Message{
		{
			ID:   "msg-1",
			Role: message.User,
			Parts: []message.ContentPart{
				message.TextContent{Text: "Hello"},
			},
		},
		{
			ID:   "msg-2",
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.ToolCall{ID: "tc-1", Name: "bash", Input: `{"command": "ls"}`},
				message.ToolCall{ID: "tc-2", Name: "edit", Input: `{"file": "test.txt"}`},
			},
		},
		{
			ID:   "msg-3",
			Role: message.Tool,
			Parts: []message.ContentPart{
				// Only tc-1 completed, tc-2 was cancelled
				message.ToolResult{ToolCallID: "tc-1", Content: "file1.txt"},
			},
		},
	}

	result := repairMessageHistory(msgs)

	// Should have 3 messages - user, assistant, consolidated tool results
	require.Len(t, result, 3)
	require.Equal(t, "msg-1", result[0].ID)
	require.Equal(t, "msg-2", result[1].ID)

	// Consolidated tool message has BOTH results
	syntheticMsg := result[2]
	require.Equal(t, "synthetic-repair-1", syntheticMsg.ID)
	require.Equal(t, message.Tool, syntheticMsg.Role)

	// Should have 2 tool results - one existing, one synthetic
	toolResults := syntheticMsg.ToolResults()
	require.Len(t, toolResults, 2)

	// Find each result
	var tc1Result, tc2Result *message.ToolResult
	for i := range toolResults {
		switch toolResults[i].ToolCallID {
		case "tc-1":
			tc1Result = &toolResults[i]
		case "tc-2":
			tc2Result = &toolResults[i]
		}
	}

	require.NotNil(t, tc1Result)
	require.Equal(t, "file1.txt", tc1Result.Content) // Existing result
	require.False(t, tc1Result.IsError)

	require.NotNil(t, tc2Result)
	require.True(t, tc2Result.IsError) // Synthetic error
	require.Contains(t, tc2Result.Content, "interrupted")
}

// TestRepairMessageHistory_MultipleAssistantMessages verifies repair
// works correctly with multiple assistant messages with tool calls
func TestRepairMessageHistory_MultipleAssistantMessages(t *testing.T) {
	msgs := []message.Message{
		{
			ID:   "msg-1",
			Role: message.User,
			Parts: []message.ContentPart{
				message.TextContent{Text: "Hello"},
			},
		},
		{
			ID:   "msg-2",
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.ToolCall{ID: "tc-1", Name: "bash", Input: `{}`},
			},
		},
		{
			ID:   "msg-3",
			Role: message.Tool,
			Parts: []message.ContentPart{
				message.ToolResult{ToolCallID: "tc-1", Content: "done"},
			},
		},
		{
			ID:   "msg-4",
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.ToolCall{ID: "tc-2", Name: "edit", Input: `{}`},
			},
		},
		// tc-2 has no result - cancelled!
	}

	result := repairMessageHistory(msgs)

	// Should have 5 messages (user, assistant, tool, assistant, synthetic-tool)
	require.Len(t, result, 5)
	require.Equal(t, "msg-1", result[0].ID)
	require.Equal(t, "msg-2", result[1].ID)
	require.Equal(t, "synthetic-repair-1", result[2].ID) // tc-1 result consolidated here
	require.Equal(t, "msg-4", result[3].ID)
	require.Equal(t, "synthetic-repair-3", result[4].ID) // tc-2 synthetic

	// Check tc-2 synthetic result
	toolResults := result[4].ToolResults()
	require.Len(t, toolResults, 1)
	require.Equal(t, "tc-2", toolResults[0].ToolCallID)
	require.True(t, toolResults[0].IsError)
}

// TestRepairMessageHistory_UserInterruptMidToolExecution verifies that when a user
// sends a new message while a tool is executing (interrupting), the orphaned tool call
// from the previous assistant message gets a synthetic result IMMEDIATELY after that
// assistant message, not at the end of the conversation.
// This is the scenario that caused the 400 Bad Request error from Anthropic API.
func TestRepairMessageHistory_UserInterruptMidToolExecution(t *testing.T) {
	// This simulates:
	// 1. User says "continue"
	// 2. Assistant calls tool tc-1 (bash)
	// 3. User interrupts with "don't run all the tests"
	// 4. Assistant calls new tool tc-2
	// 5. Tool tc-2 completes
	// Result: tc-1 has no tool result, but the conversation continued
	msgs := []message.Message{
		{
			ID:   "msg-user-1",
			Role: message.User,
			Parts: []message.ContentPart{
				message.TextContent{Text: "continue"},
			},
		},
		{
			ID:   "msg-assistant-1",
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.ToolCall{ID: "tc-1", Name: "bash", Input: `{"command": "go test ./..."}`},
			},
		},
		// User interrupts before tc-1 completes!
		{
			ID:   "msg-user-2",
			Role: message.User,
			Parts: []message.ContentPart{
				message.TextContent{Text: "don't run all the tests.. that takes too long"},
			},
		},
		{
			ID:   "msg-assistant-2",
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.TextContent{Text: "Good call. Let me just run the specific workflow tests:"},
				message.ToolCall{ID: "tc-2", Name: "bash", Input: `{"command": "go test ./internal/workflow/..."}`},
			},
		},
		{
			ID:   "msg-tool-2",
			Role: message.Tool,
			Parts: []message.ContentPart{
				message.ToolResult{ToolCallID: "tc-2", Content: "ok\tgithub.com/reliant-labs/reliant/internal/workflow\t0.321s"},
			},
		},
	}

	result := repairMessageHistory(msgs)

	// Should have 6 messages:
	// user-1, assistant-1, SYNTHETIC-tc-1, user-2, assistant-2, SYNTHETIC-tc-2
	require.Len(t, result, 6)

	// Verify order
	require.Equal(t, "msg-user-1", result[0].ID)
	require.Equal(t, "msg-assistant-1", result[1].ID)

	// CRITICAL: Synthetic tool result for tc-1 must come immediately after msg-assistant-1
	syntheticTC1 := result[2]
	require.Equal(t, "synthetic-repair-1", syntheticTC1.ID)
	require.Equal(t, message.Tool, syntheticTC1.Role)
	toolResults := syntheticTC1.ToolResults()
	require.Len(t, toolResults, 1)
	require.Equal(t, "tc-1", toolResults[0].ToolCallID)
	require.True(t, toolResults[0].IsError)

	// Rest of the conversation follows
	require.Equal(t, "msg-user-2", result[3].ID)
	require.Equal(t, "msg-assistant-2", result[4].ID)

	// tc-2 result (existing, consolidated)
	syntheticTC2 := result[5]
	require.Equal(t, "synthetic-repair-3", syntheticTC2.ID)
	require.Equal(t, message.Tool, syntheticTC2.Role)
	toolResults2 := syntheticTC2.ToolResults()
	require.Len(t, toolResults2, 1)
	require.Equal(t, "tc-2", toolResults2[0].ToolCallID)
	require.False(t, toolResults2[0].IsError) // This one completed successfully
}

// TestRepairMessageHistory_BranchedChatScenario tests the specific branched chat case
// where parent messages include an assistant with tool_call but no tool_result,
// and the branched chat has additional messages after.
func TestRepairMessageHistory_BranchedChatScenario(t *testing.T) {
	// Simulates branched chat scenario:
	// Parent: user(0) -> assistant(1) with tool_call
	// Branch at ordinal 1
	// Branched: inherits parent messages, then user(2) "hello"
	// The repair message from DB is at ordinal 3 (at the end), but we need
	// the tool_result immediately after the assistant message.
	msgs := []message.Message{
		{
			ID:   "parent-user",
			Role: message.User,
			Parts: []message.ContentPart{
				message.TextContent{Text: "run sleep 1"},
			},
		},
		{
			ID:   "parent-assistant",
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.ToolCall{ID: "toolu_parent", Name: "bash", Input: `{"command":"sleep 1"}`},
			},
		},
		// Branch point - user sends new message before tool completes
		{
			ID:   "branch-user",
			Role: message.User,
			Parts: []message.ContentPart{
				message.TextContent{Text: "hello"},
			},
		},
		// This is the repair message from DB - at the END, wrong position
		{
			ID:   "repair-message",
			Role: message.Tool,
			Parts: []message.ContentPart{
				message.ToolResult{
					ToolCallID: "toolu_parent",
					Content:    "Tool execution was cancelled before completion.",
					IsError:    true,
				},
			},
		},
	}

	result := repairMessageHistory(msgs)

	// Should reorder to: user, assistant, tool_result, user
	require.Len(t, result, 4)
	require.Equal(t, "parent-user", result[0].ID)
	require.Equal(t, "parent-assistant", result[1].ID)

	// Tool result must be immediately after assistant
	require.Equal(t, message.Tool, result[2].Role)
	toolResults := result[2].ToolResults()
	require.Len(t, toolResults, 1)
	require.Equal(t, "toolu_parent", toolResults[0].ToolCallID)

	// Then the branch user message
	require.Equal(t, "branch-user", result[3].ID)
}

// TestRepairMessageHistory_OrphanedToolResultNoAssistantMessage verifies that
// tool_results whose corresponding assistant message is completely missing
// (e.g., lost via compaction, fork, or CW chain inheritance) are dropped.
// This is the root cause of the Anthropic 400 error:
// "unexpected tool_use_id found in tool_result blocks"
func TestRepairMessageHistory_OrphanedToolResultNoAssistantMessage(t *testing.T) {
	msgs := []message.Message{
		{
			ID:   "msg-user-1",
			Role: message.User,
			Parts: []message.ContentPart{
				message.TextContent{Text: "Hello"},
			},
		},
		{
			ID:   "msg-assistant-1",
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.TextContent{Text: "Sure, let me help."},
			},
		},
		// This tool result references a tool_call from an assistant message
		// that was lost (compaction, fork, etc.). No assistant message has
		// a tool_call with ID "toolu_ORPHANED".
		{
			ID:   "msg-orphaned-tool",
			Role: message.Tool,
			Parts: []message.ContentPart{
				message.ToolResult{ToolCallID: "toolu_ORPHANED", Content: "some result"},
			},
		},
	}

	result := repairMessageHistory(msgs)

	// The orphaned tool result should be dropped entirely
	require.Len(t, result, 2)
	require.Equal(t, "msg-user-1", result[0].ID)
	require.Equal(t, "msg-assistant-1", result[1].ID)
}

// TestRepairMessageHistory_MixedOrphanedAndValidToolResults verifies that
// when a tool message contains both orphaned and valid results, only the
// orphaned ones are dropped.
func TestRepairMessageHistory_MixedOrphanedAndValidToolResults(t *testing.T) {
	msgs := []message.Message{
		{
			ID:   "msg-user-1",
			Role: message.User,
			Parts: []message.ContentPart{
				message.TextContent{Text: "Hello"},
			},
		},
		{
			ID:   "msg-assistant-1",
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.ToolCall{ID: "tc-valid", Name: "bash", Input: `{}`},
			},
		},
		{
			ID:   "msg-tool-1",
			Role: message.Tool,
			Parts: []message.ContentPart{
				message.ToolResult{ToolCallID: "tc-valid", Content: "ok"},
				message.ToolResult{ToolCallID: "tc-orphaned", Content: "ghost result"},
			},
		},
	}

	result := repairMessageHistory(msgs)

	// Should have 3 messages: user, assistant, tool (with only the valid result)
	require.Len(t, result, 3)
	require.Equal(t, "msg-user-1", result[0].ID)
	require.Equal(t, "msg-assistant-1", result[1].ID)

	// The tool message should only have the valid result, consolidated after assistant
	toolMsg := result[2]
	require.Equal(t, message.Tool, toolMsg.Role)
	toolResults := toolMsg.ToolResults()
	require.Len(t, toolResults, 1)
	require.Equal(t, "tc-valid", toolResults[0].ToolCallID)
}

// TestRepairMessageHistory_EmptyMessages verifies empty message list is handled
func TestRepairMessageHistory_EmptyMessages(t *testing.T) {
	msgs := []message.Message{}
	result := repairMessageHistory(msgs)
	require.Len(t, result, 0)
}

// TestRepairMessageHistory_NoToolCalls verifies messages without tool calls are unchanged
func TestRepairMessageHistory_NoToolCalls(t *testing.T) {
	msgs := []message.Message{
		{
			ID:   "msg-1",
			Role: message.User,
			Parts: []message.ContentPart{
				message.TextContent{Text: "Hello"},
			},
		},
		{
			ID:   "msg-2",
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.TextContent{Text: "Hi there!"},
			},
		},
	}

	result := repairMessageHistory(msgs)
	require.Len(t, result, 2)
	require.Equal(t, "msg-1", result[0].ID)
	require.Equal(t, "msg-2", result[1].ID)
}

// TestRepairMessageHistory_ResumeAfterKilledRun pins the resume-at-position
// contract: a run killed mid-tool-execution leaves a dangling tool_use at the
// TAIL of the thread. Before the resumed run's first LLM call, the repair pass
// must synthesize stub tool_results that tell the model the outcome is UNKNOWN
// (not merely "cancelled") so it verifies side effects before re-running.
func TestRepairMessageHistory_ResumeAfterKilledRun(t *testing.T) {
	msgs := []message.Message{
		{
			ID:   "msg-1",
			Role: message.User,
			Parts: []message.ContentPart{
				message.TextContent{Text: "Deploy the service"},
			},
		},
		{
			ID:   "msg-2",
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.TextContent{Text: "Deploying now."},
				message.ToolCall{ID: "tc-deploy", Name: "bash", Input: `{"command": "deploy.sh"}`},
			},
		},
		// Run terminated here: no tool_result was ever recorded.
		{
			ID:   "msg-3",
			Role: message.User,
			Parts: []message.ContentPart{
				message.TextContent{Text: "continue"},
			},
		},
	}

	result := repairMessageHistory(msgs)

	// The stub must be inserted immediately after the assistant tool_use.
	require.Len(t, result, 4)
	stub := result[2]
	require.Equal(t, message.Tool, stub.Role)
	toolResults := stub.ToolResults()
	require.Len(t, toolResults, 1)
	require.Equal(t, "tc-deploy", toolResults[0].ToolCallID)
	require.True(t, toolResults[0].IsError)
	require.Equal(t, InterruptedToolResultContent, toolResults[0].Content)
	// Wording contract: interrupted + outcome unknown + verify before re-run.
	require.Contains(t, toolResults[0].Content, "interrupted")
	require.Contains(t, toolResults[0].Content, "outcome unknown")
	require.Contains(t, toolResults[0].Content, "verify")

	// The user's resume message stays the final turn.
	require.Equal(t, "msg-3", result[3].ID)
}
