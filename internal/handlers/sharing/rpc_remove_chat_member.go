package sharing

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// RemoveChatMember takes a person's grant on a chat away (control-plane
// docs/design/chat-collaboration.md §3.2): the owner or a member at manage
// removes anyone but the owner, and a member removes their own.
// authz.ChatMemberChange decides; the store deletes the grant and records its
// share event together. Never gated by chat_collaboration.enabled, so a grant
// can always be taken back.
func (s *Service) RemoveChatMember(
	ctx context.Context,
	req *connect.Request[pb.RemoveChatMemberRequest],
) (*connect.Response[pb.RemoveChatMemberResponse], error) {
	action, err := s.changeMember(ctx, req.Msg.GetChatId(), req.Msg.GetUserId(), "")
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.RemoveChatMemberResponse{Changed: action != ""}), nil
}
