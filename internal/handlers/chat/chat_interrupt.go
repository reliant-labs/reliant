// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/threads"
)

// InterruptThread stops the work in flight on a thread so the agent reads its
// mailbox now rather than after finishing what it started.
func (s *Service) InterruptThread(
	ctx context.Context,
	req *connect.Request[reliantv1.InterruptThreadRequest],
) (*connect.Response[reliantv1.InterruptThreadResponse], error) {
	userID := auth.MustGetUserID(ctx)

	threadsSvc := s.threads
	if threadsSvc == nil {
		threadsSvc = threads.NewService(s.deps.Repo,
			threads.WithTemporalSignaler(s.deps.Temporal),
			threads.WithToolCanceler(s.deps.Router),
		)
	}

	result, err := threadsSvc.InterruptThread(ctx, threads.InterruptThreadOpts{
		UserID:   userID,
		ChatID:   req.Msg.ChatId,
		ThreadID: req.Msg.ThreadId,
	})
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to interrupt thread"))
	}

	return connect.NewResponse(&reliantv1.InterruptThreadResponse{
		CancelledToolCalls:     int32(result.CancelledToolCalls),
		UndeliverableToolCalls: result.UndeliverableToolCalls,
	}), nil
}
