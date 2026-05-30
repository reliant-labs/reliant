// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"fmt"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
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

	// RouterDecision is set when this context was created by a router node.
	// Contains the routing decision metadata for display in the UI.
	RouterDecision *RouterDecisionMeta

	// ProjectPath is the working directory for this execution.
	// All tools, preset loading, and nested child workflows operate within this directory.
	// Set via the project.path configuration on workflow/loop nodes.
	// Empty string means "use default" (typically the repository root).
	ProjectPath string

	// DaemonSelector specifies which daemon should execute tools for this workflow.
	// Set from the workflow-level daemon field. Can be overridden per-node.
	// nil means use default daemon resolution (local → cloud → wake).
	DaemonSelector *DaemonSelectorValue

	// Loop context - set when executing inside a loop
	// nil if not in a loop
	Loop *ExecLoopContext

	// Spawn depth - tracks how deep in the spawn chain this workflow is.
	// 0 = top-level (not spawned), 1 = spawned child, 2 = grandchild, etc.
	SpawnDepth int

	// ParentPermission is the permission level of the parent workflow that spawned this one.
	// The child's resolved permission is capped to be at most this permissive.
	// Empty means no constraint (root workflow).
	ParentPermission string

	// Parent context - set for child workflows
	// nil if this is a root workflow
	Parent *ParentContext

	// UserJWT is the user's bearer token captured at workflow start. Propagated
	// into activity inputs so worker processes can hydrate auth.SetUserJWT and
	// resolve the Reliant driver. See activities/types/runtime_context.go for
	// the lifecycle/security trade-off.
	UserJWT string
}

// DaemonSelectorValue holds a resolved daemon selector for runtime routing.
type DaemonSelectorValue struct {
	ID     string            `json:"id,omitempty"`
	Name   string            `json:"name,omitempty"`
	Type   string            `json:"type,omitempty"` // "local", "cloud", "any"
	Labels map[string]string `json:"labels,omitempty"`
}

// RouterDecisionMeta holds routing decision metadata for display in the UI.
type RouterDecisionMeta struct {
	Workflow string `json:"workflow"` // Selected workflow ref (e.g., "agent")
	Preset   string `json:"preset"`   // Selected preset (e.g., "code-review")
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

// WithDaemonSelector sets the daemon selector for this execution context.
// When set, tool execution routes to the specified daemon.
func (ctx *ExecutionContext) WithDaemonSelector(ds *DaemonSelectorValue) *ExecutionContext {
	ctx.DaemonSelector = ds
	return ctx
}

// HasDaemonSelector returns true if this context has a daemon selector set.
func (ctx *ExecutionContext) HasDaemonSelector() bool {
	return ctx.DaemonSelector != nil
}

// ResolveDaemonSelectorProto converts a proto DaemonSelectorProto to a runtime DaemonSelectorValue.
func ResolveDaemonSelectorProto(ds *reliantv1.DaemonSelectorProto) *DaemonSelectorValue {
	if ds == nil {
		return nil
	}
	return &DaemonSelectorValue{
		ID:     ds.GetId(),
		Name:   ds.GetName(),
		Type:   ds.GetType(),
		Labels: ds.GetLabels(),
	}
}

// ResolveCelDaemonSelector evaluates a CelDaemonSelector and returns a DaemonSelectorValue.
// For literal values, returns directly. For CEL expressions, evaluates against the given context.
func ResolveCelDaemonSelector(cds *reliantv1.CelDaemonSelector, celContext map[string]interface{}) (*DaemonSelectorValue, error) {
	if cds == nil {
		return nil, nil
	}
	switch v := cds.Value.(type) {
	case *reliantv1.CelDaemonSelector_Literal:
		return ResolveDaemonSelectorProto(v.Literal), nil
	case *reliantv1.CelDaemonSelector_Expr:
		result, err := evaluateCELTemplate(v.Expr, celContext)
		if err != nil {
			return nil, fmt.Errorf("evaluating daemon selector expression: %w", err)
		}
		return daemonSelectorFromCELResult(result)
	default:
		return nil, nil
	}
}

// buildWorkflowCELContext creates a minimal CEL context for evaluating workflow-level fields.
// Used for daemon selector evaluation before the main execution loop starts.
func buildWorkflowCELContext(workflowID, workflowName string, inputs map[string]interface{}, nodeOutputs map[string]interface{}) map[string]interface{} {
	if nodeOutputs == nil {
		nodeOutputs = make(map[string]interface{})
	}
	ctx := make(map[string]interface{})
	ctx["inputs"] = inputs
	ctx["workflow"] = map[string]interface{}{
		"id":   workflowID,
		"name": workflowName,
	}
	ctx["nodes"] = nodeOutputs
	ctx["iter"] = map[string]interface{}{"iteration": 0, "index": 0}
	return ctx
}

// daemonSelectorFromCELResult converts a CEL evaluation result to a DaemonSelectorValue.
// Supports string results (shorthand) and map results (structured).
func daemonSelectorFromCELResult(result interface{}) (*DaemonSelectorValue, error) {
	if result == nil {
		return nil, nil
	}
	switch v := result.(type) {
	case string:
		if v == "" {
			return nil, nil
		}
		// String shorthand: known types map to type field, others to name
		switch v {
		case "local", "cloud", "any":
			return &DaemonSelectorValue{Type: v}, nil
		default:
			return &DaemonSelectorValue{Name: v}, nil
		}
	case map[string]interface{}:
		ds := &DaemonSelectorValue{}
		if id, ok := v["id"].(string); ok {
			ds.ID = id
		}
		if name, ok := v["name"].(string); ok {
			ds.Name = name
		}
		if typ, ok := v["type"].(string); ok {
			ds.Type = typ
		}
		if labels, ok := v["labels"].(map[string]interface{}); ok {
			ds.Labels = make(map[string]string)
			for k, lv := range labels {
				if s, ok := lv.(string); ok {
					ds.Labels[k] = s
				}
			}
		}
		return ds, nil
	default:
		return nil, fmt.Errorf("daemon selector CEL expression must return string or map, got %T", result)
	}
}

// ForIteration creates a derived context for a loop iteration.
// When reuseThread is true, all iterations share the parent's thread.
// When reuseThread is false, each iteration gets a unique deterministic thread.
func (ctx *ExecutionContext) ForIteration(iteration int, reuseThread bool) *ExecutionContext {
	child := &ExecutionContext{
		WorkflowID:     ctx.WorkflowID,
		ChatID:         ctx.ChatID,
		WorkflowName:   ctx.WorkflowName,
		ThreadMode:     ctx.ThreadMode,
		ForkedFrom:     ctx.ForkedFrom,
		ParentThread:   ctx.ParentThread,   // Preserve parent thread chain
		ProjectPath:    ctx.ProjectPath,    // Inherit project path (can be overridden by loop's project config)
		DaemonSelector: ctx.DaemonSelector, // Inherit daemon selector
		SpawnDepth:       ctx.SpawnDepth,       // Inherit spawn depth (iterations don't increase depth)
		ParentPermission: ctx.ParentPermission, // Inherit parent permission cap
		Parent:           ctx.Parent,
		UserJWT:          ctx.UserJWT, // Reliant provider is JWT-gated; dropping it here would exclude it from availableProviders inside the loop.
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
		WorkflowID:     ctx.WorkflowID, // Same workflow ID for inline execution
		ChatID:         ctx.ChatID,
		WorkflowName:   workflowName,
		ThreadMode:     mode,
		ParentThread:   ctx.Thread,         // Always track parent's thread for save_message
		ProjectPath:    ctx.ProjectPath,    // Inherit project path (can be overridden by node's project config)
		DaemonSelector: ctx.DaemonSelector, // Inherit daemon selector (can be overridden by node's daemon field)
		SpawnDepth:       ctx.SpawnDepth,       // Inherit spawn depth (inline children don't increase depth)
		ParentPermission: ctx.ParentPermission, // Inherit parent permission cap
		Loop:             ctx.Loop,             // Inherit loop context
		UserJWT:          ctx.UserJWT,          // Reliant provider is JWT-gated; child contexts must carry it.
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
		WorkflowID:     ctx.WorkflowID,
		ChatID:         ctx.ChatID,
		WorkflowName:   ctx.WorkflowName,
		Thread:         ctx.Thread,
		ThreadMode:     ctx.ThreadMode,
		ThreadTitle:    ctx.ThreadTitle,
		ForkedFrom:     ctx.ForkedFrom,
		ParentThread:   ctx.ParentThread,
		ProjectPath:    ctx.ProjectPath,
		DaemonSelector: ctx.DaemonSelector,
		UserJWT:        ctx.UserJWT, // Reliant provider is JWT-gated; cloned contexts must carry it.
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