package chat

import (
	"context"

	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
)

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
	req *reliantv1.CancelQueuedAgentMessageRequest,
) (*reliantv1.CancelQueuedAgentMessageResponse, error) {
	userID := auth.MustGetUserID(ctx)

	if req.ChatId == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}
	if req.MessageId == "" {
		return nil, svcerr.InvalidArgument("message_id is required")
	}

	// Ownership check: the chat must belong to the caller. The DELETE
	// itself is additionally scoped to chat_id, so even if a caller guessed
	// a message ID from another chat, this ownership check is what stops
	// them from cancelling it.
	if err := s.requireChatOwner(ctx, req.ChatId, userID); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}

	cancelled, err := s.deps.Repo.CancelQueuedAgentMessage(ctx, req.MessageId, req.ChatId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to cancel message"))
	}

	if !cancelled {
		return &reliantv1.CancelQueuedAgentMessageResponse{
			Success: false,
			Message: "Already delivered to the agent — too late to cancel.",
		}, nil
	}

	return &reliantv1.CancelQueuedAgentMessageResponse{
		Success: true,
		Message: "Cancelled. The agent will never see this message.",
	}, nil
}
