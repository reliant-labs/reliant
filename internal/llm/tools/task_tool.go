// Copyright (c) 2025 Reliant Labs
package tools

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// ListTasksTool allows agents to list tasks for the current plan
type ListTasksTool struct {
	repo db.Repository
}

// NewListTasksTool creates a new task listing tool
func NewListTasksTool(repo db.Repository) Tool {
	return NewToolWrapper(&ListTasksTool{
		repo: repo,
	})
}

func (l *ListTasksTool) Name() string {
	return "list_tasks"
}

func (l *ListTasksTool) Description() string {
	return `List all tasks for the current plan.
WHEN TO USE:
- When you need to see all tasks in the plan
- To check task progress and status
- To understand what work needs to be done
- BEFORE delegating: the ready set tells you what can be worked at once

RETURNS:
- List of all tasks with their status, title, and hierarchy
- Tasks are ordered by position and show parent-child relationships
- Assignee for any task that has been claimed, so you can see what other agents
  are already working on and avoid handing out the same task twice
- A count of tasks that are READY (pending with no incomplete blocker). Ready
  tasks have no dependency between them, so they are meant to be delegated
  together in one turn rather than one after another.

If this thread has no plan of its own, the plan of the nearest ancestor thread
is shown — the board you were spawned from. It is read-only here; you can still
update the status of a task assigned to you.`
}

// ListTasksParams represents the parameters for listing tasks
type ListTasksParams struct {
}

func (l *ListTasksTool) RequiresPermission(params ListTasksParams) (bool, error) {
	// list_tasks tool doesn't require permissions as it's read-only
	return false, nil
}

// taskDisplayInfo holds pre-computed dependency information for display
type taskDisplayInfo struct {
	blockedBy    []string // titles of tasks blocking this one (incomplete)
	blocks       []string // titles of tasks this one blocks
	parallelWith []string // titles of explicitly parallel tasks
	isReady      bool     // true if pending with all blockers completed
}

func (l *ListTasksTool) Execute(rctx *rctx.ToolContext, params ListTasksParams) (ToolResponse, error) {
	if l.repo == nil {
		return NewTextErrorResponse("This tool requires a database connection and is not available in daemon-only mode"), nil
	}

	threadID := rctx.Thread
	if threadID == "" {
		return NewTextErrorResponse("No thread context available"), nil
	}

	// Reads walk up to an ancestor's plan: a spawned sub-agent has its own
	// thread and usually no plan of its own, and the board it needs is the one
	// its parent used to delegate the work.
	resolved, err := resolvePlanForRead(rctx.Context, l.repo, threadID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return NewTextErrorResponse("No plan found for this thread. Use create_plan to create one."), nil
		}
		return NewTextErrorResponse(fmt.Sprintf("Failed to find plan for thread: %v", err)), nil
	}
	plan := resolved.plan
	planID := plan.ID

	allTasks, err := l.repo.ListTasksByPlan(rctx.Context, planID)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to list tasks: %v", err)), nil
	}

	if len(allTasks) == 0 {
		return NewTextResponse("No tasks found for this plan"), nil
	}

	// Get dependencies
	allDeps, _ := l.repo.ListDependenciesByPlan(rctx.Context, planID)

	stats, _ := l.repo.GetTaskStatsByPlan(rctx.Context, planID)

	// Build task maps
	taskMap := make(map[string]*db.Task)
	rootTasks := []*db.Task{}
	for _, task := range allTasks {
		taskMap[task.ID] = task
		if task.ParentTaskID == nil || *task.ParentTaskID == "" {
			rootTasks = append(rootTasks, task)
		}
	}

	// Pre-compute dependency display info
	displayInfo := make(map[string]*taskDisplayInfo)
	for _, task := range allTasks {
		displayInfo[task.ID] = &taskDisplayInfo{}
	}

	for _, dep := range allDeps {
		fromTask := taskMap[dep.FromTaskID]
		toTask := taskMap[dep.ToTaskID]
		if fromTask == nil || toTask == nil {
			continue
		}

		switch dep.DependencyType {
		case core.DependencyTypeBlocks:
			// fromTask blocks toTask
			if fromTask.Status != int32(db.TaskStatusCompleted) {
				displayInfo[dep.ToTaskID].blockedBy = append(displayInfo[dep.ToTaskID].blockedBy, fromTask.Title)
			}
			displayInfo[dep.FromTaskID].blocks = append(displayInfo[dep.FromTaskID].blocks, toTask.Title)
		case core.DependencyTypeParallelWith:
			displayInfo[dep.FromTaskID].parallelWith = append(displayInfo[dep.FromTaskID].parallelWith, toTask.Title)
			displayInfo[dep.ToTaskID].parallelWith = append(displayInfo[dep.ToTaskID].parallelWith, fromTask.Title)
		}
	}

	// Compute ready state. A pending task with no INCOMPLETE blocker can start
	// now; collect them by display number so the summary can name them rather
	// than just count them.
	readyCount := 0
	readyLabels := []string{}
	unassignedReady := 0
	for i, task := range rootTasks {
		if task.Status == int32(db.TaskStatusPending) && len(displayInfo[task.ID].blockedBy) == 0 {
			readyLabels = append(readyLabels, fmt.Sprintf("%d", i+1))
		}
	}
	for _, task := range allTasks {
		if task.Status == int32(db.TaskStatusPending) && len(displayInfo[task.ID].blockedBy) == 0 {
			displayInfo[task.ID].isReady = true
			readyCount++
			if task.Assignee == nil || *task.Assignee == "" {
				unassignedReady++
			}
		}
	}

	// Format response
	responseText := fmt.Sprintf("Tasks for Plan (Total: %d):\n\n", len(allTasks))

	// Say whose board this is. "No task is assigned to me" and "I am reading
	// my parent's plan" call for different next moves, and only the agent can
	// tell them apart if the plan says which it is.
	if resolved.inherited {
		responseText += fmt.Sprintf(
			"This plan belongs to an ancestor thread (%s) — you are reading the board you were spawned from.\n"+
				"You cannot modify it; update the task you were given, or create_plan for work of your own.\n\n",
			resolved.ownerThreadID)
	}

	responseText += "Status Summary:\n"
	if stats != nil {
		responseText += fmt.Sprintf("- Pending: %d", stats.Pending)
		if readyCount > 0 {
			responseText += fmt.Sprintf(" (%d ready)", readyCount)
		}
		responseText += "\n"
		responseText += fmt.Sprintf("- In Progress: %d\n", stats.InProgress)
		responseText += fmt.Sprintf("- Completed: %d\n", stats.Completed)
		if stats.Failed > 0 {
			responseText += fmt.Sprintf("- Failed: %d\n", stats.Failed)
		}
		if stats.Blocked > 0 {
			responseText += fmt.Sprintf("- Blocked: %d\n", stats.Blocked)
		}
		if stats.Cancelled > 0 {
			responseText += fmt.Sprintf("- Cancelled: %d\n", stats.Cancelled)
		}
		if stats.Skipped > 0 {
			responseText += fmt.Sprintf("- Skipped: %d\n", stats.Skipped)
		}
	}
	responseText += "\n"

	// Show dependency graph summary if any dependencies exist
	if len(allDeps) > 0 {
		responseText += fmt.Sprintf("Dependencies: %d relationship(s)\n\n", len(allDeps))
	}

	// The ready set is the fan-out plan, so say it as one. This count was
	// already computed and printed as a bare statistic ("Pending: 12 (5
	// ready)"); stated as work that can start now, it is the input to a batch
	// of spawns instead of a number to scroll past.
	if readyCount > 1 && !resolved.inherited {
		responseText += fmt.Sprintf("%d tasks are ready now (no incomplete blockers)", readyCount)
		if len(readyLabels) > 0 {
			responseText += fmt.Sprintf(": %s", strings.Join(readyLabels, ", "))
		}
		responseText += ".\n"
		if unassignedReady > 1 {
			responseText += "Tasks with no dependency between them are independent — delegate them in ONE turn (several spawn calls in a single message) rather than one after another.\n"
		}
		responseText += "\n"
	}

	responseText += "Task List:\n"
	for i, task := range rootTasks {
		responseText += l.formatTask(task, i+1, "", taskMap, displayInfo)
	}

	return NewTextResponse(responseText), nil
}

func (l *ListTasksTool) formatTask(task *db.Task, num int, indent string, taskMap map[string]*db.Task, displayInfo map[string]*taskDisplayInfo) string {
	info := displayInfo[task.ID]

	// Status icon - use READY marker for pending tasks with no blockers
	status := "⏳"
	switch db.TaskStatus(task.Status) {
	case db.TaskStatusCompleted:
		status = "✅"
	case db.TaskStatusInProgress:
		status = "🔄"
	case db.TaskStatusFailed:
		status = "❌"
	case db.TaskStatusBlocked:
		status = "🚫"
	case db.TaskStatusSkipped:
		status = "⏭️"
	case db.TaskStatusPending:
		if info != nil && len(info.blockedBy) > 0 {
			status = "🚫" // blocked by dependency
		} else if info != nil && info.isReady {
			status = "▶️" // ready to start
		}
	}

	result := fmt.Sprintf("%s%d. [%s] %s %s [%s]\n", indent, num, task.ID, status, task.Title, taskStatusToString(task.Status))

	if task.Description != nil && *task.Description != "" {
		result += fmt.Sprintf("%s   Description: %s\n", indent, *task.Description)
	}
	if task.Assignee != nil && *task.Assignee != "" {
		result += fmt.Sprintf("%s   Assignee: %s\n", indent, *task.Assignee)
	}

	// Show dependency info
	if info != nil {
		if len(info.blockedBy) > 0 {
			result += fmt.Sprintf("%s   Blocked by: %s\n", indent, strings.Join(info.blockedBy, ", "))
		}
		if len(info.blocks) > 0 {
			result += fmt.Sprintf("%s   Blocks: %s\n", indent, strings.Join(info.blocks, ", "))
		}
		if len(info.parallelWith) > 0 {
			result += fmt.Sprintf("%s   Parallel with: %s\n", indent, strings.Join(info.parallelWith, ", "))
		}
	}

	// Find and display subtasks ordered by position
	var subtasks []*db.Task
	for _, t := range taskMap {
		if t.ParentTaskID != nil && *t.ParentTaskID == task.ID {
			subtasks = append(subtasks, t)
		}
	}
	sort.Slice(subtasks, func(i, j int) bool {
		return subtasks[i].Position < subtasks[j].Position
	})
	for i, sub := range subtasks {
		result += l.formatTask(sub, i+1, indent+"  ", taskMap, displayInfo)
	}

	return result
}

// UpdateTaskTool allows agents to update task status and details
type UpdateTaskTool struct {
	repo db.Repository
}

// NewUpdateTaskTool creates a new task update tool
func NewUpdateTaskTool(repo db.Repository) Tool {
	return NewToolWrapper(&UpdateTaskTool{
		repo: repo,
	})
}

func (u *UpdateTaskTool) Name() string {
	return "update_task"
}

func (u *UpdateTaskTool) Description() string {
	return `Update a task's status, details, or metadata.
WHEN TO USE:
- When starting work on a task (mark as in_progress)
- When completing a task (mark as completed)
- When a task is blocked or failed
- To update task description with findings
- To add notes, hints, or discoveries to metadata
- To claim a task by setting assignee + in_progress

STATUS OPTIONS:
- pending: Not started yet
- in_progress: Currently working on it
- completed: Successfully finished
- failed: Could not complete
- blocked: Waiting on something (add blocker to notes)
- skipped: Decided not to do
- cancelled: No longer needed

ASSIGNEE:
- Free-form text identifying who is working on this task
- Use a descriptive label: spawn title, role name, or agent identifier
- Claim pattern: update_task(task_id="X", status="in_progress", assignee="researcher-auth")
- Other agents see assignments in list_tasks and skip claimed work

METADATA OPTIONS:
- notes: Add discoveries, blockers, or important context
- preferred_agent: Suggest which agent should handle this
- tool_hints: Suggest tools to use ["use_bash", "search_first"]
- dependencies: Document what this depends on
- next_steps: What to do after this task

BEST PRACTICES:
- Update status when you start and finish tasks
- Add descriptions to document what was done
- Use metadata.notes for blockers when marking as blocked
- Add tool_hints for complex tasks to guide future execution
- Set assignee when claiming a task to prevent duplicate work`
}

// UpdateTaskParams represents the parameters for updating a task
type UpdateTaskParams struct {
	TaskID      string                 `json:"task_id"`
	Title       string                 `json:"title,omitempty"`
	Status      string                 `json:"status,omitempty"`
	Description string                 `json:"description,omitempty"`
	Assignee    string                 `json:"assignee,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

func (u *UpdateTaskTool) RequiresPermission(params UpdateTaskParams) (bool, error) {
	// update_task tool doesn't require permissions as it only modifies in-memory task state
	return false, nil
}

func (u *UpdateTaskTool) Execute(rctx *rctx.ToolContext, params UpdateTaskParams) (ToolResponse, error) {
	if u.repo == nil {
		return NewTextErrorResponse("This tool requires a database connection and is not available in daemon-only mode"), nil
	}

	if params.TaskID == "" {
		return NewTextErrorResponse("Task ID is required"), nil
	}

	// Get thread ID from context to find the plan
	threadID := rctx.Thread

	var task *db.Task
	var err error

	// Check if TaskID is a number (ordinal position)
	// Must be just a number, no other characters
	position := 0
	extra := ""
	if n, _ := fmt.Sscanf(params.TaskID, "%d%s", &position, &extra); n == 1 && extra == "" {
		// It's a number, try to get by position
		if threadID == "" {
			return NewTextErrorResponse("No thread context available for ordinal lookup"), nil
		}

		// Resolve the ordinal against the plan this thread can SEE, which for
		// a sub-agent is its parent's board. This is the claim path: an agent
		// spawned to do task 4 has to be able to say "4 is mine, and now it is
		// done". Updating one task row it was handed is single-writer — it is
		// mutating the plan's SHAPE (adding tasks, retitling it) that stays
		// with the owning thread.
		resolved, err := resolvePlanForRead(rctx.Context, u.repo, threadID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return NewTextErrorResponse("No plan found for this thread. Use create_plan to create one."), nil
			}
			return NewTextErrorResponse(fmt.Sprintf("Failed to find plan for ordinal lookup: %v", err)), nil
		}

		// Ordinals are 1-indexed, matching what list_tasks prints and what
		// create_plan's task_position documents; GetTaskByPosition slices
		// 0-indexed. Without this conversion "1" silently updated the SECOND
		// task and the last task in the list was unreachable.
		if position < 1 {
			return NewTextErrorResponse(fmt.Sprintf("Task position %d is out of range: task numbering starts at 1", position)), nil
		}
		task, err = u.repo.GetTaskByPosition(rctx.Context, resolved.plan.ID, position-1)
		if err != nil {
			return NewTextErrorResponse(fmt.Sprintf("Failed to find task at position %d: %v", position, err)), nil
		}
	} else {
		// It's a UUID, get by ID
		task, err = u.repo.GetTask(rctx.Context, params.TaskID)
		if err != nil {
			return NewTextErrorResponse(fmt.Sprintf("Failed to find task: %v", err)), nil
		}
	}

	// Update fields
	updated := false
	if params.Title != "" {
		task.Title = params.Title
		updated = true
	}
	if params.Status != "" {
		statusVal, ok := parseTaskStatus(params.Status)
		if !ok {
			return NewTextErrorResponse(fmt.Sprintf("Invalid status: %s", params.Status)), nil
		}
		task.Status = statusVal
		updated = true
	}

	if params.Description != "" {
		task.Description = &params.Description
		updated = true
	}
	if params.Assignee != "" {
		task.Assignee = &params.Assignee
		updated = true
	}

	// Persist metadata if provided
	if len(params.Metadata) > 0 {
		// Merge with existing metadata if present
		existing := make(map[string]interface{})
		if task.Metadata != nil {
			_ = json.Unmarshal([]byte(*task.Metadata), &existing)
		}
		for k, v := range params.Metadata {
			existing[k] = v
		}
		if metaJSON, err := json.Marshal(existing); err == nil {
			s := string(metaJSON)
			task.Metadata = &s
			updated = true
		}
	}

	if !updated {
		return NewTextResponse("No updates provided"), nil
	}

	task.UpdatedAt = time.Now()
	if task.Status == int32(db.TaskStatusCompleted) {
		now := time.Now()
		task.CompletedAt = &now
	}

	// Update the task
	if err := u.repo.UpdateTask(rctx.Context, task); err != nil {
		logging.Error("Failed to update task", "error", err, "task_id", task.ID)
		return NewTextErrorResponse(fmt.Sprintf("Failed to update task: %v", err)), nil
	}

	// Check if all tasks are completed
	stats, _ := u.repo.GetTaskStatsByPlan(rctx.Context, task.PlanID)
	allCompleted := false
	if stats != nil {
		allCompleted = (stats.Pending == 0 && stats.InProgress == 0 && stats.Failed == 0 && stats.Blocked == 0)
	}

	descStr := ""
	if task.Description != nil {
		descStr = *task.Description
	}

	responseText := fmt.Sprintf(`Task updated successfully!

ID: %s
Title: %s
Status: %s`,
		task.ID,
		task.Title,
		taskStatusToString(task.Status),
	)

	if descStr != "" {
		responseText += fmt.Sprintf("\nDescription: %s", descStr)
	}

	if allCompleted {
		responseText += "\n\n🎉 All tasks in the plan are now completed!"
		// Could trigger plan completion here if needed
	}

	if chatID := rctx.ChatID; chatID != "" {
		if err := u.repo.EmitChatRefetch(rctx.Context, chatID, db.RefetchPlanTasks); err != nil {
			logging.Warn("[UpdateTaskTool] Failed to emit plan_tasks refetch", "error", err)
		}
	}

	logging.Info("Task updated", "task_id", task.ID, "status", task.Status)
	return NewTextResponse(responseText), nil
}

// CreateSubtaskTool allows agents to create subtasks
type CreateSubtaskTool struct {
	repo db.Repository
}

// NewCreateSubtaskTool creates a new subtask creation tool
func NewCreateSubtaskTool(repo db.Repository) Tool {
	return NewToolWrapper(&CreateSubtaskTool{
		repo: repo,
	})
}

func (c *CreateSubtaskTool) Name() string {
	return "create_subtask"
}

func (c *CreateSubtaskTool) Description() string {
	return `Create a subtask under an existing task.
WHEN TO USE:
- When breaking down a complex task into smaller steps
- To add more granular tracking
- When discovering additional work while implementing

BEST PRACTICES:
- Keep subtasks focused and specific
- Use subtasks for logical groupings of work
- Don't create too many levels of nesting`
}

// CreateSubtaskParams represents the parameters for creating a subtask
type CreateSubtaskParams struct {
	ParentTaskID string       `json:"parent_task_id"`
	Title        string       `json:"title"`
	Description  string       `json:"description,omitempty"`
	Assignee     string       `json:"assignee,omitempty"`
	Metadata     TaskMetadata `json:"metadata,omitempty"`
}

func (c *CreateSubtaskTool) RequiresPermission(params CreateSubtaskParams) (bool, error) {
	// create_subtask tool doesn't require permissions as it only modifies in-memory task state
	return false, nil
}

func (c *CreateSubtaskTool) Execute(rctx *rctx.ToolContext, params CreateSubtaskParams) (ToolResponse, error) {
	if c.repo == nil {
		return NewTextErrorResponse("This tool requires a database connection and is not available in daemon-only mode"), nil
	}

	if params.ParentTaskID == "" {
		return NewTextErrorResponse("Parent task ID is required"), nil
	}
	if params.Title == "" {
		return NewTextErrorResponse("Task title is required"), nil
	}

	// Get parent task to get plan ID
	parentTask, err := c.repo.GetTask(rctx.Context, params.ParentTaskID)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to find parent task: %v", err)), nil
	}

	// Serialize metadata to JSON if any fields are set
	var metadataPtr *string
	if params.Metadata.PreferredAgent != "" || len(params.Metadata.ToolHints) > 0 ||
		len(params.Metadata.Dependencies) > 0 || params.Metadata.Notes != "" || params.Metadata.Priority != "" {
		if metaJSON, err := json.Marshal(params.Metadata); err == nil {
			s := string(metaJSON)
			metadataPtr = &s
		}
	}

	var assigneePtr *string
	if params.Assignee != "" {
		assigneePtr = &params.Assignee
	}

	// Create the subtask
	desc := params.Description
	subtask := &db.Task{
		ID:           uuid.New().String(),
		PlanID:       parentTask.PlanID,
		ParentTaskID: &params.ParentTaskID,
		Title:        params.Title,
		Description:  &desc,
		Status:       int32(db.TaskStatusPending),
		Position:     0,
		Metadata:     metadataPtr,
		Assignee:     assigneePtr,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	if err := c.repo.CreateTask(rctx.Context, subtask); err != nil {
		logging.Error("Failed to create subtask", "error", err)
		return NewTextErrorResponse(fmt.Sprintf("Failed to create subtask: %v", err)), nil
	}

	responseText := fmt.Sprintf(`Subtask created successfully!

ID: %s
Parent: %s
Title: %s
Status: %s`,
		subtask.ID,
		parentTask.Title,
		subtask.Title,
		taskStatusToString(subtask.Status),
	)

	if subtask.Description != nil && *subtask.Description != "" {
		responseText += fmt.Sprintf("\nDescription: %s", *subtask.Description)
	}

	if chatID := rctx.ChatID; chatID != "" {
		if err := c.repo.EmitChatRefetch(rctx.Context, chatID, db.RefetchPlanTasks); err != nil {
			logging.Warn("[CreateSubtaskTool] Failed to emit plan_tasks refetch", "error", err)
		}
	}

	logging.Info("Subtask created", "task_id", subtask.ID, "parent_id", parentTask.ID)
	return NewTextResponse(responseText), nil
}
