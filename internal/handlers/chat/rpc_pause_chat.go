package chat

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/threads"
)

// PauseChat pauses the running workflow by sending a pause signal and cancelling in-flight activities.
// The workflow stays alive in Temporal (blocked at the next step boundary) and is resumable.
// In-flight activities are cancelled immediately via the shared cancellable activity context.
func (s *Service) PauseChat(
	ctx context.Context,
	req *connect.Request[reliantv1.PauseChatRequest],
) (*connect.Response[reliantv1.PauseChatResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}

	// Get chat to verify ownership
	chat, err := s.deps.Repo.GetChatRow(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}
	if !authz.CanWriteChat(ctx, userID, chat) {
		return nil, svcerr.NotFound("chat")
	}

	if chat.WorkflowID == nil || *chat.WorkflowID == "" {
		return nil, svcerr.FailedPrecondition("chat has no workflow")
	}

	// From here to the paused-status write is one critical section: a
	// SendMessage that arrives while it runs waits, then sees the run paused
	// and resumes it. Taken before the tool cancel, not just around the
	// signal, so the order the server received the two requests in is the
	// order they take effect. See runs.Service.LockRunControl.
	release := s.runs.LockRunControl(ctx, req.Msg.ChatId)
	defer release()

	// Kill the tools BEFORE freeing the workflow to re-dispatch. The pause
	// signal below cancels the ExecuteTools activity at its next step
	// boundary, which frees the workflow to move on -- if that happened
	// first, the successor step could start while a tool from the current
	// step is still provably running on the daemon, and tools are not
	// idempotent. Push the same immediate daemon cancel InterruptThread uses,
	// scoped to every thread of the chat instead of one, and do it first.
	threadsSvc := s.threads
	if threadsSvc == nil {
		threadsSvc = threads.New(threads.Deps{Repo: s.deps.Repo, Chats: s.deps.Chats, Signaler: s.deps.Temporal, ToolCanceler: s.deps.Router})
	}
	if _, err := threadsSvc.CancelChatToolCalls(ctx, threads.CancelChatToolCallsOpts{
		UserID: userID,
		ChatID: req.Msg.ChatId,
	}); err != nil {
		logging.WarnContext(ctx, "Failed to push tool cancel while pausing; the workflow-level pause still applies",
			"error", err, "chatID", req.Msg.ChatId)
	}

	// A run that had already finished is not an error — the service reconciles
	// the stale row and reports success, because the user's intent is met.
	// A failed cancel push above must not strand the workflow un-paused, so
	// this always runs regardless of that outcome.
	if err := s.runs.Pause(ctx, req.Msg.ChatId); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to pause workflow"))
	}

	return connect.NewResponse(&reliantv1.PauseChatResponse{
		Success: true,
		Message: "Chat paused successfully",
	}), nil
}
