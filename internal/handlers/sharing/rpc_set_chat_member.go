package sharing

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// SetChatMember grants a person a level on a chat, or changes the level they
// hold (control-plane docs/design/chat-collaboration.md §3.2): the owner or a
// member at manage, as a person, to a live member of the chat's org who is not
// its owner, up to manage and up to core.MaxChatMembers members.
// authz.ChatMemberChange decides; the store applies the grant and its share
// event together. Off until chat_collaboration.enabled.
func (s *Service) SetChatMember(
	ctx context.Context,
	req *connect.Request[pb.SetChatMemberRequest],
) (*connect.Response[pb.SetChatMemberResponse], error) {
	if !GrantsAllowed(s.deps.Collaboration) {
		return nil, svcerr.WithReason(svcerr.FailedPrecondition(
			"chat collaboration is not enabled on this deployment"), ReasonCollaborationDisabled)
	}
	role, err := roleFrom(req.Msg.GetRole())
	if err != nil {
		return nil, err
	}
	chatID, target := req.Msg.GetChatId(), req.Msg.GetUserId()
	action, err := s.changeMember(ctx, chatID, target, role)
	if err != nil {
		return nil, err
	}
	resp := &pb.SetChatMemberResponse{Changed: action != ""}
	member, err := s.deps.Members.GetChatMember(ctx, chatID, target)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to read the chat member"))
	}
	resp.Member = memberProto(member)
	return connect.NewResponse(resp), nil
}
