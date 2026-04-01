// Copyright (c) 2025 Reliant Labs
// Edge case tests for compaction scenarios - testing message loading
// behavior before, during, and after compaction operations.
package handlers

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// COMPACTION MESSAGE SCENARIOS
// These tests verify proper handling of messages around compaction boundaries.
// ============================================================================

// TestCompaction_SummaryMessageFirst tests that compaction summary is first message in new context
func TestCompaction_SummaryMessageFirst(t *testing.T) {
	// After compaction, the first message should be the summary (system role)
	msgs := []message.Message{
		{ID: "summary", Role: message.System, Parts: []message.ContentPart{
			message.TextContent{Text: "This session is being continued from a previous conversation..."},
		}, ContextSequence: 1},
		{ID: "2", Role: message.User, Parts: []message.ContentPart{
			message.TextContent{Text: "Continue"},
		}, ContextSequence: 1},
		{ID: "3", Role: message.Assistant, Parts: []message.ContentPart{
			message.TextContent{Text: "Of course!"},
		}, ContextSequence: 1},
	}

	result := repairMessageHistory(msgs)
	require.Len(t, result, 3)
	require.Equal(t, message.System, result[0].Role)
}

// TestCompaction_ToolCallInProgress tests compaction when tool call was in progress
func TestCompaction_ToolCallInProgress(t *testing.T) {
	// Scenario: Compaction triggered while tool was executing
	// Previous context had: user -> assistant (with tool_call) -> [interrupted]
	// New context starts with summary

	msgs := []message.Message{
		// Summary (new context)
		{ID: "summary", Role: message.System, Parts: []message.ContentPart{
			message.TextContent{Text: "Compaction summary..."},
		}},
		// User continues
		{ID: "2", Role: message.User, Parts: []message.ContentPart{
			message.TextContent{Text: "Let's continue"},
		}},
		// Assistant responds fresh
		{ID: "3", Role: message.Assistant, Parts: []message.ContentPart{
			message.TextContent{Text: "Sure, what would you like?"},
		}},
	}

	result := repairMessageHistory(msgs)
	require.Len(t, result, 3)
	// No tool calls in this context, so no repair needed
}

// TestCompaction_ImmediatelyAfterToolResult tests compaction right after tool completed
func TestCompaction_ImmediatelyAfterToolResult(t *testing.T) {
	// Pre-compaction: user -> assistant (tool_call) -> tool (result)
	// Compaction happens, new context starts

	msgs := []message.Message{
		{ID: "summary", Role: message.System, Parts: []message.ContentPart{
			message.TextContent{Text: "Summary..."},
		}},
		{ID: "2", Role: message.User, Parts: []message.ContentPart{
			message.TextContent{Text: "What did you find?"},
		}},
		{ID: "3", Role: message.Assistant, Parts: []message.ContentPart{
			message.TextContent{Text: "Based on the previous analysis..."},
		}},
	}

	result := repairMessageHistory(msgs)
	require.Len(t, result, 3)
}

// TestCompaction_MultipleRoundsAfter tests continued tool usage after compaction
func TestCompaction_MultipleRoundsAfter(t *testing.T) {
	msgs := []message.Message{
		// Summary
		{ID: "summary", Role: message.System, Parts: []message.ContentPart{
			message.TextContent{Text: "Previous conversation summary..."},
		}},
		// New tool usage cycle
		{ID: "2", Role: message.User, Parts: []message.ContentPart{
			message.TextContent{Text: "Run tests"},
		}},
		{ID: "3", Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "tc-1", Name: "bash", Input: `{"command": "go test"}`},
		}},
		{ID: "4", Role: message.Tool, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "tc-1", Content: "PASS"},
		}},
		// Second tool call
		{ID: "5", Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "tc-2", Name: "view", Input: `{}`},
		}},
		// tc-2 orphaned (workflow cancelled)
	}

	result := repairMessageHistory(msgs)

	// Should repair tc-2
	require.Len(t, result, 6)
	require.Equal(t, message.Tool, result[5].Role) // Synthetic for tc-2
}

// TestCompaction_LongSummary tests handling of very long compaction summaries
func TestCompaction_LongSummary(t *testing.T) {
	// Create a long summary (100KB)
	longSummary := make([]byte, 100*1024)
	for i := range longSummary {
		longSummary[i] = byte('a' + (i % 26))
	}

	msgs := []message.Message{
		{ID: "summary", Role: message.System, Parts: []message.ContentPart{
			message.TextContent{Text: string(longSummary)},
		}},
		{ID: "2", Role: message.User, Parts: []message.ContentPart{
			message.TextContent{Text: "Continue"},
		}},
	}

	result := repairMessageHistory(msgs)
	require.Len(t, result, 2)

	// Summary should be preserved
	textContent, ok := result[0].Parts[0].(message.TextContent)
	require.True(t, ok)
	require.Len(t, textContent.Text, 100*1024)
}

// TestCompaction_StructuredSummary tests summary with analysis/summary tags
func TestCompaction_StructuredSummary(t *testing.T) {
	summaryWithTags := `<analysis>
The user was working on implementing a feature...
</analysis>
<summary>
- User requested feature X
- We implemented files A, B, C
- Tests pass
</summary>`

	msgs := []message.Message{
		{ID: "summary", Role: message.System, Parts: []message.ContentPart{
			message.TextContent{Text: summaryWithTags},
		}},
		{ID: "2", Role: message.User, Parts: []message.ContentPart{
			message.TextContent{Text: "What was I working on?"},
		}},
	}

	result := repairMessageHistory(msgs)
	require.Len(t, result, 2)
}

// ============================================================================
// FORK + COMPACTION COMBINED SCENARIOS
// ============================================================================

// TestForkCompaction_ForkFromPreCompaction tests forking from before compaction happened
func TestForkCompaction_ForkFromPreCompaction(t *testing.T) {
	// Scenario: Parent has messages in context 0, then compacted to context 1
	// Child forked from context 0 (before compaction)

	// Child's inherited messages + local messages
	msgs := []message.Message{
		// Inherited from parent (context 0)
		{ID: "parent-1", Role: message.User, Parts: []message.ContentPart{
			message.TextContent{Text: "Start"},
		}},
		{ID: "parent-2", Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "tc-parent", Name: "view", Input: `{}`},
		}},
		// Fork happened here, before parent compacted
		// Child's local messages
		{ID: "child-1", Role: message.User, Parts: []message.ContentPart{
			message.TextContent{Text: "Branch here"},
		}},
	}

	result := repairMessageHistory(msgs)

	// Should repair parent's orphaned tool call
	require.Len(t, result, 4)
	require.Equal(t, message.Tool, result[2].Role) // Synthetic for tc-parent
}

// TestForkCompaction_ForkFromPostCompaction tests forking after parent compacted
func TestForkCompaction_ForkFromPostCompaction(t *testing.T) {
	// Scenario: Parent compacted, child forks from post-compaction state

	msgs := []message.Message{
		// Parent's summary (inherited from post-compaction)
		{ID: "parent-summary", Role: message.System, Parts: []message.ContentPart{
			message.TextContent{Text: "Summary of previous work..."},
		}},
		{ID: "parent-2", Role: message.User, Parts: []message.ContentPart{
			message.TextContent{Text: "Continue from summary"},
		}},
		// Fork point
		// Child's messages
		{ID: "child-1", Role: message.User, Parts: []message.ContentPart{
			message.TextContent{Text: "Branch from here"},
		}},
		{ID: "child-2", Role: message.Assistant, Parts: []message.ContentPart{
			message.TextContent{Text: "OK, branching"},
		}},
	}

	result := repairMessageHistory(msgs)
	require.Len(t, result, 4)
}

// TestForkCompaction_ParentCompactedAfterFork tests parent compacting after child forked
func TestForkCompaction_ParentCompactedAfterFork(t *testing.T) {
	// Child should still see original parent messages, not parent's new summary
	// (This is determined by DB layer, but messages should still be valid)

	msgs := []message.Message{
		// Original parent messages (before parent compacted)
		{ID: "parent-1", Role: message.User, Parts: []message.ContentPart{
			message.TextContent{Text: "Original message 1"},
		}},
		{ID: "parent-2", Role: message.Assistant, Parts: []message.ContentPart{
			message.TextContent{Text: "Original response"},
		}},
		// Child's messages
		{ID: "child-1", Role: message.User, Parts: []message.ContentPart{
			message.TextContent{Text: "Child continues"},
		}},
	}

	result := repairMessageHistory(msgs)
	require.Len(t, result, 3)
}

// TestForkCompaction_ChildCompactsSeparately tests child thread compacting independently
func TestForkCompaction_ChildCompactsSeparately(t *testing.T) {
	// After fork, child can have its own compaction cycle

	msgs := []message.Message{
		// Child's compaction summary (combines inherited + local)
		{ID: "child-summary", Role: message.System, Parts: []message.ContentPart{
			message.TextContent{Text: "Child thread summary including inherited context..."},
		}},
		// New messages in child's post-compaction context
		{ID: "2", Role: message.User, Parts: []message.ContentPart{
			message.TextContent{Text: "Continue in child"},
		}},
		{ID: "3", Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "tc-1", Name: "bash", Input: `{}`},
		}},
		{ID: "4", Role: message.Tool, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "tc-1", Content: "result"},
		}},
	}

	result := repairMessageHistory(msgs)
	require.Len(t, result, 4)
}

// ============================================================================
// EDGE CASES WITH REASONING/THINKING
// ============================================================================

// TestCompaction_PreserveThinkingSignature tests that thinking signatures survive compaction boundary
func TestCompaction_PreserveThinkingSignature(t *testing.T) {
	// Post-compaction, assistant may need to reference previous thinking

	msgs := []message.Message{
		{ID: "summary", Role: message.System, Parts: []message.ContentPart{
			message.TextContent{Text: "Summary..."},
		}},
		{ID: "2", Role: message.User, Parts: []message.ContentPart{
			message.TextContent{Text: "Continue your analysis"},
		}},
		{ID: "3", Role: message.Assistant, Parts: []message.ContentPart{
			message.ReasoningContent{
				Thinking:  "Building on previous analysis...",
				Signature: "new-sig-123",
			},
			message.TextContent{Text: "Here's my continued analysis..."},
		}},
	}

	result := repairMessageHistory(msgs)
	require.Len(t, result, 3)

	// Verify thinking is preserved
	reasoning, ok := result[2].Parts[0].(message.ReasoningContent)
	require.True(t, ok)
	require.Equal(t, "new-sig-123", reasoning.Signature)
}

// TestCompaction_OrphanedToolCallWithThinking tests repair when assistant had thinking + tool call
func TestCompaction_OrphanedToolCallWithThinking(t *testing.T) {
	msgs := []message.Message{
		{ID: "summary", Role: message.System, Parts: []message.ContentPart{
			message.TextContent{Text: "Summary..."},
		}},
		{ID: "2", Role: message.User, Parts: []message.ContentPart{
			message.TextContent{Text: "Investigate the bug"},
		}},
		{ID: "3", Role: message.Assistant, Parts: []message.ContentPart{
			message.ReasoningContent{Thinking: "Let me analyze this..."},
			message.TextContent{Text: "I'll check the logs"},
			message.ToolCall{ID: "tc-1", Name: "bash", Input: `{"command": "tail logs"}`},
		}},
		// Orphaned - no tool result
	}

	result := repairMessageHistory(msgs)
	require.Len(t, result, 4)

	// Verify thinking is preserved in original message
	assistantParts := result[2].Parts
	require.Len(t, assistantParts, 3)
	_, isThinking := assistantParts[0].(message.ReasoningContent)
	require.True(t, isThinking)

	// Verify synthetic tool result was added
	require.Equal(t, message.Tool, result[3].Role)
}

// ============================================================================
// TABLE-DRIVEN TESTS FOR COMPACTION SCENARIOS
// ============================================================================

func TestCompaction_TableDriven(t *testing.T) {
	testCases := []struct {
		name          string
		messages      []message.Message
		expectedLen   int
		verifyFirst   message.MessageRole
		toolsRepaired int
	}{
		{
			name: "clean_post_compaction",
			messages: []message.Message{
				{ID: "1", Role: message.System, Parts: []message.ContentPart{message.TextContent{Text: "Summary"}}},
				{ID: "2", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "Hi"}}},
				{ID: "3", Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "Hello"}}},
			},
			expectedLen:   3,
			verifyFirst:   message.System,
			toolsRepaired: 0,
		},
		{
			name: "post_compaction_with_complete_tools",
			messages: []message.Message{
				{ID: "1", Role: message.System, Parts: []message.ContentPart{message.TextContent{Text: "Summary"}}},
				{ID: "2", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "Run"}}},
				{ID: "3", Role: message.Assistant, Parts: []message.ContentPart{
					message.ToolCall{ID: "tc-1", Name: "bash", Input: "{}"},
				}},
				{ID: "4", Role: message.Tool, Parts: []message.ContentPart{
					message.ToolResult{ToolCallID: "tc-1", Content: "ok"},
				}},
			},
			expectedLen:   4,
			verifyFirst:   message.System,
			toolsRepaired: 0,
		},
		{
			name: "post_compaction_with_orphaned_tool",
			messages: []message.Message{
				{ID: "1", Role: message.System, Parts: []message.ContentPart{message.TextContent{Text: "Summary"}}},
				{ID: "2", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "Run"}}},
				{ID: "3", Role: message.Assistant, Parts: []message.ContentPart{
					message.ToolCall{ID: "tc-1", Name: "bash", Input: "{}"},
				}},
				// Missing tool result
			},
			expectedLen:   4,
			verifyFirst:   message.System,
			toolsRepaired: 1,
		},
		{
			name: "inherited_orphan_plus_local",
			messages: []message.Message{
				// Inherited with orphan
				{ID: "inherited-1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "do"}}},
				{ID: "inherited-2", Role: message.Assistant, Parts: []message.ContentPart{
					message.ToolCall{ID: "tc-inherited", Name: "view", Input: "{}"},
				}},
				// Fork point
				{ID: "local-1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "branch"}}},
				{ID: "local-2", Role: message.Assistant, Parts: []message.ContentPart{
					message.ToolCall{ID: "tc-local", Name: "bash", Input: "{}"},
				}},
				{ID: "local-3", Role: message.Tool, Parts: []message.ContentPart{
					message.ToolResult{ToolCallID: "tc-local", Content: "done"},
				}},
			},
			expectedLen:   6, // +1 for synthetic repair of tc-inherited
			verifyFirst:   message.User,
			toolsRepaired: 1,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := repairMessageHistory(tc.messages)
			require.Len(t, result, tc.expectedLen)
			require.Equal(t, tc.verifyFirst, result[0].Role)

			// Count repaired tools (synthetic with error)
			repairedCount := 0
			for _, msg := range result {
				if msg.Role == message.Tool {
					for _, tr := range msg.ToolResults() {
						if tr.IsError && containsAny(tr.Content, "cancelled", "interrupted", "") {
							repairedCount++
						}
					}
				}
			}
			require.Equal(t, tc.toolsRepaired, repairedCount)
		})
	}
}

// containsAny checks if s contains any of the substrings
func containsAny(s string, substrs ...string) bool {
	for _, substr := range substrs {
		if substr != "" && len(s) >= len(substr) {
			for i := 0; i <= len(s)-len(substr); i++ {
				if s[i:i+len(substr)] == substr {
					return true
				}
			}
		}
	}
	return len(s) == 0 // Empty content also counts as synthetic
}
