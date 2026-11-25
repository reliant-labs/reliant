package task

import (
	"context"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
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

	var task *core.Task
	var chat *core.ChatRow
	// Read-check-write: the authorizing reads and the insert retry as one unit.
	err := s.deps.Tx.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
		var err error
		if chat, err = s.chatOfPlanID(ctx, req.Msg.PlanId); err != nil {
			return err
		}
		if !authz.CanOwnChat(ctx, userID, chat) {
			return svcerr.PermissionDenied("access denied")
		}

		if req.Msg.Title == "" {
			return svcerr.InvalidArgument("title is required")
		}

		statusVal := int32(req.Msg.Status)
		if statusVal == 0 {
			statusVal = int32(reliantv1.TaskStatus_TASK_STATUS_PENDING)
		}

		task = &core.Task{
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
		if err := s.deps.Tasks.InsertTask(ctx, task); err != nil {
			return svcerr.WrapOr(err, svcerr.Internal("failed to create task"))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	s.emitPlanTasksRefetch(ctx, chat.ID)

	return connect.NewResponse(&reliantv1.CreateTaskResponse{Task: taskToProto(task)}), nil
}
