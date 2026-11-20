package chat

import (
	"context"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// UnadoptChat clears adoption. Idempotent; the run stays in Runs.
func (s *Service) UnadoptChat(
	ctx context.Context,
	req *reliantv1.UnadoptChatRequest,
) (*reliantv1.UnadoptChatResponse, error) {
	chat, err := s.setChatAdopted(ctx, req.GetChatId(), false)
	if err != nil {
		return nil, err
	}
	return &reliantv1.UnadoptChatResponse{Chat: chatToProto(chat)}, nil
}
