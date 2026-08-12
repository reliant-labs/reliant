// Copyright (c) 2025 Reliant Labs
package tools

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// parseComplexity converts a string complexity to its int32 proto enum value.
func parseComplexity(s string) (int32, bool) {
	switch s {
	case "simple":
		return int32(reliantv1.PlanComplexity_PLAN_COMPLEXITY_SIMPLE), true
	case "moderate":
		return int32(reliantv1.PlanComplexity_PLAN_COMPLEXITY_MODERATE), true
	case "complex":
		return int32(reliantv1.PlanComplexity_PLAN_COMPLEXITY_COMPLEX), true
	default:
		return 0, false
	}
}

// complexityToString converts an int32 complexity value to a display string.
func complexityToString(c int32) string {
	switch reliantv1.PlanComplexity(c) {
	case reliantv1.PlanComplexity_PLAN_COMPLEXITY_SIMPLE:
		return "simple"
	case reliantv1.PlanComplexity_PLAN_COMPLEXITY_MODERATE:
		return "moderate"
	case reliantv1.PlanComplexity_PLAN_COMPLEXITY_COMPLEX:
		return "complex"
	default:
		return "unspecified"
	}
}

// parsePlanStatus converts a string status to its int32 proto enum value.
func parsePlanStatus(s string) (int32, bool) {
	switch s {
	case "pending":
		return int32(db.PlanStatusPending), true
	case "in_progress":
		return int32(db.PlanStatusActive), true
	case "completed":
		return int32(db.PlanStatusCompleted), true
	case "cancelled":
		return int32(db.PlanStatusCancelled), true
	default:
		return 0, false
	}
}

// planStatusToString converts an int32 plan status to a display string.
func planStatusToString(s int32) string {
	switch {
	case s == int32(db.PlanStatusPending):
		return "pending"
	case s == int32(db.PlanStatusActive):
		return "in_progress"
	case s == int32(db.PlanStatusCompleted):
		return "completed"
	case s == int32(db.PlanStatusCancelled):
		return "cancelled"
	default:
		return "unspecified"
	}
}

// taskStatusToString converts an int32 task status to a display string.
func taskStatusToString(s int32) string {
	switch {
	case s == int32(db.TaskStatusPending):
		return "pending"
	case s == int32(db.TaskStatusInProgress):
		return "in_progress"
	case s == int32(db.TaskStatusCompleted):
		return "completed"
	case s == int32(db.TaskStatusFailed):
		return "failed"
	case s == int32(db.TaskStatusBlocked):
		return "blocked"
	case s == int32(db.TaskStatusCancelled):
		return "cancelled"
	case s == int32(db.TaskStatusSkipped):
		return "skipped"
	default:
		return "unspecified"
	}
}

// parseTaskStatus converts a string task status to its int32 proto enum value.
func parseTaskStatus(s string) (int32, bool) {
	switch s {
	case "pending":
		return int32(db.TaskStatusPending), true
	case "in_progress":
		return int32(db.TaskStatusInProgress), true
	case "completed":
		return int32(db.TaskStatusCompleted), true
	case "failed":
		return int32(db.TaskStatusFailed), true
	case "blocked":
		return int32(db.TaskStatusBlocked), true
	case "cancelled":
		return int32(db.TaskStatusCancelled), true
	case "skipped":
		return int32(db.TaskStatusSkipped), true
	default:
		return 0, false
	}
}

// CreatePlanTool allows agents to create structured plans with tasks
type CreatePlanTool struct {
	repo db.Repository
}

// NewCreatePlanTool creates a new plan creation tool
func NewCreatePlanTool(repo db.Repository) Tool {
	return NewToolWrapper(&CreatePlanTool{
		repo: repo,
	})
}

func (p *CreatePlanTool) Name() string {
	return "create_plan"
}

func (p *CreatePlanTool) Description() string {
	return `Create a comprehensive plan with tasks for implementing a feature or solving a problem.
WHEN TO USE:
- AFTER you preform your initial research and analyze the problem.
- Use this tool when you need to organize complex work into structured steps
- You typically should create plans AFTER your findings. Avoid creating tasks to research, explore, identify, or search through the codebase. You should first perform your research so you can create an informed plan.
- ESPECIALLY when you are going to delegate. If you are about to spawn
  sub-agents, plan first: the task graph is what tells you which of them can run
  at the same time. An orchestrator that spawns without a plan has no record of
  what is independent, and ends up delegating one agent at a time.

VISIBILITY: Sub-agents you spawn CAN read this plan. A spawned thread with no
plan of its own resolves list_tasks / get_plan / list_ready_tasks against its
nearest ancestor's plan, so the board you build here is the board they see.
They can update the status of a task you assigned them; only this thread can
change the plan's shape (add tasks, edit the plan itself).

So a plan is how you delegate, not just how you take notes. Tasks are the units
of work you hand out — write them at the size of one sub-agent's job.

PLAN STRUCTURE:
- Title: Clear, concise title for the plan
- Description: Detailed description including:
  - Main objective
  - Approach/strategy
  - Alternative approaches (if applicable)
  - Success criteria
- Complexity: simple|moderate|complex
- Tasks: List of tasks with title, description, optional metadata, and optional dependencies
- The plan will be associated with the current session

INLINE DEPENDENCIES:
You can specify dependencies between tasks at creation time using 1-indexed task positions.
Each task can have a "dependencies" array where each entry specifies:
- task_position: The 1-indexed position of another task in the tasks array
- type: "blocks" (the other task must COMPLETE first — a real data dependency),
  "related" (informational only), or "parallel_with" (emphasis that two tasks are
  safe together; rarely needed, since anything without a "blocks" edge already is)

The dependency means: "the task at task_position has this relationship TO the current task."
For example, if task 3 has dependencies: [{task_position: 1, type: "blocks"}], it means task 1 blocks task 3.

INDEPENDENT IS THE DEFAULT. Tasks with no "blocks" edge between them can run at
the same time — you do NOT need to mark that. Add "blocks" only where a real
data dependency exists: task B consumes something task A creates. Name it in
the task description when you do.

A chain of "blocks" edges says every task must wait for the previous one, which
serializes the whole plan. Only write one when that is true.

Example — fan-out (the common shape; four independent tasks, then a join):
  tasks: [
    {title: "Design schema"},
    {title: "Implement API",       dependencies: [{task_position: 1, type: "blocks"}]},
    {title: "Implement worker",    dependencies: [{task_position: 1, type: "blocks"}]},
    {title: "Implement frontend",  dependencies: [{task_position: 1, type: "blocks"}]},
    {title: "End-to-end tests",    dependencies: [
        {task_position: 2, type: "blocks"},
        {task_position: 3, type: "blocks"},
        {task_position: 4, type: "blocks"}]}
  ]
Tasks 2, 3 and 4 all wait on the schema and on nothing else, so once it lands
all three are ready together and should be delegated in ONE turn.

Example — a genuine chain (each step consumes the last):
  tasks: [
    {title: "Write migration"},
    {title: "Regenerate ORM from applied schema", dependencies: [{task_position: 1, type: "blocks"}]}
  ]

TASK METADATA:
Each task can optionally include metadata with agent hints:
- preferred_agent: Which agent should handle this task
- tool_hints: Suggested tools to use
- dependencies: Informational dependency notes (free-form text)
- notes: Important context
- priority: high/medium/low

BEST PRACTICES:
- Break down work into clear, actionable tasks
- Cut tasks along boundaries that do not overlap (package, module, directory),
  so independent tasks can be worked at the same time without collisions
- Use inline dependencies to define the task graph upfront, and add "blocks"
  ONLY where one task genuinely consumes another's output
- Include a mini-roadmap in the description
- Document alternative approaches for pivoting
- Be specific about what needs to be done
- Consider edge cases and potential blockers
- Consider changing state in parallel with plan creation, if states are available.`
}

// TaskDependencyInput represents an inline dependency reference using 1-indexed task positions.
type TaskDependencyInput struct {
	TaskPosition int    `json:"task_position"` // 1-indexed position of the other task in this plan's task list
	Type         string `json:"type"`          // blocks, related, parallel_with
}

// TaskInput represents a task that can be created with the plan
type TaskInput struct {
	Title        string                `json:"title"`
	Description  string                `json:"description,omitempty"`
	Metadata     *TaskMetadata         `json:"metadata,omitempty"`     // Optional: agent hints, priority, notes
	Dependencies []TaskDependencyInput `json:"dependencies,omitempty"` // Optional: "these tasks (by position) have a relationship with this task"
}

// CreatePlanParams represents the parameters for creating a plan with tasks
type CreatePlanParams struct {
	Title       string      `json:"title"`
	Description string      `json:"description"`
	Complexity  string      `json:"complexity"` // simple, moderate, complex
	Tasks       []TaskInput `json:"tasks"`      // List of tasks with title and optional description
}

// CreatePlanPermissionParams represents the permission parameters for plan creation
type CreatePlanPermissionParams struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Complexity  string   `json:"complexity"`
	Tasks       []string `json:"tasks"`
}

func (p *CreatePlanTool) RequiresPermission(params CreatePlanParams) (bool, error) {
	// create_plan tool requires permissions as it creates plans and potentially transitions state
	return true, nil
}

func (p *CreatePlanTool) Execute(rctx *rctx.ToolContext, params CreatePlanParams) (ToolResponse, error) {
	if p.repo == nil {
		return NewTextErrorResponse("This tool requires a database connection and is not available in daemon-only mode"), nil
	}

	// Get thread ID from context
	threadID := rctx.Thread
	if threadID == "" {
		return NewTextErrorResponse("Thread context required"), nil
	}

	// Validate parameters
	if params.Title == "" {
		return NewTextErrorResponse("Plan title is required"), nil
	}
	if params.Description == "" {
		return NewTextErrorResponse("Plan description is required"), nil
	}

	// Validate and default complexity
	if params.Complexity == "" {
		params.Complexity = "moderate"
	}
	switch params.Complexity {
	case "simple", "moderate", "complex":
		// Valid complexity
	default:
		return NewTextErrorResponse(fmt.Sprintf("Invalid complexity: %s. Must be one of: simple, moderate, complex", params.Complexity)), nil
	}

	// Check if thread already has a plan (prevent multiple active plans)
	existingPlan, err := p.repo.GetPlanByThreadID(rctx.Context, threadID)
	if err == nil && existingPlan.Status == int32(db.PlanStatusActive) {
		return NewTextErrorResponse("Thread already has an active plan. Use update_plan to modify it or complete it first."), nil
	}

	// Convert TaskInput slice to string slice for permission params
	taskStrings := make([]string, len(params.Tasks))
	for i, task := range params.Tasks {
		if task.Description != "" {
			taskStrings[i] = fmt.Sprintf("%s: %s", task.Title, task.Description)
		} else {
			taskStrings[i] = task.Title
		}
	}

	// Create the plan
	complexityVal, _ := parseComplexity(params.Complexity)
	plan := &db.Plan{
		ID:          uuid.New().String(),
		ThreadID:    threadID,
		Title:       params.Title,
		Description: &params.Description,
		Status:      int32(db.PlanStatusActive),
		Complexity:  &complexityVal,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	if err := p.repo.CreatePlan(rctx.Context, plan); err != nil {
		logging.Error("Failed to create plan", "error", err)
		return NewTextErrorResponse(fmt.Sprintf("Failed to create plan: %v", err)), nil
	}

	// Create tasks for the plan
	var createdTasks []*db.Task
	for i, taskInput := range params.Tasks {
		desc := strings.TrimSpace(taskInput.Description)

		// Serialize metadata if provided
		var metadataPtr *string
		if taskInput.Metadata != nil {
			m := taskInput.Metadata
			if m.PreferredAgent != "" || len(m.ToolHints) > 0 || len(m.Dependencies) > 0 || m.Notes != "" || m.Priority != "" {
				if metaJSON, err := json.Marshal(m); err == nil {
					s := string(metaJSON)
					metadataPtr = &s
				}
			}
		}

		task := &db.Task{
			ID:          uuid.New().String(),
			PlanID:      plan.ID,
			Title:       strings.TrimSpace(taskInput.Title),
			Description: &desc,
			Status:      int32(db.TaskStatusPending),
			Position:    i,
			Metadata:    metadataPtr,
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		}

		if err := p.repo.CreateTask(rctx.Context, task); err != nil {
			logging.Error("Failed to create task", "error", err, "task_title", taskInput.Title)
			// Continue creating other tasks even if one fails
			continue
		}
		createdTasks = append(createdTasks, task)
	}

	// Create inline dependencies (position-based references)
	var createdDeps []string
	var depErrors []string
	for i, taskInput := range params.Tasks {
		if len(taskInput.Dependencies) == 0 {
			continue
		}
		// Find the created task for this position
		var thisTask *db.Task
		for _, t := range createdTasks {
			if t.Position == i {
				thisTask = t
				break
			}
		}
		if thisTask == nil {
			continue // task failed to create
		}

		for _, dep := range taskInput.Dependencies {
			// Validate dependency type
			depTypeVal, depOk := parseDependencyType(dep.Type)
			if !depOk {
				depErrors = append(depErrors, fmt.Sprintf("task %d: invalid dependency type %q", i+1, dep.Type))
				continue
			}

			// Validate position (1-indexed)
			refIdx := dep.TaskPosition - 1 // convert to 0-indexed
			if refIdx < 0 || refIdx >= len(params.Tasks) {
				depErrors = append(depErrors, fmt.Sprintf("task %d: task_position %d is out of range (1-%d)", i+1, dep.TaskPosition, len(params.Tasks)))
				continue
			}
			if refIdx == i {
				depErrors = append(depErrors, fmt.Sprintf("task %d: cannot depend on itself", i+1))
				continue
			}

			// Find the referenced task
			var refTask *db.Task
			for _, t := range createdTasks {
				if t.Position == refIdx {
					refTask = t
					break
				}
			}
			if refTask == nil {
				depErrors = append(depErrors, fmt.Sprintf("task %d: referenced task at position %d was not created", i+1, dep.TaskPosition))
				continue
			}

			// Create the dependency: refTask -> thisTask
			// "task_position X blocks me" means from=refTask, to=thisTask
			taskDep := &core.TaskDependency{
				ID:             uuid.New().String(),
				FromTaskID:     refTask.ID,
				ToTaskID:       thisTask.ID,
				DependencyType: depTypeVal,
				CreatedAt:      time.Now(),
			}
			if err := p.repo.CreateTaskDependency(rctx.Context, taskDep); err != nil {
				logging.Error("Failed to create inline dependency", "error", err,
					"from_task", refTask.Title, "to_task", thisTask.Title)
				depErrors = append(depErrors, fmt.Sprintf("%s -> %s: %v", refTask.Title, thisTask.Title, err))
				continue
			}
			createdDeps = append(createdDeps, fmt.Sprintf("%s %s %s", refTask.Title, dep.Type, thisTask.Title))
		}
	}

	// Format response
	complexityStr := ""
	if plan.Complexity != nil {
		complexityStr = complexityToString(*plan.Complexity)
	}
	descStr := ""
	if plan.Description != nil {
		descStr = *plan.Description
	}

	responseText := fmt.Sprintf(`Plan created successfully!

ID: %s
Title: %s
Complexity: %s
Status: %s

Description:
%s

Tasks (%d created):
`,
		plan.ID,
		plan.Title,
		complexityStr,
		planStatusToString(plan.Status),
		descStr,
		len(createdTasks),
	)

	for i, task := range createdTasks {
		responseText += fmt.Sprintf("%d. [%s] %s (Status: %s)\n", i+1, task.ID, task.Title, taskStatusToString(task.Status))
		if task.Description != nil && *task.Description != "" {
			responseText += fmt.Sprintf("   Description: %s\n", *task.Description)
		}
	}

	if len(createdDeps) > 0 {
		responseText += fmt.Sprintf("\nDependencies (%d created):\n", len(createdDeps))
		for _, d := range createdDeps {
			responseText += fmt.Sprintf("  - %s\n", d)
		}
	}
	if len(depErrors) > 0 {
		responseText += fmt.Sprintf("\nDependency warnings (%d):\n", len(depErrors))
		for _, e := range depErrors {
			responseText += fmt.Sprintf("  - %s\n", e)
		}
	}

	responseText += "\nThe plan has been associated with the current thread."

	if err := p.repo.EmitChatRefetch(rctx.Context, rctx.ChatID, db.RefetchPlanTasks); err != nil {
		logging.Warn("[CreatePlanTool] Failed to emit plan_tasks refetch", "error", err)
	}

	// Create a text response
	response := NewTextResponse(
		responseText,
	)

	return response, nil
}

// UpdatePlanTool allows agents to update existing plans
type UpdatePlanTool struct {
	repo db.Repository
}

// NewUpdatePlanTool creates a new plan update tool
func NewUpdatePlanTool(repo db.Repository) Tool {
	return NewToolWrapper[UpdatePlanParams, ToolResponse](&UpdatePlanTool{
		repo: repo,
	})
}

func (u *UpdatePlanTool) Name() string {
	return "update_plan"
}

func (u *UpdatePlanTool) RequiresPermission(params UpdatePlanParams) (bool, error) {
	// update_plan tool doesn't require permissions as it only modifies in-memory plan state
	return false, nil
}

func (u *UpdatePlanTool) Description() string {
	return `Update an existing plan's details or status.
WHEN TO USE:
- When you need to modify the plan based on new information
- When pivoting to a different approach
- When marking a plan as completed or cancelled

UPDATES ALLOWED:
- Title: Update the plan title
- Description: Add new information, document pivots
- Status: pending|in_progress|completed|cancelled
- Complexity: simple|moderate|complex

BEST PRACTICES:
- Document why changes are being made
- Keep the description updated with current approach
- Use this to track progress and pivots`
}

// UpdatePlanParams represents the parameters for updating a plan
type UpdatePlanParams struct {
	Title       string `json:"title,omitempty"`       // Optional: new title
	Description string `json:"description,omitempty"` // Optional: new description
	Status      string `json:"status,omitempty"`      // Optional: new status
	Complexity  string `json:"complexity,omitempty"`  // Optional: new complexity
}

func (u *UpdatePlanTool) Execute(rctx *rctx.ToolContext, params UpdatePlanParams) (ToolResponse, error) {
	if u.repo == nil {
		return NewTextErrorResponse("This tool requires a database connection and is not available in daemon-only mode"), nil
	}

	// Get thread ID from context
	threadID := rctx.Thread
	if threadID == "" {
		return NewTextErrorResponse("No thread context available"), nil
	}

	// Writes bind to THIS thread's plan only — an ancestor's is read-only.
	plan, err := resolvePlanForWrite(rctx.Context, u.repo, threadID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			if inherited, resolveErr := resolvePlanForRead(rctx.Context, u.repo, threadID); resolveErr == nil && inherited.inherited {
				return NewTextErrorResponse(inheritedPlanWriteRefusal(inherited.ownerThreadID)), nil
			}
			return NewTextErrorResponse("No plan found for this thread. Use create_plan to create one."), nil
		}
		return NewTextErrorResponse(fmt.Sprintf("Failed to find plan: %v", err)), nil
	}

	// Apply updates
	updated := false
	if params.Title != "" {
		plan.Title = params.Title
		updated = true
	}
	if params.Description != "" {
		plan.Description = &params.Description
		updated = true
	}
	if params.Status != "" {
		statusVal, ok := parsePlanStatus(params.Status)
		if !ok {
			return NewTextErrorResponse(fmt.Sprintf("Invalid status: %s", params.Status)), nil
		}
		plan.Status = statusVal
		updated = true
	}
	if params.Complexity != "" {
		complexityVal, ok := parseComplexity(params.Complexity)
		if !ok {
			return NewTextErrorResponse(fmt.Sprintf("Invalid complexity: %s", params.Complexity)), nil
		}
		plan.Complexity = &complexityVal
		updated = true
	}

	if !updated {
		return NewTextResponse("No updates provided"), nil
	}

	plan.UpdatedAt = time.Now()

	// Update the plan
	if err := u.repo.UpdatePlan(rctx.Context, plan); err != nil {
		logging.Error("Failed to update plan", "error", err, "plan_id", plan.ID)
		return NewTextErrorResponse(fmt.Sprintf("Failed to update plan: %v", err)), nil
	}

	// Check if plan was marked as completed and handle auto-transition
	var responseText string
	complexityStr := ""
	if plan.Complexity != nil {
		complexityStr = complexityToString(*plan.Complexity)
	}
	descStr := ""
	if plan.Description != nil {
		descStr = *plan.Description
	}

	if plan.Status == int32(db.PlanStatusCompleted) {
		completedAtStr := ""
		if plan.CompletedAt != nil {
			completedAtStr = plan.CompletedAt.Format("2006-01-02 15:04:05")
		}
		responseText = fmt.Sprintf(`Plan completed successfully! 🎉

ID: %s
Title: %s
Complexity: %s
Status: %s
Completed: %s

Description:
%s

The plan has been marked as completed. Consider transitioning to validation or creating a new session for the next phase.`,
			plan.ID,
			plan.Title,
			complexityStr,
			planStatusToString(plan.Status),
			completedAtStr,
			descStr,
		)
	} else {
		responseText = fmt.Sprintf(`Plan updated successfully!

ID: %s
Title: %s
Complexity: %s
Status: %s

Description:
%s`,
			plan.ID,
			plan.Title,
			complexityStr,
			planStatusToString(plan.Status),
			descStr,
		)
	}

	if err := u.repo.EmitChatRefetch(rctx.Context, rctx.ChatID, db.RefetchPlanTasks); err != nil {
		logging.Warn("[UpdatePlanTool] Failed to emit plan_tasks refetch", "error", err)
	}

	logging.Info("Plan updated",
		"plan_id", plan.ID,
		"thread_id", threadID,
		"status", plan.Status)

	return NewTextResponse(responseText), nil
}

// GetPlanTool allows agents to retrieve the current plan
type GetPlanTool struct {
	repo db.Repository
}

// NewGetPlanTool creates a new plan retrieval tool
func NewGetPlanTool(repo db.Repository) Tool {
	return NewToolWrapper[GetPlanParams, ToolResponse](&GetPlanTool{
		repo: repo,
	})
}

func (g *GetPlanTool) Name() string {
	return "get_plan"
}

func (g *GetPlanTool) RequiresPermission(params GetPlanParams) (bool, error) {
	// get_plan tool doesn't require permissions as it's read-only
	return false, nil
}

func (g *GetPlanTool) Description() string {
	return `Retrieve the current plan for this session.
WHEN TO USE:
- When you need to review the current plan
- To check plan status and progress
- To understand what needs to be done

RETURNS:
- Plan details including title, description, status, and complexity
- Returns error if no plan exists for the session`
}

// GetPlanParams represents the parameters for getting a plan
type GetPlanParams struct {
}

func (g *GetPlanTool) Execute(rctx *rctx.ToolContext, params GetPlanParams) (ToolResponse, error) {
	if g.repo == nil {
		return NewTextErrorResponse("This tool requires a database connection and is not available in daemon-only mode"), nil
	}

	// Get thread ID from context
	threadID := rctx.Thread
	if threadID == "" {
		return NewTextErrorResponse("No thread context available"), nil
	}

	// Reads walk up to an ancestor's plan — a spawned sub-agent needs to see
	// the board it was delegated from.
	resolved, err := resolvePlanForRead(rctx.Context, g.repo, threadID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return NewTextErrorResponse("No plan found for this thread. Use create_plan to create one."), nil
		}
		return NewTextErrorResponse(fmt.Sprintf("Failed to find plan: %v", err)), nil
	}
	plan := resolved.plan

	// Format response
	complexityStr := ""
	if plan.Complexity != nil {
		complexityStr = complexityToString(*plan.Complexity)
	}
	descStr := ""
	if plan.Description != nil {
		descStr = *plan.Description
	}

	responseText := fmt.Sprintf(`Current Plan:

ID: %s
Title: %s
Complexity: %s
Status: %s
Created: %s
Updated: %s

Description:
%s`,
		plan.ID,
		plan.Title,
		complexityStr,
		planStatusToString(plan.Status),
		plan.CreatedAt.Format("2006-01-02 15:04:05"),
		plan.UpdatedAt.Format("2006-01-02 15:04:05"),
		descStr,
	)

	if plan.CompletedAt != nil {
		responseText += fmt.Sprintf("\nCompleted: %s", plan.CompletedAt.Format("2006-01-02 15:04:05"))
	}

	if resolved.inherited {
		responseText += fmt.Sprintf(
			"\n\nThis plan belongs to an ancestor thread (%s) — you are reading the board you were spawned from, and cannot modify it.",
			resolved.ownerThreadID)
	}

	// Add task progress summary
	stats, err := g.repo.GetTaskStatsByPlan(rctx.Context, plan.ID)
	if err == nil && stats.Total > 0 {
		responseText += fmt.Sprintf("\n\nProgress: %d/%d tasks completed", stats.Completed, stats.Total)
		parts := []string{}
		if stats.Pending > 0 {
			parts = append(parts, fmt.Sprintf("%d pending", stats.Pending))
		}
		if stats.InProgress > 0 {
			parts = append(parts, fmt.Sprintf("%d in progress", stats.InProgress))
		}
		if stats.Failed > 0 {
			parts = append(parts, fmt.Sprintf("%d failed", stats.Failed))
		}
		if stats.Blocked > 0 {
			parts = append(parts, fmt.Sprintf("%d blocked", stats.Blocked))
		}
		if stats.Cancelled > 0 {
			parts = append(parts, fmt.Sprintf("%d cancelled", stats.Cancelled))
		}
		if stats.Skipped > 0 {
			parts = append(parts, fmt.Sprintf("%d skipped", stats.Skipped))
		}
		if len(parts) > 0 {
			responseText += fmt.Sprintf(" (%s)", strings.Join(parts, ", "))
		}
	}

	return NewTextResponse(responseText), nil
}
