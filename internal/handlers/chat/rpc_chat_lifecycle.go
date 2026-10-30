package chat

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/activity"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/runs"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/reliant-labs/reliant/internal/workflow"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"

	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// UpdateChatState changes chat state (idle/archived)
func (s *Service) UpdateChatState(
	ctx context.Context,
	req *connect.Request[reliantv1.UpdateChatStateRequest],
) (*connect.Response[reliantv1.UpdateChatStateResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}

	// Validate state value
	newState := db.ChatState(req.Msg.State)
	switch newState {
	case db.ChatStateIdle, db.ChatStateArchived:
		// Valid user-initiated transitions
	default:
		return nil, svcerr.InvalidArgument("invalid state, must be 'idle' or 'archived'")
	}

	// Get chat to verify ownership
	chat, err := s.deps.Repo.GetChat(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}
	if chat.UserID != userID {
		return nil, svcerr.NotFound("chat")
	}

	previousState := chat.State

	// Determine reason for state change
	var reason string
	switch newState {
	case db.ChatStateIdle:
		reason = "user_dismissed"
	case db.ChatStateArchived:
		reason = "user_archived"
	}

	// Update the state; archiving also stops the chat's run.
	if newState == db.ChatStateArchived {
		err = s.archiveChat(ctx, chat, reason)
	} else {
		err = s.deps.Repo.UpdateChatState(ctx, req.Msg.ChatId, newState, reason)
	}
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to update chat state"))
	}

	return connect.NewResponse(&reliantv1.UpdateChatStateResponse{
		State:         newState,
		PreviousState: previousState,
		Message:       fmt.Sprintf("Chat state updated to %v", newState),
	}), nil
}

// TerminateChat forcefully terminates the running workflow for a chat.
func (s *Service) TerminateChat(
	ctx context.Context,
	req *connect.Request[reliantv1.TerminateChatRequest],
) (*connect.Response[reliantv1.TerminateChatResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}

	chat, err := s.deps.Repo.GetChatRow(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}
	if chat.UserID != userID {
		return nil, svcerr.NotFound("chat")
	}

	if err := s.runs.Terminate(ctx, req.Msg.ChatId); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to terminate workflow"))
	}

	return connect.NewResponse(&reliantv1.TerminateChatResponse{
		Success: true,
		Message: "Chat terminated successfully",
	}), nil
}

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
	if chat.UserID != userID {
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
		threadsSvc = threads.NewService(s.deps.Repo,
			threads.WithTemporalSignaler(s.deps.Temporal),
			threads.WithToolCanceler(s.deps.Router),
		)
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
	chat, err := s.deps.Repo.GetChat(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}
	if chat.UserID != userID {
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
	if root, err := s.deps.Repo.GetWorkflow(ctx, *chat.WorkflowID); err == nil && root != nil && root.Status == db.Active() {
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

// REMOVED: UpdateChatAgent - agent is now a workflow param, use UpdateWorkflowParams instead

// ============================================
// Phase 4: Extended Chat Operations
// ============================================

// DismissChat clears needs_attention state when user views a chat
func (s *Service) DismissChat(
	ctx context.Context,
	req *connect.Request[reliantv1.DismissChatRequest],
) (*connect.Response[reliantv1.DismissChatResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}

	// Get chat to verify ownership
	chat, err := s.deps.Repo.GetChatRow(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}
	if chat.UserID != userID {
		return nil, svcerr.NotFound("chat")
	}

	// Only dismiss if currently unread
	if !chat.Unread {
		return connect.NewResponse(&reliantv1.DismissChatResponse{
			State:   chat.State,
			Changed: false,
			Message: "Chat does not need dismissal",
		}), nil
	}

	// Clear unread flag
	if err := s.deps.Repo.UpdateChatUnread(ctx, req.Msg.ChatId, false, "user_dismissed"); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to dismiss chat"))
	}

	return connect.NewResponse(&reliantv1.DismissChatResponse{
		State:   chat.State,
		Changed: true,
		Message: "Chat dismissed",
	}), nil
}

// MarkUnreadChat marks a chat as needing attention (mark as unread)
func (s *Service) MarkUnreadChat(
	ctx context.Context,
	req *connect.Request[reliantv1.MarkUnreadChatRequest],
) (*connect.Response[reliantv1.MarkUnreadChatResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}

	// Get chat to verify ownership
	chat, err := s.deps.Repo.GetChatRow(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}
	if chat.UserID != userID {
		return nil, svcerr.NotFound("chat")
	}

	// Don't allow marking archived chats as unread
	if chat.State == db.ChatStateArchived {
		return nil, svcerr.FailedPrecondition("cannot mark archived chat as unread")
	}

	// If already unread, no change needed
	if chat.Unread {
		return connect.NewResponse(&reliantv1.MarkUnreadChatResponse{
			State:   chat.State,
			Changed: false,
			Message: "Chat already marked as unread",
		}), nil
	}

	// Set unread flag
	if err := s.deps.Repo.UpdateChatUnread(ctx, req.Msg.ChatId, true, "user_marked_unread"); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to mark chat as unread"))
	}

	return connect.NewResponse(&reliantv1.MarkUnreadChatResponse{
		State:   chat.State,
		Changed: true,
		Message: "Chat marked as unread",
	}), nil
}

// CompactChat triggers manual context compaction for a chat
func (s *Service) CompactChat(
	ctx context.Context,
	req *connect.Request[reliantv1.CompactChatRequest],
) (*connect.Response[reliantv1.CompactChatResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}
	if req.Msg.ThreadId == "" {
		return nil, svcerr.InvalidArgument("thread_id is required")
	}

	// Get chat to verify ownership
	chat, err := s.deps.Repo.GetChatRow(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}
	if chat.UserID != userID {
		return nil, svcerr.NotFound("chat")
	}

	// Check if there are any messages to compact on the specified thread
	threadToCompact := req.Msg.ThreadId
	messages, err := s.deps.Repo.ListMessages(ctx, req.Msg.ChatId, db.MessageListOptions{
		Thread: &threadToCompact,
	})
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to check messages"))
	}

	if len(messages) == 0 {
		return nil, svcerr.FailedPrecondition("cannot compact: thread has no messages")
	}

	// Check if a workflow is already running - use Temporal as source of truth
	if workflowID := chat.MainThreadID(); workflowID != "" {
		temporalState, err := s.runs.State(ctx, workflowID)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Unavailable("failed to check workflow status"))
		}
		if temporalState.Exists && temporalState.IsRunning {
			return nil, svcerr.FailedPrecondition("cannot compact while a workflow is running")
		}
	}

	// Start the compact workflow with a new UUID
	workflowID := workflow.NewWorkflowID()

	workflowOptions := client.StartWorkflowOptions{
		ID:                       workflowID,
		TaskQueue:                workflow.SharedTaskQueue,
		WorkflowIDConflictPolicy: enums.WORKFLOW_ID_CONFLICT_POLICY_TERMINATE_EXISTING,
		WorkflowExecutionTimeout: workflow.WorkflowExecutionTimeout,
		WorkflowTaskTimeout:      workflow.DynamicWorkflowTaskTimeout,
	}

	// Compact workflow uses the specified thread
	workflowInput := v2.WorkflowInput{
		ChatID:       req.Msg.ChatId,
		WorkflowName: "builtin://compact",
		Inputs: map[string]interface{}{
			"thread": threadToCompact,
		},
		ExecContext: &v2.ExecutionContext{
			WorkflowID:   workflowID,
			ChatID:       req.Msg.ChatId,
			WorkflowName: "builtin://compact",
			Thread:       threadToCompact,
			ThreadMode:   model.ThreadModeInherit,
		},
		Trigger: launch.LoadChatTrigger(ctx, s.deps.Repo, req.Msg.ChatId),
	}

	workflowRun, err := s.deps.Temporal.ExecuteWorkflow(ctx, workflowOptions, v2.DynamicWorkflow, workflowInput)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to start compaction"))
	}

	runID := workflowRun.GetRunID()
	s.runs.RecordRun(ctx, req.Msg.ChatId, workflowID, runID)

	return connect.NewResponse(&reliantv1.CompactChatResponse{
		ChatId:     req.Msg.ChatId,
		WorkflowId: workflowID,
		RunId:      runID,
		Status:     "compacting",
		Message:    "Context compaction started",
	}), nil
}
