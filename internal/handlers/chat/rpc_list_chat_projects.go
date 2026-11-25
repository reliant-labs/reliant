package chat

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
)

// ListChatProjects lists the chat's projects, its primary first
// (chat_projects.go).
func (s *Service) ListChatProjects(
	ctx context.Context,
	req *connect.Request[pb.ListChatProjectsRequest],
) (*connect.Response[pb.ListChatProjectsResponse], error) {
	userID := auth.MustGetUserID(ctx)
	if req.Msg.GetChatId() == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}
	chat, err := s.chats().GetChatWithUserCheck(ctx, req.Msg.GetChatId(), userID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}
	projects, err := s.chatProjects(ctx, userID, chat)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.ListChatProjectsResponse{Projects: projects}), nil
}
