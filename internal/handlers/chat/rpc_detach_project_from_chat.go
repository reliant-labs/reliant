package chat

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
)

// DetachProjectFromChat detaches an attached project from a chat; its
// checkout is untouched. The primary is refused: a chat always has one.
func (s *Service) DetachProjectFromChat(
	ctx context.Context,
	req *connect.Request[pb.DetachProjectFromChatRequest],
) (*connect.Response[pb.DetachProjectFromChatResponse], error) {
	userID := auth.MustGetUserID(ctx)
	chatID, projectID := req.Msg.GetChatId(), req.Msg.GetProjectId()
	if chatID == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}
	if projectID == "" {
		return nil, svcerr.InvalidArgument("project_id is required")
	}
	chat, err := s.chats().GetChatWithUserCheck(ctx, chatID, userID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}
	if projectID == chat.ProjectID {
		return nil, svcerr.FailedPrecondition("that is the chat's own project, which it always keeps; move the chat to another project instead")
	}
	if err := s.changeChatProjects(ctx, userID, chat, func(txCtx context.Context) error {
		return s.deps.Repo.DetachChatProject(txCtx, chat.ID, projectID)
	}); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to detach the project from the chat"))
	}
	projects, err := s.chatProjects(ctx, userID, chat)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.DetachProjectFromChatResponse{Projects: projects}), nil
}
