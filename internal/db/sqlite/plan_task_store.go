package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	sqlitedb "github.com/reliant-labs/reliant/internal/db/sqlite/generated"
)

type planTaskStore struct {
	q sqlitedb.Querier
}

// NewPlanTaskStore creates the SQLite plan/task store implementation.
func NewPlanTaskStore(q sqlitedb.Querier) core.PlanTaskStore {
	return &planTaskStore{q: q}
}

func (s *planTaskStore) CreatePlan(ctx context.Context, plan *core.Plan) error {
	_, err := s.q.CreatePlan(ctx, sqlitedb.CreatePlanParams{
		ID:          plan.ID,
		ThreadID:    plan.ThreadID,
		Title:       plan.Title,
		Description: planPtrToNullString(plan.Description),
		Status:      int64(plan.Status),
		Complexity:  planInt32PtrToNullInt64(plan.Complexity),
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
	return planFromSQLc(row), nil
}

func (s *planTaskStore) GetPlanByThreadID(ctx context.Context, threadID string) (*core.Plan, error) {
	rows, err := s.q.ListPlans(ctx, threadID)
	if err != nil {
		return nil, fmt.Errorf("failed to list plans: %w", err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("no plans found for thread %s", threadID)
	}
	return planFromSQLc(rows[0]), nil
}

func (s *planTaskStore) ListPlansByThread(ctx context.Context, threadID string) ([]*core.Plan, error) {
	rows, err := s.q.ListPlans(ctx, threadID)
	if err != nil {
		return nil, fmt.Errorf("failed to list plans: %w", err)
	}
	plans := make([]*core.Plan, len(rows))
	for i, row := range rows {
		plans[i] = planFromSQLc(row)
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
		plans[i] = planFromSQLc(row)
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
		plans[i] = planFromSQLc(row)
	}
	return plans, nil
}

func (s *planTaskStore) UpdatePlan(ctx context.Context, plan *core.Plan) error {
	_, err := s.q.UpdatePlan(ctx, sqlitedb.UpdatePlanParams{
		ID:          plan.ID,
		Title:       plan.Title,
		Description: planPtrToNullString(plan.Description),
		Status:      int64(plan.Status),
		Complexity:  planInt32PtrToNullInt64(plan.Complexity),
		CompletedAt: planPtrToNullTime(plan.CompletedAt),
	})
	return err
}

func (s *planTaskStore) UpdatePlanStatus(ctx context.Context, id string, status int32, completedAt *time.Time) error {
	existing, err := s.q.GetPlan(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to get plan for status update: %w", err)
	}
	_, err = s.q.UpdatePlan(ctx, sqlitedb.UpdatePlanParams{
		ID:          id,
		Title:       existing.Title,
		Description: existing.Description,
		Status:      int64(status),
		Complexity:  existing.Complexity,
		CompletedAt: planPtrToNullTime(completedAt),
	})
	return err
}

func (s *planTaskStore) DeletePlan(ctx context.Context, id string) error {
	return s.q.DeletePlan(ctx, id)
}

func (s *planTaskStore) CreateTask(ctx context.Context, task *core.Task) error {
	_, err := s.q.CreateTask(ctx, sqlitedb.CreateTaskParams{
		ID:           task.ID,
		PlanID:       task.PlanID,
		ParentTaskID: planPtrToNullString(task.ParentTaskID),
		Title:        task.Title,
		Description:  planPtrToNullString(task.Description),
		Status:       int64(task.Status),
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
	return taskFromSQLc(row), nil
}

func (s *planTaskStore) ListTasksByPlan(ctx context.Context, planID string) ([]*core.Task, error) {
	rows, err := s.q.ListTasks(ctx, planID)
	if err != nil {
		return nil, fmt.Errorf("failed to list tasks: %w", err)
	}
	tasks := make([]*core.Task, len(rows))
	for i, row := range rows {
		tasks[i] = taskFromSQLc(row)
	}
	return tasks, nil
}

func (s *planTaskStore) UpdateTask(ctx context.Context, task *core.Task) error {
	_, err := s.q.UpdateTask(ctx, sqlitedb.UpdateTaskParams{
		ID:          task.ID,
		Title:       task.Title,
		Description: planPtrToNullString(task.Description),
		Status:      int64(task.Status),
		Position:    int64(task.Position),
		Metadata:    planPtrToNullString(task.Metadata),
		Assignee:    planPtrToNullString(task.Assignee),
		CompletedAt: planPtrToNullTime(task.CompletedAt),
	})
	return err
}

func (s *planTaskStore) UpdateTaskStatus(ctx context.Context, id string, status int32, completedAt *time.Time) error {
	existing, err := s.q.GetTask(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to get task for status update: %w", err)
	}
	_, err = s.q.UpdateTask(ctx, sqlitedb.UpdateTaskParams{
		ID:          id,
		Title:       existing.Title,
		Description: existing.Description,
		Status:      int64(status),
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
	_, err := s.q.CreateTaskDependency(ctx, sqlitedb.CreateTaskDependencyParams{
		ID:             dep.ID,
		FromTaskID:     dep.FromTaskID,
		ToTaskID:       dep.ToTaskID,
		DependencyType: int64(dep.DependencyType),
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
	return depFromSQLc(row), nil
}

func (s *planTaskStore) ListTaskDependenciesByTask(ctx context.Context, taskID string) ([]*core.TaskDependency, error) {
	rows, err := s.q.ListTaskDependenciesByTask(ctx, sqlitedb.ListTaskDependenciesByTaskParams{
		FromTaskID: taskID,
		ToTaskID:   taskID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list task dependencies: %w", err)
	}
	deps := make([]*core.TaskDependency, len(rows))
	for i, row := range rows {
		deps[i] = depFromSQLc(row)
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
		deps[i] = depFromSQLc(row)
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
		deps[i] = depFromSQLc(row)
	}
	return deps, nil
}

func (s *planTaskStore) DeleteTaskDependency(ctx context.Context, id string) error {
	return s.q.DeleteTaskDependency(ctx, id)
}

func (s *planTaskStore) DeleteTaskDependencyByPair(ctx context.Context, fromTaskID, toTaskID string, depType int32) error {
	return s.q.DeleteTaskDependencyByPair(ctx, sqlitedb.DeleteTaskDependencyByPairParams{
		FromTaskID:     fromTaskID,
		ToTaskID:       toTaskID,
		DependencyType: int64(depType),
	})
}

// Mappers

func planFromSQLc(sa sqlitedb.Plan) *core.Plan {
	return &core.Plan{
		ID:          sa.ID,
		ThreadID:    sa.ThreadID,
		Title:       sa.Title,
		Description: planNullStringToPtr(sa.Description),
		Status:      int32(sa.Status),
		Complexity:  planNullInt64ToInt32Ptr(sa.Complexity),
		ProjectID:   planNullStringToPtr(sa.ProjectID),
		CreatedAt:   sa.CreatedAt,
		UpdatedAt:   sa.UpdatedAt,
		CompletedAt: planNullTimeToPtr(sa.CompletedAt),
	}
}

func taskFromSQLc(sa sqlitedb.Task) *core.Task {
	return &core.Task{
		ID:           sa.ID,
		PlanID:       sa.PlanID,
		ParentTaskID: planNullStringToPtr(sa.ParentTaskID),
		Title:        sa.Title,
		Description:  planNullStringToPtr(sa.Description),
		Status:       int32(sa.Status),
		Position:     int(sa.Position),
		Metadata:     planNullStringToPtr(sa.Metadata),
		Assignee:     planNullStringToPtr(sa.Assignee),
		CreatedAt:    sa.CreatedAt,
		UpdatedAt:    sa.UpdatedAt,
		CompletedAt:  planNullTimeToPtr(sa.CompletedAt),
	}
}

func depFromSQLc(sa sqlitedb.TaskDependency) *core.TaskDependency {
	return &core.TaskDependency{
		ID:             sa.ID,
		FromTaskID:     sa.FromTaskID,
		ToTaskID:       sa.ToTaskID,
		DependencyType: int32(sa.DependencyType),
		CreatedAt:      sa.CreatedAt,
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

func planInt32PtrToNullInt64(v *int32) sql.NullInt64 {
	if v != nil {
		return sql.NullInt64{Int64: int64(*v), Valid: true}
	}
	return sql.NullInt64{Valid: false}
}

func planNullInt64ToInt32Ptr(ni sql.NullInt64) *int32 {
	if ni.Valid {
		v := int32(ni.Int64)
		return &v
	}
	return nil
}
