package sharing

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// ListChatMembers lists who holds access to a chat, as the caller may see it
// (control-plane docs/design/chat-collaboration.md §2.2): the owner and
// members at manage see every member and level; members at reply see the
// reply and manage members, the people a pending request waits on; view
// grantees and org-wide viewers see only the count. One decision
// (authz.ReadChatAccess) shapes the whole answer.
func (s *Service) ListChatMembers(
	ctx context.Context,
	req *connect.Request[pb.ListChatMembersRequest],
) (*connect.Response[pb.ListChatMembersResponse], error) {
	userID := auth.MustGetUserID(ctx)
	ctx = authz.WithViewerReads(ctx)
	chat, err := s.deps.Chats.GetChat(ctx, req.Msg.GetChatId())
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}
	access, err := authz.ReadChatAccess(ctx, userID, chat)
	if err != nil {
		return nil, shareRefusal(ctx, "chat", err)
	}
	members, err := s.deps.Members.ListChatMembers(ctx, chat.ID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list the chat's members"))
	}
	resp := &pb.ListChatMembersResponse{
		OwnerUserId: chat.UserID,
		MemberCount: int32(len(members)),
		// The level the caller reads at, as the decision found it.
		CallerRole: roleProto(access.Role()),
	}
	var least core.ChatMemberRole // the lowest level the caller may see listed; "" lists nobody
	switch access {
	case authz.AccessOwner, authz.AccessManage:
		least = core.ChatMemberView
	case authz.AccessReply:
		least = core.ChatMemberReply
	}
	for _, m := range members {
		if least != "" && m.Role.AtLeast(least) {
			resp.Members = append(resp.Members, memberProto(m))
		}
	}
	return connect.NewResponse(resp), nil
}
