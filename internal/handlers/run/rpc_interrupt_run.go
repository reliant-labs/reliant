package run

import (
	"context"

	"connectrpc.com/connect"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
)

// InterruptRun stops what a run is doing now without ending it.
func (s *Service) InterruptRun(
	ctx context.Context,
	req *connect.Request[reliantv1.InterruptRunRequest],
) (*connect.Response[reliantv1.InterruptRunResponse], error) {
	userID := auth.MustGetUserID(ctx)

	run, chat, err := s.runOwnedBy(ctx, userID, req.Msg.RunId)
	if err != nil {
		return nil, err
	}

	// Empty thread means "this run's own thread", which is the useful default
	// for a run-addressed API. ChatService's equivalent is chat-addressed and
	// takes the thread explicitly.
	thread := req.Msg.Thread
	if thread == "" {
		thread = run.Thread
	}

	resp, err := s.deps.Chats.InterruptThread(ctx, connect.NewRequest(&reliantv1.InterruptThreadRequest{
		ChatId:   chat.ID,
		ThreadId: thread,
	}))
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&reliantv1.InterruptRunResponse{
		CancelledToolCalls:     resp.Msg.CancelledToolCalls,
		UndeliverableToolCalls: resp.Msg.UndeliverableToolCalls,
	}), nil
}
