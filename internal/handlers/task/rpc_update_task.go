package task

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// UpdateTask updates an existing task
func (s *Service) UpdateTask(
	ctx context.Context,
	req *connect.Request[reliantv1.UpdateTaskRequest],
) (*connect.Response[reliantv1.UpdateTaskResponse], error) {
	userID := auth.MustGetUserID(ctx)

	var task *core.Task
	var chat *core.ChatRow
	// Read-check-write: the authorizing reads and the update retry as one unit.
	err := s.deps.Tx.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
		var err error
		if task, chat, err = s.writableTask(ctx, userID, req.Msg.TaskId); err != nil {
			return err
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

		if err := s.deps.Tasks.ReplaceTask(ctx, task); err != nil {
			return svcerr.WrapOr(err, svcerr.Internal("failed to update task"))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	s.emitPlanTasksRefetch(ctx, chat.ID)

	return connect.NewResponse(&reliantv1.UpdateTaskResponse{Task: taskToProto(task)}), nil
}
