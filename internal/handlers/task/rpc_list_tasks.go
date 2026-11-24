package task

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
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
	chat, err := s.chatOfPlanID(ctx, *req.Msg.PlanId)
	if err != nil {
		return nil, err
	}
	if _, denied := authz.ReadChatAccess(ctx, userID, chat); denied != nil {
		return nil, svcerr.PermissionDenied("access denied")
	}

	tasks, err := s.deps.Tasks.ListTasksByPlan(ctx, *req.Msg.PlanId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list tasks"))
	}

	protoTasks := make([]*reliantv1.Task, len(tasks))
	for i, task := range tasks {
		protoTasks[i] = taskToProto(task)
	}

	return connect.NewResponse(&reliantv1.ListTasksResponse{
		Tasks: protoTasks,
		Total: int32(len(tasks)),
	}), nil
}
