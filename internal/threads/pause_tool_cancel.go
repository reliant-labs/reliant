package threads

import (
	"context"
	"fmt"

	"github.com/reliant-labs/reliant/internal/logging"
)

const pauseToolCancelReason = "user paused the agent"

// CancelChatToolCallsOpts contains the target chat and authenticated caller.
type CancelChatToolCallsOpts struct {
	UserID string
	ChatID string
}

// CancelChatToolCallsResult reports what the pause actually stopped.
type CancelChatToolCallsResult struct {
	CancelledToolCalls     int
	UndeliverableToolCalls []string
}

// CancelChatToolCalls pushes the same immediate daemon tool-cancel that
// InterruptThread uses, scoped to every thread of the chat instead of one.
//
// Pause and interrupt are the same verb at different scope (see
// specs/interrupt-pause-spec.md): pause = whole chat, interrupt = one
// thread. Before this, pause's only route to a running tool was
// ctx-cancel -> Temporal -> worker, delivered only on the next heartbeat
// (MaxHeartbeatThrottleInterval, 3s) -- measured 1.77s of a 1.9s pause spent
// waiting on it. This gives pause the same in-process cancel signal +
// direct daemon push that already makes interrupt near-immediate, without
// duplicating the cancel loop: both call cancelToolCalls.
//
// This does not send signal.pause itself -- that remains PauseService's job,
// called separately (runs.Service.Pause). CancelChatToolCalls only adds the
// daemon-side push the workflow-level signal cannot deliver quickly.
func (s *Service) CancelChatToolCalls(ctx context.Context, opts CancelChatToolCallsOpts) (CancelChatToolCallsResult, error) {
	if opts.ChatID == "" {
		return CancelChatToolCallsResult{}, validationError{message: "chat_id is required"}
	}
	if opts.UserID == "" {
		return CancelChatToolCallsResult{}, validationError{message: "user_id is required"}
	}

	chat, err := s.repo.GetChatWithUserCheck(ctx, opts.ChatID, opts.UserID)
	if err != nil || chat == nil {
		return CancelChatToolCallsResult{}, notFoundError{message: "chat not found"}
	}

	inFlight, err := s.inFlightToolCallsForChat(ctx, opts.ChatID)
	if err != nil {
		logging.Error("Failed to read in-flight tool calls for pause",
			"error", err, "chatID", opts.ChatID)
		return CancelChatToolCallsResult{}, fmt.Errorf("failed to read tool calls: %w", err)
	}

	outcome := s.cancelToolCalls(ctx, opts.UserID, inFlight, pauseToolCancelReason)
	result := CancelChatToolCallsResult{
		CancelledToolCalls:     outcome.CancelledToolCalls,
		UndeliverableToolCalls: outcome.UndeliverableToolCalls,
	}

	logging.Info("Pause cancelled in-flight tool calls",
		"chatID", opts.ChatID,
		"cancelledToolCalls", result.CancelledToolCalls,
		"undeliverable", len(result.UndeliverableToolCalls),
	)

	return result, nil
}
