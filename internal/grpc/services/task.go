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

// TaskService implements the TaskService RPC handlers
type TaskService struct {
	reliantv1connect.UnimplementedTaskServiceHandler
	database db.Repository
}

// NewTaskService creates a new TaskService
func NewTaskService(database db.Repository) *TaskService {
	return &TaskService{database: database}
}

// getChatIDForPlan looks up the chat (conversation) ID for a plan by resolving its thread.
func (s *TaskService) getChatIDForPlan(ctx context.Context, plan *db.Plan) (string, error) {
	thread, err := s.database.GetThread(ctx, plan.ThreadID)
	if err != nil {
		return "", fmt.Errorf("failed to get thread for plan: %w", err)
	}
	return thread.ConversationID, nil
}

// taskBelongsToUser checks if a task belongs to a user via the plan's thread and chat
func (s *TaskService) taskBelongsToUser(ctx context.Context, taskID string, userID string) error {
	task, err := s.database.GetTask(ctx, taskID)
	if err != nil {
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("task not found"))
	}

	plan, err := s.database.GetPlan(ctx, task.PlanID)
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

// CreateTask creates a new task
func (s *TaskService) CreateTask(
	ctx context.Context,
	req *connect.Request[reliantv1.CreateTaskRequest],
) (*connect.Response[reliantv1.CreateTaskResponse], error) {
	userID := auth.MustGetUserID(ctx)

	// Verify user owns the plan
	plan, err := s.database.GetPlan(ctx, req.Msg.PlanId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("plan not found"))
	}

	chatID, err := s.getChatIDForPlan(ctx, plan)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}

	chat, err := s.database.GetChat(ctx, chatID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}

	if chat.UserID != userID {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("access denied"))
	}

	// Validate
	if req.Msg.PlanId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("plan_id is required"))
	}
	if req.Msg.Title == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("title is required"))
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

	if err := s.database.CreateTask(ctx, task); err != nil {
		logging.Error("Failed to create task", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create task"))
	}

	if err := s.database.EmitChatRefetch(ctx, chatID, db.RefetchPlanTasks); err != nil {
		logging.Warn("[TaskService] Failed to emit plan_tasks refetch", "error", err)
	}

	resp := &reliantv1.CreateTaskResponse{
		Task: &reliantv1.Task{
			Id:        task.ID,
			PlanId:    task.PlanID,
			Title:     task.Title,
			Status:    taskStatusFromInt32(task.Status),
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

// GetTask retrieves a task by ID
func (s *TaskService) GetTask(
	ctx context.Context,
	req *connect.Request[reliantv1.GetTaskRequest],
) (*connect.Response[reliantv1.GetTaskResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.taskBelongsToUser(ctx, req.Msg.TaskId, userID); err != nil {
		return nil, err
	}

	task, err := s.database.GetTask(ctx, req.Msg.TaskId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("task not found"))
	}

	resp := &reliantv1.GetTaskResponse{
		Task: &reliantv1.Task{
			Id:        task.ID,
			PlanId:    task.PlanID,
			Title:     task.Title,
			Status:    taskStatusFromInt32(task.Status),
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

// ListTasks lists tasks (optionally filtered by plan)
func (s *TaskService) ListTasks(
	ctx context.Context,
	req *connect.Request[reliantv1.ListTasksRequest],
) (*connect.Response[reliantv1.ListTasksResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.PlanId == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("plan_id is required"))
	}

	// Verify user owns the plan
	plan, err := s.database.GetPlan(ctx, *req.Msg.PlanId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("plan not found"))
	}

	chatID, err := s.getChatIDForPlan(ctx, plan)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}

	chat, err := s.database.GetChat(ctx, chatID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}

	if chat.UserID != userID {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("access denied"))
	}

	tasks, err := s.database.ListTasksByPlan(ctx, *req.Msg.PlanId)
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

	return connect.NewResponse(&reliantv1.ListTasksResponse{
		Tasks: protoTasks,
		Total: int32(len(tasks)),
	}), nil
}

// UpdateTask updates an existing task
func (s *TaskService) UpdateTask(
	ctx context.Context,
	req *connect.Request[reliantv1.UpdateTaskRequest],
) (*connect.Response[reliantv1.UpdateTaskResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.taskBelongsToUser(ctx, req.Msg.TaskId, userID); err != nil {
		return nil, err
	}

	task, err := s.database.GetTask(ctx, req.Msg.TaskId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("task not found"))
	}

	// Update fields
	if req.Msg.Title != nil {
		task.Title = *req.Msg.Title
	}
	if req.Msg.Description != nil {
		task.Description = req.Msg.Description
	}
	if req.Msg.Status != nil {
		task.Status = int32(*req.Msg.Status)
	}
	if req.Msg.Position != nil {
		task.Position = int(*req.Msg.Position)
	}

	if err := s.database.UpdateTask(ctx, task); err != nil {
		logging.Error("Failed to update task", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update task"))
	}

	// Get chatID from the plan's thread for refetch emission
	if taskPlan, err := s.database.GetPlan(ctx, task.PlanID); err == nil {
		if cid, err := s.getChatIDForPlan(ctx, taskPlan); err == nil {
			if err := s.database.EmitChatRefetch(ctx, cid, db.RefetchPlanTasks); err != nil {
				logging.Warn("[TaskService] Failed to emit plan_tasks refetch", "error", err)
			}
		}
	}

	resp := &reliantv1.UpdateTaskResponse{
		Task: &reliantv1.Task{
			Id:        task.ID,
			PlanId:    task.PlanID,
			Title:     task.Title,
			Status:    taskStatusFromInt32(task.Status),
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

// DeleteTask deletes a task
func (s *TaskService) DeleteTask(
	ctx context.Context,
	req *connect.Request[reliantv1.DeleteTaskRequest],
) (*connect.Response[reliantv1.DeleteTaskResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.taskBelongsToUser(ctx, req.Msg.TaskId, userID); err != nil {
		return nil, err
	}

	// Get chatID before deleting (via plan's thread)
	var deleteChatID string
	if task, err := s.database.GetTask(ctx, req.Msg.TaskId); err == nil {
		if taskPlan, err := s.database.GetPlan(ctx, task.PlanID); err == nil {
			if cid, err := s.getChatIDForPlan(ctx, taskPlan); err == nil {
				deleteChatID = cid
			}
		}
	}

	if err := s.database.DeleteTask(ctx, req.Msg.TaskId); err != nil {
		logging.Error("Failed to delete task", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to delete task"))
	}

	if deleteChatID != "" {
		if err := s.database.EmitChatRefetch(ctx, deleteChatID, db.RefetchPlanTasks); err != nil {
			logging.Warn("[TaskService] Failed to emit plan_tasks refetch", "error", err)
		}
	}

	return connect.NewResponse(&reliantv1.DeleteTaskResponse{Success: true}), nil
}
