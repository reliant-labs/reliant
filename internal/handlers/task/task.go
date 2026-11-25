// Copyright (c) 2025 Reliant Labs
package task

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

// chatOfPlanID resolves the chat of the plan planID.
func (s *Service) chatOfPlanID(ctx context.Context, planID string) (*core.ChatRow, error) {
	plan, err := s.deps.Plans.GetPlan(ctx, planID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get plan"))
	}
	return s.chatOfPlan(ctx, plan)
}

// writableTask reads a task and its chat (task → plan → thread → chat) and
// refuses a caller who may not write that chat.
func (s *Service) writableTask(ctx context.Context, userID, taskID string) (*core.Task, *core.ChatRow, error) {
	if taskID == "" {
		return nil, nil, svcerr.InvalidArgument("task_id is required")
	}
	task, err := s.deps.Tasks.GetTask(ctx, taskID)
	if err != nil {
		return nil, nil, svcerr.WrapOr(err, svcerr.Internal("failed to get task"))
	}
	chat, err := s.chatOfPlanID(ctx, task.PlanID)
	if err != nil {
		return nil, nil, err
	}
	if !authz.CanOwnChat(ctx, userID, chat) {
		return nil, nil, svcerr.PermissionDenied("access denied")
	}
	return task, chat, nil
}

// emitPlanTasksRefetch tells the chat's clients to re-read its plan and
// tasks. It runs after the write has committed and only warns on failure.
func (s *Service) emitPlanTasksRefetch(ctx context.Context, chatID string) {
	if err := s.deps.Refetch.EmitChatRefetch(ctx, chatID, core.RefetchPlanTasks); err != nil {
		logging.WarnContext(ctx, "[TaskService] Failed to emit plan_tasks refetch", "error", err)
	}
}

// taskToProto is the wire shape of a task.
func taskToProto(task *core.Task) *reliantv1.Task {
	out := &reliantv1.Task{
		Id:        task.ID,
		PlanId:    task.PlanID,
		Title:     task.Title,
		Status:    planproto.TaskStatus(task.Status),
		Position:  int32(task.Position),
		CreatedAt: task.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt: task.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
	if task.Description != nil {
		out.Description = *task.Description
	}
	if task.ParentTaskID != nil {
		out.ParentTaskId = task.ParentTaskID
	}
	return out
}
