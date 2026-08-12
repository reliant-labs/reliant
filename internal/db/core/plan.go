package core

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// ErrPlanNotFound reports that a thread has no plan yet.
//
// It wraps sql.ErrNoRows because "this thread never called create_plan" and
// "the row is missing" are the same condition to every caller, and the tool
// layer already branches on sql.ErrNoRows to emit its actionable message.
// Returning a bare fmt.Errorf here instead sent that branch down the generic
// path, so an agent that had simply not created a plan was told
// "Failed to find plan: no plans found for thread <uuid>" — which names no
// remedy, and was observed being retried eight times in a row.
var ErrPlanNotFound = fmt.Errorf("plan not found: %w", sql.ErrNoRows)

// Plan represents a high-level project plan.
type Plan struct {
	ID          string
	ThreadID    string
	Title       string
	Description *string
	Status      int32
	Complexity  *int32  // PlanComplexity proto enum value
	ProjectID   *string // optional: for cross-workflow plan sharing
	CreatedAt   time.Time
	UpdatedAt   time.Time
	CompletedAt *time.Time
}

// Task represents a task item.
type Task struct {
	ID           string
	PlanID       string
	ParentTaskID *string
	Title        string
	Description  *string
	Status       int32
	Position     int
	Metadata     *string // JSON: preferred_agent, tool_hints, dependencies, notes, priority
	Assignee     *string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	CompletedAt  *time.Time
}

// TaskDependency represents a typed relationship between two tasks.
type TaskDependency struct {
	ID             string
	FromTaskID     string
	ToTaskID       string
	DependencyType int32 // DependencyType proto enum value
	CreatedAt      time.Time
}

// Dependency type constants (matching proto DependencyType enum values).
const (
	DependencyTypeBlocks       int32 = 1 // from_task blocks to_task
	DependencyTypeRelated      int32 = 2 // informational link
	DependencyTypeParallelWith int32 = 3 // explicitly parallelizable
)

// PlanTaskStore is the shared contract for plan/task persistence across drivers.
type PlanTaskStore interface {
	CreatePlan(ctx context.Context, plan *Plan) error
	GetPlan(ctx context.Context, id string) (*Plan, error)
	GetPlanByThreadID(ctx context.Context, threadID string) (*Plan, error)
	ListPlansByThread(ctx context.Context, threadID string) ([]*Plan, error)
	ListPlansByChatID(ctx context.Context, chatID string) ([]*Plan, error)
	ListPlansByProject(ctx context.Context, projectID string) ([]*Plan, error)
	UpdatePlan(ctx context.Context, plan *Plan) error
	UpdatePlanStatus(ctx context.Context, id string, status int32, completedAt *time.Time) error
	DeletePlan(ctx context.Context, id string) error

	CreateTask(ctx context.Context, task *Task) error
	GetTask(ctx context.Context, id string) (*Task, error)
	ListTasksByPlan(ctx context.Context, planID string) ([]*Task, error)
	UpdateTask(ctx context.Context, task *Task) error
	UpdateTaskStatus(ctx context.Context, id string, status int32, completedAt *time.Time) error
	DeleteTask(ctx context.Context, id string) error

	CreateTaskDependency(ctx context.Context, dep *TaskDependency) error
	GetTaskDependency(ctx context.Context, id string) (*TaskDependency, error)
	ListTaskDependenciesByTask(ctx context.Context, taskID string) ([]*TaskDependency, error)
	ListBlockersForTask(ctx context.Context, taskID string) ([]*TaskDependency, error)
	ListDependenciesByPlan(ctx context.Context, planID string) ([]*TaskDependency, error)
	DeleteTaskDependency(ctx context.Context, id string) error
	DeleteTaskDependencyByPair(ctx context.Context, fromTaskID, toTaskID string, depType int32) error
}
