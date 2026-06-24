// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/ptr"
)

// PlanService implements the PlanService RPC handlers
type PlanService struct {
	reliantv1connect.UnimplementedPlanServiceHandler
	database db.Repository
}

// NewPlanService creates a new PlanService
func NewPlanService(database db.Repository) *PlanService {
	return &PlanService{database: database}
}

// getChatIDForPlan looks up the chat (conversation) ID for a plan by resolving its thread.
func (s *PlanService) getChatIDForPlan(ctx context.Context, plan *db.Plan) (string, error) {
	thread, err := s.database.GetThread(ctx, plan.ThreadID)
	if err != nil {
		return "", fmt.Errorf("failed to get thread for plan: %w", err)
	}
	return thread.ConversationID, nil
}

// planBelongsToUser checks if a plan belongs to a user via the thread's chat
func (s *PlanService) planBelongsToUser(ctx context.Context, planID string, userID string) error {
	plan, err := s.database.GetPlan(ctx, planID)
	if err != nil {
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("plan not found"))
	}

	chatID, err := s.getChatIDForPlan(ctx, plan)
	if err != nil {
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}

	chat, err := s.database.GetChat(ctx, chatID)
	if err != nil {
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}

	if chat.UserID != userID {
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("access denied"))
	}

	return nil
}

// CreatePlan creates a new plan
func (s *PlanService) CreatePlan(
	ctx context.Context,
	req *connect.Request[reliantv1.CreatePlanRequest],
) (*connect.Response[reliantv1.CreatePlanResponse], error) {
	userID := auth.MustGetUserID(ctx)

	// Verify user owns the chat
	chat, err := s.database.GetChat(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}
	if chat.UserID != userID {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("access denied"))
	}

	// Validate
	if req.Msg.ChatId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("chat_id is required"))
	}
	if req.Msg.Title == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("title is required"))
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
		Complexity:  planComplexityToInt32Ptr(&req.Msg.Complexity),
	}

	if err := s.database.CreatePlan(ctx, plan); err != nil {
		logging.Error("Failed to create plan", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create plan"))
	}

	if err := s.database.EmitChatRefetch(ctx, req.Msg.ChatId, db.RefetchPlanTasks); err != nil {
		logging.Warn("[PlanService] Failed to emit plan_tasks refetch", "error", err)
	}

	resp := &reliantv1.CreatePlanResponse{
		Plan: &reliantv1.Plan{
			Id:        plan.ID,
			ChatId:    req.Msg.ChatId,
			Title:     plan.Title,
			Status:    planStatusFromInt32(plan.Status),
			CreatedAt: plan.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
			UpdatedAt: plan.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
		},
	}
	if plan.Description != nil {
		resp.Plan.Description = *plan.Description
	}
	if plan.Complexity != nil {
		resp.Plan.Complexity = planComplexityFromInt32(*plan.Complexity)
	}

	return connect.NewResponse(resp), nil
}

// GetPlan retrieves a plan by ID
func (s *PlanService) GetPlan(
	ctx context.Context,
	req *connect.Request[reliantv1.GetPlanRequest],
) (*connect.Response[reliantv1.GetPlanResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.planBelongsToUser(ctx, req.Msg.PlanId, userID); err != nil {
		return nil, err
	}

	plan, err := s.database.GetPlan(ctx, req.Msg.PlanId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("plan not found"))
	}

	chatID, _ := s.getChatIDForPlan(ctx, plan)

	resp := &reliantv1.GetPlanResponse{
		Plan: &reliantv1.Plan{
			Id:        plan.ID,
			ChatId:    chatID,
			Title:     plan.Title,
			Status:    planStatusFromInt32(plan.Status),
			CreatedAt: plan.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
			UpdatedAt: plan.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
		},
	}
	if plan.Description != nil {
		resp.Plan.Description = *plan.Description
	}
	if plan.Complexity != nil {
		resp.Plan.Complexity = planComplexityFromInt32(*plan.Complexity)
	}

	return connect.NewResponse(resp), nil
}

// GetPlanByChatId retrieves a plan by chat ID
func (s *PlanService) GetPlanByChatId(
	ctx context.Context,
	req *connect.Request[reliantv1.GetPlanByChatIdRequest],
) (*connect.Response[reliantv1.GetPlanByChatIdResponse], error) {
	userID := auth.MustGetUserID(ctx)

	// Verify the chat belongs to the user
	chat, err := s.database.GetChat(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}

	if chat.UserID != userID {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("access denied"))
	}

	// Get plans for this chat (via thread join)
	plans, err := s.database.ListPlansByChatID(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get plan: %w", err))
	}

	// If no plans found, return empty response
	if len(plans) == 0 {
		return connect.NewResponse(&reliantv1.GetPlanByChatIdResponse{}), nil
	}

	// Return the first plan
	plan := plans[0]
	resp := &reliantv1.GetPlanByChatIdResponse{
		Plan: &reliantv1.Plan{
			Id:        plan.ID,
			ChatId:    req.Msg.ChatId,
			Title:     plan.Title,
			Status:    planStatusFromInt32(plan.Status),
			CreatedAt: plan.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
			UpdatedAt: plan.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
		},
	}
	if plan.Description != nil {
		resp.Plan.Description = *plan.Description
	}
	if plan.Complexity != nil {
		resp.Plan.Complexity = planComplexityFromInt32(*plan.Complexity)
	}

	return connect.NewResponse(resp), nil
}

// UpdatePlan updates an existing plan
func (s *PlanService) UpdatePlan(
	ctx context.Context,
	req *connect.Request[reliantv1.UpdatePlanRequest],
) (*connect.Response[reliantv1.UpdatePlanResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.planBelongsToUser(ctx, req.Msg.PlanId, userID); err != nil {
		return nil, err
	}

	plan, err := s.database.GetPlan(ctx, req.Msg.PlanId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("plan not found"))
	}

	chatID, _ := s.getChatIDForPlan(ctx, plan)

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
		plan.Complexity = planComplexityToInt32Ptr(req.Msg.Complexity)
	}

	if err := s.database.UpdatePlan(ctx, plan); err != nil {
		logging.Error("Failed to update plan", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update plan"))
	}

	if err := s.database.EmitChatRefetch(ctx, chatID, db.RefetchPlanTasks); err != nil {
		logging.Warn("[PlanService] Failed to emit plan_tasks refetch", "error", err)
	}

	resp := &reliantv1.UpdatePlanResponse{
		Plan: &reliantv1.Plan{
			Id:        plan.ID,
			ChatId:    chatID,
			Title:     plan.Title,
			Status:    planStatusFromInt32(plan.Status),
			CreatedAt: plan.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
			UpdatedAt: plan.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
		},
	}
	if plan.Description != nil {
		resp.Plan.Description = *plan.Description
	}
	if plan.Complexity != nil {
		resp.Plan.Complexity = planComplexityFromInt32(*plan.Complexity)
	}

	return connect.NewResponse(resp), nil
}

// DeletePlan deletes a plan
func (s *PlanService) DeletePlan(
	ctx context.Context,
	req *connect.Request[reliantv1.DeletePlanRequest],
) (*connect.Response[reliantv1.DeletePlanResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.planBelongsToUser(ctx, req.Msg.PlanId, userID); err != nil {
		return nil, err
	}

	// Get chatID before deleting
	plan, err := s.database.GetPlan(ctx, req.Msg.PlanId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("plan not found"))
	}

	chatID, _ := s.getChatIDForPlan(ctx, plan)

	if err := s.database.DeletePlan(ctx, req.Msg.PlanId); err != nil {
		logging.Error("Failed to delete plan", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to delete plan"))
	}

	if err := s.database.EmitChatRefetch(ctx, chatID, db.RefetchPlanTasks); err != nil {
		logging.Warn("[PlanService] Failed to emit plan_tasks refetch", "error", err)
	}

	return connect.NewResponse(&reliantv1.DeletePlanResponse{Success: true}), nil
}

// ListPlanTasks lists all tasks for a plan
func (s *PlanService) ListPlanTasks(
	ctx context.Context,
	req *connect.Request[reliantv1.ListPlanTasksRequest],
) (*connect.Response[reliantv1.ListPlanTasksResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.planBelongsToUser(ctx, req.Msg.PlanId, userID); err != nil {
		return nil, err
	}

	tasks, err := s.database.ListTasksByPlan(ctx, req.Msg.PlanId)
	if err != nil {
		logging.Error("Failed to list tasks", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list tasks"))
	}

	protoTasks := make([]*reliantv1.Task, len(tasks))
	for i, task := range tasks {
		protoTasks[i] = &reliantv1.Task{
			Id:        task.ID,
			PlanId:    task.PlanID,
			Title:     task.Title,
			Status:    taskStatusFromInt32(task.Status),
			Position:  int32(task.Position),
			CreatedAt: task.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
			UpdatedAt: task.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
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
