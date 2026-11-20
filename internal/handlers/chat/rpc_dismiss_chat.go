package chat

import (
	"context"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
)

// DismissChat clears needs_attention state when user views a chat
func (s *Service) DismissChat(
	ctx context.Context,
	req *reliantv1.DismissChatRequest,
) (*reliantv1.DismissChatResponse, error) {
	userID := auth.MustGetUserID(ctx)

	if req.ChatId == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}

	// Get chat to verify ownership
	chat, err := s.deps.Repo.GetChatRow(ctx, req.ChatId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}
	if !authz.CanWriteChat(ctx, userID, chat) {
		return nil, svcerr.NotFound("chat")
	}

	// Only dismiss if currently unread
	if !chat.Unread {
		return &reliantv1.DismissChatResponse{
			State:   chat.State,
			Changed: false,
			Message: "Chat does not need dismissal",
		}, nil
	}

	// Clear unread flag
	if err := s.deps.Repo.UpdateChatUnread(ctx, req.ChatId, false, "user_dismissed"); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to dismiss chat"))
	}

	return &reliantv1.DismissChatResponse{
		State:   chat.State,
		Changed: true,
		Message: "Chat dismissed",
	}, nil
}
