package tool_call

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/daemonerr"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/tools/shell"
	"github.com/reliant-labs/reliant/internal/logging"
)

// ConvertToBackground converts an executing tool call to a background process
// This allows the tool to continue running while the workflow can proceed
func (s *Service) ConvertToBackground(
	ctx context.Context,
	req *connect.Request[reliantv1.ConvertToBackgroundRequest],
) (*connect.Response[reliantv1.ConvertToBackgroundResponse], error) {
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
	chat, err := s.deps.Chats.GetChatRow(ctx, chatID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}
	if !authz.CanWriteChat(ctx, userID, chat) {
		return nil, svcerr.NotFound("chat")
	}

	// First, check if there's already a background process for this tool call
	bgManager := shell.GetBackgroundManager()
	processes := bgManager.GetProcessesByChat(chatID)
	for _, p := range processes {
		if p.Status == "running" {
			// The call is already backgrounded — record that durably with the
			// process id this response reports. The id is the chat's running
			// process rather than one matched to this tool call; that
			// attribution is what the API has always returned here.
			s.persistToolCallStatus(ctx, chatID, toolCallID, block, msg, core.ToolCallStatusBackgrounded, p.ID)

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

	// The command runs in the DAEMON — a separate process, possibly on another
	// machine — so the request has to cross the wire. The in-memory signal
	// below is kept only for an in-process executor; on its own it does
	// nothing, which is exactly what this endpoint used to do: mark the row
	// BACKGROUNDED, report success, and leave the command running in the
	// foreground with a NULL background_process_id.
	shell.GetBackgroundSignal().SetBackgrounded(toolCallID)

	if s.deps.Router == nil {
		return nil, svcerr.Unavailable("cannot background: no daemon router configured")
	}
	// Addressed by tool call id: that is the only id the UI can name, and the
	// daemon registers each execution under it (see runtime.go).
	if err := s.deps.Router.SendToolExecutionBackground(ctx, userID, toolCallID, toolCallID); err != nil {
		// Do not claim success. The command is still running in the foreground,
		// and reporting otherwise is what made this look like a working feature
		// for months.
		return nil, daemonerr.Error("background this command", err)
	}

	// Emit a tool_call backgrounded status update for the UI, and record the
	// same transition durably. The real process id arrives with the tool
	// result once the daemon has adopted the process.
	s.emitToolCallBackgrounded(ctx, chatID, toolCallID, getToolName(block))
	s.persistToolCallStatus(ctx, chatID, toolCallID, block, msg, core.ToolCallStatusBackgrounded, "")

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

// emitToolCallBackgrounded emits a tool_call backgrounded status update to chat_updates
func (s *Service) emitToolCallBackgrounded(ctx context.Context, chatID, toolCallID, toolName string) {
	update := db.ToolCallUpdate{
		ToolCallID: toolCallID,
		ToolName:   toolName,
		Status:     db.ToolCallStatusBackgrounded,
	}
	if err := s.deps.Repo.EmitToolCallBackgroundedUpdate(ctx, chatID, update); err != nil {
		logging.ErrorContext(ctx, "[ConvertToBackground] Failed to create chat update", "error", err)
	}
}
