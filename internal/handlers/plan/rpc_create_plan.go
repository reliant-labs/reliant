package plan

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
	"github.com/reliant-labs/reliant/internal/planproto"
	"github.com/reliant-labs/reliant/internal/ptr"
)

// CreatePlan creates a new plan
func (s *Service) CreatePlan(
	ctx context.Context,
	req *connect.Request[reliantv1.CreatePlanRequest],
) (*connect.Response[reliantv1.CreatePlanResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}

	var plan *core.Plan
	// Read-check-write: the chat read and the insert retry as one unit.
	err := s.deps.Tx.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
		chat, err := s.deps.Chats.GetChatRow(ctx, req.Msg.ChatId)
		if err != nil {
			return svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
		}
		if !authz.CanWriteChat(ctx, userID, chat) {
			return svcerr.PermissionDenied("access denied")
		}

		if req.Msg.Title == "" {
			return svcerr.InvalidArgument("title is required")
		}

		statusVal := int32(req.Msg.Status)
		if statusVal == 0 {
			statusVal = int32(reliantv1.PlanStatus_PLAN_STATUS_PENDING)
		}

		plan = &core.Plan{
			ID:          uuid.New().String(),
			ThreadID:    req.Msg.ChatId, // gRPC CreatePlan uses chat_id as thread_id for now
			Title:       req.Msg.Title,
			Description: ptr.StringIfNotEmpty(req.Msg.Description),
			Status:      statusVal,
			Complexity:  planproto.PlanComplexityToInt32Ptr(&req.Msg.Complexity),
		}
		if err := s.deps.Plans.InsertPlan(ctx, plan); err != nil {
			return svcerr.WrapOr(err, svcerr.Internal("failed to create plan"))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	s.emitPlanTasksRefetch(ctx, req.Msg.ChatId)

	return connect.NewResponse(&reliantv1.CreatePlanResponse{Plan: planToProto(plan, req.Msg.ChatId)}), nil
}
