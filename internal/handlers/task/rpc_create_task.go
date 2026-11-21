package task

import (
	"context"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/planproto"
	"github.com/reliant-labs/reliant/internal/ptr"
)

// CreateTask creates a new task
func (s *Service) CreateTask(
	ctx context.Context,
	req *connect.Request[reliantv1.CreateTaskRequest],
) (*connect.Response[reliantv1.CreateTaskResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.PlanId == "" {
		return nil, svcerr.InvalidArgument("plan_id is required")
	}

	// Verify user owns the plan
	plan, err := s.deps.Repo.GetPlan(ctx, req.Msg.PlanId)
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

	if !authz.CanWriteChat(ctx, userID, chat) {
		return nil, svcerr.PermissionDenied("access denied")
	}

	if req.Msg.Title == "" {
		return nil, svcerr.InvalidArgument("title is required")
	}

	statusVal := int32(req.Msg.Status)
	if statusVal == 0 {
		statusVal = int32(reliantv1.TaskStatus_TASK_STATUS_PENDING)
	}

	task := &db.Task{
		ID:          uuid.New().String(),
		PlanID:      req.Msg.PlanId,
		Title:       req.Msg.Title,
		Description: ptr.StringIfNotEmpty(req.Msg.Description),
		Status:      statusVal,
		Position:    int(req.Msg.GetPosition()),
	}

	if req.Msg.ParentTaskId != nil {
		task.ParentTaskID = req.Msg.ParentTaskId
	}

	if err := s.deps.Repo.CreateTask(ctx, task); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to create task"))
	}

	if err := s.deps.Repo.EmitChatRefetch(ctx, chatID, db.RefetchPlanTasks); err != nil {
		logging.WarnContext(ctx, "[TaskService] Failed to emit plan_tasks refetch", "error", err)
	}

	resp := &reliantv1.CreateTaskResponse{
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
