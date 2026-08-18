// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
)

// ============================================================================
// TYPES (strongly typed inputs/outputs)
// ============================================================================

// CleanupInput is the input for Cleanup activity
type CleanupInput struct {
	ChatID     string `json:"chat_id" reliant:"-"`
	WorkflowID string `json:"workflow_id,omitempty"` // Only clean up tool calls from this workflow
	Thread     string `json:"thread,omitempty"`      // Only clean up tool calls from this thread
}

// CleanupOutput is the output from Cleanup activity
type CleanupOutput struct {
	ApprovalsCancelled int `json:"approvals_cancelled"`
	ToolCallsCancelled int `json:"tool_calls_cancelled"`
}

// ============================================================================
// ACTIVITY IMPLEMENTATION
// ============================================================================

// CleanupActivity handles cleanup tasks when a workflow completes or is cancelled.
// This includes:
// - Cancelling any pending approvals
// - Cleaning up any pending tool execution requests
// - Notifying the UI of the cancelled state
type CleanupActivity struct {
	repo db.Repository
}

// NewCleanupActivity creates a new CleanupActivity
func NewCleanupActivity(repo db.Repository) *CleanupActivity {
	return &CleanupActivity{
		repo: repo,
	}
}

// Name returns the activity name for registration
func (a *CleanupActivity) Name() string {
	return "Cleanup"
}

// DisplayName returns human-readable name for UI
func (a *CleanupActivity) DisplayName() string {
	return "Cleanup"
}

// Description returns what the activity does
func (a *CleanupActivity) Description() string {
	return "Cancel pending approvals and clean up resources when workflow ends"
}

// Category returns the activity category for UI grouping
func (a *CleanupActivity) Category() schema.ActivityCategory {
	return schema.CategoryWorkflowManagement
}

// Execute cancels pending approvals and notifies UI
func (a *CleanupActivity) Execute(ctx context.Context, input CleanupInput) (CleanupOutput, error) {
	logging.Info("[Cleanup] Starting cleanup for chat", "chatID", input.ChatID)

	// Cancel orphaned tool calls (stops spinning indicators in UI for cancelled tool executions)
	toolCallsCancelled := a.cancelOrphanedToolCalls(ctx, input.ChatID, input.Thread)

	// Get all pending approvals for this chat
	pendingApprovals, err := a.repo.ListPendingApprovalsByChat(ctx, input.ChatID)
	if err != nil {
		logging.Error("[Cleanup] Failed to list pending approvals", "error", err)
		return CleanupOutput{ToolCallsCancelled: toolCallsCancelled}, fmt.Errorf("failed to list pending approvals: %w", err)
	}

	if len(pendingApprovals) == 0 {
		logging.Info("[Cleanup] No pending approvals to cancel")
		return CleanupOutput{ApprovalsCancelled: 0, ToolCallsCancelled: toolCallsCancelled}, nil
	}

	logging.Info("[Cleanup] Found pending approvals to cancel", "count", len(pendingApprovals))

	// Cancel each pending approval
	cancelledCount := 0
	for _, approval := range pendingApprovals {
		// Update status to cancelled
		err := a.repo.RunTx(ctx, func(txCtx context.Context) error {
			// Update approval status (nil for actionTaken since this is a system cancellation)
			if err := a.repo.UpdateApprovalStatus(txCtx, approval.ID, int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_DENIED), nil, nil, nil); err != nil {
				return fmt.Errorf("failed to update approval status: %w", err)
			}

			// Emit chat_update for UI notification
			updateData := map[string]interface{}{
				"update_type":   "approval",
				"id":            approval.ID,
				"approval_type": approval.ApprovalType,
				"entity_id":     approval.EntityID,
				"status":        "cancelled",
				"title":         approval.Title,
				"resolved_at":   time.Now().UTC().Format(time.RFC3339),
			}

			updateDataJSON, err := json.Marshal(updateData)
			if err != nil {
				return fmt.Errorf("failed to marshal chat_update data: %w", err)
			}

			if err := a.repo.CreateChatUpdate(txCtx, input.ChatID, db.UpdateTypeApproval, approval.ID, string(updateDataJSON)); err != nil {
				return fmt.Errorf("failed to create chat_update: %w", err)
			}

			return nil
		})

		if err != nil {
			logging.Error("[Cleanup] Failed to cancel approval", "approvalID", approval.ID, "error", err)
			// Continue with other approvals even if one fails
			continue
		}

		cancelledCount++
		logging.Info("[Cleanup] Cancelled approval", "approvalID", approval.ID, "title", approval.Title)
	}

	logging.Info("[Cleanup] Cleanup completed", "approvalsCancelled", cancelledCount, "toolCallsCancelled", toolCallsCancelled)
	return CleanupOutput{ApprovalsCancelled: cancelledCount, ToolCallsCancelled: toolCallsCancelled}, nil
}

// orphanedToolCall holds information about a tool_call that has no matching tool_result
type orphanedToolCall struct {
	ToolCallID string
	ToolName   string
	BlockID    string
}

// cancelOrphanedToolCalls finds tool_call blocks that don't have matching tool_result blocks,
// persists synthetic tool_result blocks to repair the conversation state, and emits cancelled
// status updates for the UI.
//
// This is the primary repair mechanism for orphaned tool calls. It ensures the DB state is
// always valid for branching and LLM context resolution. The CallLLM activity has additional
// defense-in-depth repair logic that handles edge cases.
//
// If thread is specified, only processes tool calls from that specific thread.
func (a *CleanupActivity) cancelOrphanedToolCalls(ctx context.Context, chatID, thread string) int {
	// Get messages from the specific thread if specified, otherwise all chat messages
	opts := db.MessageListOptions{}
	if thread != "" {
		opts.Thread = &thread
	}
	msgs, err := a.repo.ListMessages(ctx, chatID, opts)
	if err != nil {
		logging.Error("[Cleanup] Failed to list messages for tool call cleanup", "chatID", chatID, "thread", thread, "error", err)
		return 0
	}

	// Group orphaned tool calls by their parent assistant message
	// Map: assistant message ID -> list of orphaned tool calls
	orphansByMessage := make(map[string][]orphanedToolCall)
	assistantMessages := make(map[string]*db.Message) // Keep reference for metadata

	// Process messages in reverse order (most recent first) to find orphaned tool calls
	// We only need to check recent assistant messages - older ones should have results
	for i := len(msgs) - 1; i >= 0 && i >= len(msgs)-10; i-- {
		msg := msgs[i]
		if msg.Role != reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT {
			continue
		}

		// Get content blocks for this message
		blocks, err := a.repo.ListContentBlocks(ctx, msg.ID)
		if err != nil {
			logging.Error("[Cleanup] Failed to list content blocks", "messageID", msg.ID, "error", err)
			continue
		}

		// Find tool_call blocks and check if they have matching tool_results
		for _, block := range blocks {
			if block.BlockType != reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL || block.ToolCallID == nil {
				continue
			}

			toolCallID := *block.ToolCallID
			toolName := "unknown"
			if block.ToolName != nil {
				toolName = *block.ToolName
			}

			// Check if there's a matching tool_result
			resultBlock, err := a.repo.GetToolResultBlock(ctx, toolCallID)
			if err != nil {
				logging.Error("[Cleanup] Failed to check for tool result", "toolCallID", toolCallID, "error", err)
				continue
			}
			if resultBlock != nil {
				continue
			}

			// No result block YET is not the same as no result EVER, and this
			// is where that distinction has to be made rather than assumed.
			if a.callIsStillLive(ctx, toolCallID, toolName) {
				continue
			}

			orphansByMessage[msg.ID] = append(orphansByMessage[msg.ID], orphanedToolCall{
				ToolCallID: toolCallID,
				ToolName:   toolName,
				BlockID:    block.ID,
			})
			assistantMessages[msg.ID] = msg
		}
	}

	if len(orphansByMessage) == 0 {
		return 0
	}

	// For each assistant message with orphaned tool calls, create a repair tool message
	cancelledCount := 0
	for msgID, orphans := range orphansByMessage {
		assistantMsg := assistantMessages[msgID]

		// Create the repair tool message and content blocks
		repairMsgID, err := a.createRepairToolMessage(ctx, chatID, assistantMsg, orphans)
		if err != nil {
			logging.Error("[Cleanup] Failed to create repair tool message",
				"error", err,
				"chatID", chatID,
				"assistantMessageID", msgID,
				"orphanCount", len(orphans))
			// Fall back to just emitting UI events
			for _, orphan := range orphans {
				a.emitToolCancelledStatus(ctx, chatID, orphan.BlockID, orphan.ToolCallID, orphan.ToolName)
				cancelledCount++
			}
			continue
		}

		// Emit UI events for each repaired tool call
		for _, orphan := range orphans {
			logging.Info("[Cleanup] Repaired orphaned tool call",
				"toolCallID", orphan.ToolCallID,
				"toolName", orphan.ToolName,
				"blockID", orphan.BlockID,
				"repairMessageID", repairMsgID,
				"thread", thread)
			a.emitToolCancelledStatus(ctx, chatID, orphan.BlockID, orphan.ToolCallID, orphan.ToolName)
			cancelledCount++
		}
	}

	if cancelledCount > 0 {
		logging.Info("[Cleanup] Repaired orphaned tool calls", "count", cancelledCount, "thread", thread)
	}

	return cancelledCount
}

// callIsStillLive reports whether a tool call that has no result block yet is
// nonetheless still running, and therefore must not be declared orphaned.
//
// The absence of a tool_result block was previously the WHOLE test for
// "orphaned". For an in-process tool that is nearly sound: the tool and the
// workflow die together, so if the workflow is over and no result was written,
// none is coming. For a SPAWN it is simply false. A spawn's result is written
// minutes later by a different workflow running on a different thread, which
// the parent's death does not touch — so at the moment cleanup runs, every
// in-flight spawn looks exactly like an orphan.
//
// Cleanup runs from handleWorkflowCompletion on the three abnormal paths
// (cancelled, panic, error), which is precisely when a worker has crashed
// mid-run. Observed on real data: a parent hit a terminal path at 04:54:50
// while two spawn children were writing messages every few seconds; cleanup
// wrote "interrupted — outcome unknown" for both; both children carried on and
// completed SUCCESSFULLY at 05:02 and 05:05, writing their genuine results.
// The conversation was left with two tool_result blocks per call — a
// fabricated failure and a real success — which the tool-pairing validator
// then had to repair in memory on every subsequent load.
//
// Two durable facts already answer the question, and neither was consulted:
//
//	tool_calls.status            — non-terminal means the call is still going
//	tool_calls.child_workflow_id — for a spawn, the workflow actually doing it
//
// Fails CLOSED: if the status cannot be read, the call is treated as live and
// left alone. Skipping a genuinely dead call costs a stuck spinner that the
// next cleanup pass or a later terminal write resolves. Fabricating a failure
// for a live call writes a lie into conversation history that the model then
// reads as fact, and no later pass can distinguish it from a real one.
func (a *CleanupActivity) callIsStillLive(ctx context.Context, toolCallID, toolName string) bool {
	// ListToolCallsByIDs rather than GetToolCall: the latter reports a missing
	// row as an error, which would be indistinguishable from a real read
	// failure — and those two must go opposite ways. "No row exists" means
	// nothing claims the call is live (repair it); "the read failed" means we
	// do not know (leave it alone).
	calls, err := a.repo.ListToolCallsByIDs(ctx, []string{toolCallID})
	if err != nil {
		logging.Warn("[Cleanup] Could not read tool call status; treating as live and skipping repair",
			"toolCallID", toolCallID, "toolName", toolName, "error", err)
		return true
	}
	if len(calls) == 0 {
		// No durable row at all (pre-tool_calls-table history). Nothing claims
		// this call is running, so the missing result is the only evidence
		// available and it stands.
		return false
	}
	call := calls[0]

	if !call.Status.IsTerminal() {
		// A spawn is the case that matters, and it can be checked exactly:
		// ask the workflow that is executing it.
		if call.ChildWorkflowID != nil && *call.ChildWorkflowID != "" {
			if wf, err := a.repo.GetWorkflow(ctx, *call.ChildWorkflowID); err == nil && wf != nil {
				if workflowStatusIsTerminal(wf.Status) {
					// The child is over and still wrote no result: a real orphan.
					return false
				}
			}
		}

		logging.Info("[Cleanup] Tool call has no result yet but is still executing; not repairing",
			"toolCallID", toolCallID, "toolName", toolName, "status", call.Status)
		return true
	}

	return false
}

// workflowStatusIsTerminal reports whether a workflow has reached a state it
// will not leave on its own. PAUSED is deliberately NOT terminal: a paused
// workflow resumes and finishes its work, so its tool calls are still live —
// which is exactly the complement of Live().
func workflowStatusIsTerminal(status core.WorkflowStatus) bool {
	return !status.Live()
}

// createRepairToolMessage creates a tool role message with synthetic tool_result blocks
// for orphaned tool calls. This persists the repair to the database so the conversation
// state is valid for branching and LLM context resolution.
//
// Returns the created message ID on success.
func (a *CleanupActivity) createRepairToolMessage(
	ctx context.Context,
	chatID string,
	assistantMsg *db.Message,
	orphans []orphanedToolCall,
) (string, error) {
	// UTC to match every other persisted timestamp (local time would place
	// repair messages hours in the past and break time-ordering).
	now := time.Now().UTC()
	msgID := uuid.New().String()

	// Get next ordinal for this thread
	// The repair message should come after the assistant message
	nextOrdinal, err := a.repo.GetNextOrdinal(ctx, assistantMsg.ThreadID)
	if err != nil {
		return "", fmt.Errorf("failed to get next ordinal: %w", err)
	}

	// Get next chat-global seq. See 20260802000000_add_message_seq.sql.
	nextSeq, err := a.repo.GetNextSeq(ctx, chatID, assistantMsg.ThreadID)
	if err != nil {
		return "", fmt.Errorf("failed to get next seq: %w", err)
	}

	// Create the tool message with the same context_window_id as the assistant message
	repairMsg := &db.Message{
		ID:              msgID,
		ChatID:          chatID,
		Ordinal:         nextOrdinal,
		Seq:             nextSeq,
		ThreadID:        assistantMsg.ThreadID,
		ContextWindowID: assistantMsg.ContextWindowID,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_TOOL,
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	if err := a.repo.CreateMessage(ctx, repairMsg); err != nil {
		return "", fmt.Errorf("failed to create repair message: %w", err)
	}

	// Create tool_result content blocks for each orphaned tool call
	for i, orphan := range orphans {
		blockID := uuid.New().String()
		isError := true
		content := InterruptedToolResultContent

		block := &db.MessageContentBlock{
			ID:         blockID,
			MessageID:  msgID,
			Position:   i,
			BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT,
			Content:    &content,
			ToolName:   &orphan.ToolName,
			ToolCallID: &orphan.ToolCallID,
			IsError:    &isError,
			IsComplete: true,
			CreatedAt:  now,
			UpdatedAt:  now,
		}

		if err := a.repo.CreateContentBlock(ctx, block); err != nil {
			logging.Error("[Cleanup] Failed to create repair content block",
				"error", err,
				"chatID", chatID,
				"blockID", blockID,
				"toolCallID", orphan.ToolCallID)
			// Continue with other blocks - partial repair is better than none
			continue
		}

		logging.Info("[Cleanup] Created repair tool_result",
			"chatID", chatID,
			"messageID", msgID,
			"toolCallID", orphan.ToolCallID,
			"toolName", orphan.ToolName)
	}

	// Note: We don't emit a chat_update for the repair message itself.
	// The repair message will be loaded naturally when the conversation is fetched.
	// The tool_call cancelled updates (emitted by the caller) are sufficient for UI feedback.

	return msgID, nil
}

// emitToolCancelledStatus emits a tool_call cancelled status update to chat_updates
// This notifies the UI to stop showing the spinner for this tool call
//
// The same transition is recorded durably. chat_updates is a live event stream:
// on its own it stops the spinner for clients currently connected and tells any
// later reader nothing, so the row would fall back to its last durable status
// (EXECUTING) and the call would read as running again after a reload. The
// cancel is a fact about the call, so it belongs on the call.
func (a *CleanupActivity) emitToolCancelledStatus(ctx context.Context, chatID, contentBlockID, toolCallID, toolName string) {
	updateData := map[string]interface{}{
		"update_type":      "tool_call",
		"content_block_id": contentBlockID,
		"tool_call_id":     toolCallID,
		"tool_name":        toolName,
		"status":           "cancelled",
		"timestamp":        time.Now().Format(time.RFC3339),
	}

	updateDataJSON, err := json.Marshal(updateData)
	if err != nil {
		logging.Error("[Cleanup] Failed to marshal tool cancelled update", "error", err, "toolCallID", toolCallID)
		return
	}

	if err := a.repo.CreateChatUpdate(ctx, chatID, db.UpdateTypeToolCall, contentBlockID, string(updateDataJSON)); err != nil {
		logging.Error("[Cleanup] Failed to emit tool cancelled update", "error", err, "toolCallID", toolCallID)
	}

	now := time.Now().UTC()
	if err := db.UpsertToolCallStatus(ctx, a.repo, &core.ToolCall{
		ID:          toolCallID,
		ChatID:      chatID,
		ToolName:    toolName,
		Status:      core.ToolCallStatusCancelled,
		CompletedAt: &now,
		RequestedAt: now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		logging.Error("[Cleanup] Failed to persist tool cancelled status", "error", err, "toolCallID", toolCallID)
	}
}

// NOTE: cancelStreamingToolCalls removed - streaming_delta updates are now ephemeral
// and never persisted to the database, making the streaming tool cleanup obsolete.
