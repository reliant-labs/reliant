package task

import (
	"context"
	"slices"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/crud"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/store"
)

// updatableTaskFields are the columns UpdateTask's update_mask may name.
var updatableTaskFields = []string{
	store.TaskFieldTitle, store.TaskFieldDescription, store.TaskFieldStatus,
	store.TaskFieldPosition, store.TaskFieldMetadata, store.TaskFieldAssignee,
}

// CreateTask creates a task in a plan of the caller's chat.
func (s *Service) CreateTask(ctx context.Context, req *connect.Request[pb.CreateTaskRequest]) (*connect.Response[pb.CreateTaskResponse], error) {
	userID := auth.MustGetUserID(ctx)
	op := s.crudCreateTaskOp()

	entity := op.Entity
	op.Entity = func(ctx context.Context, req *pb.CreateTaskRequest) (*store.Task, error) {
		if req.PlanId == "" {
			return nil, svcerr.InvalidArgument("plan_id is required")
		}
		if req.Title == "" {
			return nil, svcerr.InvalidArgument("title is required")
		}
		e, err := entity(ctx, req)
		if err != nil {
			return nil, err
		}
		e.ID = uuid.New().String()
		if req.Status == pb.TaskStatus_TASK_STATUS_UNSPECIFIED {
			pending := int32(pb.TaskStatus_TASK_STATUS_PENDING)
			e.Status = &pending
		}
		return e, nil
	}
	persist := op.Persist
	op.Persist = func(ctx context.Context, e *store.Task) error {
		// Read-check-write: the authorizing reads and the insert retry as one unit.
		return s.deps.DB.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
			chat, err := s.writableChatOfPlan(ctx, userID, e.PlanID)
			if err != nil {
				return err
			}
			if err := persist(ctx, e); err != nil {
				return err
			}
			return orm.AfterCommit(ctx, func(ctx context.Context) { s.emitPlanTasksRefetch(ctx, chat.ID) })
		})
	}
	return crud.HandleCreate(op)(ctx, req)
}

// GetTask retrieves a task in a plan of a chat the caller may write.
func (s *Service) GetTask(ctx context.Context, req *connect.Request[pb.GetTaskRequest]) (*connect.Response[pb.GetTaskResponse], error) {
	userID := auth.MustGetUserID(ctx)
	op := s.crudGetTaskOp()

	fetch := op.Fetch
	op.Fetch = func(ctx context.Context, id string, opts ...orm.QueryOption) (*store.Task, error) {
		if id == "" {
			return nil, svcerr.InvalidArgument("id is required")
		}
		e, err := fetch(ctx, id, opts...)
		if err != nil {
			return nil, err
		}
		if _, err := s.writableChatOfPlan(ctx, userID, e.PlanID); err != nil {
			return nil, err
		}
		return e, nil
	}
	return crud.HandleGet(op)(ctx, req)
}

// ListTasks lists a plan's tasks for the owner of its chat, or a viewer of it
// shared. plan_id is required.
func (s *Service) ListTasks(ctx context.Context, req *connect.Request[pb.ListTasksRequest]) (*connect.Response[pb.ListTasksResponse], error) {
	userID := auth.MustGetUserID(ctx)
	// Serves viewers of a shared chat (authz.WithViewerReads).
	ctx = authz.WithViewerReads(ctx)
	op := s.crudListTasksOp()

	filters := op.Filters
	op.Filters = func(ctx context.Context, req *pb.ListTasksRequest) ([]orm.QueryOption, error) {
		if req.GetPlanId() == "" {
			return nil, svcerr.InvalidArgument("plan_id is required")
		}
		chat, err := s.chatOfPlan(ctx, req.GetPlanId())
		if err != nil {
			return nil, err
		}
		if _, denied := authz.ReadChatAccess(ctx, userID, chat); denied != nil {
			return nil, svcerr.NotFound("task")
		}
		var opts []orm.QueryOption
		if filters != nil {
			if opts, err = filters(ctx, req); err != nil {
				return nil, err
			}
		}
		return append(opts, orm.WithOrderBy(store.TaskFieldPosition, orm.Asc)), nil
	}
	return crud.HandleList(op)(ctx, req)
}

// UpdateTask writes the task fields update_mask names, on a task the caller
// may write. An empty mask is refused: no write is ever a full replace.
func (s *Service) UpdateTask(ctx context.Context, req *connect.Request[pb.UpdateTaskRequest]) (*connect.Response[pb.UpdateTaskResponse], error) {
	userID := auth.MustGetUserID(ctx)
	op := s.crudUpdateTaskOp()

	op.Persist = func(context.Context, *store.Task, ...orm.QueryOption) error {
		return svcerr.InvalidArgument("update_mask is required")
	}
	persistMasked := op.PersistMasked
	op.PersistMasked = func(ctx context.Context, e *store.Task, fields []string, opts ...orm.QueryOption) error {
		for _, f := range fields {
			if !slices.Contains(updatableTaskFields, f) {
				return svcerr.InvalidArgument("update_mask path " + f + " is not updatable")
			}
		}
		// Read-check-write: the authorizing reads and the update retry as one unit.
		return s.deps.DB.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
			// scope: the chat check below (writableChatOfPlan) refuses a caller who may not write it, in this transaction
			stored, err := store.GetTaskByID(ctx, s.deps.DB, e.ID)
			if err != nil {
				return err
			}
			chat, err := s.writableChatOfPlan(ctx, userID, stored.PlanID)
			if err != nil {
				return err
			}
			if err := persistMasked(ctx, e, fields, opts...); err != nil {
				return err
			}
			return orm.AfterCommit(ctx, func(ctx context.Context) { s.emitPlanTasksRefetch(ctx, chat.ID) })
		})
	}
	return crud.HandleUpdate(op)(ctx, req)
}

// DeleteTask deletes a task the caller may write.
func (s *Service) DeleteTask(ctx context.Context, req *connect.Request[pb.DeleteTaskRequest]) (*connect.Response[pb.DeleteTaskResponse], error) {
	userID := auth.MustGetUserID(ctx)
	op := s.crudDeleteTaskOp()

	persist := op.Persist
	op.Persist = func(ctx context.Context, id string, opts ...orm.QueryOption) error {
		if id == "" {
			return svcerr.InvalidArgument("id is required")
		}
		// Read-check-write: the authorizing reads and the delete retry as one unit.
		return s.deps.DB.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
			// scope: the chat check below (writableChatOfPlan) refuses a caller who may not write it, in this transaction
			stored, err := store.GetTaskByID(ctx, s.deps.DB, id)
			if err != nil {
				return err
			}
			chat, err := s.writableChatOfPlan(ctx, userID, stored.PlanID)
			if err != nil {
				return err
			}
			if err := persist(ctx, id, opts...); err != nil {
				return err
			}
			return orm.AfterCommit(ctx, func(ctx context.Context) { s.emitPlanTasksRefetch(ctx, chat.ID) })
		})
	}
	return crud.HandleDelete(op)(ctx, req)
}
