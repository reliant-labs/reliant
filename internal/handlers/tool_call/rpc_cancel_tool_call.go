package tool_call

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
)

// CancelToolCall cancels an executing tool call by setting an in-memory cancel signal and cancelling the Temporal workflow.
func (s *Service) CancelToolCall(
	ctx context.Context,
	req *connect.Request[reliantv1.CancelToolCallRequest],
) (*connect.Response[reliantv1.CancelToolCallResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, svcerr.Unauthenticated("not authenticated")
	}

	toolCallID := req.Msg.ToolCallId
	if toolCallID == "" {
		return nil, svcerr.InvalidArgument("tool_call_id is required")
	}

	// Find the content block by tool_call_id
	block, err := s.deps.Repo.GetContentBlockByToolCallID(ctx, toolCallID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get tool call"))
	}

	// Get the message to find the chat ID
	msg, err := s.deps.Repo.GetMessage(ctx, block.MessageID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to find message for tool call"))
	}

	chatID := msg.ChatID

	// Get the chat and verify ownership
	chat, err := s.deps.Repo.GetChatRow(ctx, chatID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}
	if !authz.CanWriteChat(ctx, userID, chat) {
		return nil, svcerr.NotFound("chat")
	}

	// Ask the daemon to stop this one execution.
	//
	// This used to cancel the chat's whole Temporal workflow. That is the
	// wrong blast radius: every tool call in an LLM turn executes as parallel
	// goroutines inside a SINGLE ExecuteTools activity, sharing one context.
	// Cancelling the workflow cancels that shared context, so every sibling
	// tool saw ctx.Err() != nil and reported itself cancelled -- including
	// ones that had already finished successfully. Cancelling one tool must
	// not decide the outcome of the others.
	//
	// The daemon registers a per-execution cancel func under the tool call id,
	// so this reaches exactly the one running tool. Best-effort by design: if
	// the daemon is offline or the tool runs server-side, the tool runs to
	// completion and reports its real outcome.
	if s.deps.Router != nil {
		if err := s.deps.Router.SendToolExecutionCancel(ctx, userID, toolCallID, "user cancelled tool call"); err != nil {
			logging.WarnContext(ctx, "[CancelToolCall] Could not deliver cancel to daemon; relying on cancel signal",
				"error", err, "toolCallID", toolCallID)
		}
	}

	// A spawn does not run where any of the above can reach it. If that
	// delivery fails the spawn is still running, so report it rather than
	// marking the call cancelled — a cancel that says it worked and didn't is
	// worse than one that admits it couldn't.
	if err := s.cancelChildWorkflowForToolCall(ctx, toolCallID); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Unavailable("could not cancel this spawn"))
	}

	// Emit a tool_call cancelled status update for the UI, and record the same
	// transition durably so it survives a reload.
	s.emitToolCallCancelled(ctx, chatID, toolCallID, getToolName(block))
	s.persistToolCallStatus(ctx, chatID, toolCallID, block, msg, core.ToolCallStatusCancelled, "")

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

// emitToolCallCancelled emits a tool_call cancelled status update to chat_updates.
// Keyed by the LLM tool-call id, matching every other tool-status emitter — the
// block UUID this handler also has on hand is a different identifier space and
// no status consumer looks it up.
func (s *Service) emitToolCallCancelled(ctx context.Context, chatID, toolCallID, toolName string) {
	update := db.ToolCallUpdate{
		ToolCallID: toolCallID,
		ToolName:   toolName,
		Status:     db.ToolCallStatusCancelled,
	}
	if err := s.deps.Repo.EmitToolCallCancelledUpdate(ctx, chatID, update); err != nil {
		logging.ErrorContext(ctx, "[CancelToolCall] Failed to create chat update", "error", err)
	}
}
