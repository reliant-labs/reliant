// Copyright (c) 2025 Reliant Labs
package approval

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
)

// chatBelongsToUser verifies the chat exists and is owned by the caller.
// Approvals gate tool execution, so acting on one is acting on the chat —
// ownership is transitive through Approval.ChatID. A foreign chat is
// NotFound, never PermissionDenied, so the check cannot be used as a
// cross-tenant existence oracle.
func (s *Service) chatBelongsToUser(ctx context.Context, chatID string) error {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || userID == "" {
		return svcerr.Unauthenticated("user ID not found in context")
	}
	chat, err := s.deps.Repo.GetChatRow(ctx, chatID)
	if err != nil {
		return svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}
	if chat == nil || chat.UserID != userID {
		return svcerr.NotFound("approval")
	}
	return nil
}

// approvalToProto converts a db.Approval to proto Approval
func approvalToProto(a *db.Approval) *reliantv1.Approval {
	proto := &reliantv1.Approval{
		Id:           a.ID,
		ChatId:       a.ChatID,
		ApprovalType: reliantv1.ApprovalType(a.ApprovalType),
		EntityId:     a.EntityID,
		Status:       reliantv1.ApprovalStatus(a.Status),
		Title:        a.Title,
		CreatedAt:    a.CreatedAt.Format(time.RFC3339),
	}
	if a.DenialReason != nil {
		proto.DenialReason = a.DenialReason
	}
	if a.BatchID != nil && *a.BatchID != "" {
		proto.BatchId = a.BatchID
	}

	if a.ResolvedAt != nil {
		resolvedAt := a.ResolvedAt.Format(time.RFC3339)
		proto.ResolvedAt = &resolvedAt
	}

	// Parse and populate structured metadata fields
	if a.Metadata != nil {
		var metadata map[string]interface{}
		if err := json.Unmarshal([]byte(*a.Metadata), &metadata); err == nil {
			if messageID, ok := metadata["message_id"].(string); ok {
				proto.MessageId = &messageID
			}
			if workflowID, ok := metadata["workflow_id"].(string); ok {
				proto.WorkflowId = &workflowID
			}
			if runID, ok := metadata["run_id"].(string); ok {
				proto.RunId = &runID
			}
		}
	}

	return proto
}

// retiredToolApprovalType is the approval_type of a row recorded by the
// removed integration action approval prompt: 1, the reserved
// APPROVAL_TYPE_TOOL. Retire it with runtime's actionApprovalRemovedChangeID.
const retiredToolApprovalType = 1

// answeredByItsRun reports whether denying a reaches the model through its own
// run. A tool-call approval (and a row the removed prompt recorded) asks about
// one call, and the run waiting on it answers that call in ExecuteTools with
// the refusal, so a denial message — a tool_result for every call in the turn,
// including calls that still run — would contradict and duplicate it.
func answeredByItsRun(a *db.Approval) bool {
	return a.ApprovalType == retiredToolApprovalType ||
		a.ApprovalType == int32(reliantv1.ApprovalType_APPROVAL_TYPE_TOOL_CALL)
}

// ListApprovalsByChat lists all pending approvals for a chat
func (s *Service) ListApprovalsByChat(
	ctx context.Context,
	req *connect.Request[reliantv1.ListApprovalsByChatRequest],
) (*connect.Response[reliantv1.ListApprovalsByChatResponse], error) {
	if req.Msg.ChatId == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}

	if err := s.chatBelongsToUser(ctx, req.Msg.ChatId); err != nil {
		return nil, err
	}

	approvals, err := s.deps.Repo.ListPendingApprovalsByChat(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list approvals"))
	}

	protoApprovals := make([]*reliantv1.Approval, len(approvals))
	for i, a := range approvals {
		protoApprovals[i] = approvalToProto(a)
	}

	return connect.NewResponse(&reliantv1.ListApprovalsByChatResponse{
		Approvals: protoApprovals,
		Total:     int32(len(protoApprovals)),
	}), nil
}

// Approve approves a pending approval request
func (s *Service) Approve(
	ctx context.Context,
	req *connect.Request[reliantv1.ApproveRequest],
) (*connect.Response[reliantv1.ApproveResponse], error) {
	if req.Msg.RequestId == "" {
		return nil, svcerr.InvalidArgument("request_id is required")
	}
	if _, _, err := s.resolveApprovals(ctx, []string{req.Msg.RequestId}, decision{approved: true, actionTaken: req.Msg.ActionTaken}); err != nil {
		return nil, err
	}
	return connect.NewResponse(&reliantv1.ApproveResponse{
		Success: true,
		Message: "Request approved",
	}), nil
}

// Deny denies a pending approval request
func (s *Service) Deny(
	ctx context.Context,
	req *connect.Request[reliantv1.DenyRequest],
) (*connect.Response[reliantv1.DenyResponse], error) {
	if req.Msg.RequestId == "" {
		return nil, svcerr.InvalidArgument("request_id is required")
	}
	if _, _, err := s.resolveApprovals(ctx, []string{req.Msg.RequestId}, decision{denialReason: denialReasonOr(req.Msg.DenialReason, "User denied this approval"), actionTaken: req.Msg.ActionTaken}); err != nil {
		return nil, err
	}
	logging.InfoContext(ctx, "Denied request and signalled workflow", "requestID", req.Msg.RequestId)
	return connect.NewResponse(&reliantv1.DenyResponse{
		Success: true,
		Message: "Request denied",
	}), nil
}

// BatchApprove approves every listed approval that is still pending.
func (s *Service) BatchApprove(
	ctx context.Context,
	req *connect.Request[reliantv1.BatchApproveRequest],
) (*connect.Response[reliantv1.BatchApproveResponse], error) {
	if len(req.Msg.RequestIds) == 0 {
		return nil, svcerr.InvalidArgument("request_ids cannot be empty")
	}
	chatID, approved, err := s.resolveApprovals(ctx, req.Msg.RequestIds, decision{approved: true, actionTaken: req.Msg.ActionTaken})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&reliantv1.BatchApproveResponse{
		Success:  true,
		Approved: int32(approved),
		Message:  fmt.Sprintf("Approved %d requests", approved),
		ChatId:   chatID,
	}), nil
}

// BatchDeny denies every listed approval that is still pending.
func (s *Service) BatchDeny(
	ctx context.Context,
	req *connect.Request[reliantv1.BatchDenyRequest],
) (*connect.Response[reliantv1.BatchDenyResponse], error) {
	if len(req.Msg.RequestIds) == 0 {
		return nil, svcerr.InvalidArgument("request_ids cannot be empty")
	}
	chatID, denied, err := s.resolveApprovals(ctx, req.Msg.RequestIds, decision{denialReason: denialReasonOr(req.Msg.DenialReason, "User denied one or more approvals"), actionTaken: req.Msg.ActionTaken})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&reliantv1.BatchDenyResponse{
		Success: true,
		Denied:  int32(denied),
		Message: fmt.Sprintf("Denied %d requests", denied),
		ChatId:  chatID,
	}), nil
}

// decision is what the person decided about one or more approvals.
type decision struct {
	approved bool
	// denialReason is the reason a denial records (denials only).
	denialReason string
	// actionTaken is the button that was clicked, when the client names one.
	actionTaken *string
}

func denialReasonOr(reason *string, fallback string) string {
	if reason != nil && *reason != "" {
		return *reason
	}
	return fallback
}

// resolveApprovals records d on every listed approval that is still pending,
// all in one transaction, then signals each one's waiting run. It returns the
// chat they belong to and how many it resolved.
//
// Every id is checked before anything is written: it must exist, and all of
// them must be one chat the caller owns. An approval already decided — on
// another surface, or by its run timing out — is skipped, read again inside
// the transaction so a decision that lands concurrently is never overwritten.
// Only when none of them was still pending is that an error
// (FailedPrecondition), which is what lets a batch approve what is left of
// itself after one of its approvals was decided elsewhere.
func (s *Service) resolveApprovals(ctx context.Context, ids []string, d decision) (string, int, error) {
	var chatID string
	for _, id := range ids {
		approval, err := s.deps.Repo.GetApproval(ctx, id)
		if err != nil {
			return "", 0, svcerr.WrapOr(err, svcerr.Internal("failed to get approval"))
		}
		if approval == nil {
			return "", 0, svcerr.NotFound("approval")
		}
		// The same-chat rule makes one ownership check cover every id.
		if chatID == "" {
			chatID = approval.ChatID
			if err := s.chatBelongsToUser(ctx, chatID); err != nil {
				return "", 0, err
			}
		} else if chatID != approval.ChatID {
			return "", 0, svcerr.InvalidArgument("all approvals must belong to the same chat")
		}
	}

	status := reliantv1.ApprovalStatus_APPROVAL_STATUS_APPROVED
	statusName := "approved"
	var denialReason *string
	if !d.approved {
		status = reliantv1.ApprovalStatus_APPROVAL_STATUS_DENIED
		statusName = "denied"
		denialReason = &d.denialReason
	}
	var actionTaken *string
	if d.actionTaken != nil && *d.actionTaken != "" {
		actionTaken = d.actionTaken
	}

	var resolved []*db.Approval
	err := s.deps.Repo.RunTx(ctx, orm.Serializable, func(txCtx context.Context) error {
		resolved = nil
		for _, id := range ids {
			approval, err := s.deps.Repo.GetApproval(txCtx, id)
			if err != nil {
				return err
			}
			if approval == nil || approval.Status != int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING) {
				continue
			}
			if err := s.deps.Repo.UpdateApprovalStatus(txCtx, approval.ID, int32(status), denialReason, actionTaken, nil); err != nil {
				return fmt.Errorf("failed to update approval status: %w", err)
			}
			updateData := map[string]interface{}{
				"update_type":   "approval",
				"id":            approval.ID,
				"approval_type": approval.ApprovalType,
				"entity_id":     approval.EntityID,
				"status":        statusName,
				"title":         approval.Title,
				"resolved_at":   time.Now().Format(time.RFC3339),
			}
			if denialReason != nil {
				updateData["denial_reason"] = *denialReason
			}
			if actionTaken != nil {
				updateData["action_taken"] = *actionTaken
			}
			updateDataJSON, err := json.Marshal(updateData)
			if err != nil {
				return fmt.Errorf("failed to marshal chat_update data: %w", err)
			}
			if err := s.deps.Repo.CreateChatUpdate(txCtx, approval.ChatID, db.UpdateTypeApproval, approval.ID, string(updateDataJSON)); err != nil {
				return fmt.Errorf("failed to create chat_update: %w", err)
			}
			resolved = append(resolved, approval)
		}
		return nil
	})
	if err != nil {
		return "", 0, svcerr.WrapOr(err, svcerr.Internal("failed to resolve approval"))
	}
	if len(resolved) == 0 {
		return "", 0, svcerr.FailedPrecondition("approval already processed")
	}

	// One denial message for the chat, unless every denied approval is
	// answered by its own run.
	if !d.approved {
		for _, approval := range resolved {
			if !answeredByItsRun(approval) {
				if err := s.createDenialMessage(ctx, chatID, d.denialReason); err != nil {
					logging.ErrorContext(ctx, "Failed to create denial message", "error", err, "chatID", chatID)
				}
				break
			}
		}
	}

	signal := map[string]interface{}{"status": statusName, "action_taken": ""}
	if actionTaken != nil {
		signal["action_taken"] = *actionTaken
	}
	if denialReason != nil {
		signal["denial_reason"] = *denialReason
	}
	for _, approval := range resolved {
		s.signalApproval(ctx, approval, signal)
	}
	return chatID, len(resolved), nil
}

// signalApproval sends a signal to the workflow using PauseService.SignalWithRecovery
func (s *Service) signalApproval(ctx context.Context, approval *db.Approval, signalData map[string]interface{}) {
	if approval.TemporalWorkflowID == "" {
		logging.WarnContext(ctx, "[Approval] No temporal_workflow_id on approval, cannot signal", "approvalID", approval.ID)
		return
	}
	signalName := "signal.approval." + approval.ID
	if err := s.deps.Signals.SignalWithRecovery(ctx, approval.TemporalWorkflowID, signalName, signalData, nil); err != nil {
		logging.WarnContext(ctx, "[Approval] Failed to signal approval resolution",
			"error", err,
			"approvalID", approval.ID,
			"temporalWorkflowID", approval.TemporalWorkflowID,
		)
	}
}

// createDenialMessage creates a tool message with denied tool results for the given chat
func (s *Service) createDenialMessage(ctx context.Context, chatID string, denialReason string) error {

	// Get all messages to find the last assistant message with tool_calls
	msgs, err := s.deps.Repo.ListMessages(ctx, chatID, db.MessageListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list messages: %w", err)
	}

	// Find the last assistant message
	var lastAssistantMsg *db.Message
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT {
			lastAssistantMsg = msgs[i]
			break
		}
	}

	if lastAssistantMsg == nil {
		logging.DebugContext(ctx, "[Approval] No assistant message found for denial", "chatID", chatID)
		return nil
	}

	// Get content blocks for the assistant message to find tool_calls
	blocks, err := s.deps.Repo.ListContentBlocks(ctx, lastAssistantMsg.ID)
	if err != nil {
		return fmt.Errorf("failed to list content blocks: %w", err)
	}

	// Filter for tool_call blocks
	var toolCallBlocks []*db.MessageContentBlock
	for _, block := range blocks {
		if block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL && block.ToolCallID != nil {
			toolCallBlocks = append(toolCallBlocks, block)
		}
	}

	if len(toolCallBlocks) == 0 {
		logging.DebugContext(ctx, "[Approval] No tool_call blocks found in assistant message", "chatID", chatID, "messageID", lastAssistantMsg.ID)
		return nil
	}

	logging.DebugContext(ctx, "[Approval] Found tool_call blocks to deny", "count", len(toolCallBlocks), "chatID", chatID)

	// Create the denial message and content blocks atomically
	now := time.Now().UTC()
	messageID := uuid.New().String()
	isError := true
	denialContent := denialReason

	err = s.deps.Repo.RunTx(ctx, orm.Serializable, func(txCtx context.Context) error {
		// Create the tool message (same context_window as the assistant message)
		msg := &db.Message{
			ID:              messageID,
			ChatID:          chatID,
			ThreadID:        lastAssistantMsg.ThreadID,
			ContextWindowID: lastAssistantMsg.ContextWindowID,
			Role:            reliantv1.MessageRole_MESSAGE_ROLE_TOOL,
			CreatedAt:       now,
			UpdatedAt:       now,
		}

		// The next ordinal and seq are allocated with the insert.
		if err := s.deps.Repo.CreateNumberedMessage(txCtx, msg); err != nil {
			return fmt.Errorf("failed to create denial message: %w", err)
		}

		// Create a text content block with the denial notice
		textBlockID := uuid.New().String()
		denialNotice := "User denied one or more tool calls"
		version := 1
		textBlock := &db.MessageContentBlock{
			ID:        textBlockID,
			MessageID: messageID,
			Position:  0,
			BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
			Content:   &denialNotice,
			Version:   &version,
			CreatedAt: now,
			UpdatedAt: now,
		}

		if err := s.deps.Repo.CreateContentBlock(txCtx, textBlock); err != nil {
			return fmt.Errorf("failed to create text content block: %w", err)
		}

		// Create tool_result blocks for each denied tool_call
		for i, toolCallBlock := range toolCallBlocks {
			blockID := uuid.New().String()
			block := &db.MessageContentBlock{
				ID:         blockID,
				MessageID:  messageID,
				Position:   i + 1, // Start after the text block
				BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT,
				ToolCallID: toolCallBlock.ToolCallID,
				Content:    &denialContent,
				IsError:    &isError,
				Version:    &version,
				CreatedAt:  now,
				UpdatedAt:  now,
			}

			if err := s.deps.Repo.CreateContentBlock(txCtx, block); err != nil {
				return fmt.Errorf("failed to create tool_result block %d: %w", i, err)
			}
		}

		// Create chat_update for the new message
		contentBlocksData := make([]map[string]interface{}, 0, len(toolCallBlocks)+1)

		// Add text block
		contentBlocksData = append(contentBlocksData, map[string]interface{}{
			"id":       textBlockID,
			"type":     "text",
			"content":  denialNotice,
			"position": 0,
		})

		// Add tool_result blocks
		for i, toolCallBlock := range toolCallBlocks {
			contentBlocksData = append(contentBlocksData, map[string]interface{}{
				"id":           uuid.New().String(),
				"type":         "tool_result",
				"tool_call_id": *toolCallBlock.ToolCallID,
				"content":      denialContent,
				"is_error":     true,
				"position":     i + 1,
			})
		}

		// seq is what the client sorts by; without it this message
		// deserializes at seq 0 and jumps to the top of the transcript.
		updateData := db.MessageUpdateData{
			UpdateType:    "message",
			ID:            messageID,
			ChatID:        chatID,
			Seq:           msg.Seq,
			Ordinal:       msg.Ordinal,
			Thread:        lastAssistantMsg.ThreadID,
			Role:          "tool",
			ContentBlocks: contentBlocksData,
			CreatedAt:     now.Format(time.RFC3339),
		}

		updateDataJSON, err := json.Marshal(updateData)
		if err != nil {
			return fmt.Errorf("failed to marshal chat_update data: %w", err)
		}

		if err := s.deps.Repo.CreateChatUpdate(txCtx, chatID, db.UpdateTypeMessage, messageID, string(updateDataJSON)); err != nil {
			return fmt.Errorf("failed to create chat_update: %w", err)
		}

		return nil
	})

	if err != nil {
		return err
	}

	logging.DebugContext(ctx, "[Approval] Created denial message",
		"chatID", chatID, "messageID", messageID, "toolCallCount", len(toolCallBlocks))

	return nil
}
