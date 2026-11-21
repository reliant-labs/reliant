package task

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/planproto"
)

// UpdateTask updates an existing task
func (s *Service) UpdateTask(
	ctx context.Context,
	req *connect.Request[reliantv1.UpdateTaskRequest],
) (*connect.Response[reliantv1.UpdateTaskResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.taskBelongsToUser(ctx, req.Msg.TaskId, userID); err != nil {
		return nil, err
	}

	task, err := s.deps.Repo.GetTask(ctx, req.Msg.TaskId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get task"))
	}

	// Update fields
	if req.Msg.Title != nil {
		task.Title = *req.Msg.Title
	}
	if req.Msg.Description != nil {
		task.Description = req.Msg.Description
	}
	if req.Msg.Status != nil {
		task.Status = int32(*req.Msg.Status)
	}
	if req.Msg.Position != nil {
		task.Position = int(*req.Msg.Position)
	}

	if err := s.deps.Repo.UpdateTask(ctx, task); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to update task"))
	}

	// Get chatID from the plan's thread for refetch emission
	if taskPlan, err := s.deps.Repo.GetPlan(ctx, task.PlanID); err == nil {
		if cid, err := s.getChatIDForPlan(ctx, taskPlan); err == nil {
			if err := s.deps.Repo.EmitChatRefetch(ctx, cid, db.RefetchPlanTasks); err != nil {
				logging.WarnContext(ctx, "[TaskService] Failed to emit plan_tasks refetch", "error", err)
			}
		}
	}

	resp := &reliantv1.UpdateTaskResponse{
		Task: &reliantv1.Task{
			Id:        task.ID,
			PlanId:    task.PlanID,
			Title:     task.Title,
			Status:    planproto.TaskStatus(task.Status),
			Position:  int32(task.Position),
			CreatedAt: task.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
			UpdatedAt: task.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
		},
	}
	if task.Description != nil {
		resp.Task.Description = *task.Description
	}
	if task.ParentTaskID != nil {
		resp.Task.ParentTaskId = task.ParentTaskID
	}

	return connect.NewResponse(resp), nil
}
