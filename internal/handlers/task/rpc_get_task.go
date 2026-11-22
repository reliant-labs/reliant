package task

import (
	"context"

	"connectrpc.com/connect"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
)

// GetTask retrieves a task by ID
func (s *Service) GetTask(
	ctx context.Context,
	req *connect.Request[reliantv1.GetTaskRequest],
) (*connect.Response[reliantv1.GetTaskResponse], error) {
	userID := auth.MustGetUserID(ctx)

	task, _, err := s.writableTask(ctx, userID, req.Msg.TaskId)
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&reliantv1.GetTaskResponse{Task: taskToProto(task)}), nil
}
