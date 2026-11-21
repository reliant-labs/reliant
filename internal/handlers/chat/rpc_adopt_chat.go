package chat

import (
	"context"

	"connectrpc.com/connect"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// AdoptChat takes a run into the caller's own chats. Idempotent: adopting an
// adopted chat keeps its original adopted_at. A chat that is not the caller's
// reads as not found, the same as any other owner-scoped chat RPC.
func (s *Service) AdoptChat(
	ctx context.Context,
	req *connect.Request[reliantv1.AdoptChatRequest],
) (*connect.Response[reliantv1.AdoptChatResponse], error) {
	chat, err := s.setChatAdopted(ctx, req.Msg.GetChatId(), true)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&reliantv1.AdoptChatResponse{Chat: chatToProto(chat)}), nil
}
