// Copyright (c) 2025 Reliant Labs
package e2e

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
)

// ============================================================================
// AGENT LOOP STRESS & BOUNDARY TESTS
//
// These tests exercise the agent loop across multiple iterations, max_turns
// enforcement, and related boundary conditions. They complement the contract
// tests in internal/workflow/ by running against the real Temporal engine.
// ============================================================================

// TestAgent_ManyIterations forces the agent loop through 4 tool-call iterations
// plus a final text response (5 LLM calls total). This exercises the loop's
// iter.iteration tracking and while-condition evaluation across many cycles
// with properly typed *v3.IterContext.
func TestAgent_ManyIterations(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Cleanup()

	// Iteration 0: tool call
	h.MockLLM.SetResponseWithToolCall(
		"Step 1: checking environment",
		"Bash",
		map[string]interface{}{"command": "echo step1"},
	)

	// Iteration 1: tool call
	h.MockLLM.AddResponse(MockResponse{
		Text:      "Step 2: reading config",
		ToolCalls: []MockToolCall{{Name: "Bash", Input: map[string]interface{}{"command": "echo step2"}}},
	})

	// Iteration 2: tool call
	h.MockLLM.AddResponse(MockResponse{
		Text:      "Step 3: applying changes",
		ToolCalls: []MockToolCall{{Name: "Bash", Input: map[string]interface{}{"command": "echo step3"}}},
	})

	// Iteration 3: tool call
	h.MockLLM.AddResponse(MockResponse{
		Text:      "Step 4: verifying results",
		ToolCalls: []MockToolCall{{Name: "Bash", Input: map[string]interface{}{"command": "echo step4"}}},
	})

	// Iteration 4: no tool calls → loop exits
	h.MockLLM.AddResponse(MockResponse{
		Text: "All 4 steps completed successfully.",
	})

	chatID := h.StartAgentWorkflowViaGRPC(t, "Do 4 things in sequence")

	// Expected message flow:
	//   1. user
	//   2. assistant (tool call) — iteration 0
	//   3. tool result
	//   4. assistant (tool call) — iteration 1
	//   5. tool result
	//   6. assistant (tool call) — iteration 2
	//   7. tool result
	//   8. assistant (tool call) — iteration 3
	//   9. tool result
	//  10. assistant (final)     — iteration 4
	messages := h.WaitForMessages(t, chatID, 10)
	h.WaitForWorkflowComplete(t, chatID)

	h.AssertMessageRoles(t, messages,
		reliantv1.MessageRole_MESSAGE_ROLE_USER,
		reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, reliantv1.MessageRole_MESSAGE_ROLE_TOOL,
		reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, reliantv1.MessageRole_MESSAGE_ROLE_TOOL,
		reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, reliantv1.MessageRole_MESSAGE_ROLE_TOOL,
		reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, reliantv1.MessageRole_MESSAGE_ROLE_TOOL,
		reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
	)

	// Verify LLM was called exactly 5 times (4 tool + 1 final)
	require.Equal(t, 5, h.MockLLM.CallCount(), "LLM should be called 5 times (4 tool iterations + 1 final)")

	// Verify final response has no tool calls
	AssertNoToolCalls(t, h.DB, messages[9].ID)

	// Verify final response content
	AssertTextContentContains(t, h.DB, messages[9].ID, "completed successfully")

	// Verify each assistant message with tool calls actually has tool calls persisted
	for _, idx := range []int{1, 3, 5, 7} {
		AssertToolCallExists(t, h.DB, messages[idx].ID, "Bash")
	}

	// Verify context grows with each LLM call (each call should include prior messages)
	calls := h.MockLLM.GetCalls()
	for i := 1; i < len(calls); i++ {
		assert.Greater(t, len(calls[i].Messages), len(calls[i-1].Messages),
			"LLM call %d should have more context than call %d", i, i-1)
	}

	t.Logf("Many iterations: %d messages, %d LLM calls, context grew from %d to %d messages",
		len(messages), h.MockLLM.CallCount(), len(calls[0].Messages), len(calls[4].Messages))
}

// TestAgent_MaxTurnsEnforcement sets max_turns=2 and queues 5 tool call responses.
// The loop should exit after 2 iterations (not consume all 5) and yield, since
// the agent workflow's yield condition is:
//
//	inputs.yield || iter.iteration >= inputs.max_turns
//
// We verify:
// 1. Only 2 LLM calls are consumed (not more)
// 2. Correct messages are saved (user + 2*(assistant+tool))
// 3. The workflow yields (chat state = needs_attention)
func TestAgent_MaxTurnsEnforcement(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Cleanup()

	// Queue 5 tool-call responses — only 2 should be consumed before yield
	h.MockLLM.SetResponseWithToolCall(
		"Working on step 1",
		"Bash",
		map[string]interface{}{"command": "echo step1"},
	)
	h.MockLLM.AddResponse(MockResponse{
		Text:      "Working on step 2",
		ToolCalls: []MockToolCall{{Name: "Bash", Input: map[string]interface{}{"command": "echo step2"}}},
	})
	// These should NOT be consumed
	h.MockLLM.AddResponse(MockResponse{
		Text:      "Should not be consumed",
		ToolCalls: []MockToolCall{{Name: "Bash", Input: map[string]interface{}{"command": "echo step3"}}},
	})
	h.MockLLM.AddResponse(MockResponse{
		Text:      "Also should not be consumed",
		ToolCalls: []MockToolCall{{Name: "Bash", Input: map[string]interface{}{"command": "echo step4"}}},
	})
	h.MockLLM.AddResponse(MockResponse{Text: "Never reached"})

	// Start with max_turns=2 via workflow param
	chatID := h.StartAgentWorkflowViaGRPC(t, "Do many things",
		WithWorkflowParam("max_turns", float64(2)),
	)

	// Loop runs 2 iterations, then while condition fails (iter.iteration=2 is NOT < 2),
	// yield condition is true → workflow yields.
	// Messages: user + 2*(assistant+tool) = 5
	messages := h.WaitForMessages(t, chatID, 5)

	// Wait for the yield to activate (chat state → needs_attention)
	h.WaitForPendingYield(t, chatID, 5*time.Second)

	// Verify only 2 LLM calls were made (max_turns enforcement)
	require.Equal(t, 2, h.MockLLM.CallCount(),
		"LLM should be called exactly 2 times before yield (max_turns=2)")

	h.AssertMessageRoles(t, messages,
		reliantv1.MessageRole_MESSAGE_ROLE_USER,
		reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, reliantv1.MessageRole_MESSAGE_ROLE_TOOL,
		reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, reliantv1.MessageRole_MESSAGE_ROLE_TOOL,
	)

	// Verify tool calls were persisted
	AssertToolCallExists(t, h.DB, messages[1].ID, "Bash")
	AssertToolCallExists(t, h.DB, messages[3].ID, "Bash")

	t.Logf("Max turns enforcement: %d messages, %d LLM calls, yielded at max_turns=2",
		len(messages), h.MockLLM.CallCount())
}

// TestAgent_MaxTurnsWithSingleTurn tests the edge case where max_turns=1.
// Only one iteration should execute before the loop yields.
func TestAgent_MaxTurnsWithSingleTurn(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Cleanup()

	// Queue tool-call responses — only 1 should be consumed
	h.MockLLM.SetResponseWithToolCall(
		"Running the one allowed step",
		"Bash",
		map[string]interface{}{"command": "echo only-step"},
	)
	h.MockLLM.AddResponse(MockResponse{
		Text:      "Should not be consumed",
		ToolCalls: []MockToolCall{{Name: "Bash", Input: map[string]interface{}{"command": "echo nope"}}},
	})
	h.MockLLM.AddResponse(MockResponse{Text: "Also should not be consumed"})

	chatID := h.StartAgentWorkflowViaGRPC(t, "Do one thing",
		WithWorkflowParam("max_turns", float64(1)),
	)

	// 1 iteration: user + assistant(tool) + tool = 3 messages
	messages := h.WaitForMessages(t, chatID, 3)

	// Yield activates at max_turns boundary
	h.WaitForPendingYield(t, chatID, 5*time.Second)

	require.Equal(t, 1, h.MockLLM.CallCount(),
		"LLM should be called exactly 1 time (max_turns=1)")

	h.AssertMessageRoles(t, messages,
		reliantv1.MessageRole_MESSAGE_ROLE_USER,
		reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, reliantv1.MessageRole_MESSAGE_ROLE_TOOL,
	)

	AssertToolCallExists(t, h.DB, messages[1].ID, "Bash")

	t.Logf("Max turns single turn: %d messages, %d LLM calls, yielded at max_turns=1",
		len(messages), h.MockLLM.CallCount())
}

// TestAgent_SixIterations pushes the loop to 6 iterations to ensure there are
// no issues with higher iteration counts. This is a regression guard against
// type/casting issues in the iter context that might only manifest after several
// iterations.
func TestAgent_SixIterations(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Cleanup()

	// Queue 6 tool call responses + 1 final text response
	for i := 1; i <= 6; i++ {
		resp := MockResponse{
			Text:      fmt.Sprintf("Iteration %d", i),
			ToolCalls: []MockToolCall{{Name: "Bash", Input: map[string]interface{}{"command": fmt.Sprintf("echo iter%d", i)}}},
		}
		if i == 1 {
			h.MockLLM.SetResponseWithToolCall(resp.Text, resp.ToolCalls[0].Name, resp.ToolCalls[0].Input)
		} else {
			h.MockLLM.AddResponse(resp)
		}
	}
	h.MockLLM.AddResponse(MockResponse{Text: "All six iterations done."})

	chatID := h.StartAgentWorkflowViaGRPC(t, "Do 6 things")

	// 1 user + 6*(assistant+tool) + 1 final assistant = 14
	messages := h.WaitForMessages(t, chatID, 14)
	h.WaitForWorkflowComplete(t, chatID)

	require.Equal(t, 7, h.MockLLM.CallCount(),
		"LLM should be called 7 times (6 tool iterations + 1 final)")

	// Verify first message is user, last is assistant with no tools
	assert.Equal(t, reliantv1.MessageRole_MESSAGE_ROLE_USER, messages[0].Role)
	assert.Equal(t, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, messages[13].Role)
	AssertNoToolCalls(t, h.DB, messages[13].ID)

	// Verify all tool call messages have persisted tool calls
	for i := 1; i <= 11; i += 2 { // indices 1, 3, 5, 7, 9, 11 are assistant messages with tools
		AssertToolCallExists(t, h.DB, messages[i].ID, "Bash")
	}

	t.Logf("Six iterations: %d messages, %d LLM calls", len(messages), h.MockLLM.CallCount())
}

// TestAgent_MaxTurnsThreeIterations verifies max_turns=3 stops at exactly 3 iterations.
// Uses a different max_turns value to ensure the parameter is respected correctly.
func TestAgent_MaxTurnsThreeIterations(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Cleanup()

	// Queue 5 tool-call responses — only 3 should be consumed
	for i := 1; i <= 5; i++ {
		resp := MockResponse{
			Text:      fmt.Sprintf("Step %d", i),
			ToolCalls: []MockToolCall{{Name: "Bash", Input: map[string]interface{}{"command": fmt.Sprintf("echo step%d", i)}}},
		}
		if i == 1 {
			h.MockLLM.SetResponseWithToolCall(resp.Text, resp.ToolCalls[0].Name, resp.ToolCalls[0].Input)
		} else {
			h.MockLLM.AddResponse(resp)
		}
	}

	chatID := h.StartAgentWorkflowViaGRPC(t, "Do many things",
		WithWorkflowParam("max_turns", float64(3)),
	)

	// 3 iterations: user + 3*(assistant+tool) = 7
	messages := h.WaitForMessages(t, chatID, 7)
	h.WaitForPendingYield(t, chatID, 5*time.Second)

	require.Equal(t, 3, h.MockLLM.CallCount(),
		"LLM should be called exactly 3 times (max_turns=3)")

	h.AssertMessageRoles(t, messages,
		reliantv1.MessageRole_MESSAGE_ROLE_USER,
		reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, reliantv1.MessageRole_MESSAGE_ROLE_TOOL,
		reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, reliantv1.MessageRole_MESSAGE_ROLE_TOOL,
		reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, reliantv1.MessageRole_MESSAGE_ROLE_TOOL,
	)

	t.Logf("Max turns=3: %d messages, %d LLM calls, yielded",
		len(messages), h.MockLLM.CallCount())
}
