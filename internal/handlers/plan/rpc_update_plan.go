package plan

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/planproto"
)

// UpdatePlan updates an existing plan
func (s *Service) UpdatePlan(
	ctx context.Context,
	req *connect.Request[reliantv1.UpdatePlanRequest],
) (*connect.Response[reliantv1.UpdatePlanResponse], error) {
	userID := auth.MustGetUserID(ctx)

	var plan *core.Plan
	var chat *core.ChatRow
	// Read-check-write: the authorizing reads and the update retry as one unit.
	err := s.deps.Tx.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
		var err error
		if plan, chat, err = s.writablePlan(ctx, userID, req.Msg.PlanId); err != nil {
			return err
		}

		// Update fields
		if req.Msg.Title != nil {
			plan.Title = *req.Msg.Title
		}
		if req.Msg.Description != nil {
			plan.Description = req.Msg.Description
		}
		if req.Msg.Status != nil {
			plan.Status = int32(*req.Msg.Status)
		}
		if req.Msg.Complexity != nil {
			plan.Complexity = planproto.PlanComplexityToInt32Ptr(req.Msg.Complexity)
		}

		if err := s.deps.Plans.ReplacePlan(ctx, plan); err != nil {
			return svcerr.WrapOr(err, svcerr.Internal("failed to update plan"))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	s.emitPlanTasksRefetch(ctx, chat.ID)

	return connect.NewResponse(&reliantv1.UpdatePlanResponse{Plan: planToProto(plan, chat.ID)}), nil
}
