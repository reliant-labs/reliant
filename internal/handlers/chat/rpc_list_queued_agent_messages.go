package chat

import (
	"context"
	"time"

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
	req *reliantv1.ListQueuedAgentMessagesRequest,
) (*reliantv1.ListQueuedAgentMessagesResponse, error) {
	userID := auth.MustGetUserID(ctx)

	if req.ChatId == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}
	if req.ThreadId == "" {
		return nil, svcerr.InvalidArgument("thread_id is required")
	}

	// Ownership check: mirrors SendAgentMessage exactly -- the chat must
	// belong to the caller, and the thread must belong to that chat, both
	// returning the same NotFound so thread IDs can't be enumerated.
	if err := s.requireChatOwner(ctx, req.ChatId, userID); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}

	target, err := s.deps.Repo.GetThread(ctx, req.ThreadId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get thread"))
	}
	if target == nil {
		return nil, svcerr.NotFound("thread")
	}
	if target.ChatID != req.ChatId {
		return nil, svcerr.NotFound("thread")
	}

	queued, err := s.deps.Repo.ListQueuedAgentMessagesForThread(ctx, req.ThreadId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list queued messages"))
	}

	messages := make([]*reliantv1.QueuedAgentMessage, 0, len(queued))
	for _, msg := range queued {
		// An LLM-only row (an async ask_user answer) is shown on its question
		// card; in the pending strip it would read as something the user
		// typed and could cancel.
		if msg.DisplayStyle != nil && *msg.DisplayStyle == int32(reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN) {
			continue
		}
		messages = append(messages, &reliantv1.QueuedAgentMessage{
			Id:          msg.ID,
			Body:        msg.Body,
			CreatedAt:   msg.CreatedAt.Format(time.RFC3339),
			SenderKind:  msg.Kind,
			Attachments: msg.Attachments,
		})
	}

	return &reliantv1.ListQueuedAgentMessagesResponse{
		Messages: messages,
	}, nil
}
