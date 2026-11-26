package plan

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
	"github.com/reliant-labs/reliant/internal/store"
)

// updatablePlanFields are the columns UpdatePlan's update_mask may name.
var updatablePlanFields = []string{store.PlanFieldTitle, store.PlanFieldDescription, store.PlanFieldStatus, store.PlanFieldComplexity}

// CreatePlan creates a plan in the caller's chat.
func (s *Service) CreatePlan(ctx context.Context, req *connect.Request[pb.CreatePlanRequest]) (*connect.Response[pb.CreatePlanResponse], error) {
	userID := auth.MustGetUserID(ctx)
	op := s.crudCreatePlanOp()

	var chatID string
	entity := op.Entity
	op.Entity = func(ctx context.Context, req *pb.CreatePlanRequest) (*store.Plan, error) {
		if req.ChatId == "" {
			return nil, svcerr.InvalidArgument("chat_id is required")
		}
		if req.Title == "" {
			return nil, svcerr.InvalidArgument("title is required")
		}
		e, err := entity(ctx, req)
		if err != nil {
			return nil, err
		}
		chatID = req.ChatId
		e.ID = uuid.New().String()
		e.ThreadID = req.ChatId // CreatePlan uses chat_id as thread_id for now
		if req.Status == pb.PlanStatus_PLAN_STATUS_UNSPECIFIED {
			pending := int32(pb.PlanStatus_PLAN_STATUS_PENDING)
			e.Status = &pending
		}
		return e, nil
	}
	persist := op.Persist
	op.Persist = func(ctx context.Context, e *store.Plan) error {
		// Read-check-write: the chat read and the insert retry as one unit.
		return s.deps.DB.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
			if _, err := s.writableChatOfThread(ctx, userID, e.ThreadID); err != nil {
				return err
			}
			if err := persist(ctx, e); err != nil {
				return err
			}
			return orm.AfterCommit(ctx, func(ctx context.Context) { s.emitPlanTasksRefetch(ctx, chatID) })
		})
	}
	pack := op.Pack
	op.Pack = func(e *store.Plan) (*pb.CreatePlanResponse, error) {
		resp, err := pack(e)
		if err != nil {
			return nil, err
		}
		resp.Plan.ChatId = chatID
		return resp, nil
	}
	return crud.HandleCreate(op)(ctx, req)
}

// GetPlan retrieves a plan the caller may write, with the chat it belongs to.
func (s *Service) GetPlan(ctx context.Context, req *connect.Request[pb.GetPlanRequest]) (*connect.Response[pb.GetPlanResponse], error) {
	userID := auth.MustGetUserID(ctx)
	op := s.crudGetPlanOp()

	var chatID string
	fetch := op.Fetch
	op.Fetch = func(ctx context.Context, id string, opts ...orm.QueryOption) (*store.Plan, error) {
		if id == "" {
			return nil, svcerr.InvalidArgument("id is required")
		}
		e, err := fetch(ctx, id, opts...)
		if err != nil {
			return nil, err
		}
		chat, err := s.writableChatOfThread(ctx, userID, e.ThreadID)
		if err != nil {
			return nil, err
		}
		chatID = chat.ID
		return e, nil
	}
	pack := op.Pack
	op.Pack = func(e *store.Plan) (*pb.GetPlanResponse, error) {
		resp, err := pack(e)
		if err != nil {
			return nil, err
		}
		resp.Plan.ChatId = chatID
		return resp, nil
	}
	return crud.HandleGet(op)(ctx, req)
}

// UpdatePlan writes the plan fields update_mask names, on a plan the caller
// may write. An empty mask is refused: no write is ever a full replace.
func (s *Service) UpdatePlan(ctx context.Context, req *connect.Request[pb.UpdatePlanRequest]) (*connect.Response[pb.UpdatePlanResponse], error) {
	userID := auth.MustGetUserID(ctx)
	op := s.crudUpdatePlanOp()

	var chatID string
	op.Persist = func(context.Context, *store.Plan, ...orm.QueryOption) error {
		return svcerr.InvalidArgument("update_mask is required")
	}
	persistMasked := op.PersistMasked
	op.PersistMasked = func(ctx context.Context, e *store.Plan, fields []string, opts ...orm.QueryOption) error {
		for _, f := range fields {
			if !slices.Contains(updatablePlanFields, f) {
				return svcerr.InvalidArgument("update_mask path " + f + " is not updatable")
			}
		}
		// Read-check-write: the authorizing reads and the update retry as one unit.
		return s.deps.DB.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
			// scope: the chat check below (writableChatOfThread) refuses a caller who may not write it, in this transaction
			stored, err := store.GetPlanByID(ctx, s.deps.DB, e.ID)
			if err != nil {
				return err
			}
			chat, err := s.writableChatOfThread(ctx, userID, stored.ThreadID)
			if err != nil {
				return err
			}
			chatID = chat.ID
			if err := persistMasked(ctx, e, fields, opts...); err != nil {
				return err
			}
			return orm.AfterCommit(ctx, func(ctx context.Context) { s.emitPlanTasksRefetch(ctx, chat.ID) })
		})
	}
	pack := op.Pack
	op.Pack = func(e *store.Plan) (*pb.UpdatePlanResponse, error) {
		resp, err := pack(e)
		if err != nil {
			return nil, err
		}
		resp.Plan.ChatId = chatID
		return resp, nil
	}
	return crud.HandleUpdate(op)(ctx, req)
}

// DeletePlan deletes a plan the caller may write.
func (s *Service) DeletePlan(ctx context.Context, req *connect.Request[pb.DeletePlanRequest]) (*connect.Response[pb.DeletePlanResponse], error) {
	userID := auth.MustGetUserID(ctx)
	op := s.crudDeletePlanOp()

	persist := op.Persist
	op.Persist = func(ctx context.Context, id string, opts ...orm.QueryOption) error {
		if id == "" {
			return svcerr.InvalidArgument("id is required")
		}
		// Read-check-write: the authorizing reads and the delete retry as one unit.
		return s.deps.DB.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
			// scope: the chat check below (writableChatOfThread) refuses a caller who may not write it, in this transaction
			stored, err := store.GetPlanByID(ctx, s.deps.DB, id)
			if err != nil {
				return err
			}
			chat, err := s.writableChatOfThread(ctx, userID, stored.ThreadID)
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
