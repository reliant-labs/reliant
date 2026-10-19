// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/logging"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// RetryUsageLimitWait is "Retry now" on a run parked until a provider's usage
// limit resets: it signals the run, which wakes every thread waiting on a limit
// and retries their steps at once. If the limit is still enforced the step fails
// the same way and the run parks again on the new reset, so pressing it early
// costs one refused request.
//
// A chat whose run is not waiting is left alone (woken = false): the button is
// drawn from the chat's activity, and the activity may have moved on by the time
// the click lands.
func (s *Service) RetryUsageLimitWait(
	ctx context.Context,
	req *connect.Request[reliantv1.RetryUsageLimitWaitRequest],
) (*connect.Response[reliantv1.RetryUsageLimitWaitResponse], error) {
	userID := auth.MustGetUserID(ctx)
	if req.Msg.ChatId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("chat_id is required"))
	}
	chat, err := s.getChatForUser(ctx, req.Msg.ChatId, userID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}
	if chat.Activity == nil || reliantv1.ChatActivity(*chat.Activity) != reliantv1.ChatActivity_CHAT_ACTIVITY_WAITING_FOR_USAGE_LIMIT {
		return connect.NewResponse(&reliantv1.RetryUsageLimitWaitResponse{Woken: false}), nil
	}
	if chat.WorkflowID == nil || *chat.WorkflowID == "" {
		return connect.NewResponse(&reliantv1.RetryUsageLimitWaitResponse{Woken: false}), nil
	}
	if err := s.deps.Temporal.SignalWorkflow(ctx, *chat.WorkflowID, "", v2.UsageLimitRetrySignalName, nil); err != nil {
		logging.Warn("Could not wake a run waiting on a usage limit",
			"error", err, "chatID", chat.ID, "workflowID", *chat.WorkflowID)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to wake the run"))
	}
	logging.Info("Woke a run waiting on a usage limit", "chatID", chat.ID, "workflowID", *chat.WorkflowID)
	return connect.NewResponse(&reliantv1.RetryUsageLimitWaitResponse{Woken: true}), nil
}
