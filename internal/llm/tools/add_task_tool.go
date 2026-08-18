// Copyright (c) 2025 Reliant Labs
//
// forge:exclude-contract
//
// Registry/lookup-table package: the exported vars are populated once at init
// by the packages that register into them, then read. A getter returns the
// same map or slice header, so it moves the mutation surface without
// narrowing it.
package tools

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// AddTaskTool allows agents to add new tasks to an existing plan
type AddTaskTool struct {
	repo db.Repository
}

// NewAddTaskTool creates a new task addition tool
func NewAddTaskTool(repo db.Repository) Tool {
	return NewToolWrapper(&AddTaskTool{
		repo: repo,
	})
}

func (a *AddTaskTool) Name() string {
	return "add_task"
}

func (a *AddTaskTool) Description() string {
	return `Add a new task to the current plan. This is your primary tool for dynamic planning and sub-planning.

WHEN TO USE:
- When you discover additional work that needs to be done
- When you encounter missing dependencies or prerequisites
- When breaking down complex work into more steps
- When pivoting approach requires new tasks

SUB-PLANNING WITH parent_id:
- Use parent_id to create subtasks under an existing task when you discover complexity
- Break down tasks that prove more complex than initially planned
- Create hierarchical task structures for better organization
- Each subtask inherits context from parent but can have specialized metadata

COMPLEXITY DISCOVERY PATTERNS:
- Implementation agent finds task needs research → add subtask with preferred_agent: "research"
- Research agent discovers multiple integration points → add subtasks for each integration
- Any agent finds unfamiliar tech/patterns → add subtask with tool_hints: ["search_first", "use_subagent"]
- Task requires multiple phases → add sequential subtasks with position ordering

METADATA OPTIONS:
- preferred_agent: Which agent should handle this (planning/research/implementation/debugging/tdd/finalize)
- tool_hints: Suggested tools to use ["use_bash", "use_subagent", "search_first", "test_first"]
- dependencies: What this task depends on (packages, files, other tasks)
- notes: Important context or discoveries
- priority: high/medium/low

SUB-PLANNING EXAMPLES:
1. Complex Implementation Discovery:
   parent_id: "task_123", title: "Research authentication patterns", 
   preferred_agent: "research", notes: "Found unfamiliar OAuth flow"

2. Multi-Step Breakdown:
   parent_id: "task_456", title: "Setup database schema", position: 1
   parent_id: "task_456", title: "Create migration scripts", position: 2

3. Cross-Agent Coordination:
   parent_id: "task_789", title: "Write integration tests", 
   preferred_agent: "tdd", dependencies: ["API endpoints complete"]

BEST PRACTICES:
- Add tasks as soon as you discover they're needed
- Use parent_id when expanding existing tasks that prove complex
- Include metadata hints for better execution
- Position subtasks logically in sequence
- Use descriptive titles and comprehensive descriptions
- Create subtasks for different agent specializations when needed`
}

// TaskMetadata represents structured metadata for a task
type TaskMetadata struct {
	PreferredAgent string   `json:"preferred_agent,omitempty"`
	ToolHints      []string `json:"tool_hints,omitempty"`
	Dependencies   []string `json:"dependencies,omitempty"`
	Notes          string   `json:"notes,omitempty"`
	Priority       string   `json:"priority,omitempty"`
	// Context was removed because OpenAI Responses function schema validation requires
	// closed JSON schemas (additionalProperties=false) and required keys to match
	// properties exactly. An unstructured map breaks these constraints.
}

// AddTaskParams represents the parameters for adding a task
type AddTaskParams struct {
	Title       string       `json:"title"`
	Description string       `json:"description,omitempty"`
	Position    int          `json:"position,omitempty"`  // Optional: adds at end if not specified
	ParentID    string       `json:"parent_id,omitempty"` // Optional: for creating subtasks
	Metadata    TaskMetadata `json:"metadata,omitempty"`
}

func (a *AddTaskTool) RequiresPermission(params AddTaskParams) (bool, error) {
	// add_task tool doesn't require permissions as it only modifies in-memory task state
	return false, nil
}

func (a *AddTaskTool) Execute(rctx *rctx.ToolContext, params AddTaskParams) (ToolResponse, error) {
	if a.repo == nil {
		return NewTextErrorResponse("This tool requires a database connection and is not available in daemon-only mode"), nil
	}

	// Validate parameters
	if params.Title == "" {
		return NewTextErrorResponse("Task title is required"), nil
	}

	// Get thread ID from context
	threadID := rctx.Thread
	if threadID == "" {
		return NewTextErrorResponse("No thread context available"), nil
	}

	// Writes bind to THIS thread's plan only. An ancestor's plan is readable
	// (list_tasks/get_plan walk up) but has a single writer, so say so rather
	// than advising create_plan — that advice would fragment the parent's
	// board into private per-sub-agent copies.
	plan, err := resolvePlanForWrite(rctx.Context, a.repo, threadID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			if inherited, resolveErr := resolvePlanForRead(rctx.Context, a.repo, threadID); resolveErr == nil && inherited.inherited {
				return NewTextErrorResponse(inheritedPlanWriteRefusal(inherited.ownerThreadID)), nil
			}
			return NewTextErrorResponse("No plan found for this thread. Use create_plan to create one."), nil
		}
		return NewTextErrorResponse(fmt.Sprintf("Failed to find plan: %v", err)), nil
	}

	// Check if plan is still active
	if plan.Status == int32(db.PlanStatusCompleted) || plan.Status == int32(db.PlanStatusCancelled) {
		return NewTextErrorResponse("Cannot add tasks to a completed or cancelled plan"), nil
	}

	// Create the task
	var parentTaskID *string
	if params.ParentID != "" {
		parentTaskID = &params.ParentID
	}
	desc := params.Description

	// Serialize metadata to JSON if any fields are set
	var metadataPtr *string
	if params.Metadata.PreferredAgent != "" || len(params.Metadata.ToolHints) > 0 ||
		len(params.Metadata.Dependencies) > 0 || params.Metadata.Notes != "" || params.Metadata.Priority != "" {
		if metaJSON, err := json.Marshal(params.Metadata); err == nil {
			s := string(metaJSON)
			metadataPtr = &s
		}
	}

	task := &db.Task{
		ID:           uuid.New().String(),
		PlanID:       plan.ID,
		ParentTaskID: parentTaskID,
		Title:        params.Title,
		Description:  &desc,
		Status:       int32(db.TaskStatusPending),
		Position:     params.Position,
		Metadata:     metadataPtr,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	if err := a.repo.CreateTask(rctx.Context, task); err != nil {
		logging.Error("Failed to add task", "error", err)
		return NewTextErrorResponse(fmt.Sprintf("Failed to add task: %v", err)), nil
	}

	// Format response
	descStr := ""
	if task.Description != nil {
		descStr = *task.Description
	}

	responseText := fmt.Sprintf(`Task added successfully!

ID: %s
Plan: %s
Title: %s
Status: %d`,
		task.ID,
		plan.Title,
		task.Title,
		task.Status,
	)

	if descStr != "" {
		responseText += fmt.Sprintf("\nDescription: %s", descStr)
	}

	if params.ParentID != "" {
		responseText += fmt.Sprintf("\nParent Task ID: %s", params.ParentID)
	}

	if params.Metadata.PreferredAgent != "" {
		responseText += fmt.Sprintf("\nPreferred Agent: %s", params.Metadata.PreferredAgent)
	}

	if len(params.Metadata.ToolHints) > 0 {
		responseText += fmt.Sprintf("\nTool Hints: %v", params.Metadata.ToolHints)
	}

	if len(params.Metadata.Dependencies) > 0 {
		responseText += fmt.Sprintf("\nDependencies: %v", params.Metadata.Dependencies)
	}

	if params.Metadata.Notes != "" {
		responseText += fmt.Sprintf("\nNotes: %s", params.Metadata.Notes)
	}

	if err := a.repo.EmitChatRefetch(rctx.Context, rctx.ChatID, db.RefetchPlanTasks); err != nil {
		logging.Warn("[AddTaskTool] Failed to emit plan_tasks refetch", "error", err)
	}

	logging.Info("Task added to plan",
		"task_id", task.ID,
		"plan_id", plan.ID,
		"title", task.Title)

	return NewTextResponse(responseText), nil
}
