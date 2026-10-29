package chat

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
)

// ListQueuedAgentMessages returns the entries currently sitting in a
// thread's mailbox (agent_messages) with status = queued -- what
// SendAgentMessage (or spawn_send) put there but the target thread hasn't
// drained yet. This is what makes a queued message visible to the user
// instead of it being invisible until the agent happens to drain it.
func (s *Service) ListQueuedAgentMessages(
	ctx context.Context,
	req *connect.Request[reliantv1.ListQueuedAgentMessagesRequest],
) (*connect.Response[reliantv1.ListQueuedAgentMessagesResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}
	if req.Msg.ThreadId == "" {
		return nil, svcerr.InvalidArgument("thread_id is required")
	}

	// Ownership check: mirrors SendAgentMessage exactly -- the chat must
	// belong to the caller, and the thread must belong to that chat, both
	// returning the same NotFound so thread IDs can't be enumerated.
	if err := s.requireChatOwner(ctx, req.Msg.ChatId, userID); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}

	target, err := s.deps.Repo.GetThread(ctx, req.Msg.ThreadId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get thread"))
	}
	if target == nil {
		return nil, svcerr.NotFound("thread")
	}
	if target.ChatID != req.Msg.ChatId {
		return nil, svcerr.NotFound("thread")
	}

	queued, err := s.deps.Repo.ListQueuedAgentMessagesForThread(ctx, req.Msg.ThreadId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list queued messages"))
	}

	messages := make([]*reliantv1.QueuedAgentMessage, len(queued))
	for i, msg := range queued {
		messages[i] = &reliantv1.QueuedAgentMessage{
			Id:          msg.ID,
			Body:        msg.Body,
			CreatedAt:   msg.CreatedAt.Format(time.RFC3339),
			SenderKind:  msg.Kind,
			Attachments: msg.Attachments,
		}
	}

	return connect.NewResponse(&reliantv1.ListQueuedAgentMessagesResponse{
		Messages: messages,
	}), nil
}

// CancelQueuedAgentMessage revokes a single queued mailbox entry before the
// target agent drains it.
//
// This is a race against the agent's own next turn: CallLLM's drain may pick
// the row up between the user seeing it and clicking cancel. The
// deletion is conditioned on status = queued at the database level (see
// CancelQueuedAgentMessage in the postgres store), so the outcome is never
// ambiguous -- either this call wins the row and deletes it, or the drain
// already won it and this call reports failure without touching the row.
func (s *Service) CancelQueuedAgentMessage(
	ctx context.Context,
	req *connect.Request[reliantv1.CancelQueuedAgentMessageRequest],
) (*connect.Response[reliantv1.CancelQueuedAgentMessageResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}
	if req.Msg.MessageId == "" {
		return nil, svcerr.InvalidArgument("message_id is required")
	}

	// Ownership check: the chat must belong to the caller. The DELETE
	// itself is additionally scoped to chat_id, so even if a caller guessed
	// a message ID from another chat, this ownership check is what stops
	// them from cancelling it.
	if err := s.requireChatOwner(ctx, req.Msg.ChatId, userID); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}

	cancelled, err := s.deps.Repo.CancelQueuedAgentMessage(ctx, req.Msg.MessageId, req.Msg.ChatId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to cancel message"))
	}

	if !cancelled {
		return connect.NewResponse(&reliantv1.CancelQueuedAgentMessageResponse{
			Success: false,
			Message: "Already delivered to the agent — too late to cancel.",
		}), nil
	}

	return connect.NewResponse(&reliantv1.CancelQueuedAgentMessageResponse{
		Success: true,
		Message: "Cancelled. The agent will never see this message.",
	}), nil
}
