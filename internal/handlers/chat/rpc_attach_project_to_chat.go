package chat

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/logging"
)

// AttachProjectToChat attaches another of the caller's projects to a chat
// (chat_projects.go has the rules).
func (s *Service) AttachProjectToChat(
	ctx context.Context,
	req *connect.Request[pb.AttachProjectToChatRequest],
) (*connect.Response[pb.AttachProjectToChatResponse], error) {
	userID := auth.MustGetUserID(ctx)
	chatID, projectID := req.Msg.GetChatId(), req.Msg.GetProjectId()
	if chatID == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}
	if projectID == "" {
		return nil, svcerr.InvalidArgument("project_id is required")
	}
	chat, err := s.deps.Chats.GetChatWithUserCheck(ctx, chatID, userID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}
	if projectID != chat.ProjectID {
		primary, err := s.ownedProject(ctx, userID, chat.ProjectID)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to load the chat's project"))
		}
		if err := s.changeChatProjects(ctx, userID, chat, func(txCtx context.Context) error {
			return s.attachProject(txCtx, userID, chat, primary, projectID)
		}); err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to attach the project to the chat"))
		}
		logging.InfoContext(ctx, "[AttachProjectToChat] attached a project to a chat", "chatID", chat.ID, "projectID", projectID)
	}
	projects, err := s.chatProjects(ctx, userID, chat)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.AttachProjectToChatResponse{Projects: projects}), nil
}
