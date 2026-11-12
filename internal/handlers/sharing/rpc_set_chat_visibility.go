package sharing

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/chatproto"
)

// SetChatVisibility shares a chat with its org or makes it private: the owner
// either way (sharing as a person, and only a chat with an org), an org admin
// only to make a member's shared chat private. authz.ChatShareChange decides;
// the store applies the change and its share event together, and a change
// decided on a row that moved since writes nothing.
func (s *Service) SetChatVisibility(
	ctx context.Context,
	req *connect.Request[pb.SetChatVisibilityRequest],
) (*connect.Response[pb.SetChatVisibilityResponse], error) {
	userID := auth.MustGetUserID(ctx)
	to, err := visibilityFrom(req.Msg.GetVisibility())
	if err != nil {
		return nil, err
	}
	// Deciding who may change it reads the row as a viewer could.
	ctx = authz.WithViewerReads(ctx)
	chat, err := s.deps.Repo.GetChat(ctx, req.Msg.GetChatId())
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}
	change, err := authz.ChatShareChange(ctx, userID, chat, chat.ID, to)
	if err != nil {
		return nil, shareRefusal(ctx, "chat", err)
	}
	changed := false
	if authz.ChatVisibility(chat) != to {
		if changed, err = s.deps.Repo.ApplyShareChange(ctx, change); err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to change who can read the chat"))
		}
	}
	resp := &pb.SetChatVisibilityResponse{Changed: changed}
	// The chat as the caller now reads it: an admin who made it private
	// reads it no longer.
	// One decision shapes it (authz.ReadChatAccess).
	if after, err := s.deps.Repo.GetChat(ctx, chat.ID); err == nil {
		if access, denied := authz.ReadChatAccess(ctx, userID, after); denied == nil {
			resp.Chat = chatproto.Chat(after)
			if access.Viewer() {
				resp.Chat = chatproto.ForViewer(resp.Chat)
			}
		}
	}
	return connect.NewResponse(resp), nil
}
