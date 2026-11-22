// yours: scaffolded once, never touched again — forge will not overwrite this file
// Service implementation; canonical pattern in `forge skill load service-layer`.
package planning

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/pkg/config"
)

// TaskStats counts a plan's tasks by status.
type TaskStats struct {
	Total      int
	Pending    int
	InProgress int
	Completed  int
	Failed     int
	Blocked    int
	Cancelled  int
	Skipped    int
}

// Deps holds dependencies for the planning package.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config

	// Plans holds the plan rows.
	Plans Plans
	// Tasks holds the task rows.
	Tasks Tasks
	// Dependencies holds the typed links between tasks.
	Dependencies Dependencies
}

//forge:contract
type service struct {
	deps Deps
}

// New creates a new planning service instance.
//
// forge:constructor
func New(deps Deps) (Service, error) {
	if deps.Plans == nil || deps.Tasks == nil || deps.Dependencies == nil {
		return nil, errors.New("planning: Deps.Plans, Tasks and Dependencies are required")
	}
	return &service{deps: deps}, nil
}

// GetPlanByThreadID returns the plan a thread works on; svcerr.NotFound when
// it has none.
func (s *service) GetPlanByThreadID(ctx context.Context, threadID string) (*core.Plan, error) {
	if threadID == "" {
		return nil, fmt.Errorf("thread ID cannot be empty")
	}
	return s.deps.Plans.GetPlanByThreadID(ctx, threadID)
}

// UpdatePlan rewrites a plan row.
func (s *service) UpdatePlan(ctx context.Context, plan *core.Plan) error {
	if plan == nil {
		return fmt.Errorf("plan cannot be nil")
	}
	if plan.ID == "" {
		return fmt.Errorf("plan ID cannot be empty")
	}
	return s.deps.Plans.ReplacePlan(ctx, plan)
}

// UpdateTask rewrites a task row.
func (s *service) UpdateTask(ctx context.Context, task *core.Task) error {
	if task == nil {
		return fmt.Errorf("task cannot be nil")
	}
	if task.ID == "" {
		return fmt.Errorf("task ID cannot be empty")
	}
	return s.deps.Tasks.ReplaceTask(ctx, task)
}

// GetTaskByPosition returns the plan's task at a 0-indexed position, in the
// order ListTasksByPlan lists them.
func (s *service) GetTaskByPosition(ctx context.Context, planID string, position int) (*core.Task, error) {
	if planID == "" {
		return nil, fmt.Errorf("plan ID cannot be empty")
	}
	tasks, err := s.deps.Tasks.ListTasksByPlan(ctx, planID)
	if err != nil {
		return nil, err
	}
	if position < 0 || position >= len(tasks) {
		return nil, fmt.Errorf("position %d out of range", position)
	}
	return tasks[position], nil
}

// GetTaskStatsByPlan counts the plan's tasks by status.
func (s *service) GetTaskStatsByPlan(ctx context.Context, planID string) (*TaskStats, error) {
	if planID == "" {
		return nil, fmt.Errorf("plan ID cannot be empty")
	}
	tasks, err := s.deps.Tasks.ListTasksByPlan(ctx, planID)
	if err != nil {
		return nil, err
	}
	stats := &TaskStats{Total: len(tasks)}
	for _, task := range tasks {
		switch task.Status {
		case int32(reliantv1.TaskStatus_TASK_STATUS_PENDING):
			stats.Pending++
		case int32(reliantv1.TaskStatus_TASK_STATUS_IN_PROGRESS):
			stats.InProgress++
		case int32(reliantv1.TaskStatus_TASK_STATUS_COMPLETED):
			stats.Completed++
		case int32(reliantv1.TaskStatus_TASK_STATUS_FAILED):
			stats.Failed++
		case int32(reliantv1.TaskStatus_TASK_STATUS_BLOCKED):
			stats.Blocked++
		case int32(reliantv1.TaskStatus_TASK_STATUS_CANCELLED):
			stats.Cancelled++
		case int32(reliantv1.TaskStatus_TASK_STATUS_SKIPPED):
			stats.Skipped++
		}
	}
	return stats, nil
}

// CreateTaskDependency links two tasks. A task cannot depend on itself.
func (s *service) CreateTaskDependency(ctx context.Context, dep *core.TaskDependency) error {
	if dep == nil {
		return fmt.Errorf("dependency cannot be nil")
	}
	if dep.FromTaskID == "" || dep.ToTaskID == "" {
		return fmt.Errorf("from_task_id and to_task_id are required")
	}
	if dep.FromTaskID == dep.ToTaskID {
		return fmt.Errorf("a task cannot depend on itself")
	}
	return s.deps.Dependencies.InsertTaskDependency(ctx, dep)
}

// ListDependenciesByPlan lists every dependency among the plan's tasks.
func (s *service) ListDependenciesByPlan(ctx context.Context, planID string) ([]*core.TaskDependency, error) {
	if planID == "" {
		return nil, fmt.Errorf("plan ID cannot be empty")
	}
	return s.deps.Dependencies.ListDependenciesByPlan(ctx, planID)
}

// DeleteTaskDependencyByPair removes the dependency of one type between two
// tasks.
func (s *service) DeleteTaskDependencyByPair(ctx context.Context, fromTaskID, toTaskID string, depType int32) error {
	if fromTaskID == "" || toTaskID == "" || depType == 0 {
		return fmt.Errorf("from_task_id, to_task_id, and dependency_type are required")
	}
	return s.deps.Dependencies.DeleteTaskDependencyByPair(ctx, fromTaskID, toTaskID, depType)
}
