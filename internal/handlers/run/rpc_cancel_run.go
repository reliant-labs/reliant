package run

import (
	"context"

	"connectrpc.com/connect"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
)

// CancelRun hard-stops a run. Terminal by intent.
func (s *Service) CancelRun(
	ctx context.Context,
	req *connect.Request[reliantv1.CancelRunRequest],
) (*connect.Response[reliantv1.CancelRunResponse], error) {
	userID := auth.MustGetUserID(ctx)

	_, chat, err := s.runOwnedBy(ctx, userID, req.Msg.RunId)
	if err != nil {
		return nil, err
	}

	resp, err := s.deps.Chats.TerminateChat(ctx, connect.NewRequest(&reliantv1.TerminateChatRequest{
		ChatId: chat.ID,
	}))
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&reliantv1.CancelRunResponse{
		Success: resp.Msg.Success,
		Message: resp.Msg.Message,
	}), nil
}
