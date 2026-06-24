// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	"go.temporal.io/sdk/client"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/llm/tools/shell"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

type ToolCallService struct {
	reliantv1connect.UnimplementedToolCallServiceHandler
	database   db.Repository
	tempClient client.Client
	router     toolexec.DaemonRouter
}

// NewToolCallService creates a new ToolCallService
func NewToolCallService(database db.Repository, tempClient client.Client, router toolexec.DaemonRouter) *ToolCallService {
	return &ToolCallService{
		database:   database,
		tempClient: tempClient,
		router:     router,
	}
}

// CancelToolCall cancels an executing tool call by setting an in-memory cancel signal and cancelling the Temporal workflow.
func (s *ToolCallService) CancelToolCall(
	ctx context.Context,
	req *connect.Request[reliantv1.CancelToolCallRequest],
) (*connect.Response[reliantv1.CancelToolCallResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("not authenticated"))
	}

	toolCallID := req.Msg.ToolCallId
	if toolCallID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("tool_call_id is required"))
	}

	// Find the content block by tool_call_id
	block, err := s.database.GetContentBlockByToolCallID(ctx, toolCallID)
	if err != nil {
		logging.Error("[CancelToolCall] Failed to find tool call", "error", err, "toolCallID", toolCallID)
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("tool call not found: %s", toolCallID))
	}

	// Get the message to find the chat ID
	msg, err := s.database.GetMessage(ctx, block.MessageID)
	if err != nil {
		logging.Error("[CancelToolCall] Failed to find message for tool call", "error", err, "messageID", block.MessageID)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to find message for tool call"))
	}

	chatID := msg.ChatID

	// Get the chat and verify ownership
	chat, err := s.database.GetChat(ctx, chatID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}
	if chat.UserID != userID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}

	// Set the in-memory signal for immediate detection by running tool
	// This prevents the tool from emitting "completed" status after user clicked cancel
	shell.GetCancelSignal().SetCancelled(toolCallID)

	// Cancel via Temporal for immediate effect if workflow is running
	if chat.WorkflowID != nil && *chat.WorkflowID != "" {
		runID := ""
		if chat.RunID != nil {
			runID = *chat.RunID
		}
		// Errors are logged but don't fail - the in-memory cancel signal is the primary mechanism
		_ = s.tempClient.CancelWorkflow(ctx, *chat.WorkflowID, runID)
	}

	// Emit a tool_call cancelled status update for the UI
	s.emitToolCallCancelled(ctx, chatID, block.ID, toolCallID, getToolName(block))

	sessionID := ""
	if chat.WorkflowID != nil {
		sessionID = *chat.WorkflowID
	}

	return connect.NewResponse(&reliantv1.CancelToolCallResponse{
		Success:    true,
		Message:    "Tool call cancellation requested",
		ToolCallId: toolCallID,
		SessionId:  sessionID,
		ChatId:     chatID,
	}), nil
}

// ConvertToBackground converts an executing tool call to a background process
// This allows the tool to continue running while the workflow can proceed
func (s *ToolCallService) ConvertToBackground(
	ctx context.Context,
	req *connect.Request[reliantv1.ConvertToBackgroundRequest],
) (*connect.Response[reliantv1.ConvertToBackgroundResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("not authenticated"))
	}

	toolCallID := req.Msg.ToolCallId
	if toolCallID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("tool_call_id is required"))
	}

	// Find the content block by tool_call_id
	block, err := s.database.GetContentBlockByToolCallID(ctx, toolCallID)
	if err != nil {
		logging.Error("[ConvertToBackground] Failed to find tool call", "error", err, "toolCallID", toolCallID)
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("tool call not found: %s", toolCallID))
	}

	// Get the message to find the chat ID
	msg, err := s.database.GetMessage(ctx, block.MessageID)
	if err != nil {
		logging.Error("[ConvertToBackground] Failed to find message for tool call", "error", err, "messageID", block.MessageID)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to find message for tool call"))
	}

	chatID := msg.ChatID

	// Get the chat and verify ownership
	chat, err := s.database.GetChat(ctx, chatID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}
	if chat.UserID != userID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}

	// First, check if there's already a background process for this tool call
	bgManager := shell.GetBackgroundManager()
	processes := bgManager.GetProcessesByChat(chatID)
	for _, p := range processes {
		if p.Status == "running" {
			sessionID := ""
			if chat.WorkflowID != nil {
				sessionID = *chat.WorkflowID
			}

			return connect.NewResponse(&reliantv1.ConvertToBackgroundResponse{
				Success:    true,
				Message:    "Tool execution is already running as background process",
				ProcessId:  p.ID,
				ToolCallId: toolCallID,
				SessionId:  sessionID,
				ChatId:     chatID,
			}), nil
		}
	}

	// Set the in-memory signal for immediate detection by running tool
	// This is the primary mechanism since the DB flag may not be visible to the tool executor
	shell.GetBackgroundSignal().SetBackgrounded(toolCallID)

	// Emit a tool_call backgrounded status update for the UI
	s.emitToolCallBackgrounded(ctx, chatID, block.ID, toolCallID, getToolName(block))

	sessionID := ""
	if chat.WorkflowID != nil {
		sessionID = *chat.WorkflowID
	}

	// Return success - the tool executor will handle the actual background conversion
	return connect.NewResponse(&reliantv1.ConvertToBackgroundResponse{
		Success:    true,
		Message:    "Background conversion requested - tool will be converted to background process",
		ProcessId:  "", // Process ID will be assigned when the tool converts
		ToolCallId: toolCallID,
		SessionId:  sessionID,
		ChatId:     chatID,
	}), nil
}

// emitToolCallCancelled emits a tool_call cancelled status update to chat_updates
func (s *ToolCallService) emitToolCallCancelled(ctx context.Context, chatID, contentBlockID, toolCallID, toolName string) {
	update := db.ToolCallUpdate{
		ContentBlockID: contentBlockID,
		ToolCallID:     toolCallID,
		ToolName:       toolName,
		Status:         db.ToolCallStatusCancelled,
	}
	if err := s.database.EmitToolCallCancelledUpdate(ctx, chatID, update); err != nil {
		logging.Error("[CancelToolCall] Failed to create chat update", "error", err)
	}
}

// emitToolCallBackgrounded emits a tool_call backgrounded status update to chat_updates
func (s *ToolCallService) emitToolCallBackgrounded(ctx context.Context, chatID, contentBlockID, toolCallID, toolName string) {
	update := db.ToolCallUpdate{
		ContentBlockID: contentBlockID,
		ToolCallID:     toolCallID,
		ToolName:       toolName,
		Status:         db.ToolCallStatusBackgrounded,
	}
	if err := s.database.EmitToolCallBackgroundedUpdate(ctx, chatID, update); err != nil {
		logging.Error("[ConvertToBackground] Failed to create chat update", "error", err)
	}
}

// getToolName extracts tool name from content block
func getToolName(block *db.MessageContentBlock) string {
	if block.ToolName != nil {
		return *block.ToolName
	}
	return "unknown"
}
