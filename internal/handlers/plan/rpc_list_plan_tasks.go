package plan

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/planproto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ListPlanTasks lists all tasks for a plan
func (s *Service) ListPlanTasks(
	ctx context.Context,
	req *connect.Request[reliantv1.ListPlanTasksRequest],
) (*connect.Response[reliantv1.ListPlanTasksResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.PlanId == "" {
		return nil, svcerr.InvalidArgument("plan_id is required")
	}
	plan, err := s.deps.Plans.GetPlan(ctx, req.Msg.PlanId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get plan"))
	}
	if _, err := s.writableChatOfThread(ctx, userID, plan.ThreadID); err != nil {
		return nil, err
	}

	tasks, err := s.deps.Tasks.ListTasksByPlan(ctx, req.Msg.PlanId)
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
			CreatedAt: timestamppb.New(task.CreatedAt),
			UpdatedAt: timestamppb.New(task.UpdatedAt),
		}
		if task.Description != nil {
			protoTasks[i].Description = *task.Description
		}
		if task.ParentTaskID != nil {
			protoTasks[i].ParentTaskId = task.ParentTaskID
		}
	}

	return connect.NewResponse(&reliantv1.ListPlanTasksResponse{
		Tasks: protoTasks,
		Total: int32(len(tasks)),
	}), nil
}
