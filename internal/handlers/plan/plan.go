// Copyright (c) 2025 Reliant Labs
package plan

import (
	"context"
	"fmt"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/planproto"
)

// chatOfPlan resolves the chat a plan belongs to, through the plan's thread.
func (s *Service) chatOfPlan(ctx context.Context, plan *core.Plan) (*core.ChatRow, error) {
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

// writablePlan reads a plan and its chat (plan → thread → chat) and refuses a
// caller who may not write that chat.
func (s *Service) writablePlan(ctx context.Context, userID, planID string) (*core.Plan, *core.ChatRow, error) {
	if planID == "" {
		return nil, nil, svcerr.InvalidArgument("plan_id is required")
	}
	plan, err := s.deps.Plans.GetPlan(ctx, planID)
	if err != nil {
		return nil, nil, svcerr.WrapOr(err, svcerr.Internal("failed to get plan"))
	}
	chat, err := s.chatOfPlan(ctx, plan)
	if err != nil {
		return nil, nil, err
	}
	if !authz.CanOwnChat(ctx, userID, chat) {
		return nil, nil, svcerr.PermissionDenied("access denied")
	}
	return plan, chat, nil
}

// emitPlanTasksRefetch tells the chat's clients to re-read its plan and
// tasks. It runs after the write has committed and only warns on failure.
func (s *Service) emitPlanTasksRefetch(ctx context.Context, chatID string) {
	if err := s.deps.Refetch.EmitChatRefetch(ctx, chatID, core.RefetchPlanTasks); err != nil {
		logging.WarnContext(ctx, "[PlanService] Failed to emit plan_tasks refetch", "error", err)
	}
}

// planToProto is the wire shape of a plan in chatID.
func planToProto(plan *core.Plan, chatID string) *reliantv1.Plan {
	out := &reliantv1.Plan{
		Id:        plan.ID,
		ChatId:    chatID,
		Title:     plan.Title,
		Status:    planproto.PlanStatus(plan.Status),
		CreatedAt: plan.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt: plan.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
	if plan.Description != nil {
		out.Description = *plan.Description
	}
	if plan.Complexity != nil {
		out.Complexity = planproto.PlanComplexity(*plan.Complexity)
	}
	return out
}
