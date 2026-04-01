// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"go.temporal.io/sdk/client"

	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/logging"
)

// ApprovalService implements the ApprovalService RPC handlers
type ApprovalService struct {
	reliantv1connect.UnimplementedApprovalServiceHandler
	database   db.Repository
	tempClient client.Client
}

// NewApprovalService creates a new ApprovalService
func NewApprovalService(database db.Repository, tempClient client.Client) *ApprovalService {
	return &ApprovalService{
		database:   database,
		tempClient: tempClient,
	}
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

	if a.ResolvedAt != nil {
		resolvedAt := a.ResolvedAt.Format(time.RFC3339)
		proto.ResolvedAt = &resolvedAt
	}

	// Parse and populate structured metadata fields
	if a.Metadata != nil {
		var metadata map[string]interface{}
		if err := json.Unmarshal([]byte(*a.Metadata), &metadata); err == nil {
			if toolName, ok := metadata["tool_name"].(string); ok {
				proto.ToolName = &toolName
			}
			if toolCallID, ok := metadata["tool_call_id"].(string); ok {
				proto.ToolCallId = &toolCallID
			}
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

// ListApprovalsByChat lists all pending approvals for a chat
func (s *ApprovalService) ListApprovalsByChat(
	ctx context.Context,
	req *connect.Request[reliantv1.ListApprovalsByChatRequest],
) (*connect.Response[reliantv1.ListApprovalsByChatResponse], error) {
	if req.Msg.ChatId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("chat_id is required"))
	}

	approvals, err := s.database.ListPendingApprovalsByChat(ctx, req.Msg.ChatId)
	if err != nil {
		logging.Error("Failed to list approvals", "error", err, "chatID", req.Msg.ChatId)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list approvals"))
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
func (s *ApprovalService) Approve(
	ctx context.Context,
	req *connect.Request[reliantv1.ApproveRequest],
) (*connect.Response[reliantv1.ApproveResponse], error) {
	if req.Msg.RequestId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("request_id is required"))
	}

	// Get the approval
	approval, err := s.database.GetApproval(ctx, req.Msg.RequestId)
	if err != nil || approval == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("approval not found"))
	}

	if approval.Status != int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("approval already processed"))
	}

	// Get action_taken from request
	var actionTaken *string
	if req.Msg.ActionTaken != nil && *req.Msg.ActionTaken != "" {
		actionTaken = req.Msg.ActionTaken
	}

	// Dual-write: Update approval status AND create chat_update atomically
	err = s.database.RunTx(ctx, func(txCtx context.Context) error {
		// Update approval status
		if err := s.database.UpdateApprovalStatus(txCtx, approval.ID, int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_APPROVED), nil, actionTaken, nil); err != nil {
			return fmt.Errorf("failed to update approval status: %w", err)
		}

		// Build chat_update data
		updateData := map[string]interface{}{
			"update_type":   "approval",
			"id":            approval.ID,
			"approval_type": approval.ApprovalType,
			"entity_id":     approval.EntityID,
			"status":        "approved",
			"title":         approval.Title,
			"resolved_at":   time.Now().Format(time.RFC3339),
		}

		if actionTaken != nil {
			updateData["action_taken"] = *actionTaken
		}

		// Marshal update data
		updateDataJSON, err := json.Marshal(updateData)
		if err != nil {
			return fmt.Errorf("failed to marshal chat_update data: %w", err)
		}

		// Create chat_update in same transaction
		if err := s.database.CreateChatUpdate(txCtx, approval.ChatID, db.UpdateTypeApproval, approval.ID, string(updateDataJSON)); err != nil {
			return fmt.Errorf("failed to create chat_update: %w", err)
		}

		return nil
	})

	if err != nil {
		logging.Error("Failed to approve request", "error", err, "requestID", req.Msg.RequestId)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to approve"))
	}

	// Signal the workflow
	s.signalWorkflow(ctx, approval, "approved", "", actionTaken)

	return connect.NewResponse(&reliantv1.ApproveResponse{
		Success: true,
		Message: "Request approved",
	}), nil
}

// Deny denies a pending approval request
func (s *ApprovalService) Deny(
	ctx context.Context,
	req *connect.Request[reliantv1.DenyRequest],
) (*connect.Response[reliantv1.DenyResponse], error) {
	if req.Msg.RequestId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("request_id is required"))
	}

	// Get the approval
	approval, err := s.database.GetApproval(ctx, req.Msg.RequestId)
	if err != nil || approval == nil {
		logging.Error("Failed to get approval", "error", err, "requestID", req.Msg.RequestId)
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("approval not found"))
	}

	if approval.Status != int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("approval already processed"))
	}

	// Use default denial reason if not provided
	denialReason := "User denied this tool call"
	if req.Msg.DenialReason != nil && *req.Msg.DenialReason != "" {
		denialReason = *req.Msg.DenialReason
	}
	denialReasonPtr := &denialReason

	// Get action_taken from request
	var actionTaken *string
	if req.Msg.ActionTaken != nil && *req.Msg.ActionTaken != "" {
		actionTaken = req.Msg.ActionTaken
	}

	// Dual-write: Update approval status AND create chat_update atomically
	err = s.database.RunTx(ctx, func(txCtx context.Context) error {
		// Update approval status
		if err := s.database.UpdateApprovalStatus(txCtx, approval.ID, int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_DENIED), denialReasonPtr, actionTaken, nil); err != nil {
			return fmt.Errorf("failed to update approval status: %w", err)
		}

		// Build chat_update data
		updateData := map[string]interface{}{
			"update_type":   "approval",
			"id":            approval.ID,
			"approval_type": approval.ApprovalType,
			"entity_id":     approval.EntityID,
			"status":        "denied",
			"title":         approval.Title,
			"denial_reason": denialReason,
			"resolved_at":   time.Now().Format(time.RFC3339),
		}

		if actionTaken != nil {
			updateData["action_taken"] = *actionTaken
		}

		// Marshal update data
		updateDataJSON, err := json.Marshal(updateData)
		if err != nil {
			return fmt.Errorf("failed to marshal chat_update data: %w", err)
		}

		// Create chat_update in same transaction
		if err := s.database.CreateChatUpdate(txCtx, approval.ChatID, db.UpdateTypeApproval, approval.ID, string(updateDataJSON)); err != nil {
			return fmt.Errorf("failed to create chat_update: %w", err)
		}

		return nil
	})

	if err != nil {
		logging.Error("Failed to deny request", "error", err, "approvalID", approval.ID)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to deny"))
	}

	// Create denial message with tool_results marked as denied
	if err := s.createDenialMessage(ctx, approval.ChatID, denialReason); err != nil {
		logging.Error("Failed to create denial message", "error", err, "approvalID", approval.ID)
		// Don't fail the request, just log the error
	}

	// Cancel the workflow instead of signaling
	s.cancelWorkflow(ctx, approval)

	logging.Info("Denied request and cancelled workflow", "requestID", req.Msg.RequestId, "reason", denialReason)

	return connect.NewResponse(&reliantv1.DenyResponse{
		Success: true,
		Message: "Request denied",
	}), nil
}

// BatchApprove approves multiple pending approval requests at once
func (s *ApprovalService) BatchApprove(
	ctx context.Context,
	req *connect.Request[reliantv1.BatchApproveRequest],
) (*connect.Response[reliantv1.BatchApproveResponse], error) {
	if len(req.Msg.RequestIds) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("request_ids cannot be empty"))
	}

	// Get action_taken from request
	var actionTaken *string
	if req.Msg.ActionTaken != nil && *req.Msg.ActionTaken != "" {
		actionTaken = req.Msg.ActionTaken
	}

	// Process each approval
	var chatID string
	for _, requestID := range req.Msg.RequestIds {
		approval, err := s.database.GetApproval(ctx, requestID)
		if err != nil || approval == nil {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("approval not found: %s", requestID))
		}

		// Validate all approvals belong to the same chat
		if chatID == "" {
			chatID = approval.ChatID
		} else if chatID != approval.ChatID {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("all approvals must belong to the same chat"))
		}

		if approval.Status != int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING) {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("approval %s is not pending", requestID))
		}

		// Dual-write: Update approval status AND create chat_update atomically
		err = s.database.RunTx(ctx, func(txCtx context.Context) error {
			// Update approval status
			if err := s.database.UpdateApprovalStatus(txCtx, approval.ID, int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_APPROVED), nil, actionTaken, nil); err != nil {
				return fmt.Errorf("failed to update approval status: %w", err)
			}

			// Build chat_update data
			updateData := map[string]interface{}{
				"update_type":   "approval",
				"id":            approval.ID,
				"approval_type": approval.ApprovalType,
				"entity_id":     approval.EntityID,
				"status":        "approved",
				"title":         approval.Title,
				"resolved_at":   time.Now().Format(time.RFC3339),
			}

			if actionTaken != nil {
				updateData["action_taken"] = *actionTaken
			}

			// Marshal update data
			updateDataJSON, err := json.Marshal(updateData)
			if err != nil {
				return fmt.Errorf("failed to marshal chat_update data: %w", err)
			}

			// Create chat_update in same transaction
			if err := s.database.CreateChatUpdate(txCtx, approval.ChatID, db.UpdateTypeApproval, approval.ID, string(updateDataJSON)); err != nil {
				return fmt.Errorf("failed to create chat_update: %w", err)
			}

			return nil
		})

		if err != nil {
			logging.Error("Failed to update approval in batch", "requestID", requestID, "error", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to approve"))
		}

		// Signal workflow
		s.signalWorkflow(ctx, approval, "approved", "", actionTaken)
	}

	return connect.NewResponse(&reliantv1.BatchApproveResponse{
		Success:  true,
		Approved: int32(len(req.Msg.RequestIds)),
		Message:  fmt.Sprintf("Approved %d requests", len(req.Msg.RequestIds)),
	}), nil
}

// BatchDeny denies multiple pending approval requests at once
func (s *ApprovalService) BatchDeny(
	ctx context.Context,
	req *connect.Request[reliantv1.BatchDenyRequest],
) (*connect.Response[reliantv1.BatchDenyResponse], error) {
	if len(req.Msg.RequestIds) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("request_ids cannot be empty"))
	}

	// Use default denial reason if not provided
	denialReason := "User denied one or more tool calls"
	if req.Msg.DenialReason != nil && *req.Msg.DenialReason != "" {
		denialReason = *req.Msg.DenialReason
	}
	denialReasonPtr := &denialReason

	// Get action_taken from request
	var actionTaken *string
	if req.Msg.ActionTaken != nil && *req.Msg.ActionTaken != "" {
		actionTaken = req.Msg.ActionTaken
	}

	// Process each approval
	var chatID string
	for _, requestID := range req.Msg.RequestIds {
		approval, err := s.database.GetApproval(ctx, requestID)
		if err != nil || approval == nil {
			logging.Error("Approval not found in batch", "requestID", requestID)
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("approval not found: %s", requestID))
		}

		// Validate all approvals belong to the same chat
		if chatID == "" {
			chatID = approval.ChatID
		} else if chatID != approval.ChatID {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("all approvals must belong to the same chat"))
		}

		if approval.Status != int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING) {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("approval %s is not pending", requestID))
		}

		// Dual-write: Update approval status AND create chat_update atomically
		err = s.database.RunTx(ctx, func(txCtx context.Context) error {
			// Update approval status
			if err := s.database.UpdateApprovalStatus(txCtx, approval.ID, int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_DENIED), denialReasonPtr, actionTaken, nil); err != nil {
				return fmt.Errorf("failed to update approval status: %w", err)
			}

			// Build chat_update data
			updateData := map[string]interface{}{
				"update_type":   "approval",
				"id":            approval.ID,
				"approval_type": approval.ApprovalType,
				"entity_id":     approval.EntityID,
				"status":        "denied",
				"title":         approval.Title,
				"denial_reason": denialReason,
				"resolved_at":   time.Now().Format(time.RFC3339),
			}

			if actionTaken != nil {
				updateData["action_taken"] = *actionTaken
			}

			// Marshal update data
			updateDataJSON, err := json.Marshal(updateData)
			if err != nil {
				return fmt.Errorf("failed to marshal chat_update data: %w", err)
			}

			// Create chat_update in same transaction
			if err := s.database.CreateChatUpdate(txCtx, approval.ChatID, db.UpdateTypeApproval, approval.ID, string(updateDataJSON)); err != nil {
				return fmt.Errorf("failed to create chat_update: %w", err)
			}

			return nil
		})

		if err != nil {
			logging.Error("Failed to update approval in batch", "requestID", requestID, "error", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to deny"))
		}

		// Cancel workflow (only once - they should all be same chat/workflow)
		s.cancelWorkflow(ctx, approval)
	}

	// Create denial message once for the chat (all approvals are same chat)
	if chatID != "" {
		if err := s.createDenialMessage(ctx, chatID, denialReason); err != nil {
			logging.Error("Failed to create denial message", "error", err, "chatID", chatID)
			// Don't fail the request, just log the error
		}
	}

	return connect.NewResponse(&reliantv1.BatchDenyResponse{
		Success: true,
		Denied:  int32(len(req.Msg.RequestIds)),
		Message: fmt.Sprintf("Denied %d requests", len(req.Msg.RequestIds)),
	}), nil
}

// signalWorkflow sends a signal to the workflow if metadata contains workflow_id and run_id
func (s *ApprovalService) signalWorkflow(ctx context.Context, approval *db.Approval, status string, denialReason string, actionTaken *string) {
	if approval.Metadata == nil {
		return
	}

	var metadata map[string]interface{}
	if err := json.Unmarshal([]byte(*approval.Metadata), &metadata); err != nil {
		logging.Warn("[Approval] Failed to parse approval metadata", "approvalID", approval.ID, "error", err)
		return
	}

	workflowID, hasWorkflowID := metadata["workflow_id"].(string)
	runID, hasRunID := metadata["run_id"].(string)

	if !hasWorkflowID || !hasRunID {
		return
	}

	// Send signal using approval-specific signal name
	signalName := "approval-" + approval.ID
	signalData := map[string]interface{}{
		"approval_id": approval.ID,
		"status":      status,
	}
	if denialReason != "" {
		signalData["denial_reason"] = denialReason
	}
	if actionTaken != nil {
		signalData["action_taken"] = *actionTaken
	}

	err := s.tempClient.SignalWorkflow(ctx, workflowID, runID, signalName, signalData)
	if err != nil {
		logging.Error("[Approval] Failed to send signal to workflow", "error", err,
			"workflowID", workflowID, "runID", runID, "signalName", signalName)
	}
}

// cancelWorkflow cancels the workflow associated with the approval
func (s *ApprovalService) cancelWorkflow(ctx context.Context, approval *db.Approval) {
	if approval.Metadata == nil {
		return
	}

	var metadata map[string]interface{}
	if err := json.Unmarshal([]byte(*approval.Metadata), &metadata); err != nil {
		logging.Warn("[Approval] Failed to parse approval metadata", "approvalID", approval.ID, "error", err)
		return
	}

	workflowID, hasWorkflowID := metadata["workflow_id"].(string)
	runID, hasRunID := metadata["run_id"].(string)

	if !hasWorkflowID || !hasRunID {
		return
	}

	err := s.tempClient.CancelWorkflow(ctx, workflowID, runID)
	if err != nil {
		logging.Error("[Approval] Failed to cancel workflow", "error", err,
			"workflowID", workflowID, "runID", runID)
	}
}

// createDenialMessage creates a tool message with denied tool results for the given chat
func (s *ApprovalService) createDenialMessage(ctx context.Context, chatID string, denialReason string) error {

	// Get all messages to find the last assistant message with tool_calls
	msgs, err := s.database.ListMessages(ctx, chatID, db.MessageListOptions{})
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
		logging.Debug("[Approval] No assistant message found for denial", "chatID", chatID)
		return nil
	}

	// Get content blocks for the assistant message to find tool_calls
	blocks, err := s.database.ListContentBlocks(ctx, lastAssistantMsg.ID)
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
		logging.Debug("[Approval] No tool_call blocks found in assistant message", "chatID", chatID, "messageID", lastAssistantMsg.ID)
		return nil
	}

	logging.Debug("[Approval] Found tool_call blocks to deny", "count", len(toolCallBlocks), "chatID", chatID)

	// Get next ordinal for the chat
	nextOrdinal, err := s.database.GetNextOrdinal(ctx, lastAssistantMsg.ThreadID)
	if err != nil {
		return fmt.Errorf("failed to get next ordinal: %w", err)
	}

	// Create the denial message and content blocks atomically
	now := time.Now().UTC()
	messageID := uuid.New().String()
	isError := true
	denialContent := denialReason

	err = s.database.RunTx(ctx, func(txCtx context.Context) error {
		// Create the tool message (same context_window as the assistant message)
		msg := &db.Message{
			ID:              messageID,
			ChatID:          chatID,
			Ordinal:         nextOrdinal,
			ThreadID:        lastAssistantMsg.ThreadID,
			ContextWindowID: lastAssistantMsg.ContextWindowID,
			Role:            reliantv1.MessageRole_MESSAGE_ROLE_TOOL,
			CreatedAt:       now,
			UpdatedAt:       now,
		}

		if err := s.database.CreateMessage(txCtx, msg); err != nil {
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

		if err := s.database.CreateContentBlock(txCtx, textBlock); err != nil {
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

			if err := s.database.CreateContentBlock(txCtx, block); err != nil {
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

		updateData := map[string]interface{}{
			"update_type":    "message",
			"id":             messageID,
			"chat_id":        chatID,
			"ordinal":        nextOrdinal,
			"thread":         lastAssistantMsg.ThreadID,
			"role":           "tool",
			"content_blocks": contentBlocksData,
			"created_at":     now.Format(time.RFC3339),
		}

		updateDataJSON, err := json.Marshal(updateData)
		if err != nil {
			return fmt.Errorf("failed to marshal chat_update data: %w", err)
		}

		if err := s.database.CreateChatUpdate(txCtx, chatID, db.UpdateTypeMessage, messageID, string(updateDataJSON)); err != nil {
			return fmt.Errorf("failed to create chat_update: %w", err)
		}

		return nil
	})

	if err != nil {
		return err
	}

	logging.Debug("[Approval] Created denial message",
		"chatID", chatID, "messageID", messageID, "toolCallCount", len(toolCallBlocks))

	return nil
}
