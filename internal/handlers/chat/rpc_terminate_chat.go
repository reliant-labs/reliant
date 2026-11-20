package chat

import (
	"context"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
)

// TerminateChat forcefully terminates the running workflow for a chat.
func (s *Service) TerminateChat(
	ctx context.Context,
	req *reliantv1.TerminateChatRequest,
) (*reliantv1.TerminateChatResponse, error) {
	userID := auth.MustGetUserID(ctx)

	if req.ChatId == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}

	chat, err := s.deps.Repo.GetChatRow(ctx, req.ChatId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}
	if !authz.CanWriteChat(ctx, userID, chat) {
		return nil, svcerr.NotFound("chat")
	}

	if err := s.runs.Terminate(ctx, req.ChatId); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to terminate workflow"))
	}

	return &reliantv1.TerminateChatResponse{
		Success: true,
		Message: "Chat terminated successfully",
	}, nil
}
