package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	pgdb "github.com/reliant-labs/reliant/internal/db/postgres/generated"
)

type planTaskStore struct {
	q pgdb.Querier
}

// NewPlanTaskStore creates the Postgres plan/task store implementation.
func NewPlanTaskStore(q pgdb.Querier) core.PlanTaskStore {
	return &planTaskStore{q: q}
}

func (s *planTaskStore) CreatePlan(ctx context.Context, plan *core.Plan) error {
	_, err := s.q.CreatePlan(ctx, pgdb.CreatePlanParams{
		ID:          plan.ID,
		ThreadID:    plan.ThreadID,
		Title:       plan.Title,
		Description: planPtrToNullString(plan.Description),
		Status:      plan.Status,
		Complexity:  planInt32PtrToNullInt32(plan.Complexity),
		ProjectID:   planPtrToNullString(plan.ProjectID),
		CreatedAt:   plan.CreatedAt,
		UpdatedAt:   plan.UpdatedAt,
		CompletedAt: planPtrToNullTime(plan.CompletedAt),
	})
	return err
}

func (s *planTaskStore) GetPlan(ctx context.Context, id string) (*core.Plan, error) {
	row, err := s.q.GetPlan(ctx, id)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("plan not found: %s", id)
		}
		return nil, fmt.Errorf("failed to get plan: %w", err)
	}
	return planFromPG(row), nil
}

func (s *planTaskStore) GetPlanByThreadID(ctx context.Context, threadID string) (*core.Plan, error) {
	rows, err := s.q.ListPlans(ctx, threadID)
	if err != nil {
		return nil, fmt.Errorf("failed to list plans: %w", err)
	}
	if len(rows) == 0 {
		return nil, core.ErrPlanNotFound
	}
	return planFromPG(rows[0]), nil
}

func (s *planTaskStore) ListPlansByThread(ctx context.Context, threadID string) ([]*core.Plan, error) {
	rows, err := s.q.ListPlans(ctx, threadID)
	if err != nil {
		return nil, fmt.Errorf("failed to list plans: %w", err)
	}
	plans := make([]*core.Plan, len(rows))
	for i, row := range rows {
		plans[i] = planFromPG(row)
	}
	return plans, nil
}

func (s *planTaskStore) ListPlansByChatID(ctx context.Context, chatID string) ([]*core.Plan, error) {
	rows, err := s.q.ListPlansByChatID(ctx, chatID)
	if err != nil {
		return nil, fmt.Errorf("failed to list plans by chat: %w", err)
	}
	plans := make([]*core.Plan, len(rows))
	for i, row := range rows {
		plans[i] = planFromPG(row)
	}
	return plans, nil
}

func (s *planTaskStore) ListPlansByProject(ctx context.Context, projectID string) ([]*core.Plan, error) {
	rows, err := s.q.ListPlansByProject(ctx, sql.NullString{String: projectID, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("failed to list plans by project: %w", err)
	}
	plans := make([]*core.Plan, len(rows))
	for i, row := range rows {
		plans[i] = planFromPG(row)
	}
	return plans, nil
}

func (s *planTaskStore) UpdatePlan(ctx context.Context, plan *core.Plan) error {
	_, err := s.q.UpdatePlan(ctx, pgdb.UpdatePlanParams{
		ID:          plan.ID,
		Title:       plan.Title,
		Description: planPtrToNullString(plan.Description),
		Status:      plan.Status,
		Complexity:  planInt32PtrToNullInt32(plan.Complexity),
		CompletedAt: planPtrToNullTime(plan.CompletedAt),
	})
	return err
}

func (s *planTaskStore) UpdatePlanStatus(ctx context.Context, id string, status int32, completedAt *time.Time) error {
	// Get existing plan to preserve title/description/complexity
	existing, err := s.q.GetPlan(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to get plan for status update: %w", err)
	}
	_, err = s.q.UpdatePlan(ctx, pgdb.UpdatePlanParams{
		ID:          id,
		Title:       existing.Title,
		Description: existing.Description,
		Status:      status,
		Complexity:  existing.Complexity,
		CompletedAt: planPtrToNullTime(completedAt),
	})
	return err
}

func (s *planTaskStore) DeletePlan(ctx context.Context, id string) error {
	return s.q.DeletePlan(ctx, id)
}

func (s *planTaskStore) CreateTask(ctx context.Context, task *core.Task) error {
	_, err := s.q.CreateTask(ctx, pgdb.CreateTaskParams{
		ID:           task.ID,
		PlanID:       task.PlanID,
		ParentTaskID: planPtrToNullString(task.ParentTaskID),
		Title:        task.Title,
		Description:  planPtrToNullString(task.Description),
		Status:       task.Status,
		Position:     int64(task.Position),
		Metadata:     planPtrToNullString(task.Metadata),
		Assignee:     planPtrToNullString(task.Assignee),
		CreatedAt:    task.CreatedAt,
		UpdatedAt:    task.UpdatedAt,
		CompletedAt:  planPtrToNullTime(task.CompletedAt),
	})
	return err
}

func (s *planTaskStore) GetTask(ctx context.Context, id string) (*core.Task, error) {
	row, err := s.q.GetTask(ctx, id)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("task not found: %s", id)
		}
		return nil, fmt.Errorf("failed to get task: %w", err)
	}
	return taskFromPG(row), nil
}

func (s *planTaskStore) ListTasksByPlan(ctx context.Context, planID string) ([]*core.Task, error) {
	rows, err := s.q.ListTasks(ctx, planID)
	if err != nil {
		return nil, fmt.Errorf("failed to list tasks: %w", err)
	}
	tasks := make([]*core.Task, len(rows))
	for i, row := range rows {
		tasks[i] = taskFromPG(row)
	}
	return tasks, nil
}

func (s *planTaskStore) UpdateTask(ctx context.Context, task *core.Task) error {
	_, err := s.q.UpdateTask(ctx, pgdb.UpdateTaskParams{
		ID:          task.ID,
		Title:       task.Title,
		Description: planPtrToNullString(task.Description),
		Status:      task.Status,
		Position:    int64(task.Position),
		Metadata:    planPtrToNullString(task.Metadata),
		Assignee:    planPtrToNullString(task.Assignee),
		CompletedAt: planPtrToNullTime(task.CompletedAt),
	})
	return err
}

func (s *planTaskStore) UpdateTaskStatus(ctx context.Context, id string, status int32, completedAt *time.Time) error {
	// Get existing task to preserve all fields
	existing, err := s.q.GetTask(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to get task for status update: %w", err)
	}
	_, err = s.q.UpdateTask(ctx, pgdb.UpdateTaskParams{
		ID:          id,
		Title:       existing.Title,
		Description: existing.Description,
		Status:      status,
		Position:    existing.Position,
		Metadata:    existing.Metadata,
		Assignee:    existing.Assignee,
		CompletedAt: planPtrToNullTime(completedAt),
	})
	return err
}

func (s *planTaskStore) DeleteTask(ctx context.Context, id string) error {
	return s.q.DeleteTask(ctx, id)
}

// Task dependency methods

func (s *planTaskStore) CreateTaskDependency(ctx context.Context, dep *core.TaskDependency) error {
	_, err := s.q.CreateTaskDependency(ctx, pgdb.CreateTaskDependencyParams{
		ID:             dep.ID,
		FromTaskID:     dep.FromTaskID,
		ToTaskID:       dep.ToTaskID,
		DependencyType: dep.DependencyType,
		CreatedAt:      dep.CreatedAt,
	})
	return err
}

func (s *planTaskStore) GetTaskDependency(ctx context.Context, id string) (*core.TaskDependency, error) {
	row, err := s.q.GetTaskDependency(ctx, id)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("task dependency not found: %s", id)
		}
		return nil, fmt.Errorf("failed to get task dependency: %w", err)
	}
	return depFromPG(row), nil
}

func (s *planTaskStore) ListTaskDependenciesByTask(ctx context.Context, taskID string) ([]*core.TaskDependency, error) {
	rows, err := s.q.ListTaskDependenciesByTask(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("failed to list task dependencies: %w", err)
	}
	deps := make([]*core.TaskDependency, len(rows))
	for i, row := range rows {
		deps[i] = depFromPG(row)
	}
	return deps, nil
}

func (s *planTaskStore) ListBlockersForTask(ctx context.Context, taskID string) ([]*core.TaskDependency, error) {
	rows, err := s.q.ListBlockersForTask(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("failed to list blockers: %w", err)
	}
	deps := make([]*core.TaskDependency, len(rows))
	for i, row := range rows {
		deps[i] = depFromPG(row)
	}
	return deps, nil
}

func (s *planTaskStore) ListDependenciesByPlan(ctx context.Context, planID string) ([]*core.TaskDependency, error) {
	rows, err := s.q.ListDependenciesByPlan(ctx, planID)
	if err != nil {
		return nil, fmt.Errorf("failed to list plan dependencies: %w", err)
	}
	deps := make([]*core.TaskDependency, len(rows))
	for i, row := range rows {
		deps[i] = depFromPG(row)
	}
	return deps, nil
}

func (s *planTaskStore) DeleteTaskDependency(ctx context.Context, id string) error {
	return s.q.DeleteTaskDependency(ctx, id)
}

func (s *planTaskStore) DeleteTaskDependencyByPair(ctx context.Context, fromTaskID, toTaskID string, depType int32) error {
	return s.q.DeleteTaskDependencyByPair(ctx, pgdb.DeleteTaskDependencyByPairParams{
		FromTaskID:     fromTaskID,
		ToTaskID:       toTaskID,
		DependencyType: depType,
	})
}

// Mappers

func planFromPG(row pgdb.Plan) *core.Plan {
	return &core.Plan{
		ID:          row.ID,
		ThreadID:    row.ThreadID,
		Title:       row.Title,
		Description: planNullStringToPtr(row.Description),
		Status:      row.Status,
		Complexity:  planNullInt32ToInt32Ptr(row.Complexity),
		ProjectID:   planNullStringToPtr(row.ProjectID),
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
		CompletedAt: planNullTimeToPtr(row.CompletedAt),
	}
}

func taskFromPG(row pgdb.Task) *core.Task {
	return &core.Task{
		ID:           row.ID,
		PlanID:       row.PlanID,
		ParentTaskID: planNullStringToPtr(row.ParentTaskID),
		Title:        row.Title,
		Description:  planNullStringToPtr(row.Description),
		Status:       row.Status,
		Position:     int(row.Position),
		Metadata:     planNullStringToPtr(row.Metadata),
		Assignee:     planNullStringToPtr(row.Assignee),
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
		CompletedAt:  planNullTimeToPtr(row.CompletedAt),
	}
}

func depFromPG(row pgdb.TaskDependency) *core.TaskDependency {
	return &core.TaskDependency{
		ID:             row.ID,
		FromTaskID:     row.FromTaskID,
		ToTaskID:       row.ToTaskID,
		DependencyType: row.DependencyType,
		CreatedAt:      row.CreatedAt,
	}
}

func planPtrToNullString(s *string) sql.NullString {
	if s != nil {
		return sql.NullString{String: *s, Valid: true}
	}
	return sql.NullString{Valid: false}
}

func planNullStringToPtr(ns sql.NullString) *string {
	if ns.Valid {
		return &ns.String
	}
	return nil
}

func planPtrToNullTime(t *time.Time) sql.NullTime {
	if t != nil {
		return sql.NullTime{Time: *t, Valid: true}
	}
	return sql.NullTime{Valid: false}
}

func planNullTimeToPtr(nt sql.NullTime) *time.Time {
	if nt.Valid {
		return &nt.Time
	}
	return nil
}

func planInt32PtrToNullInt32(i *int32) sql.NullInt32 {
	if i != nil {
		return sql.NullInt32{Int32: *i, Valid: true}
	}
	return sql.NullInt32{Valid: false}
}

func planNullInt32ToInt32Ptr(ni sql.NullInt32) *int32 {
	if ni.Valid {
		v := ni.Int32
		return &v
	}
	return nil
}
