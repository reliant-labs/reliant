// Copyright (c) 2025 Reliant Labs
package task

import (
	"context"
	"fmt"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
)

// chatOfPlan resolves the chat a plan belongs to, through the plan's thread.
func (s *Service) chatOfPlan(ctx context.Context, planID string) (*core.ChatRow, error) {
	plan, err := s.deps.Plans.GetPlan(ctx, planID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get plan"))
	}
	thread, err := s.deps.Threads.GetThread(ctx, plan.ThreadID)
	if err != nil {
		return nil, svcerr.WrapOr(fmt.Errorf("failed to get thread for plan: %w", err), svcerr.Internal("failed to get chat"))
	}
	chat, err := s.deps.Chats.GetChatRow(ctx, thread.ChatID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}
	return chat, nil
}

// writableChatOfPlan is the chat of planID when userID may write it. A caller
// who may not gets the same NotFound a missing task does, so a task's
// existence in another tenant is never revealed.
//
// TODO(forge#2461): a task reaches its owner through plan -> thread -> chat,
// which authz decides with org membership and sharing, so no one-column
// forge:owner can say it. Replace this check with a declaration once forge's
// CRUD ownership can follow a join.
func (s *Service) writableChatOfPlan(ctx context.Context, userID, planID string) (*core.ChatRow, error) {
	chat, err := s.chatOfPlan(ctx, planID)
	if err != nil {
		return nil, err
	}
	if !authz.CanWriteChat(ctx, userID, chat) {
		return nil, svcerr.NotFound("task")
	}
	return chat, nil
}

// emitPlanTasksRefetch tells the chat's clients to re-read its plan and
// tasks. It runs after the write has committed and only warns on failure.
func (s *Service) emitPlanTasksRefetch(ctx context.Context, chatID string) {
	if err := s.deps.Refetch.EmitChatRefetch(ctx, chatID, core.RefetchPlanTasks); err != nil {
		logging.WarnContext(ctx, "[TaskService] Failed to emit plan_tasks refetch", "error", err)
	}
}
