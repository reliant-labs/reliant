package task

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/planproto"
)

// ListTasks lists tasks (optionally filtered by plan)
func (s *Service) ListTasks(
	ctx context.Context,
	req *connect.Request[reliantv1.ListTasksRequest],
) (*connect.Response[reliantv1.ListTasksResponse], error) {
	userID := auth.MustGetUserID(ctx)
	// Serves viewers of a shared chat (authz.WithViewerReads).
	ctx = authz.WithViewerReads(ctx)

	if req.Msg.GetPlanId() == "" {
		return nil, svcerr.InvalidArgument("plan_id is required")
	}

	// The owner of the plan's chat, or a viewer of it shared.
	plan, err := s.deps.Repo.GetPlan(ctx, *req.Msg.PlanId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get plan"))
	}

	chatID, err := s.getChatIDForPlan(ctx, plan)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}

	chat, err := s.deps.Repo.GetChatRow(ctx, chatID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}

	if _, denied := authz.ReadChatAccess(ctx, userID, chat); denied != nil {
		return nil, svcerr.PermissionDenied("access denied")
	}

	tasks, err := s.deps.Repo.ListTasksByPlan(ctx, *req.Msg.PlanId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list tasks"))
	}

	protoTasks := make([]*reliantv1.Task, len(tasks))
	for i, task := range tasks {
		protoTasks[i] = &reliantv1.Task{
			Id:        task.ID,
			PlanId:    task.PlanID,
			Title:     task.Title,
			Status:    planproto.TaskStatus(task.Status),
			Position:  int32(task.Position),
			CreatedAt: task.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
			UpdatedAt: task.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
		}
		if task.Description != nil {
			protoTasks[i].Description = *task.Description
		}
		if task.ParentTaskID != nil {
			protoTasks[i].ParentTaskId = task.ParentTaskID
		}
	}

	return connect.NewResponse(&reliantv1.ListTasksResponse{
		Tasks: protoTasks,
		Total: int32(len(tasks)),
	}), nil
}
