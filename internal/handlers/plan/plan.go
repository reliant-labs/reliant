// Copyright (c) 2025 Reliant Labs
package plan

import (
	"context"
	"fmt"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"google.golang.org/protobuf/types/known/timestamppb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/planproto"
)

// chatOfThread resolves the chat a plan belongs to, through the plan's thread.
func (s *Service) chatOfThread(ctx context.Context, threadID string) (*core.ChatRow, error) {
	thread, err := s.deps.Threads.GetThread(ctx, threadID)
	if err != nil {
		return nil, svcerr.WrapOr(fmt.Errorf("failed to get thread for plan: %w", err), svcerr.Internal("failed to get chat"))
	}
	chat, err := s.deps.Chats.GetChatRow(ctx, thread.ChatID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}
	return chat, nil
}

// writableChatOfThread is the chat of threadID when userID may write it. A
// caller who may not gets the same NotFound a missing plan does, so a plan's
// existence in another tenant is never revealed.
//
// TODO(forge#2461): a plan reaches its owner through thread -> chat, which
// authz decides with org membership and sharing, so no one-column forge:owner
// can say it. Replace this check with a declaration once forge's CRUD
// ownership can follow a join.
func (s *Service) writableChatOfThread(ctx context.Context, userID, threadID string) (*core.ChatRow, error) {
	chat, err := s.chatOfThread(ctx, threadID)
	if err != nil {
		return nil, err
	}
	if !authz.CanWriteChat(ctx, userID, chat) {
		return nil, svcerr.NotFound("plan")
	}
	return chat, nil
}

// emitPlanTasksRefetch tells the chat's clients to re-read its plan and
// tasks. It runs after the write has committed and only warns on failure.
func (s *Service) emitPlanTasksRefetch(ctx context.Context, chatID string) {
	if err := s.deps.Refetch.EmitChatRefetch(ctx, chatID, core.RefetchPlanTasks); err != nil {
		logging.WarnContext(ctx, "[PlanService] Failed to emit plan_tasks refetch", "error", err)
	}
}

// corePlanToProto is the wire shape of a plan read through the stores, in
// chatID.
func corePlanToProto(plan *core.Plan, chatID string) *reliantv1.Plan {
	out := &reliantv1.Plan{
		Id:        plan.ID,
		ChatId:    chatID,
		Title:     plan.Title,
		Status:    planproto.PlanStatus(plan.Status),
		CreatedAt: timestamppb.New(plan.CreatedAt),
		UpdatedAt: timestamppb.New(plan.UpdatedAt),
	}
	if plan.Description != nil {
		out.Description = *plan.Description
	}
	if plan.Complexity != nil {
		out.Complexity = planproto.PlanComplexity(*plan.Complexity)
	}
	return out
}
