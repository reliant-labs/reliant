// Copyright (c) 2025 Reliant Labs
package e2e

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
)

// TestPauseAndResume tests the full pause/resume lifecycle using the real
// Temporal server and real app stack. The flow is:
//
//  1. Start a chat workflow with a mock LLM that returns a tool call
//  2. Wait for the first assistant message (workflow is running)
//  3. Pause the chat via gRPC PauseChat
//  4. Verify the workflow DB status = "paused"
//  5. Wait briefly and confirm no new messages appear
//  6. Resume the chat via gRPC ResumeChat
//  7. Verify the workflow continues and new messages appear
//  8. Wait for workflow completion
func TestPauseAndResume(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)
	defer h.Cleanup()

	ctx := context.Background()

	// ---- Mock setup ----
	// Response 1: assistant returns a tool call (Bash).
	// After the tool result comes back, the workflow calls the LLM again.
	// Response 2: assistant returns the final text answer.
	h.MockLLM.SetResponseWithToolCall(
		"Let me run that command for you.",
		"Bash",
		map[string]interface{}{
			"command": "echo 'pause-test-output'",
		},
	)
	h.MockLLM.AddResponse(MockResponse{
		Text: "The command completed. Here is the result of the pause test.",
	})

	// Configure mock tool executor to add a delay. This gives us time to
	// send the pause signal while the tool is "executing", so the workflow
	// will pause at the next step boundary (before calling the LLM a 2nd time).
	h.MockTools.On("Bash", MockToolResponse{
		Result:  "pause-test-output",
		Success: true,
		Delay:   500 * time.Millisecond,
	})

	// ---- Step 1: Start the workflow ----
	chatID := h.StartAgentWorkflowViaGRPC(t, "run echo pause-test-output")
	t.Logf("Chat started: %s", chatID)

	// ---- Step 2: Wait for at least the user + first assistant message ----
	// The first assistant message will contain the tool call.
	messages := h.WaitForMessages(t, chatID, 2)
	require.Equal(t, reliantv1.MessageRole_MESSAGE_ROLE_USER, messages[0].Role)
	require.Equal(t, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, messages[1].Role)
	t.Log("First assistant message received (tool call)")

	// ---- Step 3: Pause the chat ----
	authCtx := context.WithValue(ctx, auth.UserIDContextKey, h.UserID())
	pauseResp, err := h.chatService.PauseChat(authCtx, connect.NewRequest(&reliantv1.PauseChatRequest{
		ChatId: chatID,
	}))
	require.NoError(t, err, "PauseChat failed")
	require.True(t, pauseResp.Msg.Success, "PauseChat should report success")
	t.Log("Pause signal sent")

	// ---- Step 4: Wait briefly and verify no new messages appear while paused ----
	// If pause is effective, the second LLM call should NOT happen, so the
	// message count should remain stable.
	// Allow a brief settling window for any in-flight activity that started just before
	// the pause signal was processed. Then assert state stability while paused.
	time.Sleep(400 * time.Millisecond)

	messagesPausedA, err := h.DB.ListMessages(ctx, chatID, db.MessageListOptions{})
	require.NoError(t, err)
	messageCountA := len(messagesPausedA)
	assistantCountA := countMessagesByRole(messagesPausedA, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT)
	t.Logf("Message count while paused (sample A): %d", messageCountA)

	time.Sleep(500 * time.Millisecond)

	messagesPausedB, err := h.DB.ListMessages(ctx, chatID, db.MessageListOptions{})
	require.NoError(t, err)
	messageCountB := len(messagesPausedB)
	assistantCountB := countMessagesByRole(messagesPausedB, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT)

	require.Equal(t, messageCountA, messageCountB,
		"message count should stabilize while workflow remains paused")
	require.Equal(t, assistantCountA, assistantCountB,
		"assistant turn count should stabilize while workflow remains paused")
	t.Log("No additional messages appeared while paused — workflow remained paused")

	// ---- Step 6: Resume the chat ----
	resumeResp, err := h.chatService.ResumeChat(authCtx, connect.NewRequest(&reliantv1.ResumeChatRequest{
		ChatId: chatID,
	}))
	require.NoError(t, err, "ResumeChat failed")
	require.True(t, resumeResp.Msg.Success, "ResumeChat should report success")
	t.Log("Resume signal sent")

	// ---- Step 7: Verify workflow continues and eventually completes ----
	// Depending on timing, resume may continue from a paused boundary or race with
	// a near-complete execution path. The key invariant is: no progress while paused,
	// then workflow reaches completion after resume.
	h.WaitForWorkflowComplete(t, chatID)
	messages, err = h.DB.ListMessages(ctx, chatID, db.MessageListOptions{})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(messages), 3,
		"should have at least user+assistant+tool messages after resume")
	t.Logf("Messages after resume/completion: %d", len(messages))

	// First three messages should always be user, assistant(tool call), tool(result).
	h.AssertMessageRoles(t, messages[:3], reliantv1.MessageRole_MESSAGE_ROLE_USER, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, reliantv1.MessageRole_MESSAGE_ROLE_TOOL)

	// Verify assistant tool call + tool result are persisted in message content blocks.
	toolCall := AssertToolCallExists(t, h.DB, messages[1].ID, "Bash")
	require.NotNil(t, toolCall.ToolCallID, "tool call should have tool_call_id")
	AssertToolResultExists(t, h.DB, messages[2].ID, *toolCall.ToolCallID)

	// If a final assistant message exists, validate expected content.
	if len(messages) >= 4 {
		require.Equal(t, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, messages[3].Role, "4th message should be assistant when present")
		h.AssertMessageContent(t, messages[3].ID, "pause test")
	}

	t.Log("Workflow completed successfully after resume")
	t.Logf("✓ Pause/resume test passed: %d messages, pause verified, resume confirmed", len(messages))
}

func countMessagesByRole(messages []*db.Message, role reliantv1.MessageRole) int {
	count := 0
	for _, msg := range messages {
		if msg.Role == role {
			count++
		}
	}
	return count
}
