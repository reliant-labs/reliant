package task

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/planproto"
)

// GetTask retrieves a task by ID
func (s *Service) GetTask(
	ctx context.Context,
	req *connect.Request[reliantv1.GetTaskRequest],
) (*connect.Response[reliantv1.GetTaskResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.taskBelongsToUser(ctx, req.Msg.TaskId, userID); err != nil {
		return nil, err
	}

	task, err := s.deps.Repo.GetTask(ctx, req.Msg.TaskId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get task"))
	}

	resp := &reliantv1.GetTaskResponse{
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
