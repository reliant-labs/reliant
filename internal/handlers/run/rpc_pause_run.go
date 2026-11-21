package run

import (
	"context"

	"connectrpc.com/connect"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
)

// PauseRun parks a run, leaving it live and resumable.
func (s *Service) PauseRun(
	ctx context.Context,
	req *connect.Request[reliantv1.PauseRunRequest],
) (*connect.Response[reliantv1.PauseRunResponse], error) {
	userID := auth.MustGetUserID(ctx)

	_, chat, err := s.runOwnedBy(ctx, userID, req.Msg.RunId)
	if err != nil {
		return nil, err
	}

	// Delegated so the tool-cancel-before-pause ordering is not duplicated:
	// tools must be cancelled BEFORE the workflow is freed to re-dispatch, or a
	// successor step can start while a non-idempotent tool is still running.
	resp, err := s.deps.Chats.PauseChat(ctx, connect.NewRequest(&reliantv1.PauseChatRequest{
		ChatId: chat.ID,
	}))
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&reliantv1.PauseRunResponse{
		Success: resp.Msg.Success,
		Message: resp.Msg.Message,
	}), nil
}
