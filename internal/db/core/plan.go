package core

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"
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
//
// It is also a svcerr NotFound, so a handler's svcerr.Wrap answers
// CodeNotFound "plan not found"; its text is unchanged.
var ErrPlanNotFound = fmt.Errorf("%w: %w", svcerr.NotFound("plan"), sql.ErrNoRows)

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
