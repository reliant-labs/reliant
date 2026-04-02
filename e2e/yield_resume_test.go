// Copyright (c) 2025 Reliant Labs
package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	db_core "github.com/reliant-labs/reliant/internal/db/core"
	messageModel "github.com/reliant-labs/reliant/internal/models/message"
)

// TestYieldResume_WithUserData exercises the full yield → needs_attention →
// user sends follow-up → workflow resumes cycle. This complements
// TestAgent_MaxTurnsWithSingleTurn (which only tests the yield half) by
// verifying the resume path works end-to-end.
//
// The agent workflow's yield condition is:
//
//	inputs.yield || iter.iteration >= inputs.max_turns
//
// With max_turns=1, the loop yields every time the while condition fails
// (since iter.iteration is always >= 1 after the first run). On resume
// with action="reply", the loop re-enters for another iteration. This
// test verifies that the resume path works: new messages appear and the
// LLM is called again after the user's follow-up.
//
// Flow:
//  1. Start workflow with max_turns=1
//  2. LLM returns a tool call → consumes the single allowed turn
//  3. While condition fails (iter >= max_turns), yield fires → needs_attention
//  4. User sends follow-up (resolves yield as "reply")
//  5. Loop re-enters, LLM returns text-only response → new assistant message
//  6. While condition fails again, yield fires again → needs_attention (second yield)
//  7. Test verifies the resume produced new messages
func TestYieldResume_WithUserData(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)
	defer h.Cleanup()

	// Turn 1 (before yield): LLM returns a tool call, consuming the 1 allowed turn.
	h.MockLLM.SetResponseWithToolCall(
		"Working on it.",
		"Bash",
		map[string]interface{}{"command": "echo step1"},
	)
	// Turn 2 (after resume): LLM gives a final text answer — no tool calls.
	h.MockLLM.AddResponse(MockResponse{Text: "All done with your request!"})

	// Start with max_turns=1. The yield condition "inputs.yield || iter.iteration >= inputs.max_turns"
	// fires when the while condition fails at iter >= 1.
	chatID := h.StartAgentWorkflowViaGRPC(t, "Do something for me",
		WithWorkflowParam("max_turns", float64(1)),
	)

	// 1 iteration produces: user + assistant(tool call) + tool result = 3 messages.
	h.WaitForMessages(t, chatID, 3)

	// Wait for the chat to enter the yielded state (first yield).
	h.WaitForPendingYield(t, chatID, 10*time.Second)

	// Snapshot messages before resume so we can compare later.
	messagesBefore := h.GetMessages(t, chatID)
	require.GreaterOrEqual(t, len(messagesBefore), 2, "should have at least user + assistant messages before resume")
	t.Logf("Messages before resume: %d", len(messagesBefore))

	// Look up the pending yield so we can pass its ID to SendMessage.
	pendingYield, err := h.DB.GetPendingYieldByChatID(context.Background(), chatID)
	require.NoError(t, err, "failed to get pending yield")
	require.NotNil(t, pendingYield, "expected a pending yield record after max_turns yield")
	t.Logf("Pending yield: id=%s status=%s", pendingYield.ID, pendingYield.Status)

	// Send a follow-up message to resume the workflow, passing the yield ID.
	h.SendMessageViaGRPC(t, chatID, "Continue please",
		WithSendYieldID(pendingYield.ID),
	)

	// After resume the loop re-enters, runs CallLLM ("All done"), then yields again
	// because iter.iteration >= max_turns is still true. Wait for the second yield.
	// First, wait for the new messages to appear (user follow-up + assistant response).
	// Before resume: user, assistant(tool), tool = 3
	// After resume:  + user("Continue please"), assistant("All done") = 5
	h.WaitForMessages(t, chatID, 5)

	// Wait for the second yield to activate (chat returns to needs_attention).
	// The state briefly goes to "running" during resume, then back to needs_attention.
	h.WaitForPendingYield(t, chatID, 10*time.Second)

	// Verify more messages appeared after resume.
	finalMessages := h.GetMessages(t, chatID)
	assert.Greater(t, len(finalMessages), len(messagesBefore),
		"should have more messages after resume (got %d, had %d before)",
		len(finalMessages), len(messagesBefore))

	// Find the last assistant message — should contain the post-resume response.
	var lastAssistant *db.Message
	for i := len(finalMessages) - 1; i >= 0; i-- {
		if finalMessages[i].Role == roleAssistant {
			lastAssistant = finalMessages[i]
			break
		}
	}
	require.NotNil(t, lastAssistant, "should have an assistant message after resume")
	AssertTextContentContains(t, h.DB, lastAssistant.ID, "All done")

	// Verify the user's follow-up message was persisted.
	var foundFollowUp bool
	for _, msg := range finalMessages {
		if msg.Role == roleUser {
			blocks, err := h.DB.ListContentBlocks(context.Background(), msg.ID)
			if err == nil {
				for _, b := range blocks {
					if b.Content != nil && *b.Content == "Continue please" {
						foundFollowUp = true
					}
				}
			}
		}
	}
	assert.True(t, foundFollowUp, "user's follow-up message should be persisted")

	// Verify LLM was called at least twice (once before yield, once after resume).
	assert.GreaterOrEqual(t, h.MockLLM.CallCount(), 2,
		"LLM should be called at least twice (before yield + after resume)")

	t.Logf("Yield/resume test passed: %d messages before yield, %d after resume, %d LLM calls",
		len(messagesBefore), len(finalMessages), h.MockLLM.CallCount())
}

// TestYieldContinue_ExitsLoop exercises the "Continue" button path where the
// user resolves a yield with action="continue" (via YieldService.ResolveYield)
// instead of sending a follow-up message. When the loop receives action="continue",
// it should exit the loop (break) rather than re-enter it (continue), and the
// workflow should complete.
//
// Flow:
//  1. Start workflow with max_turns=1
//  2. LLM returns a tool call → consumes the single allowed turn
//  3. While condition fails, yield fires → needs_attention
//  4. User clicks "Continue" (ResolveYield with action="continue")
//  5. Loop exits (does NOT re-enter), workflow completes
//  6. Test verifies no additional LLM calls were made after yield
func TestYieldContinue_ExitsLoop(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)
	defer h.Cleanup()

	// Turn 1 (before yield): LLM returns a tool call.
	h.MockLLM.SetResponseWithToolCall(
		"Working on it.",
		"Bash",
		map[string]interface{}{"command": "echo step1"},
	)
	// This response should NOT be consumed — "continue" exits the loop.
	h.MockLLM.AddResponse(MockResponse{Text: "Should not be reached"})

	chatID := h.StartAgentWorkflowViaGRPC(t, "Do something for me",
		WithWorkflowParam("max_turns", float64(1)),
	)

	// 1 iteration produces: user + assistant(tool call) + tool result = 3 messages.
	h.WaitForMessages(t, chatID, 3)

	// Wait for the chat to enter the yielded state.
	h.WaitForPendingYield(t, chatID, 10*time.Second)

	// Snapshot LLM call count before resolution.
	callsBefore := h.MockLLM.CallCount()

	// Look up the pending yield.
	pendingYield, err := h.DB.GetPendingYieldByChatID(context.Background(), chatID)
	require.NoError(t, err, "failed to get pending yield")
	require.NotNil(t, pendingYield, "expected a pending yield record")

	// Resolve via "continue" — this is the Continue button path.
	h.ResolveYieldViaGRPC(t, pendingYield.ID, "continue")

	// The workflow should complete (loop exits on "continue").
	h.WaitForWorkflowComplete(t, chatID)

	// Verify no additional LLM calls were made after the yield was resolved.
	assert.Equal(t, callsBefore, h.MockLLM.CallCount(),
		"LLM should NOT be called again after continue — loop should exit")

	// Verify the yield was resolved with action="continue".
	resolvedYield, err := h.DB.GetYieldByID(context.Background(), pendingYield.ID)
	require.NoError(t, err)
	assert.Equal(t, db.YieldStatusResolved, resolvedYield.Status)
	require.NotNil(t, resolvedYield.ActionTaken)
	assert.Equal(t, "continue", *resolvedYield.ActionTaken)

	t.Logf("Continue test passed: %d LLM calls (all before yield), workflow completed", callsBefore)
}

// TestYieldResume_MultipleRounds exercises multiple yield→reply→resume cycles,
// finishing with a "continue" to exit the loop. This verifies the loop can
// bounce between yielding and resuming repeatedly and tests both resume actions
// in a single workflow.
//
// With max_turns=1, the yield condition (iter.iteration >= max_turns) is always
// true after the first iteration, so every time the while condition fails
// (either from max_turns or no tool calls), a yield fires.
//
// Flow:
//  1. Start workflow with max_turns=1
//  2. Iteration 0: LLM returns tool call → yield #1 (iter >= max_turns)
//  3. User sends reply → resume, iteration 1: LLM returns tool call → yield #2
//  4. User sends reply → resume, iteration 2: LLM returns text-only → yield #3
//  5. User clicks "Continue" → loop exits, workflow completes
func TestYieldResume_MultipleRounds(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)
	defer h.Cleanup()

	// Iteration 0 (before first yield): tool call.
	h.MockLLM.SetResponseWithToolCall(
		"Step 1: checking.",
		"Bash",
		map[string]interface{}{"command": "echo step1"},
	)
	// Iteration 1 (after first resume): tool call.
	h.MockLLM.AddResponse(MockResponse{
		Text:      "Step 2: applying.",
		ToolCalls: []MockToolCall{{Name: "Bash", Input: map[string]interface{}{"command": "echo step2"}}},
	})
	// Iteration 2 (after second resume): text-only response.
	h.MockLLM.AddResponse(MockResponse{Text: "All done after multiple rounds!"})

	chatID := h.StartAgentWorkflowViaGRPC(t, "Multi-round task",
		WithWorkflowParam("max_turns", float64(1)),
	)

	// --- Round 1: yield after iteration 0 ---
	// Messages: user + assistant(tool) + tool = 3
	h.WaitForMessages(t, chatID, 3)
	h.WaitForPendingYield(t, chatID, 10*time.Second)

	yield1, err := h.DB.GetPendingYieldByChatID(context.Background(), chatID)
	require.NoError(t, err)
	require.NotNil(t, yield1, "expected yield #1")
	assert.Equal(t, 1, h.MockLLM.CallCount(), "1 LLM call before first yield")
	t.Logf("Round 1: yield #1 created (id=%s), 1 LLM call", yield1.ID)

	// Resume with reply.
	h.SendMessageViaGRPC(t, chatID, "Continue to step 2",
		WithSendYieldID(yield1.ID),
	)

	// --- Round 2: yield after iteration 1 ---
	// Before: user, assistant(tool), tool = 3
	// After resume: + user("Continue to step 2"), assistant(tool), tool = 6
	h.WaitForMessages(t, chatID, 6)
	h.WaitForPendingYield(t, chatID, 10*time.Second)

	// Poll for yield #2 — yield creation may lag slightly behind message persistence.
	var yield2 *db.Yield
	require.Eventually(t, func() bool {
		y, err := h.DB.GetPendingYieldByChatID(context.Background(), chatID)
		if err != nil || y == nil || y.ID == yield1.ID {
			return false
		}
		yield2 = y
		return true
	}, 10*time.Second, 100*time.Millisecond, "expected yield #2 (different from yield #1)")
	assert.Equal(t, 2, h.MockLLM.CallCount(), "2 LLM calls after second yield")
	t.Logf("Round 2: yield #2 created (id=%s), 2 LLM calls", yield2.ID)

	// Resume with reply again.
	h.SendMessageViaGRPC(t, chatID, "Finish up",
		WithSendYieldID(yield2.ID),
	)

	// --- Round 3: text-only response → yield #3 (iter still >= max_turns) ---
	// Before: 6 messages
	// After resume: + user("Finish up"), assistant("All done") = 8
	h.WaitForMessages(t, chatID, 8)
	h.WaitForPendingYield(t, chatID, 10*time.Second)

	var yield3 *db.Yield
	require.Eventually(t, func() bool {
		y, err := h.DB.GetPendingYieldByChatID(context.Background(), chatID)
		if err != nil || y == nil || y.ID == yield2.ID {
			return false
		}
		yield3 = y
		return true
	}, 10*time.Second, 100*time.Millisecond, "expected yield #3 (different from yield #2)")
	assert.Equal(t, 3, h.MockLLM.CallCount(), "3 LLM calls after third yield")
	t.Logf("Round 3: yield #3 created (id=%s), 3 LLM calls", yield3.ID)

	// Resolve with "continue" — loop exits, workflow completes.
	h.ResolveYieldViaGRPC(t, yield3.ID, "continue")
	h.WaitForWorkflowComplete(t, chatID)

	// Verify all three yields were resolved with correct actions.
	for i, tc := range []struct {
		id     string
		action string
	}{
		{yield1.ID, "reply"},
		{yield2.ID, "reply"},
		{yield3.ID, "continue"},
	} {
		y, err := h.DB.GetYieldByID(context.Background(), tc.id)
		require.NoError(t, err, "failed to get yield #%d", i+1)
		assert.Equal(t, db.YieldStatusResolved, y.Status,
			fmt.Sprintf("yield #%d should be resolved", i+1))
		require.NotNil(t, y.ActionTaken)
		assert.Equal(t, tc.action, *y.ActionTaken,
			fmt.Sprintf("yield #%d action should be %q", i+1, tc.action))
	}

	// Verify user follow-up messages were persisted.
	finalMessages := h.GetMessages(t, chatID)
	var userTexts []string
	for _, msg := range finalMessages {
		if msg.Role == roleUser {
			blocks, err := h.DB.ListContentBlocks(context.Background(), msg.ID)
			if err == nil {
				for _, b := range blocks {
					if b.Content != nil {
						userTexts = append(userTexts, *b.Content)
					}
				}
			}
		}
	}
	assert.Contains(t, userTexts, "Multi-round task", "initial user message")
	assert.Contains(t, userTexts, "Continue to step 2", "first follow-up")
	assert.Contains(t, userTexts, "Finish up", "second follow-up")

	// Verify the final assistant message is the text-only response.
	var lastAssistant *db.Message
	for i := len(finalMessages) - 1; i >= 0; i-- {
		if finalMessages[i].Role == roleAssistant {
			lastAssistant = finalMessages[i]
			break
		}
	}
	require.NotNil(t, lastAssistant, "should have a final assistant message")
	AssertTextContentContains(t, h.DB, lastAssistant.ID, "All done after multiple rounds")

	t.Logf("Multi-round test passed: %d messages, %d LLM calls, 3 yields (2 reply + 1 continue)",
		len(finalMessages), h.MockLLM.CallCount())
}

// TestYieldReply_MessageInCorrectThread verifies that when a user replies to a
// yielded agent, the message is saved to the yield's thread (not the root thread)
// and the next LLM call sees that message in its conversation history.
//
// This is a regression test for a bug where the frontend didn't send yield_id
// (due to update_type serialization), causing the message to go to the wrong
// thread and the next call_llm to fail with "must end with a user message".
//
// Flow:
//  1. Start workflow with max_turns=1
//  2. LLM returns a tool call → consumes the single allowed turn
//  3. While condition fails, yield fires → needs_attention
//  4. User sends reply WITH yield_id
//  5. Verify: message saved to yield's thread, not the root thread
//  6. Verify: next LLM call includes the user's reply in history
func TestYieldReply_MessageInCorrectThread(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)
	defer h.Cleanup()

	// Turn 1 (before yield): LLM returns a tool call, consuming the 1 allowed turn.
	h.MockLLM.SetResponseWithToolCall(
		"Working on it.",
		"Bash",
		map[string]interface{}{"command": "echo step1"},
	)
	// Turn 2 (after resume): LLM gives a final text answer — no tool calls.
	h.MockLLM.AddResponse(MockResponse{Text: "Done with your follow-up!"})

	chatID := h.StartAgentWorkflowViaGRPC(t, "Initial prompt",
		WithWorkflowParam("max_turns", float64(1)),
	)

	// 1 iteration produces: user + assistant(tool call) + tool result = 3 messages.
	h.WaitForMessages(t, chatID, 3)
	h.WaitForPendingYield(t, chatID, 10*time.Second)

	// Get the pending yield — its ThreadID is where the reply must land.
	pendingYield, err := h.DB.GetPendingYieldByChatID(context.Background(), chatID)
	require.NoError(t, err, "failed to get pending yield")
	require.NotNil(t, pendingYield, "expected a pending yield")
	yieldThreadID := pendingYield.ThreadID
	require.NotEmpty(t, yieldThreadID, "yield must have a non-empty thread_id")
	t.Logf("Yield thread: %s", yieldThreadID)

	// Send a reply with the yield_id — this is the critical path.
	const replyText = "Please add more details"
	h.SendMessageViaGRPC(t, chatID, replyText,
		WithSendYieldID(pendingYield.ID),
	)

	// After resume: + user(reply) + assistant("Done") = 5 total.
	h.WaitForMessages(t, chatID, 5)

	// Wait for the second yield (iter >= max_turns still true).
	h.WaitForPendingYield(t, chatID, 10*time.Second)

	// ─── VERIFY 1: User reply was saved to the yield's thread ───
	ctx := context.Background()
	yieldThreadMessages, err := h.DB.ListMessages(ctx, chatID, db_core.MessageListOptions{
		Thread: &yieldThreadID,
	})
	require.NoError(t, err, "failed to list messages in yield thread")

	var foundReplyInYieldThread bool
	for _, msg := range yieldThreadMessages {
		if msg.Role != roleUser {
			continue
		}
		blocks, err := h.DB.ListContentBlocks(ctx, msg.ID)
		if err != nil {
			continue
		}
		for _, b := range blocks {
			if b.Content != nil && strings.Contains(*b.Content, replyText) {
				foundReplyInYieldThread = true
				assert.Equal(t, yieldThreadID, msg.ThreadID,
					"reply message thread_id should match the yield's thread")
			}
		}
	}
	assert.True(t, foundReplyInYieldThread,
		"user reply %q should be in the yield thread (%s)", replyText, yieldThreadID)

	// ─── VERIFY 2: The LLM call after resume includes the user reply ───
	calls := h.MockLLM.GetCalls()
	require.GreaterOrEqual(t, len(calls), 2,
		"expected at least 2 LLM calls (before yield + after resume)")

	// The last call (after resume) should contain the user's reply.
	postResumeCall := calls[len(calls)-1]
	var foundReplyInLLM bool
	for _, msg := range postResumeCall.Messages {
		if msg.Role != messageModel.User {
			continue
		}
		for _, part := range msg.Parts {
			if textPart, ok := part.(messageModel.TextContent); ok {
				if strings.Contains(textPart.Text, replyText) {
					foundReplyInLLM = true
				}
			}
		}
	}
	assert.True(t, foundReplyInLLM,
		"LLM call after yield resume should include the user's reply %q in its message history", replyText)

	t.Logf("Thread routing test passed: reply in yield thread=%s, LLM saw reply, %d total calls",
		yieldThreadID, len(calls))
}
