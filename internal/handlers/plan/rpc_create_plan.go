package plan

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

// CreatePlan creates a new plan
func (s *Service) CreatePlan(
	ctx context.Context,
	req *connect.Request[reliantv1.CreatePlanRequest],
) (*connect.Response[reliantv1.CreatePlanResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}

	// Verify user owns the chat
	chat, err := s.deps.Repo.GetChatRow(ctx, req.Msg.ChatId)
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
		statusVal = int32(reliantv1.PlanStatus_PLAN_STATUS_PENDING)
	}

	plan := &db.Plan{
		ID:          uuid.New().String(),
		ThreadID:    req.Msg.ChatId, // gRPC CreatePlan uses chat_id as thread_id for now
		Title:       req.Msg.Title,
		Description: ptr.StringIfNotEmpty(req.Msg.Description),
		Status:      statusVal,
		Complexity:  planproto.PlanComplexityToInt32Ptr(&req.Msg.Complexity),
	}

	if err := s.deps.Repo.CreatePlan(ctx, plan); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to create plan"))
	}

	if err := s.deps.Repo.EmitChatRefetch(ctx, req.Msg.ChatId, db.RefetchPlanTasks); err != nil {
		logging.WarnContext(ctx, "[PlanService] Failed to emit plan_tasks refetch", "error", err)
	}

	resp := &reliantv1.CreatePlanResponse{
		Plan: &reliantv1.Plan{
			Id:        plan.ID,
			ChatId:    req.Msg.ChatId,
			Title:     plan.Title,
			Status:    planproto.PlanStatus(plan.Status),
			CreatedAt: plan.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
			UpdatedAt: plan.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
		},
	}
	if plan.Description != nil {
		resp.Plan.Description = *plan.Description
	}
	if plan.Complexity != nil {
		resp.Plan.Complexity = planproto.PlanComplexity(*plan.Complexity)
	}

	return connect.NewResponse(resp), nil
}
