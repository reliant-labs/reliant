package task

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
)

// DeleteTask deletes a task
func (s *Service) DeleteTask(
	ctx context.Context,
	req *connect.Request[reliantv1.DeleteTaskRequest],
) (*connect.Response[reliantv1.DeleteTaskResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.taskBelongsToUser(ctx, req.Msg.TaskId, userID); err != nil {
		return nil, err
	}

	// Get chatID before deleting (via plan's thread)
	var deleteChatID string
	if task, err := s.deps.Repo.GetTask(ctx, req.Msg.TaskId); err == nil {
		if taskPlan, err := s.deps.Repo.GetPlan(ctx, task.PlanID); err == nil {
			if cid, err := s.getChatIDForPlan(ctx, taskPlan); err == nil {
				deleteChatID = cid
			}
		}
	}

	if err := s.deps.Repo.DeleteTask(ctx, req.Msg.TaskId); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to delete task"))
	}

	if deleteChatID != "" {
		if err := s.deps.Repo.EmitChatRefetch(ctx, deleteChatID, db.RefetchPlanTasks); err != nil {
			logging.WarnContext(ctx, "[TaskService] Failed to emit plan_tasks refetch", "error", err)
		}
	}

	return connect.NewResponse(&reliantv1.DeleteTaskResponse{Success: true}), nil
}
