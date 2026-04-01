// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"fmt"

	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// ExecutionContext is the single source of truth for workflow execution state.
// It replaces scattered context maps and provides unified access via thread.* namespace
// with explicit, typed fields.
//
// Key design principles:
// - Thread is a first-class field, not derived from maps
// - Passed to all executors and activities
// - Child contexts derived via ForChild() and ForIteration()
// - Config (model, temperature) is separate from execution state
type ExecutionContext struct {
	// Identity
	WorkflowID   string
	ChatID       string
	WorkflowName string

	// Thread - THE authoritative thread for this execution
	// This is the single source of truth - no lookups needed
	Thread       string
	ThreadMode   string // "new", "inherit", "fork" — uses model.ThreadMode* constants
	ThreadTitle  string // Human-readable title for the thread (e.g., preset name or node ID)
	ForkedFrom   string // If mode=fork, the parent thread we copied context from
	ParentThread string // The parent's thread (used for mode=inherit and mode=fork resolution)

	// ProjectPath is the working directory for this execution.
	// All tools, preset loading, and nested child workflows operate within this directory.
	// Set via the project.path configuration on workflow/loop nodes.
	// Empty string means "use default" (typically the repository root).
	ProjectPath string

	// Loop context - set when executing inside a loop
	// nil if not in a loop
	Loop *ExecLoopContext

	// Parent context - set for child workflows
	// nil if this is a root workflow
	Parent *ParentContext
}

// ExecLoopContext tracks loop execution state at runtime.
type ExecLoopContext struct {
	NodeID    string // The loop node ID (e.g., "agent_loop")
	Iteration int    // Current iteration (0-indexed)
}

// ParentContext tracks parent workflow information for child workflows.
type ParentContext struct {
	WorkflowID string // Parent's workflow ID
	StepPath   string // Which step spawned this child (e.g., "agent_loop")
}

// NewExecutionContext creates a root execution context for a new workflow.
func NewExecutionContext(workflowID, chatID, workflowName, thread string) *ExecutionContext {
	return &ExecutionContext{
		WorkflowID:   workflowID,
		ChatID:       chatID,
		WorkflowName: workflowName,
		Thread:       thread,
		ThreadMode:   model.ThreadModeNew,
	}
}

// WithThreadMode sets the thread mode and optional forked-from thread.
func (ctx *ExecutionContext) WithThreadMode(mode string, forkedFrom string) *ExecutionContext {
	ctx.ThreadMode = mode
	ctx.ForkedFrom = forkedFrom
	return ctx
}

// WithThreadTitle sets the human-readable title for the thread.
func (ctx *ExecutionContext) WithThreadTitle(title string) *ExecutionContext {
	ctx.ThreadTitle = title
	return ctx
}

// WithParent sets the parent workflow context.
func (ctx *ExecutionContext) WithParent(parentWorkflowID, stepPath string) *ExecutionContext {
	ctx.Parent = &ParentContext{
		WorkflowID: parentWorkflowID,
		StepPath:   stepPath,
	}
	return ctx
}

// WithLoop sets the loop context.
func (ctx *ExecutionContext) WithLoop(nodeID string, iteration int) *ExecutionContext {
	ctx.Loop = &ExecLoopContext{
		NodeID:    nodeID,
		Iteration: iteration,
	}
	return ctx
}

// WithProjectPath sets the project path for this execution context.
// When set, all tools and preset loading use this directory.
func (ctx *ExecutionContext) WithProjectPath(projectPath string) *ExecutionContext {
	ctx.ProjectPath = projectPath
	return ctx
}

// ForIteration creates a derived context for a loop iteration.
// When reuseThread is true, all iterations share the parent's thread.
// When reuseThread is false, each iteration gets a unique deterministic thread.
func (ctx *ExecutionContext) ForIteration(iteration int, reuseThread bool) *ExecutionContext {
	child := &ExecutionContext{
		WorkflowID:   ctx.WorkflowID,
		ChatID:       ctx.ChatID,
		WorkflowName: ctx.WorkflowName,
		ThreadMode:   ctx.ThreadMode,
		ForkedFrom:   ctx.ForkedFrom,
		ParentThread: ctx.ParentThread, // Preserve parent thread chain
		ProjectPath:  ctx.ProjectPath,  // Inherit project path (can be overridden by loop's project config)
		Parent:       ctx.Parent,
	}

	if reuseThread {
		child.Thread = ctx.Thread
	} else {
		child.Thread = DeterministicThread(ctx.WorkflowID, fmt.Sprintf("%s:iter:%d", ctx.Thread, iteration))
	}

	// Set loop context
	if ctx.Loop != nil {
		child.Loop = &ExecLoopContext{
			NodeID:    ctx.Loop.NodeID,
			Iteration: iteration,
		}
	}

	return child
}

// ForChild creates a derived context for a child workflow/inline execution.
// Thread resolution:
// - mode: inherit → use parent's thread
// - mode: own → new deterministic thread
// - mode: fork → new deterministic thread (caller should copy context from ForkedFrom)
func (ctx *ExecutionContext) ForChild(stepID string, mode string, workflowName string, memo bool) *ExecutionContext {
	child := &ExecutionContext{
		WorkflowID:   ctx.WorkflowID, // Same workflow ID for inline execution
		ChatID:       ctx.ChatID,
		WorkflowName: workflowName,
		ThreadMode:   mode,
		ParentThread: ctx.Thread,      // Always track parent's thread for save_message
		ProjectPath:  ctx.ProjectPath, // Inherit project path (can be overridden by node's project config)
		Loop:         ctx.Loop,        // Inherit loop context
		Parent: &ParentContext{
			WorkflowID: ctx.WorkflowID,
			StepPath:   stepID,
		},
	}

	// Resolve thread based on mode
	switch mode {
	case model.ThreadModeInherit:
		child.Thread = ctx.Thread
	case model.ThreadModeFork:
		key := fmt.Sprintf("%s:fork:%s", stepID, ctx.Thread)
		if !memo && ctx.Loop != nil {
			key = fmt.Sprintf("%s:fork:%s:iter:%d", stepID, ctx.Thread, ctx.Loop.Iteration)
		}
		child.Thread = DeterministicThread(ctx.WorkflowID, key)
		child.ForkedFrom = ctx.Thread
	case model.ThreadModeNew:
		key := fmt.Sprintf("%s:own", stepID)
		if !memo && ctx.Loop != nil {
			key = fmt.Sprintf("%s:own:iter:%d", stepID, ctx.Loop.Iteration)
		}
		child.Thread = DeterministicThread(ctx.WorkflowID, key)
	default:
		// Default to inherit
		child.Thread = ctx.Thread
	}

	return child
}

// ForChildWorkflow creates a context for a spawned child workflow (separate workflow ID).
func (ctx *ExecutionContext) ForChildWorkflow(childWorkflowID, stepID string, mode string, workflowName string, memo bool) *ExecutionContext {
	child := ctx.ForChild(stepID, mode, workflowName, memo)
	child.WorkflowID = childWorkflowID
	child.Parent = &ParentContext{
		WorkflowID: ctx.WorkflowID,
		StepPath:   stepID,
	}
	return child
}

// IsInLoop returns true if this context is executing inside a loop.
func (ctx *ExecutionContext) IsInLoop() bool {
	return ctx.Loop != nil
}

// IsChildWorkflow returns true if this is a child workflow (has a parent).
func (ctx *ExecutionContext) IsChildWorkflow() bool {
	return ctx.Parent != nil
}

// HasProjectPath returns true if this context has a project path set.
func (ctx *ExecutionContext) HasProjectPath() bool {
	return ctx.ProjectPath != ""
}

// Clone creates a deep copy of the execution context.
func (ctx *ExecutionContext) Clone() *ExecutionContext {
	clone := &ExecutionContext{
		WorkflowID:   ctx.WorkflowID,
		ChatID:       ctx.ChatID,
		WorkflowName: ctx.WorkflowName,
		Thread:       ctx.Thread,
		ThreadMode:   ctx.ThreadMode,
		ThreadTitle:  ctx.ThreadTitle,
		ForkedFrom:   ctx.ForkedFrom,
		ParentThread: ctx.ParentThread,
		ProjectPath:  ctx.ProjectPath,
	}

	if ctx.Loop != nil {
		clone.Loop = &ExecLoopContext{
			NodeID:    ctx.Loop.NodeID,
			Iteration: ctx.Loop.Iteration,
		}
	}

	if ctx.Parent != nil {
		clone.Parent = &ParentContext{
			WorkflowID: ctx.Parent.WorkflowID,
			StepPath:   ctx.Parent.StepPath,
		}
	}

	return clone
}
