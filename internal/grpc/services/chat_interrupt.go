// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/threads"
)

// InterruptThread stops the work in flight on a thread so the agent reads its
// mailbox now rather than after finishing what it started.
func (s *ChatService) InterruptThread(
	ctx context.Context,
	req *connect.Request[reliantv1.InterruptThreadRequest],
) (*connect.Response[reliantv1.InterruptThreadResponse], error) {
	userID := auth.MustGetUserID(ctx)

	threadsSvc := s.threads
	if threadsSvc == nil {
		threadsSvc = threads.NewService(s.database,
			threads.WithTemporalSignaler(s.tempClient),
			threads.WithToolCanceler(s.daemonRouter),
		)
	}

	result, err := threadsSvc.InterruptThread(ctx, threads.InterruptThreadOpts{
		UserID:   userID,
		ChatID:   req.Msg.ChatId,
		ThreadID: req.Msg.ThreadId,
	})
	if err != nil {
		return nil, interruptThreadError(err)
	}

	return connect.NewResponse(&reliantv1.InterruptThreadResponse{
		CancelledToolCalls:     int32(result.CancelledToolCalls),
		UndeliverableToolCalls: result.UndeliverableToolCalls,
	}), nil
}

func interruptThreadError(err error) error {
	switch {
	case errors.Is(err, threads.ErrInvalidArgument):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, threads.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, threads.ErrInterruptUndeliverable):
		return connect.NewError(connect.CodeUnavailable, err)
	default:
		logging.Error("Failed to interrupt thread", "error", err)
		return connect.NewError(connect.CodeInternal, fmt.Errorf("failed to interrupt thread"))
	}
}
