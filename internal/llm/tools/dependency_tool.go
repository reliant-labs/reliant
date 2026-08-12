// Copyright (c) 2025 Reliant Labs
package tools

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// AddDependencyTool allows agents to create typed dependencies between tasks
type AddDependencyTool struct {
	repo db.Repository
}

func NewAddDependencyTool(repo db.Repository) Tool {
	return NewToolWrapper(&AddDependencyTool{repo: repo})
}

func (t *AddDependencyTool) Name() string { return "add_dependency" }

func (t *AddDependencyTool) Description() string {
	return `Create a dependency between two tasks in the current plan.

DEPENDENCY TYPES:
- blocks: from_task must complete before to_task can start
- related: informational link, no execution constraint
- parallel_with: explicitly marks tasks as parallelizable

EXAMPLES:
- Task A blocks Task B: add_dependency(from_task="A-id", to_task="B-id", type="blocks")
  Means B cannot start until A completes.

- Tasks can run together: add_dependency(from_task="A-id", to_task="B-id", type="parallel_with")
  Explicitly marks A and B as safe to run in parallel.

- Informational link: add_dependency(from_task="A-id", to_task="B-id", type="related")
  No execution constraint, just documents a relationship.

USE WITH list_ready_tasks:
After adding 'blocks' dependencies, use list_ready_tasks to see which tasks
have no unresolved blockers and are ready to work on.`
}

type AddDependencyParams struct {
	FromTask string `json:"from_task"` // Task ID (or ordinal) that is the source
	ToTask   string `json:"to_task"`   // Task ID (or ordinal) that is the target
	Type     string `json:"type"`      // blocks, related, parallel_with
}

func (t *AddDependencyTool) RequiresPermission(params AddDependencyParams) (bool, error) {
	return false, nil
}

// parseDependencyType converts a string dependency type to its int32 proto enum value.
func parseDependencyType(s string) (int32, bool) {
	switch s {
	case "blocks":
		return core.DependencyTypeBlocks, true
	case "related":
		return core.DependencyTypeRelated, true
	case "parallel_with":
		return core.DependencyTypeParallelWith, true
	default:
		return 0, false
	}
}

func (t *AddDependencyTool) Execute(rctx *rctx.ToolContext, params AddDependencyParams) (ToolResponse, error) {
	if t.repo == nil {
		return NewTextErrorResponse("This tool requires a database connection and is not available in daemon-only mode"), nil
	}

	if params.FromTask == "" || params.ToTask == "" {
		return NewTextErrorResponse("Both from_task and to_task are required"), nil
	}

	// Validate and convert type
	depType, ok := parseDependencyType(params.Type)
	if !ok {
		return NewTextErrorResponse(fmt.Sprintf("Invalid dependency type: %s. Must be one of: blocks, related, parallel_with", params.Type)), nil
	}

	if rctx.Thread == "" {
		return NewTextErrorResponse("No thread context available"), nil
	}

	// Resolve task IDs (support ordinal lookup)
	fromID, fromTask, err := t.resolveTask(rctx, params.FromTask)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to resolve from_task: %v", err)), nil
	}
	toID, toTask, err := t.resolveTask(rctx, params.ToTask)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to resolve to_task: %v", err)), nil
	}

	if fromID == toID {
		return NewTextErrorResponse("A task cannot depend on itself"), nil
	}

	// Verify both tasks belong to same plan
	if fromTask.PlanID != toTask.PlanID {
		return NewTextErrorResponse("Both tasks must belong to the same plan"), nil
	}

	dep := &core.TaskDependency{
		ID:             uuid.New().String(),
		FromTaskID:     fromID,
		ToTaskID:       toID,
		DependencyType: depType,
		CreatedAt:      time.Now(),
	}

	if err := t.repo.CreateTaskDependency(rctx.Context, dep); err != nil {
		logging.Error("Failed to create dependency", "error", err)
		return NewTextErrorResponse(fmt.Sprintf("Failed to create dependency: %v", err)), nil
	}

	responseText := fmt.Sprintf(`Dependency created successfully!

ID: %s
Type: %d
From: %s (%s)
To: %s (%s)`,
		dep.ID,
		dep.DependencyType,
		fromTask.Title, fromID,
		toTask.Title, toID,
	)

	if depType == core.DependencyTypeBlocks {
		responseText += fmt.Sprintf("\n\n\"%s\" must complete before \"%s\" can start.", fromTask.Title, toTask.Title)
	}

	if err := t.repo.EmitChatRefetch(rctx.Context, rctx.ChatID, db.RefetchPlanTasks); err != nil {
		logging.Warn("[AddDependencyTool] Failed to emit refetch", "error", err)
	}

	return NewTextResponse(responseText), nil
}

func (t *AddDependencyTool) resolveTask(rctx *rctx.ToolContext, taskRef string) (string, *db.Task, error) {
	// Try ordinal lookup first
	position := 0
	extra := ""
	if n, _ := fmt.Sscanf(taskRef, "%d%s", &position, &extra); n == 1 && extra == "" {
		plan, err := t.repo.GetPlanByThreadID(rctx.Context, rctx.Thread)
		if err != nil {
			return "", nil, err
		}
		// 1-indexed, as list_tasks displays and create_plan documents;
		// GetTaskByPosition slices 0-indexed.
		if position < 1 {
			return "", nil, fmt.Errorf("task position %d is out of range: task numbering starts at 1", position)
		}
		task, err := t.repo.GetTaskByPosition(rctx.Context, plan.ID, position-1)
		if err != nil {
			return "", nil, err
		}
		return task.ID, task, nil
	}

	// UUID lookup
	task, err := t.repo.GetTask(rctx.Context, taskRef)
	if err != nil {
		return "", nil, err
	}
	return task.ID, task, nil
}

// RemoveDependencyTool allows agents to remove dependencies between tasks
type RemoveDependencyTool struct {
	repo db.Repository
}

func NewRemoveDependencyTool(repo db.Repository) Tool {
	return NewToolWrapper(&RemoveDependencyTool{repo: repo})
}

func (t *RemoveDependencyTool) Name() string { return "remove_dependency" }

func (t *RemoveDependencyTool) Description() string {
	return `Remove a dependency between two tasks.

Specify from_task, to_task, and type to identify which dependency to remove.`
}

type RemoveDependencyParams struct {
	FromTask string `json:"from_task"`
	ToTask   string `json:"to_task"`
	Type     string `json:"type"`
}

func (t *RemoveDependencyTool) RequiresPermission(params RemoveDependencyParams) (bool, error) {
	return false, nil
}

func (t *RemoveDependencyTool) Execute(rctx *rctx.ToolContext, params RemoveDependencyParams) (ToolResponse, error) {
	if t.repo == nil {
		return NewTextErrorResponse("This tool requires a database connection and is not available in daemon-only mode"), nil
	}

	if params.FromTask == "" || params.ToTask == "" || params.Type == "" {
		return NewTextErrorResponse("from_task, to_task, and type are all required"), nil
	}

	removeDepType, ok := parseDependencyType(params.Type)
	if !ok {
		return NewTextErrorResponse(fmt.Sprintf("Invalid dependency type: %s. Must be one of: blocks, related, parallel_with", params.Type)), nil
	}

	if err := t.repo.DeleteTaskDependencyByPair(rctx.Context, params.FromTask, params.ToTask, removeDepType); err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to remove dependency: %v", err)), nil
	}

	if rctx.ChatID != "" {
		if err := t.repo.EmitChatRefetch(rctx.Context, rctx.ChatID, db.RefetchPlanTasks); err != nil {
			logging.Warn("[RemoveDependencyTool] Failed to emit refetch", "error", err)
		}
	}

	return NewTextResponse("Dependency removed successfully."), nil
}

// ListReadyTasksTool lists tasks that are ready to work on (no unresolved blockers)
type ListReadyTasksTool struct {
	repo db.Repository
}

func NewListReadyTasksTool(repo db.Repository) Tool {
	return NewToolWrapper(&ListReadyTasksTool{repo: repo})
}

func (t *ListReadyTasksTool) Name() string { return "list_ready_tasks" }

func (t *ListReadyTasksTool) Description() string {
	return `List tasks that are ready to work on — no unresolved blockers.

A task is "ready" when:
1. Its status is "pending" (not started yet)
2. All tasks that block it (via 'blocks' dependencies) have status "completed"

This is the deterministic way for agents to know what to pick up next.
Tasks with no blocking dependencies are always ready (if pending).

RETURNS:
- List of ready tasks with their details
- Total count of ready tasks vs total pending`
}

type ListReadyTasksParams struct{}

func (t *ListReadyTasksTool) RequiresPermission(params ListReadyTasksParams) (bool, error) {
	return false, nil
}

func (t *ListReadyTasksTool) Execute(rctx *rctx.ToolContext, params ListReadyTasksParams) (ToolResponse, error) {
	if t.repo == nil {
		return NewTextErrorResponse("This tool requires a database connection and is not available in daemon-only mode"), nil
	}

	threadID := rctx.Thread
	if threadID == "" {
		return NewTextErrorResponse("No thread context available"), nil
	}

	// A read: walk up to the ancestor's plan so a sub-agent can see what is
	// ready on the board it was spawned from.
	resolved, err := resolvePlanForRead(rctx.Context, t.repo, threadID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return NewTextErrorResponse("No plan found for this thread."), nil
		}
		return NewTextErrorResponse(fmt.Sprintf("Failed to find plan: %v", err)), nil
	}
	plan := resolved.plan

	allTasks, err := t.repo.ListTasksByPlan(rctx.Context, plan.ID)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to list tasks: %v", err)), nil
	}

	// Build task status map
	taskStatus := make(map[string]int32)
	for _, task := range allTasks {
		taskStatus[task.ID] = task.Status
	}

	// Get all dependencies for the plan
	allDeps, err := t.repo.ListDependenciesByPlan(rctx.Context, plan.ID)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to list dependencies: %v", err)), nil
	}

	// Build blocker map: taskID -> list of blocking task IDs
	blockers := make(map[string][]string)
	for _, dep := range allDeps {
		if dep.DependencyType == core.DependencyTypeBlocks {
			blockers[dep.ToTaskID] = append(blockers[dep.ToTaskID], dep.FromTaskID)
		}
	}

	// Find ready tasks
	var readyTasks []*db.Task
	pendingCount := 0

	for _, task := range allTasks {
		if task.Status != int32(db.TaskStatusPending) {
			continue
		}
		pendingCount++

		// Check if all blockers are completed
		blockerIDs := blockers[task.ID]
		allBlockersComplete := true
		for _, blockerID := range blockerIDs {
			if status, ok := taskStatus[blockerID]; ok && status != int32(db.TaskStatusCompleted) {
				allBlockersComplete = false
				break
			}
		}

		if allBlockersComplete {
			readyTasks = append(readyTasks, task)
		}
	}

	if len(readyTasks) == 0 {
		if pendingCount == 0 {
			return NewTextResponse("No pending tasks. All tasks are either completed, in progress, or in another state."), nil
		}
		return NewTextResponse(fmt.Sprintf("No ready tasks. %d pending task(s) are blocked by incomplete dependencies.", pendingCount)), nil
	}

	responseText := fmt.Sprintf("Ready Tasks (%d ready / %d pending / %d total):\n\n", len(readyTasks), pendingCount, len(allTasks))

	for i, task := range readyTasks {
		responseText += fmt.Sprintf("%d. [%s] %s\n", i+1, task.ID, task.Title)
		if task.Description != nil && *task.Description != "" {
			responseText += fmt.Sprintf("   Description: %s\n", *task.Description)
		}
		if task.Assignee != nil && *task.Assignee != "" {
			responseText += fmt.Sprintf("   Assignee: %s\n", *task.Assignee)
		}
	}

	return NewTextResponse(responseText), nil
}
