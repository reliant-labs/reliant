package chat

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/activity"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/runs"
)

// ResumeChat resumes a paused workflow by restarting its dedicated worker
// The workflow continues exactly where it left off - all local state is preserved.
func (s *Service) ResumeChat(
	ctx context.Context,
	req *connect.Request[reliantv1.ResumeChatRequest],
) (*connect.Response[reliantv1.ResumeChatResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}

	// Get chat to verify ownership
	chat, err := s.chatViews().GetChat(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}
	if !authz.CanOwnChat(ctx, userID, chat) {
		return nil, svcerr.NotFound("chat")
	}

	if chat.WorkflowID == nil || *chat.WorkflowID == "" {
		return nil, svcerr.FailedPrecondition("chat has no workflow")
	}

	// A resume is a signal plus a status write, like a pause; interleaved
	// with one, the row can end up saying the opposite of what the run is
	// doing. See runs.Service.LockRunControl.
	release := s.runs.LockRunControl(ctx, req.Msg.ChatId)
	defer release()

	// Resume is for the whole chat: every blocked thread goes on, not only
	// the root — usage-limit waits retry, and every stopped sub-agent resumes
	// (resumeWholeChat). A run that is
	// executing — not paused — has no pause for signal.resume to release;
	// sent anyway, the resume would be held and release the next pause the
	// run takes on its own. For it, Resume is the wake alone: its threads
	// parked on a usage limit retry, and its stopped sub-agents resume.
	if root, err := s.deps.Workflows.LoadWorkflow(ctx, *chat.WorkflowID); err == nil && root != nil && root.Status == db.Active() {
		if err := s.resumeWholeChat(ctx, chat); err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to resume workflow"))
		}
		runID := ""
		if chat.RunID != nil {
			runID = *chat.RunID
		}
		return connect.NewResponse(&reliantv1.ResumeChatResponse{
			Success:      true,
			Message:      "Chat resumed successfully",
			WorkflowId:   *chat.WorkflowID,
			RunId:        runID,
			RecoveryType: reliantv1.RecoveryType_RECOVERY_TYPE_RESUMED,
		}), nil
	}

	outcome, err := s.runs.Resume(ctx, req.Msg.ChatId, nil)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to resume workflow"))
	}
	if outcome.Kind == runs.OutcomeResumed {
		// After the resume, so it reaches the run the resume woke — or the
		// one a reset just made, whose replay rebuilt the stopped agents.
		if err := s.resumeWholeChat(ctx, chat); err != nil {
			logging.WarnContext(ctx, "Resumed the chat's run but could not wake its stopped sub-agents", "chatID", chat.ID, "error", err)
		}
	}

	switch outcome.Kind {
	case runs.OutcomeUnresumable:
		return nil, svcerr.FailedPrecondition("this conversation experienced a workflow error and cannot be resumed - use the branch feature to start a new conversation from any previous message")

	case runs.OutcomeNeedsRecovery, runs.OutcomeNeedsRestart:
		// The frontend prompts the user to confirm starting a new conversation.
		// Nothing was resumed, so the call records no "resume" event.
		activity.Skip(ctx)
		return connect.NewResponse(&reliantv1.ResumeChatResponse{
			Success:       false,
			Message:       "The workflow session was interrupted and cannot be resumed. Your message history is preserved. Would you like to start a new conversation?",
			WorkflowId:    outcome.WorkflowID,
			NeedsRecovery: true,
			RecoveryType:  reliantv1.RecoveryType_RECOVERY_TYPE_WORKFLOW_LOST,
		}), nil
	}

	return connect.NewResponse(&reliantv1.ResumeChatResponse{
		Success:      true,
		Message:      "Chat resumed successfully",
		WorkflowId:   outcome.WorkflowID,
		RunId:        outcome.RunID,
		RecoveryType: reliantv1.RecoveryType_RECOVERY_TYPE_RESUMED,
	}), nil
}
