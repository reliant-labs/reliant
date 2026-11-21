package chat

import (
	"context"

	"connectrpc.com/connect"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// UnadoptChat clears adoption. Idempotent; the run stays in Runs.
func (s *Service) UnadoptChat(
	ctx context.Context,
	req *connect.Request[reliantv1.UnadoptChatRequest],
) (*connect.Response[reliantv1.UnadoptChatResponse], error) {
	chat, err := s.setChatAdopted(ctx, req.Msg.GetChatId(), false)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&reliantv1.UnadoptChatResponse{Chat: chatToProto(chat)}), nil
}
