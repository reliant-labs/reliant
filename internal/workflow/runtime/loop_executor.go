// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/ptr"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/core"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/types/known/structpb"
)

// InlineLoopExecutor manages inline loop execution within the parent workflow.
// Instead of spawning child workflows for each iteration, it:
// - Loads the sub-workflow definition once
// - Executes sub-workflow nodes inline per iteration
// - Tracks iteration state for CEL evaluation (iter.*)
// - Shares the parent's thread path for message continuity
type InlineLoopExecutor struct {
	ctx            workflow.Context
	workflowID     string
	chatID         string
	workflowName   string
	workflowInputs map[string]interface{}
	nodeOutputs    map[string]interface{}
	childTracker   *ChildWorkflowTracker
	logger         log.Logger

	// Loop-specific state
	loopID               string
	loopStep             *core.TriggeredNode // The loop node with config to evaluate
	iteration            int
	subWorkflow          *reliantv1.Workflow
	subWorkflowSemantics *RuntimeSemantics         // Core semantics for nested nodes in subWorkflow
	invocationContract   *core.SubWorkflowContract // Core contract for this loop invocation

	// prevIterOutputs holds the outputs from the previous loop iteration,
	// made available as outputs.* in CEL expressions for inner nodes.
	prevIterOutputs map[string]interface{}

	// resolvedItems holds the pre-evaluated items list for sequential loops
	// that specify an items expression. When set, iter.item is populated per iteration.
	resolvedItems []interface{}
	resolvedKeys  []string

	// threadTracker tracks threads for runtime thread mapping
	threadTracker *ThreadTracker

	// execContext is the unified execution context (thread, message, loop, parent).
	execContext *ExecutionContext

	// projectPath for loading presets in spawned workflows
	projectPath string

	// activityIDPrefix is prepended to all activity IDs to ensure uniqueness
	// when multiple inline workflows run in parallel. Set by parent executor.
	activityIDPrefix string

	// pauseCtrl bundles pause-checking and cancellable-context callbacks.
	pauseCtrl *PauseController

	// makeThreadPauseCtrl creates per-thread PauseControllers for pause-aware execution.
	// Propagated from the root workflow to support nested spawn tool calls.
	makeThreadPauseCtrl func(string) *PauseController
}

// NewInlineLoopExecutor creates a new executor for inline loop execution.
func NewInlineLoopExecutor(
	ctx workflow.Context,
	workflowID string,
	chatID string,
	workflowName string,
	workflowInputs map[string]interface{},
	nodeOutputs map[string]interface{},
	childTracker *ChildWorkflowTracker,
	loopStep *core.TriggeredNode,
) (*InlineLoopExecutor, error) {
	node := loopStep.Node
	nid := node.GetId()
	if node.GetType() != model.NodeTypeLoop {
		return nil, fmt.Errorf("expected loop node, got type %s for step %s", node.GetType(), nid)
	}

	// Validate that loop has either external workflow reference OR inline definition
	la := model.GetLoopArgs(node)
	hasRef := model.CelStringRaw(la.GetRef()) != ""
	hasInline := la.GetInline() != nil && len(la.GetInline().GetNodes()) > 0
	if !hasRef && !hasInline {
		return nil, fmt.Errorf("loop step %s must specify either 'ref' or 'inline'", nid)
	}
	isParallel := model.CelBoolValue(la.GetParallel())
	if !isParallel && model.DirectCelExpr(la.GetWhile()) == "" {
		return nil, fmt.Errorf("loop step %s must specify 'while' condition", nid)
	}
	if isParallel && !model.CelStringIsSet(la.GetItems()) {
		return nil, fmt.Errorf("parallel loop step %s must specify 'items'", nid)
	}

	logger := workflow.GetLogger(ctx)

	return &InlineLoopExecutor{
		ctx:            ctx,
		workflowID:     workflowID,
		chatID:         chatID,
		workflowName:   workflowName,
		workflowInputs: workflowInputs,
		nodeOutputs:    nodeOutputs,
		childTracker:   childTracker,
		logger:         logger,
		loopID:         loopStep.Node.GetId(),
		loopStep:       loopStep,
		iteration:      0,
	}, nil
}

// WithThreadTracker sets the thread tracker for recording thread resolutions.
func (e *InlineLoopExecutor) WithThreadTracker(tracker *ThreadTracker) *InlineLoopExecutor {
	e.threadTracker = tracker
	return e
}

// WithExecContext sets the unified execution context.
func (e *InlineLoopExecutor) WithExecContext(ctx *ExecutionContext) *InlineLoopExecutor {
	e.execContext = ctx
	return e
}

// WithProjectPath sets the project path for loading presets in spawned workflows.
func (e *InlineLoopExecutor) WithProjectPath(projectPath string) *InlineLoopExecutor {
	e.projectPath = projectPath
	return e
}

// WithActivityIDPrefix sets a prefix for all activity IDs generated by this executor.
// This is critical for parallel execution - ensures uniqueness when multiple
// inline workflows run concurrently with the same loop structure.
func (e *InlineLoopExecutor) WithActivityIDPrefix(prefix string) *InlineLoopExecutor {
	e.activityIDPrefix = prefix
	return e
}

// WithPauseController sets the PauseController for pause-aware execution.
func (e *InlineLoopExecutor) WithPauseController(pc *PauseController) *InlineLoopExecutor {
	e.pauseCtrl = pc
	return e
}

// WithMakeThreadPauseCtrl sets the per-thread PauseController factory for spawn support.
func (e *InlineLoopExecutor) WithMakeThreadPauseCtrl(fn func(string) *PauseController) *InlineLoopExecutor {
	e.makeThreadPauseCtrl = fn
	return e
}

// WithInvocationContract sets the core semantic contract for this loop invocation.
func (e *InlineLoopExecutor) WithInvocationContract(contract core.SubWorkflowContract) *InlineLoopExecutor {
	e.invocationContract = &contract
	// Only override when the identity is a resolved literal, not a template.
	// See InlineWorkflowExecutor.WithInvocationContract for rationale.
	if contract.WorkflowIdentity != "" && !strings.Contains(contract.WorkflowIdentity, "{{") {
		e.workflowName = contract.WorkflowIdentity
	}
	return e
}

// GetThread returns the current thread from execContext.
func (e *InlineLoopExecutor) GetThread() string {
	if e.execContext != nil {
		return e.execContext.Thread
	}
	return ""
}

func (e *InlineLoopExecutor) inputPolicy() core.InputPolicy {
	if e.invocationContract == nil {
		return core.InputPolicyRefPresetsArgsDefaults
	}
	return e.invocationContract.InputPolicy
}

func (e *InlineLoopExecutor) loadStrategy() core.LoadStrategy {
	if e.invocationContract == nil {
		return core.LoadStrategyLoadByWorkflowRef
	}
	return e.invocationContract.LoadStrategy
}

func (e *InlineLoopExecutor) workflowIdentity() string {
	if e.invocationContract != nil && e.invocationContract.WorkflowIdentity != "" {
		return e.invocationContract.WorkflowIdentity
	}
	return e.workflowName
}

// loadAndMergePresets loads presets specified on the loop node and merges their params into iterInputs.
// Presets are merged as a base layer - explicit args will override these values later.
//
// The presets map can target:
// - "default": params are applied directly to top-level inputs
// - "GroupName": params are applied as "GroupName.param" keys
//
// Preset names may be CEL templates (e.g. `{{inputs.preset_name}}`). The
// provided evalCtx is used to resolve them per-iteration. If evalCtx is nil,
// preset names are treated as literals.
func (e *InlineLoopExecutor) loadAndMergePresets(ctx workflow.Context, iterInputs map[string]interface{}, evalCtx wfcel.CELEvalContext) error {
	if e.projectPath == "" {
		return fmt.Errorf("project path not set, cannot load presets")
	}

	presets := model.GetLoopArgs(e.loopStep.Node).GetPresets()
	if len(presets) == 0 {
		return nil
	}

	e.logger.Info("[InlineLoop] Loading presets for loop iteration",
		"loopID", e.loopID,
		"iteration", e.iteration,
		"presets", presets,
	)

	// Sort preset group names for deterministic activity scheduling order.
	groupNames := make([]string, 0, len(presets))
	for groupName := range presets {
		groupNames = append(groupNames, groupName)
	}
	sort.Strings(groupNames)

	// Load each preset via activity and merge params
	for _, groupName := range groupNames {
		rawName := presets[groupName]
		if rawName == "" {
			continue
		}

		presetName, err := ResolvePresetName(rawName, evalCtx)
		if err != nil {
			e.logger.Warn("[InlineLoop] Failed to resolve preset template, skipping",
				"loopID", e.loopID,
				"iteration", e.iteration,
				"group", groupName,
				"template", rawName,
				"error", err,
			)
			continue
		}
		if presetName != rawName {
			e.logger.Info("[InlineLoop] Resolved preset template",
				"loopID", e.loopID,
				"iteration", e.iteration,
				"group", groupName,
				"template", rawName,
				"resolved", presetName,
			)
		}
		if presetName == "" {
			continue
		}

		params, err := e.loadPresetParams(ctx, presetName)
		if err != nil {
			e.logger.Warn("[InlineLoop] Failed to load preset",
				"loopID", e.loopID,
				"preset", presetName,
				"group", groupName,
				"error", err,
			)
			continue // Skip failed presets, don't fail the whole workflow
		}

		// Merge params into iterInputs (nested: group params under iterInputs[groupName])
		if groupName == DefaultPresetGroup {
			for paramName, paramValue := range params {
				iterInputs[paramName] = paramValue
			}
		} else {
			groupMap, _ := iterInputs[groupName].(map[string]interface{})
			if groupMap == nil {
				groupMap = make(map[string]interface{})
				iterInputs[groupName] = groupMap
			}
			for paramName, paramValue := range params {
				groupMap[paramName] = paramValue
			}
		}

		e.logger.Info("[InlineLoop] Applied preset params",
			"loopID", e.loopID,
			"preset", presetName,
			"group", groupName,
			"paramCount", len(params),
		)
	}

	return nil
}

// loadPresetParams loads a preset by name and returns its params.
// Uses the V2_LoadPresetParams activity to avoid import cycles.
func (e *InlineLoopExecutor) loadPresetParams(ctx workflow.Context, presetName string) (map[string]interface{}, error) {
	activityCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
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
	}).Get(ctx, &params)

	if err != nil {
		return nil, err
	}

	return params, nil
}

// Execute runs the loop inline and returns the loop output.
// This is the main entry point - it handles the full loop lifecycle:
// 1. Load sub-workflow definition
// 2. Execute iterations inline with execContext
// 3. Evaluate while condition after each iteration
// 4. Return aggregated output
// Note: save_message (if configured) is executed by the parent workflow after loop completion,
// consistent with how all other node types handle save_message.
func (e *InlineLoopExecutor) Execute() (*reliantv1.LoopOutput, error) {
	la := model.GetLoopArgs(e.loopStep.Node)

	// Branch to parallel execution if parallel is set
	if model.CelBoolValue(la.GetParallel()) {
		return e.ExecuteParallel()
	}

	e.logger.Info("[InlineLoop] Starting inline loop execution",
		"loopID", e.loopID,
		"while", model.DirectCelExpr(la.GetWhile()),
		"workflowIdentity", e.workflowIdentity(),
		"loadStrategy", e.loadStrategy(),
		"hasSaveMessage", e.loopStep.Node.GetSaveMessage() != nil,
		"hasExecContext", e.execContext != nil,
	)

	// Load sub-workflow definition
	if err := e.loadSubWorkflow(); err != nil {
		return nil, fmt.Errorf("failed to load loop sub-workflow: %w", err)
	}

	// If the sequential loop has an items expression, resolve it upfront
	// so iter.item is available on each iteration.
	if model.CelStringIsSet(la.GetItems()) {
		items, err := e.evaluateItems()
		if err != nil {
			return nil, fmt.Errorf("failed to evaluate loop items: %w", err)
		}
		keyExpr := la.GetKey()
		keys, err := e.resolveIterationKeys(items, keyExpr)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve loop iteration keys: %w", err)
		}
		e.resolvedItems = items
		e.resolvedKeys = keys
		e.logger.Info("[InlineLoop] Resolved items for sequential loop",
			"loopID", e.loopID,
			"itemCount", len(items),
		)
	}

	// Track outputs from the last iteration
	var lastIterationOutputs map[string]interface{}

	e.logger.Info("[InlineLoop] About to enter main loop",
		"loopID", e.loopID,
		"iteration", e.iteration,
		"subWorkflowNodes", len(e.subWorkflow.GetNodes()),
		"subWorkflowEdges", len(e.subWorkflow.GetEdges()),
	)

	// Main loop - continues while 'while' condition is true
	for {
		// Yield to the Temporal scheduler to prevent deadlock detection during replay.
		_ = workflow.Sleep(e.ctx, 0)

		// Check for workflow cancellation at the start of each iteration
		// This ensures we respond to CancelWorkflow requests promptly
		if e.ctx.Err() != nil {
			e.logger.Info("[InlineLoop] Workflow cancelled, exiting loop",
				"loopID", e.loopID,
				"iteration", e.iteration,
			)
			return nil, e.ctx.Err()
		}

		// Check for pause signal at iteration boundary
		e.pauseCtrl.DoCheckPause(e.ctx)

		// Auto-stop when items-based sequential loop exhausts its items
		if e.resolvedItems != nil && e.iteration >= len(e.resolvedItems) {
			e.logger.Info("[InlineLoop] All items processed, exiting loop",
				"loopID", e.loopID,
				"iteration", e.iteration,
				"itemCount", len(e.resolvedItems),
			)
			break
		}

		e.logger.Info("[InlineLoop] Starting iteration",
			"loopID", e.loopID,
			"iteration", e.iteration,
		)

		// Execute this iteration
		iterOutputs, err := e.executeIteration()
		if err != nil {
			e.logger.Error("[InlineLoop] Iteration failed, exiting loop",
				"loopID", e.loopID,
				"iteration", e.iteration,
				"error", err,
				"isTerminal", isTerminalError(err),
			)
			// Propagate all activity errors - let Temporal handle retries.
			// If an activity exhausted its retry attempts, the loop should fail.
			// Soft/expected errors (like tool validation) should be in outputs, not thrown.
			return nil, err
		}

		lastIterationOutputs = iterOutputs
		e.prevIterOutputs = iterOutputs
		e.iteration++

		// Check while condition (required - always present)
		// Loop continues while condition is true, exits when false
		shouldContinue, err := e.evaluateWhileCondition(iterOutputs)
		if err != nil {
			e.logger.Error("[InlineLoop] While condition evaluation failed",
				"loopID", e.loopID,
				"iteration", e.iteration-1,
				"while", model.DirectCelExpr(model.GetLoopArgs(e.loopStep.Node).GetWhile()),
				"error", err,
			)
			// Fail fast - while condition errors indicate a bug in the workflow definition
			return nil, fmt.Errorf("while condition evaluation failed at iteration %d: %w", e.iteration-1, err)
		}

		if !shouldContinue {
			e.logger.Info("[InlineLoop] While condition no longer satisfied, exiting",
				"loopID", e.loopID,
				"iteration", e.iteration-1,
				"while", model.DirectCelExpr(model.GetLoopArgs(e.loopStep.Node).GetWhile()),
			)
			break
		}
	}

	e.logger.Info("[InlineLoop] Loop completed",
		"loopID", e.loopID,
		"iterations", e.iteration,
	)

	loopOutputs := lastIterationOutputs
	if loopOutputs == nil {
		loopOutputs = map[string]interface{}{}
	}
	protoOutputs, err := structpb.NewStruct(loopOutputs)
	if err != nil {
		return nil, fmt.Errorf("failed to build loop outputs struct: %w", err)
	}

	return &reliantv1.LoopOutput{
		Outputs:    protoOutputs,
		Iterations: int32(e.iteration),
	}, nil
}

// loadSubWorkflow loads the sub-workflow definition from the registry or constructs it from inline config.
func backfillNodeOutputsFromEvents(events []*core.WorkflowEvent, nodeOutputs map[string]interface{}) {
	for _, event := range events {
		if event == nil || event.StepID == "" || event.Data == nil {
			continue
		}
		nodeOutputs[event.StepID] = event.Data
	}
}

func (e *InlineLoopExecutor) loadSubWorkflow() error {
	if e.loadStrategy() == core.LoadStrategyInlineEmbedded {
		wf := model.GetLoopArgs(e.loopStep.Node).GetInline()
		if wf == nil || len(wf.GetNodes()) == 0 {
			return fmt.Errorf("failed to convert inline loop config to workflow")
		}
		if wf.GetName() == "" {
			wf.Name = e.workflowIdentity()
		}
		e.subWorkflow = wf
		e.logger.Info("[InlineLoop] Using inline sub-workflow",
			"loopID", e.loopID,
			"workflowIdentity", e.workflowIdentity(),
			"nodeCount", len(wf.GetNodes()),
			"edgeCount", len(wf.GetEdges()),
		)
		return e.compileSubWorkflowSemantics()
	}

	// Use an activity to load the workflow (same as main workflow loading)
	activityCtx := workflow.WithActivityOptions(e.ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    30 * time.Second,
			MaximumAttempts:    3,
		},
	})

	workflowRef := e.workflowIdentity()
	if e.invocationContract != nil && e.invocationContract.WorkflowRef != "" && !strings.Contains(e.invocationContract.WorkflowRef, "{{") {
		workflowRef = e.invocationContract.WorkflowRef
	}

	// Pass ChatID to allow the activity to resolve project-relative workflow paths
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
	e.logger.Info("[InlineLoop] Loaded external sub-workflow",
		"loopID", e.loopID,
		"workflowIdentity", e.workflowIdentity(),
		"workflowRef", workflowRef,
		"nodeCount", len(wf.GetNodes()),
		"edgeCount", len(wf.GetEdges()),
	)

	return e.compileSubWorkflowSemantics()
}

func (e *InlineLoopExecutor) compileSubWorkflowSemantics() error {
	if e.subWorkflow == nil {
		return fmt.Errorf("sub-workflow not loaded for loop %s", e.loopID)
	}
	semantics, err := CompileRuntimeSemantics(e.subWorkflow, e.workflowIdentity())
	if err != nil {
		return err
	}
	e.subWorkflowSemantics = semantics
	return nil
}

// buildIterCtx returns the iter context map for the current iteration.
// When the loop has resolved items, includes item and key; otherwise just iteration/index.
func (e *InlineLoopExecutor) buildIterCtx() map[string]interface{} {
	if e.resolvedItems != nil && e.iteration < len(e.resolvedItems) {
		item := e.resolveIterItem(e.resolvedItems[e.iteration])
		key := e.resolvedKeys[e.iteration]
		return model.BuildParallelIterContext(e.iteration, item, key)
	}
	return model.BuildIterContext(e.iteration)
}

// buildIterContextModel returns the IterContext struct for CEL eval contexts.
func (e *InlineLoopExecutor) buildIterContextModel() *model.IterContext {
	ic := &model.IterContext{Iteration: e.iteration, Index: e.iteration}
	if e.resolvedItems != nil && e.iteration < len(e.resolvedItems) {
		ic.Item = e.resolveIterItem(e.resolvedItems[e.iteration])
		ic.Key = e.resolvedKeys[e.iteration]
	}
	return ic
}

func (e *InlineLoopExecutor) buildIterationInputs() (map[string]interface{}, error) {
	if e.inputPolicy() == core.InputPolicyInlineInheritParentInputs {
		iterInputs := make(map[string]interface{}, len(e.workflowInputs)+2)
		for key, value := range e.workflowInputs {
			iterInputs[key] = value
		}
		iterInputs["loop"] = map[string]interface{}{"iteration": e.iteration}
		iterInputs["iter"] = e.buildIterCtx()
		return iterInputs, nil
	}

	iterCtx := e.buildIterCtx()
	evalResult, err := EvaluateNodeConfig(
		e.loopStep.Node,
		e.nodeOutputs,
		e.workflowID,
		e.workflowIdentity(),
		e.workflowInputs,
		iterCtx,
		nil, // loopOutputs - this is the loop's own config eval, not inner nodes
		e.execContext,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to evaluate loop config: %w", err)
	}

	iterInputs := make(map[string]interface{})
	if len(model.GetLoopArgs(e.loopStep.Node).GetPresets()) > 0 {
		presetEvalCtx := &wfcel.EdgeEvalContext{
			Nodes:  e.nodeOutputs,
			Inputs: e.workflowInputs,
			Iter:   e.buildIterContextModel(),
		}
		if err := e.loadAndMergePresets(e.ctx, iterInputs, presetEvalCtx); err != nil {
			e.logger.Warn("[InlineLoop] Failed to load presets, continuing without them",
				"loopID", e.loopID,
				"error", err,
			)
		}
	}

	// Passthrough: forward specified parent inputs to the loop body.
	if passthrough := model.NodePassthrough(evalResult); len(passthrough) > 0 {
		for _, name := range passthrough {
			if val, ok := e.workflowInputs[name]; ok {
				iterInputs[name] = val
			}
		}
	}

	for key, value := range model.NodeMergedSubWorkflowInputs(evalResult) {
		if key == "loop" {
			continue
		}
		iterInputs[key] = value
	}
	if len(e.subWorkflow.GetInputs()) > 0 {
		iterInputs = ApplyDefaults(iterInputs, e.subWorkflow.GetInputs())
	}
	iterInputs["loop"] = map[string]interface{}{"iteration": e.iteration}
	iterInputs["iter"] = e.buildIterCtx()
	return iterInputs, nil
}

// executeIteration runs all nodes in the sub-workflow for a single iteration.
// Returns the evaluated workflow outputs when complete.
func (e *InlineLoopExecutor) executeIteration() (map[string]interface{}, error) {
	// Create a fresh step outputs map for this iteration
	// This is scoped to the sub-workflow execution
	iterNodeOutputs := make(map[string]interface{})

	// Build iteration inputs from core semantic contract.
	iterInputs, err := e.buildIterationInputs()
	if err != nil {
		return nil, err
	}
	e.logger.Info("[InlineLoop] Built iteration inputs",
		"loopID", e.loopID,
		"iteration", e.iteration,
		"inputPolicy", e.inputPolicy(),
		"inputKeys", getMapKeys(iterInputs),
	)

	// Create state machine for sub-workflow
	iterStateMachine := NewSimplifiedStateMachine(e.workflowID, e.subWorkflow)

	// Create step executor for this iteration
	// Derive iteration-specific execution context
	// Loops are always passthrough — they inherit the parent's thread transparently.
	// Thread decisions belong on the child nodes (workflow/call_llm), not on the loop.
	var iterExecContext *ExecutionContext
	if e.execContext != nil {
		iterExecContext = e.execContext.ForIteration(e.iteration, true).
			WithLoop(e.loopID, e.iteration)
	}

	iterExecutor := NewStepExecutor(
		e.ctx,
		e.workflowID,
		e.chatID,
		e.workflowName,
		iterInputs,
		iterNodeOutputs,
		e.childTracker,
	).WithLoopContext(e.loopID, e.iteration).
		WithExecContext(iterExecContext).
		WithProjectPath(e.projectPath).
		WithWorkflow(e.subWorkflow).
		WithPauseController(e.pauseCtrl).
		WithMakeThreadPauseCtrl(e.makeThreadPauseCtrl)

	// Initialize with start event
	events := []*core.WorkflowEvent{{
		ID:           fmt.Sprintf("loop-%s-iter%d-start", e.loopID, e.iteration),
		WorkflowID:   e.workflowID,
		ChatID:       e.chatID,
		WorkflowName: e.workflowIdentity(),
		StepID:       "", // Empty = workflow started
		Data:         iterInputs,
	}}
	// Track running steps for this iteration
	var runningSteps []*RunningStep

	// Initialize join state for the sub-workflow
	joinState := NewJoinState()
	joinState.InitializeJoins(e.subWorkflow)

	// Main execution loop for this iteration
	for {
		// Yield to the Temporal scheduler to prevent deadlock detection during replay.
		_ = workflow.Sleep(e.ctx, 0)

		// Check for workflow cancellation at the start of each execution cycle
		if e.ctx.Err() != nil {
			e.logger.Info("[InlineLoop] Workflow cancelled during iteration execution",
				"loopID", e.loopID,
				"iteration", e.iteration,
			)
			return nil, e.ctx.Err()
		}

		// Check for pause signal at step boundary within iteration
		e.pauseCtrl.DoCheckPause(e.ctx)

		// Sync workflow params from parent inputs before edge evaluation.
		// This ensures signaled updates (e.g., mode changes) take effect immediately.
		// Only needed for ref'd workflows. Inline workflows inherit parent inputs
		// directly each iteration, so they're already in sync.
		if e.inputPolicy() == core.InputPolicyRefPresetsArgsDefaults {
			for key, parentValue := range e.workflowInputs {
				if _, exists := iterInputs[key]; exists {
					if !reflect.DeepEqual(iterInputs[key], parentValue) {
						iterInputs[key] = parentValue
					}
				}
			}
		}

		// Process join events
		events = processJoinEvents(events, joinState, e.subWorkflow, e.workflowID, e.chatID, e.workflowIdentity(), iterNodeOutputs, e.logger, nil, workflow.Now(e.ctx))
		// Find triggered steps
		if len(events) > 0 {
			e.logger.Info("[InlineLoop] First event details",
				"loopID", e.loopID,
				"eventID", events[0].ID,
				"eventStepID", events[0].StepID,
			)
		}
		backfillNodeOutputsFromEvents(events, iterNodeOutputs)
		triggeredSteps, err := iterStateMachine.FindTriggeredNodes(events, iterNodeOutputs, iterInputs)
		if err != nil {
			return nil, fmt.Errorf("find triggered steps for loop %s iteration %d: %w", e.loopID, e.iteration, err)
		}
		triggeredIDs := make([]string, len(triggeredSteps))
		for i, ts := range triggeredSteps {
			triggeredIDs[i] = ts.Node.GetId()
		}
		e.logger.Info("[InlineLoop] Found triggered steps",
			"loopID", e.loopID,
			"iteration", e.iteration,
			"triggeredCount", len(triggeredSteps),
			"triggeredIDs", triggeredIDs,
			"nodeOutputsKeys", getMapKeys(iterNodeOutputs),
		)
		events = nil

		// Execute triggered steps
		for _, step := range triggeredSteps {
			// Skip joins - handled above
			if step.Node.GetType() == model.NodeTypeJoin {
				continue
			}

			// Check node condition - if false, skip execution
			skipped, skipEvt, condErr := skipNodeIfConditionFalse(
				e.ctx, step.Node, iterNodeOutputs, iterInputs,
				e.workflowID, e.chatID, e.workflowIdentity(), e.logger,
			)
			if condErr != nil {
				return nil, condErr
			}
			if skipped {
				events = append(events, skipEvt)
				continue
			}

			// Handle nested loops inline (recursively)
			if step.Node.GetType() == model.NodeTypeLoop {
				e.logger.Info("[InlineLoop] Executing nested loop",
					"loopID", e.loopID,
					"iteration", e.iteration,
					"nestedLoopID", step.Node.GetId(),
				)

				nestedContract, nestedContractErr := e.subWorkflowSemantics.RequireContractForNode(step.Node.GetId(), model.NodeTypeLoop)
				if nestedContractErr != nil {
					return nil, nestedContractErr
				}
				nestedExecutor, err := NewInlineLoopExecutor(
					e.ctx,
					e.workflowID,
					e.chatID,
					e.workflowIdentity(),
					iterInputs,
					iterNodeOutputs,
					e.childTracker,
					step,
				)
				if err != nil {
					return nil, fmt.Errorf("failed to create nested loop executor for %s: %w", step.Node.GetId(), err)
				}

				// Pass thread tracker, exec context, spawn config, and activity ID prefix to nested loop
				// Propagate our prefix to ensure nested loop activities are also unique
				if e.activityIDPrefix != "" {
					nestedExecutor = nestedExecutor.WithActivityIDPrefix(e.activityIDPrefix)
				}
				nestedExecutor = nestedExecutor.WithThreadTracker(e.threadTracker)
				if iterExecContext != nil {
					nestedExecutor = nestedExecutor.WithExecContext(iterExecContext)
				}
				if e.projectPath != "" {
					nestedExecutor = nestedExecutor.WithProjectPath(e.projectPath)
				}
				nestedExecutor = nestedExecutor.WithPauseController(e.pauseCtrl).
					WithMakeThreadPauseCtrl(e.makeThreadPauseCtrl).
					WithInvocationContract(nestedContract)

				nestedOutput, err := nestedExecutor.Execute()
				if err != nil {
					return nil, fmt.Errorf("nested loop %s failed: %w", step.Node.GetId(), err)
				}

				nestedOutputMap := model.ProtoLoopOutputToMap(nestedOutput)

				// Store nested loop output
				iterNodeOutputs[step.Node.GetId()] = nestedOutputMap

				// Create completion event
				// No timestamp - loopID + iteration + nodeID is deterministically unique
				events = append(events, &core.WorkflowEvent{
					ID:           fmt.Sprintf("nested-loop-complete-%s-iter%d-%s", e.loopID, e.iteration, step.Node.GetId()),
					WorkflowID:   e.workflowID,
					ChatID:       e.chatID,
					WorkflowName: e.workflowIdentity(),
					StepID:       step.Node.GetId(),
					Data:         nestedOutputMap,
				})
				continue
			}

			// Handle workflow nodes inline
			if step.Node.GetType() == model.NodeTypeWorkflow {
				e.logger.Info("[InlineLoop] Executing inline workflow",
					"loopID", e.loopID,
					"iteration", e.iteration,
					"stepID", step.Node.GetId(),
					"stepType", step.Node.GetType(),
				)

				// Use helpers for consistent context building
				nestedIterCtx := e.buildIterCtx()

				// DEBUG: Log node outputs before evaluating config (for inject debugging)
				if model.NodeInjectConfig(step.Node) != nil {
					e.logger.Info("[InlineLoop] BEFORE EvaluateNodeConfig for step with inject",
						"loopID", e.loopID,
						"iteration", e.iteration,
						"stepID", step.Node.GetId(),
						"injectContent", model.NodeInjectConfig(step.Node).GetContent(),
						"iterNodeOutputsKeys", getMapKeys(iterNodeOutputs),
					)
					// Log each node output's response_text
					for nodeID, output := range iterNodeOutputs {
						if m, ok := output.(map[string]interface{}); ok {
							if rt, ok := m["response_text"]; ok {
								rtStr := fmt.Sprintf("%v", rt)
								if len(rtStr) > 200 {
									rtStr = rtStr[:200] + "..."
								}
								e.logger.Info("[InlineLoop] Node output available for inject",
									"nodeID", nodeID,
									"response_text", rtStr,
								)
							}
						}
					}
				}

				// Evaluate node config
				evalResult, err := EvaluateNodeConfig(
					step.Node,
					iterNodeOutputs,
					e.workflowID,
					e.workflowIdentity(),
					iterInputs,
					nestedIterCtx,
					e.prevIterOutputs, // previous iteration outputs for outputs.* namespace
					e.execContext,
				)
				if err != nil {
					e.logger.Error("[InlineLoop] Failed to evaluate workflow step config",
						"stepID", step.Node.GetId(),
						"error", err,
					)
					// Fail fast - CEL evaluation errors should halt the workflow, not silently continue
					return nil, fmt.Errorf("step %s config evaluation failed: %w", step.Node.GetId(), err)
				}

				// Debug: Log inject details when present
				if ic := model.NodeInjectConfig(evalResult); ic != nil && model.NodeInjectConfig(step.Node) != nil {
					nodeOutputsDebug := make(map[string]string)
					for k, v := range iterNodeOutputs {
						if m, ok := v.(map[string]interface{}); ok {
							if rt, ok := m["response_text"]; ok {
								if rtStr, ok := rt.(string); ok {
									if len(rtStr) > 100 {
										nodeOutputsDebug[k+".response_text"] = rtStr[:100] + "..."
									} else {
										nodeOutputsDebug[k+".response_text"] = rtStr
									}
								} else {
									nodeOutputsDebug[k+".response_text"] = fmt.Sprintf("<type:%T>", rt)
								}
							} else {
								nodeOutputsDebug[k] = "<no response_text>"
							}
						} else {
							nodeOutputsDebug[k] = fmt.Sprintf("<type:%T>", v)
						}
					}
					e.logger.Info("[InlineLoop] Inject message evaluation",
						"loopID", e.loopID,
						"iteration", e.iteration,
						"stepID", step.Node.GetId(),
						"contentLength", len(model.CelStringValue(ic.GetContent())),
						"nodeOutputsKeys", getMapKeys(iterNodeOutputs),
						"nodeOutputsDebug", nodeOutputsDebug,
					)
				}

				// Create inline workflow executor
				inlineExecutor, err := NewInlineWorkflowExecutor(
					e.ctx,
					e.workflowID,
					e.chatID,
					e.workflowIdentity(),
					iterInputs,
					iterNodeOutputs,
					e.childTracker,
					step.Node,
					evalResult,
					e.loopID,
					e.iteration,
				)
				if err != nil {
					e.logger.Error("[InlineLoop] Failed to create inline workflow executor",
						"stepID", step.Node.GetId(),
						"error", err,
					)
					// Fail fast - executor creation errors should halt the workflow
					return nil, fmt.Errorf("failed to create executor for step %s: %w", step.Node.GetId(), err)
				}

				contract, contractErr := e.subWorkflowSemantics.RequireContractForNode(step.Node.GetId(), model.NodeTypeWorkflow)
				if contractErr != nil {
					return nil, contractErr
				}
				if contract.WorkflowIdentity == "" {
					return nil, fmt.Errorf("missing workflow identity in core semantics contract for nested loop step %q", step.Node.GetId())
				}

				// Build child execution context for this inline workflow
				var childExecCtx *ExecutionContext
				if iterExecContext != nil {
					childExecCtx = iterExecContext.ForChild(step.Node.GetId(), model.NodeThreadMode(evalResult), contract.WorkflowIdentity, model.NodeThreadMemo(evalResult))

					// Set thread title: use node ID, with ordinal for loop iterations
					childExecCtx.ThreadTitle = step.Node.GetId()
					if e.iteration >= 0 {
						childExecCtx.ThreadTitle = fmt.Sprintf("%s #%d", step.Node.GetId(), e.iteration+1)
					}

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
						if iterExecContext.Parent != nil {
							parentWorkflowID = iterExecContext.Parent.WorkflowID
						}

						var loopIter *int64
						if e.iteration >= 0 {
							iter := int64(e.iteration)
							loopIter = &iter
						}

						if initErr := initChildWorkflow(ChildWorkflowInitOpts{
							Ctx:              e.ctx,
							ChatID:           e.chatID,
							ParentWorkflowID: parentWorkflowID,
							ChildWorkflowID:  e.workflowID, // Inline workflows use parent's workflow ID
							ChildThreadID:    childExecCtx.Thread,
							WorkflowName:     contract.WorkflowIdentity,
							ThreadTitle:      ptr.StringIfNotEmpty(childExecCtx.ThreadTitle),
							ThreadMode:       model.NodeThreadMode(evalResult),
							ForkFromThread:   childExecCtx.ForkedFrom,
							ParentThread:     iterExecContext.Thread,
							SpawnedByNodeID:  step.Node.GetId(),
							LoopIteration:    loopIter,
							InjectMessage:    injectMsg,
							Logger:           e.logger,
						}); initErr != nil {
							e.logger.Error("[InlineLoop] Failed to initialize child workflow thread",
								"stepID", step.Node.GetId(),
								"error", initErr,
							)
							return nil, fmt.Errorf("failed to initialize child workflow thread for step %s: %w", step.Node.GetId(), initErr)
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
						e.logger.Info("[InlineLoop] Pre-saved inject message to inherited thread",
							"stepID", step.Node.GetId(),
							"thread", childExecCtx.Thread,
						)
					}

					inlineExecutor = inlineExecutor.WithExecContext(childExecCtx)
				}
				if e.projectPath != "" {
					inlineExecutor = inlineExecutor.WithProjectPath(e.projectPath)
				}
				inlineExecutor = inlineExecutor.WithPauseController(e.pauseCtrl).
					WithMakeThreadPauseCtrl(e.makeThreadPauseCtrl).
					WithInvocationContract(contract)

				// Execute workflow inline
				inlineOutput, err := inlineExecutor.Execute()
				if err != nil {
					e.logger.Error("[InlineLoop] Inline workflow execution failed",
						"stepID", step.Node.GetId(),
						"error", err,
					)
					// Propagate the error - let the loop handle retry/failure
					return nil, fmt.Errorf("inline workflow %s failed: %w", step.Node.GetId(), err)
				}

				// Store output for edge routing
				iterNodeOutputs[step.Node.GetId()] = inlineOutput

				// Execute save_message if configured on the inline workflow node
				// IMPORTANT: Use iterExecContext (the loop iteration's context), not childExecCtx,
				// so that save_message saves to the loop's thread, not the forked child's thread.
				// The save_message is declared on the node in the loop's inline workflow, so it
				// should act in the loop's context.
				if step.Node.GetSaveMessage() != nil {
					_, err := ExecuteSaveMessageForNode(
						e.ctx,
						step.Node,
						inlineOutput,
						iterNodeOutputs,
						e.workflowID,
						e.workflowIdentity(),
						e.chatID,
						iterInputs,
						iterExecContext, // Use loop iteration's context so save_message saves to loop's thread
						e.loopID,
						e.iteration,
					)
					if err != nil {
						e.logger.Error("[InlineLoop] save_message failed for inline workflow",
							"loopID", e.loopID,
							"iteration", e.iteration,
							"stepID", step.Node.GetId(),
							"error", err,
						)
						// Don't fail the workflow - save_message errors are logged but non-fatal
					}
				}

				// Debug: Log what we're storing with more detail
				respText := ""
				respTextType := "<missing>"
				if rt, ok := inlineOutput["response_text"]; ok {
					respTextType = fmt.Sprintf("%T", rt)
					if rtStr, ok := rt.(string); ok {
						if len(rtStr) > 200 {
							respText = rtStr[:200] + "..."
						} else {
							respText = rtStr
						}
					} else if rt == nil {
						respText = "<nil>"
					}
				}
				e.logger.Info("[InlineLoop] Stored workflow output",
					"loopID", e.loopID,
					"iteration", e.iteration,
					"stepID", step.Node.GetId(),
					"outputKeys", getMapKeys(inlineOutput),
					"responseTextType", respTextType,
					"responseTextPreview", respText,
				)

				// Create completion event
				events = append(events, &core.WorkflowEvent{
					ID:           fmt.Sprintf("workflow-complete-%s-%d", step.Node.GetId(), workflow.Now(e.ctx).UnixNano()),
					WorkflowID:   e.workflowID,
					ChatID:       e.chatID,
					WorkflowName: e.workflowIdentity(),
					StepID:       step.Node.GetId(),
					Data:         inlineOutput,
				})
				continue
			}

			e.logger.Info("[InlineLoop] Executing step",
				"loopID", e.loopID,
				"iteration", e.iteration,
				"stepID", step.Node.GetId(),
			)

			// Start step execution - StepID used as tracking key
			running := iterExecutor.Start(step)
			runningSteps = append(runningSteps, running)
		}

		// Check completion
		if len(runningSteps) == 0 && len(events) == 0 {
			e.logger.Info("[InlineLoop] Iteration complete, evaluating outputs",
				"loopID", e.loopID,
				"iteration", e.iteration,
				"outputDefs", e.subWorkflow.GetOutputs(),
				"nodeOutputs", iterNodeOutputs,
			)

			// Build workflow context for output evaluation
			workflowContext := buildWorkflowContext(e.workflowID, e.workflowIdentity(), e.chatID, iterInputs)

			// Evaluate sub-workflow outputs
			outputs, err := EvaluateWorkflowOutputs(e.subWorkflow.GetOutputs(), iterNodeOutputs, workflowContext)
			if err != nil {
				e.logger.Error("[InlineLoop] Failed to evaluate outputs",
					"loopID", e.loopID,
					"iteration", e.iteration,
					"error", err,
				)
				return nil, fmt.Errorf("failed to evaluate sub-workflow outputs: %w", err)
			}

			e.logger.Info("[InlineLoop] Outputs evaluated",
				"loopID", e.loopID,
				"iteration", e.iteration,
				"outputs", outputs,
			)

			return outputs, nil
		}

		// Wait for step completions
		if len(runningSteps) > 0 {
			completedSteps := waitForStepCompletions(e.ctx, runningSteps)

			for _, running := range completedSteps {
				stepEvent := iterExecutor.HandleCompletion(running)
				runningSteps = removeRunningStep(runningSteps, running)

				// Handle CanceledError - activity was cancelled by the shared activityCtx
				// (e.g., due to pause). Propagate upward so the parent workflow can
				// run checkPause() (which blocks until resume and refreshes activityCtx)
				// before re-triggering the step.
				if stepEvent.Error != nil {
					var canceledErr *temporal.CanceledError
					if errors.As(stepEvent.Error, &canceledErr) {
						e.logger.Info("[InlineLoop] Activity cancelled, propagating to parent for re-trigger",
							"stepID", running.StepID,
						)
						return nil, stepEvent.Error
					}
				}

				// Handle retry exhaustion - pause workflow and retry on resume.
				// This handles rate limits, transient errors, etc. that exhaust
				// Temporal's retry budget.
				if stepEvent.RetryExhausted {
					e.logger.Info("[InlineLoop] *** RETRY EXHAUSTION DETECTED *** Activity exhausted retries, triggering pause",
						"loopID", e.loopID,
						"iteration", e.iteration,
						"stepID", running.StepID,
						"error", stepEvent.Error,
					)

					// Emit error to UI so user knows what happened
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
					notifyWorkflowStatus(e.ctx, e.chatID, e.workflowID, e.workflowName, "started", "", "", nil)

					e.logger.Info("[InlineLoop] Resumed after pause, retrying step",
						"loopID", e.loopID,
						"iteration", e.iteration,
						"stepID", running.StepID,
					)

					// Retry the step
					triggeredNode := &core.TriggeredNode{
						Node:  running.Node,
						Event: running.Event,
					}
					newRunning := iterExecutor.Start(triggeredNode)
					runningSteps = append(runningSteps, newRunning)
					continue
				}

				if routingErr := EnsureStepEventRoutable(stepEvent); routingErr != nil {
					e.logger.Error("[InlineLoop] Step failed, aborting iteration",
						"loopID", e.loopID,
						"iteration", e.iteration,
						"stepID", stepEvent.StepID,
						"retryExhausted", stepEvent.RetryExhausted,
						"isTerminal", isTerminalError(routingErr),
						"error", routingErr,
					)
					return nil, routingErr
				}

				if stepEvent.StepID != "" && stepEvent.Data != nil {
					iterNodeOutputs[stepEvent.StepID] = stepEvent.Data
				}
				events = append(events, stepEvent.ToEvent())
			}
		}
	}
}

// evaluateWhileCondition evaluates the "while" CEL expression against iteration outputs.
// Returns true if the loop should continue, false if it should exit.
// EXPLICIT NAMESPACE MODEL:
//   - outputs.*: Sub-workflow outputs from the current iteration
//   - iter.*: Loop iteration context (iter.iteration)
//   - inputs.*: Workflow inputs (for iteration limits like inputs.max_turns)
func (e *InlineLoopExecutor) evaluateWhileCondition(outputs map[string]interface{}) (bool, error) {
	whileExpr := model.DirectCelExpr(model.GetLoopArgs(e.loopStep.Node).GetWhile())

	e.logger.Debug("[InlineLoop] Evaluating while condition",
		"loopID", e.loopID,
		"iteration", e.iteration-1,
		"while", whileExpr,
		"outputs", outputs,
	)

	ctx := &wfcel.LoopEvalContext{
		Iter:    e.buildIterContextModel(),
		Outputs: outputs,
		Inputs:  e.workflowInputs,
	}
	result, err := wfcel.EvaluateBool(whileExpr, ctx)
	if err != nil {
		e.logger.Debug("[InlineLoop] While condition CEL error",
			"loopID", e.loopID,
			"while", whileExpr,
			"error", err,
		)
		return false, err
	}

	e.logger.Debug("[InlineLoop] While condition evaluated",
		"loopID", e.loopID,
		"while", whileExpr,
		"result", result,
	)

	return result, nil
}

// isTerminalError checks if an error should stop the loop entirely.
// Terminal errors are non-retryable and indicate a fundamental failure
// that won't be fixed by retrying (e.g., auth errors, validation errors).
func isTerminalError(err error) bool {
	if err == nil {
		return false
	}

	// Check for Temporal ApplicationError with TerminalError type
	// The error chain is: ActivityError -> ApplicationError (TerminalError)
	var appErr *temporal.ApplicationError
	if errors.As(err, &appErr) {
		// Check both Type() and NonRetryable() flag
		if appErr.Type() == "TerminalError" || appErr.NonRetryable() {
			return true
		}
	}

	return false
}
