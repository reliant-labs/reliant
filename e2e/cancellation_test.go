// Copyright (c) 2025 Reliant Labs
package e2e

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/enums/v1"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
)

// TestCancellation_RunningWorkflow tests that cancelling a running workflow
// via the CancelChat gRPC endpoint:
//  1. Successfully returns a success response
//  2. Causes the Temporal workflow to stop (cancelled/terminated status)
//  3. Stops producing new messages after cancellation
func TestCancellation_RunningWorkflow(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)
	defer h.Cleanup()

	ctx := context.Background()

	// ---- Mock setup ----
	// Response: assistant returns a tool call that will take a while to execute.
	// This keeps the workflow busy so we can cancel it mid-flight.
	h.MockLLM.SetResponseWithToolCall(
		"Running a long task...",
		"Bash",
		map[string]interface{}{
			"command": "sleep 10",
		},
	)

	// Configure mock tool executor with a long delay so the workflow stays
	// in-progress long enough for us to send the cancel signal.
	h.MockTools.On("Bash", MockToolResponse{
		Result:  "still running...",
		Success: true,
		Delay:   5 * time.Second,
	})

	// ---- Step 1: Start the workflow ----
	chatID := h.StartAgentWorkflowViaGRPC(t, "Run a long task")
	t.Logf("Chat started: %s", chatID)

	// ---- Step 2: Wait for workflow to be in progress ----
	// At least user message + first assistant message (with tool call) should exist.
	messages := h.WaitForMessages(t, chatID, 2)
	require.Equal(t, reliantv1.MessageRole_MESSAGE_ROLE_USER, messages[0].Role)
	require.Equal(t, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, messages[1].Role)
	t.Log("Workflow is running — first assistant message received")

	// ---- Step 3: Cancel the workflow ----
	authCtx := context.WithValue(ctx, auth.UserIDContextKey, h.UserID())
	cancelResp, err := h.chatService.CancelChat(authCtx, connect.NewRequest(&reliantv1.CancelChatRequest{
		ChatId: chatID,
	}))
	require.NoError(t, err, "CancelChat should not return an error")
	require.True(t, cancelResp.Msg.Success, "CancelChat should report success")
	t.Logf("Cancel signal sent: %s", cancelResp.Msg.Message)

	// ---- Step 4: Wait for the workflow to actually terminate ----
	// Poll Temporal until the workflow is no longer running.
	h.waitForWorkflowNotRunning(t, chatID)
	t.Log("Workflow is no longer running")

	// ---- Step 5: Verify Temporal workflow status is cancelled/terminated ----
	chat, err := h.DB.GetChat(ctx, chatID)
	require.NoError(t, err, "should be able to get chat")

	workflowID := chatID
	if chat.WorkflowID != nil && *chat.WorkflowID != "" {
		workflowID = *chat.WorkflowID
	}

	desc, err := h.TemporalClient.DescribeWorkflowExecution(ctx, workflowID, "")
	require.NoError(t, err, "DescribeWorkflowExecution should succeed")

	status := desc.WorkflowExecutionInfo.Status
	assert.True(t,
		status == enums.WORKFLOW_EXECUTION_STATUS_CANCELED ||
			status == enums.WORKFLOW_EXECUTION_STATUS_TERMINATED,
		"workflow status should be CANCELED or TERMINATED, got %s", status.String(),
	)
	t.Logf("Temporal workflow status: %s", status.String())

	// ---- Step 6: Verify no new messages are produced after cancellation ----
	// Snapshot message count, wait, and confirm it's stable.
	msgsBefore, err := h.DB.ListMessages(ctx, chatID, db.MessageListOptions{})
	require.NoError(t, err)
	countBefore := len(msgsBefore)

	time.Sleep(1 * time.Second)

	msgsAfter, err := h.DB.ListMessages(ctx, chatID, db.MessageListOptions{})
	require.NoError(t, err)
	countAfter := len(msgsAfter)

	assert.Equal(t, countBefore, countAfter,
		"no new messages should appear after cancellation (before=%d, after=%d)", countBefore, countAfter)

	t.Logf("✓ Cancellation test passed: workflow cancelled, %d messages total, no activity after cancel", countAfter)
}

// waitForWorkflowNotRunning polls Temporal until the workflow is no longer in RUNNING status.
func (h *TestHarness) waitForWorkflowNotRunning(t *testing.T, chatID string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), MaxWaitTime)
	defer cancel()

	chat, err := h.DB.GetChat(ctx, chatID)
	require.NoError(t, err)

	workflowID := chatID
	if chat.WorkflowID != nil && *chat.WorkflowID != "" {
		workflowID = *chat.WorkflowID
	}

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			h.LogWorkflowDiagnostics(t, chatID)
			t.Fatalf("timeout waiting for workflow to stop running (chatID: %s)", chatID)
		case <-ticker.C:
			desc, err := h.TemporalClient.DescribeWorkflowExecution(ctx, workflowID, "")
			if err != nil {
				continue
			}
			if desc.WorkflowExecutionInfo.Status != enums.WORKFLOW_EXECUTION_STATUS_RUNNING {
				return
			}
		}
	}
}
