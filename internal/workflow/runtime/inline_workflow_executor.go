// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/core"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// InlineWorkflowExecutor manages inline sub-workflow execution within the parent workflow.
// Instead of spawning a Temporal child workflow, it:
// - Loads the sub-workflow definition
// - Executes sub-workflow nodes inline
// - Shares the parent's Temporal workflow context
// - Returns the sub-workflow's outputs
//
// This is similar to InlineLoopExecutor but for single-execution workflow: nodes.
type InlineWorkflowExecutor struct {
	ctx            workflow.Context
	workflowID     string
	chatID         string
	workflowName   string
	workflowInputs map[string]interface{}
	nodeOutputs    map[string]interface{}
	childTracker   *ChildWorkflowTracker
	logger         log.Logger

	// Inline workflow-specific state
	nodeID               string                    // The workflow: node ID being executed
	node                 *reliantv1.Node           // The workflow: node being executed (original, pre-evaluation)
	evalResult           *reliantv1.Node           // The workflow: node after CEL evaluation (resolved CelStrings)
	subWorkflowName      string                    // Name/identity of the sub-workflow to execute
	subWorkflowInputs    map[string]interface{}    // Evaluated inputs for the sub-workflow
	subWorkflow          *reliantv1.Workflow       // Loaded sub-workflow definition
	subWorkflowSemantics *RuntimeSemantics         // Core semantics for nested workflow/loop nodes in subWorkflow
	invocationContract   *core.SubWorkflowContract // Core contract for this node invocation
	loopNodeID           string                    // Parent loop node ID (if executing within a loop)
	loopIteration        int                       // Parent loop iteration (if executing within a loop)

	// threadTracker tracks threads for runtime thread mapping
	threadTracker *ThreadTracker

	// execContext is the unified execution context (thread, message, loop, parent).
	// Required: This is the source of truth for execution state.
	execContext *ExecutionContext

	// projectPath for loading presets in spawned workflows
	projectPath string

	// pauseCtrl bundles pause-checking and cancellable-context callbacks.
	pauseCtrl *PauseController

	// makeThreadPauseCtrl creates per-thread PauseControllers for pause-aware execution.
	// Propagated from the root workflow to support nested spawn tool calls.
	makeThreadPauseCtrl func(string) *PauseController
}

// NewInlineWorkflowExecutor creates a new executor for inline workflow execution.
func NewInlineWorkflowExecutor(
	ctx workflow.Context,
	workflowID string,
	chatID string,
	workflowName string,
	workflowInputs map[string]interface{},
	nodeOutputs map[string]interface{},
	childTracker *ChildWorkflowTracker,
	node *reliantv1.Node,
	evalResult *reliantv1.Node,
	loopNodeID string,
	loopIteration int,
) (*InlineWorkflowExecutor, error) {
	subWorkflowName := model.NodeRef(evalResult)
	if subWorkflowName == "" {
		if wfArgs := model.GetSubWorkflowArgs(node); wfArgs != nil && wfArgs.GetInline() != nil {
			// Inline nodes inherit parent workflow identity by contract.
			subWorkflowName = workflowName
		} else {
			return nil, fmt.Errorf("workflow node %s has empty workflow ref", node.GetId())
		}
	}

	// Build sub-workflow inputs from resolved args + sub-workflow args
	subInputs := model.NodeMergedSubWorkflowInputs(evalResult)

	return &InlineWorkflowExecutor{
		ctx:               ctx,
		workflowID:        workflowID,
		chatID:            chatID,
		workflowName:      workflowName,
		workflowInputs:    workflowInputs,
		nodeOutputs:       nodeOutputs,
		childTracker:      childTracker,
		logger:            workflow.GetLogger(ctx),
		nodeID:            node.GetId(),
		node:              node,
		evalResult:        evalResult,
		subWorkflowName:   subWorkflowName,
		subWorkflowInputs: subInputs,
		loopNodeID:        loopNodeID,
		loopIteration:     loopIteration,
	}, nil
}

// WithThreadTracker sets the thread tracker for recording thread resolutions.
func (e *InlineWorkflowExecutor) WithThreadTracker(tracker *ThreadTracker) *InlineWorkflowExecutor {
	e.threadTracker = tracker
	return e
}

// WithExecContext sets the unified execution context.
// Required: This is the source of truth for execution state.
func (e *InlineWorkflowExecutor) WithExecContext(ctx *ExecutionContext) *InlineWorkflowExecutor {
	e.execContext = ctx
	return e
}

// WithProjectPath sets the project path for loading presets in spawned workflows.
func (e *InlineWorkflowExecutor) WithProjectPath(projectPath string) *InlineWorkflowExecutor {
	e.projectPath = projectPath
	return e
}

// WithPauseController sets the PauseController for pause-aware execution.
func (e *InlineWorkflowExecutor) WithPauseController(pc *PauseController) *InlineWorkflowExecutor {
	e.pauseCtrl = pc
	return e
}

// WithMakeThreadPauseCtrl sets the per-thread PauseController factory for spawn support.
func (e *InlineWorkflowExecutor) WithMakeThreadPauseCtrl(fn func(string) *PauseController) *InlineWorkflowExecutor {
	e.makeThreadPauseCtrl = fn
	return e
}

// WithInvocationContract sets the core semantic contract for this sub-workflow invocation.
func (e *InlineWorkflowExecutor) WithInvocationContract(contract core.SubWorkflowContract) *InlineWorkflowExecutor {
	e.invocationContract = &contract
	// Only override the sub-workflow name when the contract identity is a resolved
	// literal (not a template like "{{inputs.some_ref}}"). Template refs are
	// resolved at runtime by EvaluateNodeConfig and stored in subWorkflowName before
	// this method is called; overwriting with the unresolved template would cause
	// ActivityLoadWorkflow to fail with "workflow not found".
	if contract.WorkflowIdentity != "" && !strings.Contains(contract.WorkflowIdentity, "{{") {
		e.subWorkflowName = contract.WorkflowIdentity
	}
	return e
}

// WithWorkflowContext overrides the workflow context.
// This is required when running inside workflow.Go() goroutines to avoid
// "trying to block on coroutine which is already blocked" errors.
// Each goroutine must use its own context for blocking operations.
func (e *InlineWorkflowExecutor) WithWorkflowContext(ctx workflow.Context) *InlineWorkflowExecutor {
	e.ctx = ctx
	e.logger = workflow.GetLogger(ctx)
	return e
}

// GetThread returns the current thread from execContext.
func (e *InlineWorkflowExecutor) GetThread() string {
	if e.execContext != nil {
		return e.execContext.Thread
	}
	return ""
}

// loadAndMergePresets loads presets specified on the node and merges their params into subInputs.
// Presets are merged as a base layer - explicit args will override these values later.
//
// Preset names may contain CEL {{...}} template expressions which are evaluated
// against the current workflow inputs + node outputs before loading.
//
// The presets map can target:
// - "default": params are applied directly to top-level inputs
// - "GroupName": params are applied as "GroupName.param" keys
func (e *InlineWorkflowExecutor) loadAndMergePresets(subInputs map[string]interface{}) error {
	if e.projectPath == "" {
		return fmt.Errorf("project path not set, cannot load presets")
	}

	wfArgs := model.GetSubWorkflowArgs(e.node)
	presets := wfArgs.GetPresets()
	if len(presets) == 0 {
		return nil
	}

	e.logger.Info("[InlineWorkflow] Loading presets for sub-workflow",
		"nodeID", e.nodeID,
		"presets", presets,
	)

	evalCtx := &wfcel.EdgeEvalContext{
		Nodes:  e.nodeOutputs,
		Inputs: e.workflowInputs,
	}
	return applyPresets(presets, subInputs, evalCtx, e.loadPresetParams, e.logger, e.nodeID)
}

// presetLoaderFunc loads a preset's params by name. Extracted for testability.
type presetLoaderFunc func(presetName string) (map[string]interface{}, error)

// applyPresets resolves CEL templates in preset names, loads each preset via the
// given loader, and merges the resulting params into subInputs. This is a pure
// helper (no Temporal dependency) so it can be unit-tested with a fake loader.
func applyPresets(
	presets map[string]string,
	subInputs map[string]interface{},
	evalCtx *wfcel.EdgeEvalContext,
	loader presetLoaderFunc,
	logger log.Logger,
	nodeID string,
) error {
	// Sort preset group names for deterministic activity scheduling order.
	// Map iteration order is non-deterministic, which would cause different
	// activity scheduling order on replay, triggering non-determinism errors.
	groupNames := make([]string, 0, len(presets))
	for groupName := range presets {
		groupNames = append(groupNames, groupName)
	}
	sort.Strings(groupNames)

	for _, groupName := range groupNames {
		rawPresetName := presets[groupName]
		if rawPresetName == "" {
			continue
		}

		resolvedPresetName, err := ResolvePresetName(rawPresetName, evalCtx)
		if err != nil {
			// Match existing "failed to load preset → skip" pattern.
			logger.Warn("[InlineWorkflow] Failed to resolve preset template",
				"nodeID", nodeID,
				"group", groupName,
				"template", rawPresetName,
				"error", err,
			)
			continue
		}

		if resolvedPresetName != rawPresetName {
			logger.Info("[InlineWorkflow] Resolved preset template",
				"nodeID", nodeID,
				"group", groupName,
				"template", rawPresetName,
				"resolved", resolvedPresetName,
			)
		}

		if resolvedPresetName == "" {
			logger.Info("[InlineWorkflow] Skipping empty preset name after template evaluation",
				"nodeID", nodeID,
				"group", groupName,
				"template", rawPresetName,
			)
			continue
		}

		params, err := loader(resolvedPresetName)
		if err != nil {
			logger.Warn("[InlineWorkflow] Failed to load preset",
				"nodeID", nodeID,
				"preset", resolvedPresetName,
				"group", groupName,
				"error", err,
			)
			continue // Skip failed presets, don't fail the whole workflow
		}

		mergePresetParams(subInputs, groupName, params)

		logger.Info("[InlineWorkflow] Applied preset params",
			"nodeID", nodeID,
			"preset", resolvedPresetName,
			"group", groupName,
			"paramCount", len(params),
		)
	}

	return nil
}

// mergePresetParams merges a single preset's params into subInputs under the
// given group. The default group is flattened onto the top-level; named groups
// are nested under subInputs[groupName].
func mergePresetParams(subInputs map[string]interface{}, groupName string, params map[string]interface{}) {
	if groupName == DefaultPresetGroup {
		for paramName, paramValue := range params {
			subInputs[paramName] = paramValue
		}
		return
	}
	groupMap, _ := subInputs[groupName].(map[string]interface{})
	if groupMap == nil {
		groupMap = make(map[string]interface{})
		subInputs[groupName] = groupMap
	}
	for paramName, paramValue := range params {
		groupMap[paramName] = paramValue
	}
}

// loadPresetParams loads a preset by name and returns its params.
// Uses the V2_LoadPresetParams activity to avoid import cycles.
func (e *InlineWorkflowExecutor) loadPresetParams(presetName string) (map[string]interface{}, error) {
	activityCtx := workflow.WithActivityOptions(e.ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    10 * time.Second,
			MaximumAttempts:    3,
		},
	})

	var params map[string]interface{}
	err := workflow.ExecuteActivity(activityCtx, "LoadPresetParams", map[string]interface{}{
		"project_path": e.projectPath,
		"preset_name":  presetName,
	}).Get(e.ctx, &params)

	if err != nil {
		return nil, err
	}

	return params, nil
}

func (e *InlineWorkflowExecutor) inputPolicy() core.InputPolicy {
	if e.invocationContract == nil {
		return core.InputPolicyRefPresetsArgsDefaults
	}
	return e.invocationContract.InputPolicy
}

func (e *InlineWorkflowExecutor) loadStrategy() core.LoadStrategy {
	if e.invocationContract == nil {
		return core.LoadStrategyLoadByWorkflowRef
	}
	return e.invocationContract.LoadStrategy
}

func (e *InlineWorkflowExecutor) compileSubWorkflowSemantics() error {
	if e.subWorkflow == nil {
		return fmt.Errorf("sub-workflow not loaded for node %s", e.nodeID)
	}
	semantics, err := CompileRuntimeSemantics(e.subWorkflow, e.subWorkflowName)
	if err != nil {
		return err
	}
	e.subWorkflowSemantics = semantics
	return nil
}

// buildSubWorkflowInputs assembles the input map for a sub-workflow. The result
// depends on the node's InputPolicy, which determines whether the sub-workflow
// is inlined or ref-based.
//
// InputPolicyInlineInheritParentInputs (inline nodes):
//
//	Returns the parent's workflowInputs map *by reference*. Because the pointer
//	is shared, any global signal update (e.g. user changes a param in the
//	toolbar) mutates the parent map and is visible to every inline child
//	immediately—no extra propagation path is needed. This is the correct
//	semantic: an inline sub-workflow IS the parent, just scoped to a subset of
//	nodes. Example: the `planning` node inside a one-ring workflow.
//
// InputPolicyRefPresetsArgsDefaults (ref-based nodes):
//
//	Builds a *new* map by layering presets → resolved args → schema defaults.
//	Args containing template expressions like `model: "{{inputs.model}}"` are
//	resolved once at node-start time. This map is then registered with the
//	ChildWorkflowTracker (see RegisterThreadInputs) so that the signal handler
//	in setupInputUpdateHandler can propagate global updates into it. Users can
//	also send thread-scoped param updates via the UI for per-thread overrides.
//	Example: a `plan` node that refs `builtin://agent`.
//
// Staleness gotcha for ref-based sub-workflows:
//
//	If a ref-based node resolves its args BEFORE a global signal arrives, those
//	resolved values are stale. The global-to-thread propagation in
//	setupInputUpdateHandler fixes this for values that are re-read at evaluation
//	time (e.g. conditions in loops), because it writes directly into the
//	registered thread input map. However, args that were already expanded into
//	concrete values during construction (e.g. a system_prompt template resolved
//	at node start) are NOT retroactively updated. For those, callers should use
//	thread-scoped param updates via the __thread signal key.
func (e *InlineWorkflowExecutor) buildSubWorkflowInputs() map[string]interface{} {
	if e.inputPolicy() == core.InputPolicyInlineInheritParentInputs {
		// Share the parent's map directly — NOT a copy.
		// See doc comment above for the full rationale.
		return e.workflowInputs
	}

	subInputs := make(map[string]interface{})
	if wfArgs := model.GetSubWorkflowArgs(e.node); wfArgs != nil && len(wfArgs.GetPresets()) > 0 {
		if err := e.loadAndMergePresets(subInputs); err != nil {
			e.logger.Warn("[InlineWorkflow] Failed to load presets, continuing without them",
				"nodeID", e.nodeID,
				"error", err,
			)
		}
	}

	// Passthrough: forward specified parent inputs to the child workflow.
	// Applied after presets so passthrough values override preset defaults,
	// but before explicit args so args always win.
	if passthrough := model.NodePassthrough(e.evalResult); len(passthrough) > 0 {
		for _, name := range passthrough {
			if val, ok := e.workflowInputs[name]; ok {
				subInputs[name] = val
			}
		}
	}

	for key, value := range e.subWorkflowInputs {
		subInputs[key] = value
	}

	if len(e.subWorkflow.GetInputs()) > 0 {
		subInputs = ApplyDefaultsForRuntime(subInputs, e.subWorkflow.GetInputs())
	}

	// Last, so nothing above can undo it: an unattended run stays unattended in
	// every child. See unattended.go for why this is enforced here instead of
	// being left to each YAML's passthrough list.
	propagateUnattended(e.workflowInputs, subInputs)

	return subInputs
}

// Execute runs the sub-workflow inline and returns its outputs.
// This is the main entry point - it handles the full sub-workflow lifecycle:
// 1. Emit thread_created event for threads owned by this node
// 2. Load sub-workflow definition
// 3. Auto-save trigger message if present in execContext
// 4. Execute sub-workflow nodes
// 5. Evaluate and return sub-workflow outputs
func (e *InlineWorkflowExecutor) Execute() (map[string]interface{}, error) {
	e.logger.Info("[InlineWorkflow] Starting inline workflow execution",
		"nodeID", e.nodeID,
		"subWorkflow", e.subWorkflowName,
		"hasExecContext", e.execContext != nil,
	)

	// Emit thread_created for all threads this node owns
	if e.execContext != nil {
		e.emitThreadCreated()
	}

	// Load sub-workflow definition
	if err := e.loadSubWorkflow(); err != nil {
		return nil, fmt.Errorf("failed to load sub-workflow %s: %w", e.subWorkflowName, err)
	}

	// Execute sub-workflow
	outputs, err := e.executeSubWorkflow()
	if err != nil {
		return nil, fmt.Errorf("sub-workflow %s failed: %w", e.subWorkflowName, err)
	}

	// Emit thread_completed for all threads this node owns
	if e.execContext != nil {
		e.emitThreadCompleted()
	}

	e.logger.Info("[InlineWorkflow] Inline workflow completed",
		"nodeID", e.nodeID,
		"subWorkflow", e.subWorkflowName,
		"outputKeys", getMapKeys(outputs),
	)

	return outputs, nil
}

// loadSubWorkflow loads the sub-workflow definition from the registry or from inline config based on core contract.
func (e *InlineWorkflowExecutor) loadSubWorkflow() error {
	if e.loadStrategy() == core.LoadStrategyInlineEmbedded {
		// Use evalResult (CEL-resolved node) to extract the inline workflow.
		// The original e.node still contains unresolved CelString templates
		// (e.g. ref: "{{inputs.some_ref}}") in nested nodes. The evalResult
		// has those templates resolved to concrete values.
		sourceNode := e.evalResult
		if sourceNode == nil {
			sourceNode = e.node
		}
		wfArgs := model.GetSubWorkflowArgs(sourceNode)
		if wfArgs == nil || wfArgs.GetInline() == nil {
			return fmt.Errorf("inline sub-workflow missing embedded workflow for node %s", e.nodeID)
		}
		wf := wfArgs.GetInline()
		if wf.GetName() == "" {
			wf.Name = e.subWorkflowName
		}
		e.subWorkflow = wf
		e.logger.Info("[InlineWorkflow] Using inline sub-workflow",
			"nodeID", e.nodeID,
			"workflowIdentity", e.subWorkflowName,
			"nodeCount", len(wf.GetNodes()),
		)
		return e.compileSubWorkflowSemantics()
	}

	activityCtx := workflow.WithActivityOptions(e.ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    30 * time.Second,
			MaximumAttempts:    3,
		},
	})

	workflowRef := e.subWorkflowName
	if e.invocationContract != nil && e.invocationContract.WorkflowRef != "" && !strings.Contains(e.invocationContract.WorkflowRef, "{{") {
		workflowRef = e.invocationContract.WorkflowRef
	}
	loadInput := map[string]string{
		"chat_id":       e.chatID,
		"workflow_name": workflowRef,
	}
	// LoadedWorkflow matches the output of ActivityLoadWorkflow (LoadWorkflowOutput in handlers)
	var loadedWf LoadedWorkflow
	if err := workflow.ExecuteActivity(activityCtx, "ActivityLoadWorkflow", loadInput).Get(e.ctx, &loadedWf); err != nil {
		return fmt.Errorf("activity load failed: %w", err)
	}

	wf, err := LoadWorkflow(loadedWf.WorkflowJSON)
	if err != nil {
		return fmt.Errorf("parse workflow: %w", err)
	}

	e.subWorkflow = wf
	e.logger.Info("[InlineWorkflow] Loaded sub-workflow",
		"nodeID", e.nodeID,
		"workflowIdentity", e.subWorkflowName,
		"workflowRef", workflowRef,
		"nodeCount", len(wf.Nodes),
	)
	return e.compileSubWorkflowSemantics()
}

// executeSubWorkflow runs all nodes in the sub-workflow.
// Returns the evaluated workflow outputs when complete.
func (e *InlineWorkflowExecutor) executeSubWorkflow() (map[string]interface{}, error) {
	// Create step outputs map for the sub-workflow (scoped to sub-workflow)
	subNodeOutputs := make(map[string]interface{})

	// Build unique activity ID base for this execution.
	// Use workflowID (unique per spawn) + nodeID + subWorkflowName.
	// Do NOT use workflow.Now() - parallel goroutines get the same time,
	// causing duplicate activity IDs.
	uniqueActivityIDBase := fmt.Sprintf("%s-%s-%s", e.workflowID, e.nodeID, e.subWorkflowName)

	// Get thread from execContext (required)
	subThread := e.GetThread()
	if subThread == "" {
		return nil, fmt.Errorf("sub-workflow %s requires execContext with thread (node: %s)", e.subWorkflowName, e.nodeID)
	}
	e.logger.Info("[InlineWorkflow] Using thread from execContext",
		"nodeID", e.nodeID,
		"thread", subThread,
		"mode", e.execContext.ThreadMode,
	)

	// Build sub-workflow inputs from core semantic contract.
	subInputs := e.buildSubWorkflowInputs()
	// Inherit mode from parent if not explicitly set in sub-workflow inputs
	if _, ok := subInputs["mode"]; !ok {
		subInputs["mode"] = getModeFromInputs(e.workflowInputs)
	}

	// Register thread inputs for per-thread param queries and updates.
	// This allows the root workflow's query/signal handlers to access this thread's inputs.
	if e.childTracker != nil && subThread != "" {
		e.childTracker.RegisterThreadInputs(subThread, subInputs)
		defer e.childTracker.UnregisterThreadInputs(subThread)
	}

	e.logger.Info("[InlineWorkflow] Executing sub-workflow",
		"nodeID", e.nodeID,
		"subWorkflow", e.subWorkflowName,
		"thread", subThread,
		"inputKeys", getMapKeys(subInputs),
	)

	// Create state machine for sub-workflow
	stateMachine := NewSimplifiedStateMachine(e.workflowID, e.subWorkflow)

	// Create step executor for sub-workflow
	// Use parent's loopNodeID if set, otherwise use this node's ID
	stepLoopNodeID := e.loopNodeID
	if stepLoopNodeID == "" {
		stepLoopNodeID = e.nodeID
	}
	executor := NewStepExecutor(
		e.ctx,
		e.workflowID,
		e.chatID,
		e.subWorkflowName,
		subInputs,
		subNodeOutputs,
		e.childTracker,
	).WithLoopContext(stepLoopNodeID, e.loopIteration).
		WithExecContext(e.execContext).
		WithProjectPath(e.projectPath).
		WithWorkflow(e.subWorkflow).
		WithPauseController(e.pauseCtrl).
		WithMakeThreadPauseCtrl(e.makeThreadPauseCtrl)

	// Initialize join state for the sub-workflow
	joinState := NewJoinState()
	joinState.InitializeJoins(e.subWorkflow)

	// Start with initial event
	initialEvent := &core.WorkflowEvent{
		ID:           fmt.Sprintf("%s-start", uniqueActivityIDBase),
		WorkflowID:   e.workflowID,
		ChatID:       e.chatID,
		WorkflowName: e.subWorkflowName,
		StepID:       "",
		Data:         subInputs,
	}

	events := []*core.WorkflowEvent{initialEvent}
	var runningSteps []*RunningStep

	// Main execution loop (same pattern as InlineLoopExecutor)
	for {
		// Yield to the Temporal scheduler to prevent deadlock detection during replay.
		_ = workflow.Sleep(e.ctx, 0)

		// Check for workflow cancellation
		if e.ctx.Err() != nil {
			e.logger.Info("[InlineWorkflow] Workflow cancelled",
				"nodeID", e.nodeID,
				"subWorkflow", e.subWorkflowName,
			)
			return nil, e.ctx.Err()
		}

		// Check for pause signal at step boundary
		e.pauseCtrl.DoCheckPause(e.ctx)

		// Process join events first
		events = processJoinEvents(events, joinState, e.subWorkflow, e.workflowID, e.chatID, e.subWorkflowName, subNodeOutputs, e.logger, nil, workflow.Now(e.ctx))

		// Find triggered steps
		triggeredNodes, err := stateMachine.FindTriggeredNodes(events, subNodeOutputs, subInputs)
		if err != nil {
			return nil, fmt.Errorf("find triggered nodes for inline workflow %s: %w", e.subWorkflowName, err)
		}
		events = nil // Clear processed events

		// Process triggered nodes
		for _, triggered := range triggeredNodes {
			node := triggered.Node

			// Skip join nodes - they are handled by processJoinEvents
			if node.GetType() == model.NodeTypeJoin {
				continue
			}

			// Check node condition - if false, skip execution
			skipped, skipEvt, condErr := skipNodeIfConditionFalse(
				e.ctx, node, subNodeOutputs, subInputs,
				e.workflowID, e.chatID, e.subWorkflowName, e.logger,
				nil,
			)
			if condErr != nil {
				return nil, condErr
			}
			if skipped {
				events = append(events, skipEvt)
				continue
			}

			// Handle loop nodes
			if node.GetType() == model.NodeTypeLoop {
				loopContract, contractErr := e.subWorkflowSemantics.RequireContractForNode(node.GetId(), model.NodeTypeLoop)
				if contractErr != nil {
					return nil, contractErr
				}
				loopExecutor, err := NewInlineLoopExecutor(
					e.ctx,
					e.workflowID,
					e.chatID,
					e.subWorkflowName,
					subInputs,
					subNodeOutputs,
					e.childTracker,
					triggered,
				)
				if err != nil {
					return nil, fmt.Errorf("failed to create loop executor for %s: %w", node.GetId(), err)
				}

				// Pass exec context, project path, and activity ID prefix
				// The activity ID prefix ensures uniqueness when multiple inline workflows
				// run in parallel (e.g., impl_1, impl_2, impl_3 all running agent loops)
				loopExecutor = loopExecutor.WithActivityIDPrefix(uniqueActivityIDBase)
				if e.execContext != nil {
					loopExecutor = loopExecutor.WithExecContext(e.execContext)
				}
				if e.projectPath != "" {
					loopExecutor = loopExecutor.WithProjectPath(e.projectPath)
				}
				loopExecutor = loopExecutor.WithPauseController(e.pauseCtrl).
					WithMakeThreadPauseCtrl(e.makeThreadPauseCtrl).
					WithInvocationContract(loopContract)

				loopOutput, err := loopExecutor.Execute()
				if err != nil {
					return nil, fmt.Errorf("loop %s failed: %w", node.GetId(), err)
				}

				// Store loop output and create completion event
				nid := node.GetId()
				// ProtoLoopOutputToMap handles both sequential and parallel loop output formats.
				loopOutputMap := model.ProtoLoopOutputToMap(loopOutput)
				subNodeOutputs[nid] = loopOutputMap
				loopEvent := &core.WorkflowEvent{
					ID:           fmt.Sprintf("%s-loop-%s", uniqueActivityIDBase, nid),
					WorkflowID:   e.workflowID,
					ChatID:       e.chatID,
					WorkflowName: e.subWorkflowName,
					StepID:       nid,
					Data:         loopOutputMap,
				}
				events = append(events, loopEvent)
				continue
			}

			// Handle workflow nodes (recursive inline execution)
			if node.GetType() == model.NodeTypeWorkflow || node.GetType() == model.NodeTypeRouter {
				var inlineOutput map[string]interface{}
				var err error

				if node.GetType() == model.NodeTypeRouter {
					// Router nodes handle their own workflow selection and execution
					inlineOutput, err = e.executeNestedRouter(triggered, subNodeOutputs, subInputs, uniqueActivityIDBase)
				} else {
					inlineOutput, err = e.executeNestedWorkflow(triggered, subNodeOutputs, subInputs, uniqueActivityIDBase)
				}
				if err != nil {
					return nil, err
				}
				nid := node.GetId()
				subNodeOutputs[nid] = inlineOutput

				// Execute save_message if configured on the nested workflow node
				// IMPORTANT: Use parent's execContext, not child's, so save_message saves to the
				// parent workflow's thread. The save_message is declared on the node in the parent
				// workflow, so it should act in the parent's context.
				if node.GetSaveMessage() != nil {
					_, err := ExecuteSaveMessageForNode(
						e.ctx,
						node,
						inlineOutput,
						subNodeOutputs,
						e.workflowID,
						e.subWorkflowName,
						e.chatID,
						subInputs,
						e.execContext, // Use parent's context so save_message saves to parent's thread
						e.loopNodeID,
						e.loopIteration,
					)
					if err != nil {
						e.logger.Error("[InlineWorkflow] save_message failed for nested workflow",
							"nodeID", e.nodeID,
							"nestedNodeID", nid,
							"error", err,
						)
						// Don't fail - save_message errors are logged but non-fatal
					}
				}

				nestedEvent := &core.WorkflowEvent{
					ID:           fmt.Sprintf("%s-nested-%s", uniqueActivityIDBase, nid),
					WorkflowID:   e.workflowID,
					ChatID:       e.chatID,
					WorkflowName: e.subWorkflowName,
					StepID:       nid,
					Data:         inlineOutput,
				}
				events = append(events, nestedEvent)
				continue
			}

			// Handle approval nodes inline (signal-based)
			if node.GetType() == model.NodeTypeApproval {
				approvalOutput, err := e.executeApproval(triggered, subNodeOutputs, subInputs, uniqueActivityIDBase)
				if err != nil {
					return nil, fmt.Errorf("approval %s failed: %w", node.GetId(), err)
				}
				nid := node.GetId()
				subNodeOutputs[nid] = approvalOutput
				approvalEvent := &core.WorkflowEvent{
					ID:           fmt.Sprintf("%s-approval-%s", uniqueActivityIDBase, nid),
					WorkflowID:   e.workflowID,
					ChatID:       e.chatID,
					WorkflowName: e.subWorkflowName,
					StepID:       nid,
					Data:         approvalOutput,
				}
				events = append(events, approvalEvent)
				continue
			}

			// Handle ask_question nodes inline (signal-based, like approval)
			if node.GetType() == model.NodeTypeAskQuestion {
				questionOutput, err := e.executeAskQuestion(triggered, subNodeOutputs, subInputs, uniqueActivityIDBase)
				if err != nil {
					return nil, fmt.Errorf("ask_question %s failed: %w", node.GetId(), err)
				}
				nid := node.GetId()
				subNodeOutputs[nid] = questionOutput
				questionEvent := &core.WorkflowEvent{
					ID:           fmt.Sprintf("ask-question-%s-%s", nid, uniqueActivityIDBase),
					WorkflowID:   e.workflowID,
					ChatID:       e.chatID,
					WorkflowName: e.subWorkflowName,
					StepID:       nid,
					Data:         questionOutput,
				}
				events = append(events, questionEvent)
				continue
			}

			// Start regular step execution (action, run)
			// StepID (node.ID) is used as the tracking key
			running := executor.Start(triggered)
			runningSteps = append(runningSteps, running)
		}

		// Check if we're done (no running steps and no pending events)
		if len(runningSteps) == 0 && len(events) == 0 {
			// Evaluate workflow outputs
			outputs, err := e.evaluateOutputs(subNodeOutputs, subInputs)
			if err != nil {
				e.logger.Error("[InlineWorkflow] Failed to evaluate outputs",
					"nodeID", e.nodeID,
					"subWorkflow", e.subWorkflowName,
					"error", err,
				)
				return nil, fmt.Errorf("failed to evaluate sub-workflow outputs: %w", err)
			}

			// DEBUG: Log response_text for inject debugging
			if rt, ok := outputs["response_text"]; ok {
				rtStr := fmt.Sprintf("%v", rt)
				if len(rtStr) > 200 {
					rtStr = rtStr[:200] + "..."
				}
				e.logger.Info("[InlineWorkflow] Output response_text",
					"nodeID", e.nodeID,
					"subWorkflow", e.subWorkflowName,
					"response_text_len", len(fmt.Sprintf("%v", rt)),
					"response_text_preview", rtStr,
				)
			} else {
				e.logger.Info("[InlineWorkflow] Output has NO response_text key",
					"nodeID", e.nodeID,
					"subWorkflow", e.subWorkflowName,
					"outputKeys", getMapKeys(outputs),
				)
			}

			return outputs, nil
		}

		// Wait for step completions
		if len(runningSteps) > 0 {
			completedSteps := waitForStepCompletions(e.ctx, runningSteps)

			for _, running := range completedSteps {
				stepEvent := executor.HandleCompletion(running)
				runningSteps = removeRunningStep(runningSteps, running)

				// Handle CanceledError - activity was cancelled by the shared activityCtx
				// (e.g., due to pause). Propagate upward so the parent workflow can
				// run checkPause() (which blocks until resume and refreshes activityCtx)
				// before re-triggering the step.
				if stepEvent.Error != nil {
					var canceledErr *temporal.CanceledError
					if errors.As(stepEvent.Error, &canceledErr) {
						e.logger.Info("[InlineWorkflow] Activity cancelled, propagating to parent for re-trigger",
							"stepID", running.StepID,
						)
						return nil, stepEvent.Error
					}
				}

				// Handle retry exhaustion - pause workflow and retry on resume.
				if stepEvent.RetryExhausted {
					e.logger.Info("[InlineWorkflow] *** RETRY EXHAUSTION DETECTED *** Activity exhausted retries, triggering pause",
						"nodeID", e.nodeID,
						"subWorkflow", e.subWorkflowName,
						"stepID", running.StepID,
						"error", stepEvent.Error,
					)

					// Emit error to UI
					errorCtx := workflow.WithActivityOptions(e.ctx, workflow.ActivityOptions{
						StartToCloseTimeout: 30 * time.Second,
						RetryPolicy: &temporal.RetryPolicy{
							MaximumAttempts: 3,
						},
					})
					var errorResult map[string]interface{}
					errStr := stepEvent.Error.Error()
					errorPayload := map[string]interface{}{
						"chat_id":       e.chatID,
						"workflow_id":   e.workflowID,
						"workflow_name": e.workflowName,
						"error_message": errStr,
						"error_type":    "retry_exhaustion",
					}
					if summary := extractLLMErrorSummary(errStr); summary != "" {
						errorPayload["error_summary"] = summary + ". Workflow paused — send a message to retry."
					}
					_ = workflow.ExecuteActivity(errorCtx, "WorkflowError", errorPayload).Get(e.ctx, &errorResult)

					// Update DB status to paused
					notifyWorkflowStatus(e.ctx, e.chatID, e.workflowID, e.workflowName, "paused", "", "", nil)

					// Self-pause and block until resume
					e.pauseCtrl.DoRequestPause()
					e.pauseCtrl.DoCheckPause(e.ctx)
					_ = workflow.Sleep(e.ctx, 0)

					// Resumed! Update DB status back to running
					notifyWorkflowStatus(e.ctx, e.chatID, e.workflowID, e.workflowName, "started", "", "", &workflowStatusOpts{Resumed: true})

					e.logger.Info("[InlineWorkflow] Resumed after pause, retrying step",
						"nodeID", e.nodeID,
						"stepID", running.StepID,
					)

					// Retry the step
					triggeredNode := &core.TriggeredNode{
						Node:  running.Node,
						Event: running.Event,
					}
					newRunning := executor.Start(triggeredNode)
					runningSteps = append(runningSteps, newRunning)
					continue
				}

				if routingErr := EnsureStepEventRoutable(stepEvent); routingErr != nil {
					e.logger.Error("[InlineWorkflow] Step failed, aborting inline workflow",
						"nodeID", e.nodeID,
						"subWorkflow", e.subWorkflowName,
						"stepID", stepEvent.StepID,
						"retryExhausted", stepEvent.RetryExhausted,
						"isTerminal", isTerminalError(routingErr),
						"error", routingErr,
					)
					return nil, routingErr
				}

				events = append(events, stepEvent.ToEvent())
			}
		}
	}
}

// executeApproval handles approval nodes inline using a signal-based wait pattern
// (matching a signal-based system). It creates an approval record via the ApprovalCreate
// activity, then waits for a Temporal signal from the gRPC approval service.
func (e *InlineWorkflowExecutor) executeApproval(
	triggered *core.TriggeredNode,
	nodeOutputs map[string]interface{},
	workflowInputs map[string]interface{},
	activityIDBase string,
) (map[string]interface{}, error) {
	node := triggered.Node
	event := triggered.Event

	const defaultApprovalTimeout = 1 * time.Hour

	// Evaluate node config to resolve CEL expressions (title, timeout)
	iterCtx := model.BuildIterContext(e.loopIteration)
	evalResult, err := EvaluateNodeConfig(
		node, nodeOutputs, e.workflowID, event.WorkflowName,
		workflowInputs, iterCtx, nil, e.execContext,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to evaluate approval node config: %w", err)
	}

	// Extract approval args
	args := model.GetApprovalArgs(evalResult)
	if args == nil {
		return nil, fmt.Errorf("expected approval node, got %s", model.NodeType(node))
	}

	title := model.CelStringValue(args.GetTitle())
	timeoutStr := model.CelStringValue(args.GetTimeout())

	timeout := defaultApprovalTimeout
	if timeoutStr != "" {
		if parsed, parseErr := time.ParseDuration(timeoutStr); parseErr == nil {
			timeout = parsed
		}
	}

	// Get the Temporal workflow execution ID for signal routing
	temporalWorkflowID := workflow.GetInfo(e.ctx).WorkflowExecution.ID

	// STEP 1: Call ApprovalCreate activity (fast DB write)
	createCtx := workflow.WithActivityOptions(e.ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts: 3,
		},
	})

	createInput := map[string]interface{}{
		"chat_id":              e.chatID,
		"workflow_id":          e.workflowID,
		"temporal_workflow_id": temporalWorkflowID,
		"step_id":              node.GetId(),
		"title":                title,
		"timeout":              timeoutStr,
	}

	var createOutput struct {
		ApprovalID      string `json:"approval_id"`
		AlreadyResolved bool   `json:"already_resolved"`
		Status          string `json:"status"`
		ActionTaken     string `json:"action_taken"`
	}
	if err := workflow.ExecuteActivity(createCtx, "ApprovalCreate", createInput).Get(e.ctx, &createOutput); err != nil {
		return nil, fmt.Errorf("ApprovalCreate activity failed: %w", err)
	}

	// STEP 2: If already resolved (idempotency on replay), return immediately
	if createOutput.AlreadyResolved {
		return map[string]interface{}{
			"approval_id":  createOutput.ApprovalID,
			"status":       createOutput.Status,
			"action_taken": createOutput.ActionTaken,
		}, nil
	}

	// STEP 3: Wait for signal or timeout
	signalName := "signal.approval." + createOutput.ApprovalID
	signalCh := workflow.GetSignalChannel(e.ctx, signalName)

	timeoutCtx, cancelTimer := workflow.WithCancel(e.ctx)
	timeoutFuture := workflow.NewTimer(timeoutCtx, timeout)

	selector := workflow.NewSelector(e.ctx)

	var status, actionTaken, denialReason string

	selector.AddReceive(signalCh, func(ch workflow.ReceiveChannel, more bool) {
		var signalData map[string]interface{}
		ch.Receive(e.ctx, &signalData)
		if s, ok := signalData["status"].(string); ok {
			status = s
		} else {
			status = "approved" // default if signal data is missing status
		}
		if at, ok := signalData["action_taken"].(string); ok {
			actionTaken = at
		}
		if dr, ok := signalData["denial_reason"].(string); ok {
			denialReason = dr
		}
		cancelTimer()
	})

	selector.AddFuture(timeoutFuture, func(f workflow.Future) {
		status = "timeout"
	})

	selector.Select(e.ctx)

	// STEP 4: On timeout, resolve the approval in DB
	if status == "timeout" {
		resolveCtx := workflow.WithActivityOptions(e.ctx, workflow.ActivityOptions{
			StartToCloseTimeout: 30 * time.Second,
			RetryPolicy: &temporal.RetryPolicy{
				MaximumAttempts: 3,
			},
		})
		resolveInput := map[string]interface{}{
			"approval_id": createOutput.ApprovalID,
			"status":      "timeout",
		}
		var resolveOutput map[string]interface{}
		if err := workflow.ExecuteActivity(resolveCtx, "ApprovalResolve", resolveInput).Get(e.ctx, &resolveOutput); err != nil {
			e.logger.Warn("[InlineWorkflow] Failed to resolve approval as timeout in DB",
				"approvalID", createOutput.ApprovalID,
				"error", err,
			)
		}
	}

	// Build output matching ApprovalOutput proto shape
	output := map[string]interface{}{
		"approval_id":  createOutput.ApprovalID,
		"status":       status,
		"action_taken": actionTaken,
	}
	if denialReason != "" {
		output["denial_reason"] = denialReason
	}

	return output, nil
}

// executeAskQuestion handles ask_question nodes inline using a signal-based wait pattern
// (matching the approval pattern). It creates a question record via the QuestionCreate
// activity, then waits for a Temporal signal from the gRPC question service.
func (e *InlineWorkflowExecutor) executeAskQuestion(
	triggered *core.TriggeredNode,
	nodeOutputs map[string]interface{},
	workflowInputs map[string]interface{},
	_ string,
) (map[string]interface{}, error) {
	node := triggered.Node
	event := triggered.Event
	iterCtx := model.BuildIterContext(e.loopIteration)
	evalResult, err := EvaluateNodeConfig(
		node,
		nodeOutputs,
		e.workflowID,
		event.WorkflowName,
		workflowInputs,
		iterCtx,
		nil,
		e.execContext,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to evaluate ask_question node config: %w", err)
	}

	metadata := ""
	if args := model.GetAskQuestionArgs(evalResult); args != nil {
		metadata = model.CelStringValue(args.GetMetadata())
	}

	threadID := ""
	if e.execContext != nil {
		threadID = e.execContext.Thread
	}

	return executeAskQuestionSignalFlow(e.ctx, askQuestionExecution{
		ChatID:        e.chatID,
		WorkflowID:    e.workflowID,
		ThreadID:      threadID,
		StepID:        node.GetId(),
		LoopNodeID:    e.loopNodeID,
		LoopIteration: e.loopIteration,
		Metadata:      metadata,
		Unattended:    IsUnattended(workflowInputs),
		Logger:        e.logger,
	})
}

// parseQuestionResponse converts the raw question response data into a map with has_feedback/response keys.
func parseQuestionResponse(responseData string) map[string]interface{} {
	result := map[string]interface{}{
		"has_feedback": false,
		"response":     "",
	}
	if responseData == "" {
		return result
	}
	// Parse the answer to check if user provided feedback
	var answer struct {
		Answers []struct {
			Question string   `json:"question"`
			Selected []string `json:"selected"`
			Freetext string   `json:"freetext"`
		} `json:"answers"`
	}
	if err := json.Unmarshal([]byte(responseData), &answer); err == nil && len(answer.Answers) > 0 {
		first := answer.Answers[0]
		// If user selected "Continue" with no freetext, no feedback
		hasContinue := false
		for _, s := range first.Selected {
			if s == "Continue" {
				hasContinue = true
			}
		}
		if hasContinue && first.Freetext == "" {
			result["has_feedback"] = false
		} else {
			result["has_feedback"] = true
			if first.Freetext != "" {
				result["response"] = first.Freetext
			} else {
				result["response"] = strings.Join(first.Selected, ", ")
			}
		}
	}
	return result
}

// executeNestedWorkflow handles workflow: or agent: nodes within the sub-workflow
func (e *InlineWorkflowExecutor) executeNestedWorkflow(
	triggered *core.TriggeredNode,
	subNodeOutputs map[string]interface{},
	subInputs map[string]interface{},
	uniqueActivityIDBase string,
) (map[string]interface{}, error) {
	node := triggered.Node
	nid := node.GetId()

	// Use helpers for consistent context building
	iterCtx := model.BuildIterContext(e.loopIteration)

	// Evaluate node config
	evalResult, err := EvaluateNodeConfig(
		node,
		subNodeOutputs,
		e.workflowID,
		e.subWorkflowName,
		subInputs,
		iterCtx,
		nil, // loopOutputs - not in a loop
		e.execContext,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to evaluate nested workflow config for %s: %w", nid, err)
	}

	contract, err := e.subWorkflowSemantics.RequireContractForNode(nid, node.GetType())
	if err != nil {
		return nil, err
	}
	if contract.WorkflowIdentity == "" {
		return nil, fmt.Errorf("missing workflow identity in core semantics contract for nested node %q", nid)
	}

	// Determine workflow name from core semantic contract
	nestedWorkflowName := contract.WorkflowIdentity

	// Create nested executor
	nestedExecutor, err := NewInlineWorkflowExecutor(
		e.ctx,
		e.workflowID,
		e.chatID,
		e.subWorkflowName,
		subInputs,
		subNodeOutputs,
		e.childTracker,
		node,
		evalResult,
		e.loopNodeID,
		e.loopIteration,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create nested executor for %s: %w", nid, err)
	}

	// Pass thread tracker, exec context, and spawn config to nested executor
	nestedExecutor = nestedExecutor.WithThreadTracker(e.threadTracker).
		WithInvocationContract(contract)
	if e.execContext != nil {
		childExecCtx := e.execContext.ForChild(nid, model.NodeThreadMode(evalResult), nestedWorkflowName, true)

		// Create child thread and optionally save inject message
		// For non-inherit modes, this creates the thread with proper fork metadata
		if model.NodeThreadMode(evalResult) != model.ThreadModeInherit {
			var injectMsg *InjectMessageConfig
			if ic := model.NodeInjectConfig(evalResult); ic != nil && model.CelStringValue(ic.GetContent()) != "" {
				attIDs, attFiles := resolveInjectAttachments(ic, e.logger)
				injectMsg = &InjectMessageConfig{
					Role:        model.CelStringValue(ic.GetRole()),
					Content:     model.CelStringValue(ic.GetContent()),
					Attachments: attIDs,
					Files:       attFiles,
				}
			}

			parentWorkflowID := ""
			if e.execContext.Parent != nil {
				parentWorkflowID = e.execContext.Parent.WorkflowID
			}

			var loopIter *int64
			if e.loopIteration >= 0 {
				iter := int64(e.loopIteration)
				loopIter = &iter
			}

			if initErr := initChildWorkflow(ChildWorkflowInitOpts{
				Ctx:              e.ctx,
				ChatID:           e.chatID,
				ParentWorkflowID: parentWorkflowID,
				ChildWorkflowID:  e.workflowID, // Inline workflows use parent's workflow ID
				ChildThreadID:    childExecCtx.Thread,
				WorkflowName:     nestedWorkflowName,
				ThreadMode:       model.NodeThreadMode(evalResult),
				ForkFromThread:   childExecCtx.ForkedFrom,
				ParentThread:     e.execContext.Thread, // Current execution's thread
				SpawnedByNodeID:  nid,
				LoopIteration:    loopIter,
				InjectMessage:    injectMsg,
				Logger:           e.logger,
			}); initErr != nil {
				e.logger.Error("[InlineWorkflow] Failed to initialize nested workflow thread",
					"nodeID", nid,
					"error", initErr,
				)
				return nil, fmt.Errorf("failed to initialize nested workflow thread for node %s: %w", nid, initErr)
			}
		} else if ic := model.NodeInjectConfig(evalResult); ic != nil && model.CelStringValue(ic.GetContent()) != "" {
			// For inherit mode, just save the inject message (thread already exists)
			activityCtx := workflow.WithActivityOptions(e.ctx, workflow.ActivityOptions{
				StartToCloseTimeout: 30 * time.Second,
				RetryPolicy: &temporal.RetryPolicy{
					InitialInterval:    time.Second,
					BackoffCoefficient: 2.0,
					MaximumInterval:    10 * time.Second,
					MaximumAttempts:    3,
				},
			})
			attIDs, attFiles := resolveInjectAttachments(ic, e.logger)
			flatInput := &types.SaveMessageInput{
				ChatID:      e.chatID,
				Thread:      childExecCtx.Thread,
				Role:        model.CelStringValue(ic.GetRole()),
				Content:     model.CelStringValue(ic.GetContent()),
				Attachments: attIDs,
				InjectFiles: injectFilesToData(attFiles),
				WorkflowID:  e.workflowID,
			}
			rtx := types.RuntimeContext{
				ChatID:     e.chatID,
				Thread:     childExecCtx.Thread,
				WorkflowID: e.workflowID,
			}
			saveInput := types.ActivityInput{Runtime: rtx, Node: buildSaveMessageNode(flatInput)}
			_ = workflow.ExecuteActivity(activityCtx, "SaveMessage", saveInput).Get(e.ctx, nil)
			e.logger.Info("[InlineWorkflow] Pre-saved inject message to inherited thread",
				"nodeID", nid,
				"thread", childExecCtx.Thread,
			)
		}

		nestedExecutor = nestedExecutor.WithExecContext(childExecCtx)
	}
	if e.projectPath != "" {
		nestedExecutor = nestedExecutor.WithProjectPath(e.projectPath)
	}
	nestedExecutor = nestedExecutor.WithPauseController(e.pauseCtrl).
		WithMakeThreadPauseCtrl(e.makeThreadPauseCtrl)

	// Override the sub-workflow name for the nested execution
	nestedExecutor.subWorkflowName = nestedWorkflowName

	return nestedExecutor.Execute()
}

// executeNestedRouter handles router nodes within a sub-workflow.
func (e *InlineWorkflowExecutor) executeNestedRouter(
	triggered *core.TriggeredNode,
	subNodeOutputs map[string]interface{},
	subInputs map[string]interface{},
	uniqueActivityIDBase string,
) (map[string]interface{}, error) {
	node := triggered.Node
	nid := node.GetId()

	// Evaluate node config
	iterCtx := model.BuildIterContext(e.loopIteration)
	evalResult, err := EvaluateNodeConfig(
		node,
		subNodeOutputs,
		e.workflowID,
		e.subWorkflowName,
		subInputs,
		iterCtx,
		nil, // loopOutputs
		e.execContext,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to evaluate nested router config for %s: %w", nid, err)
	}

	// Create router executor
	routerExec := NewRouterExecutor(
		e.ctx,
		e.workflowID,
		e.chatID,
		e.subWorkflowName,
		subInputs,
		subNodeOutputs,
		e.childTracker,
		node,
		evalResult,
	)

	// Wire up context
	if e.execContext != nil {
		routerExec = routerExec.WithExecContext(e.execContext)
	}
	if e.projectPath != "" {
		routerExec = routerExec.WithProjectPath(e.projectPath)
	}
	routerExec = routerExec.
		WithThreadTracker(e.threadTracker).
		WithPauseController(e.pauseCtrl).
		WithMakeThreadPauseCtrl(e.makeThreadPauseCtrl)

	return routerExec.Execute()
}

// evaluateOutputsMap evaluates a map of CEL expressions against the given context.
// Each entry in outputsMap maps an output name to a CEL expression string.
// Returns a map of output names to their evaluated values.
func evaluateOutputsMap(outputsMap map[string]string, celContext map[string]interface{}, logger log.Logger) (map[string]interface{}, error) {
	outputs := make(map[string]interface{})
	for name, expr := range outputsMap {
		// Unwrap template syntax if present.
		// TrimSpace first: YAML folded scalars (>) append a trailing newline
		// which would cause the "}}" suffix check to fail.
		exprStr := strings.TrimSpace(expr)
		if len(exprStr) > 4 && exprStr[:2] == "{{" && exprStr[len(exprStr)-2:] == "}}" {
			exprStr = exprStr[2 : len(exprStr)-2]
		}

		result, err := evaluateCELValue(exprStr, celContext)
		if err != nil {
			logger.Warn("Output evaluation failed",
				"output", name,
				"expr", expr,
				"error", err,
			)
			// Set to nil on error rather than failing
			outputs[name] = nil
			continue
		}

		if result == nil {
			logger.Warn("Output evaluated to nil",
				"output", name,
				"expr", expr,
			)
		}
		outputs[name] = result
	}

	return outputs, nil
}

// evaluateOutputs evaluates the sub-workflow's output expressions
func (e *InlineWorkflowExecutor) evaluateOutputs(nodeOutputs, inputs map[string]interface{}) (map[string]interface{}, error) {
	if len(e.subWorkflow.Outputs) == 0 {
		// No outputs defined - return step outputs as-is
		return nodeOutputs, nil
	}

	// Build context for CEL evaluation
	celContext := map[string]interface{}{
		"nodes": nodeOutputs,
		"workflow": map[string]interface{}{
			"inputs": inputs,
		},
	}

	return evaluateOutputsMap(e.subWorkflow.Outputs, celContext, e.logger)
}

// emitThreadCreated emits a thread_created event for threads owned by this node.
// Only emits for own and fork modes - inherit doesn't create a new thread.
func (e *InlineWorkflowExecutor) emitThreadCreated() {
	if e.execContext == nil || e.execContext.Thread == "" {
		return
	}

	// Only emit for own and fork modes (inherit doesn't create a new thread)
	if e.execContext.ThreadMode == model.ThreadModeInherit {
		e.logger.Info("[InlineWorkflow] Thread inherited (no creation event)",
			"nodeID", e.nodeID,
			"thread", e.execContext.Thread,
		)
		return
	}

	e.logger.Info("[InlineWorkflow] Thread created",
		"nodeID", e.nodeID,
		"thread", e.execContext.Thread,
		"mode", e.execContext.ThreadMode,
		"forkedFrom", e.execContext.ForkedFrom,
	)

	// Let Temporal auto-generate ActivityID for deterministic replay
	activityCtx := workflow.WithActivityOptions(e.ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    5 * time.Second,
			MaximumAttempts:    3,
		},
	})

	// Thread title: use execContext.ThreadTitle if set, otherwise the node ID.
	threadTitle := e.execContext.ThreadTitle
	if threadTitle == "" {
		threadTitle = e.nodeID
	}

	// A fork is self-describing through its fork metadata; anything else this
	// executor creates was created by a graph node.
	origin := model.ThreadOriginForMode(e.execContext.ThreadMode)

	input := map[string]interface{}{
		"chat_id":      e.chatID,
		"thread_id":    e.execContext.Thread,
		"status":       "started",
		"workflow_id":  e.workflowID,
		"node_id":      e.nodeID,
		"thread_title": threadTitle,
		"origin":       origin,
	}

	// Add router decision metadata if this thread was created by a router node
	if e.execContext.RouterDecision != nil {
		input["router_decision"] = map[string]string{
			"workflow": e.execContext.RouterDecision.Workflow,
			"preset":   e.execContext.RouterDecision.Preset,
		}
	}

	// Fire-and-forget: don't block waiting for the result.
	// This is critical for parallel execution - blocking here causes
	// "trying to block on coroutine which is already blocked" errors
	// when multiple inline workflows run concurrently.
	_ = workflow.ExecuteActivity(activityCtx, "ThreadStatus", input)
}

// emitThreadCompleted emits a thread_completed event for threads owned by this node.
func (e *InlineWorkflowExecutor) emitThreadCompleted() {
	if e.execContext == nil || e.execContext.Thread == "" {
		return
	}

	// Only emit for own and fork modes (inherit doesn't own the thread)
	if e.execContext.ThreadMode == model.ThreadModeInherit {
		return
	}

	e.logger.Info("[InlineWorkflow] Thread completed",
		"nodeID", e.nodeID,
		"thread", e.execContext.Thread,
		"mode", e.execContext.ThreadMode,
	)

	// Let Temporal auto-generate ActivityID for deterministic replay
	activityCtx := workflow.WithActivityOptions(e.ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    5 * time.Second,
			MaximumAttempts:    3,
		},
	})

	input := map[string]interface{}{
		"chat_id":     e.chatID,
		"thread_id":   e.execContext.Thread,
		"status":      "completed",
		"workflow_id": e.workflowID,
		"node_id":     e.nodeID,
	}

	// Fire-and-forget: don't block waiting for the result.
	// This is critical for parallel execution.
	_ = workflow.ExecuteActivity(activityCtx, "ThreadStatus", input)
}
