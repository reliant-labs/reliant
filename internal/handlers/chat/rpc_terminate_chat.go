package chat

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
)

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
	if !authz.CanOwnChat(ctx, userID, chat) {
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
