// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/workflow/core"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/types/known/structpb"
)

// StepExecutor handles all step execution with a unified lifecycle:
//  1. Evaluate config (CEL templates)
//  2. Start execution (returns future for async steps)
//  3. Handle completion (normalize output, save_message, create event)
//
// This consolidates scattered logic from workflow.go into a cohesive abstraction.
type StepExecutor struct {
	ctx            workflow.Context
	workflowID     string
	chatID         string
	workflowName   string
	workflowInputs map[string]interface{}
	nodeOutputs    map[string]interface{}
	childTracker   *ChildWorkflowTracker
	logger         log.Logger
	// loopNodeID is set when executing within a loop to track which loop spawned children.
	loopNodeID string
	// loopIteration is the 0-indexed iteration within the loop.
	loopIteration int
	// threadTracker tracks threads for runtime thread mapping
	threadTracker *ThreadTracker
	// execContext is the unified execution context (thread, message, loop, parent).
	execContext *ExecutionContext
	// projectPath for loading presets in spawned workflows
	projectPath string
	// workflow is the workflow definition (for introspection of other nodes)
	workflow *reliantv1.Workflow
	// pauseCtrl bundles pause-checking and cancellable-context callbacks.
	// All methods are nil-receiver safe so callers never need a nil guard.
	pauseCtrl *PauseController
	// makeThreadPauseCtrl creates per-thread PauseControllers for pause-aware execution.
	// Used by spawn support to create pause-aware controllers for child threads.
	makeThreadPauseCtrl func(string) *PauseController
}

// NewStepExecutor creates a new StepExecutor with all required context.
func NewStepExecutor(
	ctx workflow.Context,
	workflowID string,
	chatID string,
	workflowName string,
	workflowInputs map[string]interface{},
	nodeOutputs map[string]interface{},
	childTracker *ChildWorkflowTracker,
) *StepExecutor {
	return &StepExecutor{
		ctx:            ctx,
		workflowID:     workflowID,
		chatID:         chatID,
		workflowName:   workflowName,
		workflowInputs: workflowInputs,
		nodeOutputs:    nodeOutputs,
		childTracker:   childTracker,
		logger:         workflow.GetLogger(ctx),
	}
}

// WithLoopContext sets the loop node ID and iteration for child workflows and activities spawned within this executor.
// When set, child workflows will have spawned_by_node_id set to this loop ID, and activities will have loop context recorded.
func (e *StepExecutor) WithLoopContext(loopID string, iteration int) *StepExecutor {
	e.loopNodeID = loopID
	e.loopIteration = iteration
	return e
}

// WithThreadTracker sets the thread tracker for recording thread resolutions.
func (e *StepExecutor) WithThreadTracker(tracker *ThreadTracker) *StepExecutor {
	e.threadTracker = tracker
	return e
}

// WithExecContext sets the unified execution context.
// This is the source of truth for execution state.
func (e *StepExecutor) WithExecContext(ctx *ExecutionContext) *StepExecutor {
	e.execContext = ctx
	return e
}

// WithProjectPath sets the project path for loading presets in spawned workflows.
func (e *StepExecutor) WithProjectPath(projectPath string) *StepExecutor {
	e.projectPath = projectPath
	return e
}

// WithWorkflow sets the workflow definition for node introspection (e.g., detecting expected response tools).
func (e *StepExecutor) WithWorkflow(wf *reliantv1.Workflow) *StepExecutor {
	e.workflow = wf
	return e
}

// WithPauseController sets the PauseController for pause-aware activity dispatch.
func (e *StepExecutor) WithPauseController(pc *PauseController) *StepExecutor {
	e.pauseCtrl = pc
	return e
}

// WithMakeThreadPauseCtrl sets the per-thread PauseController factory for spawn support.
func (e *StepExecutor) WithMakeThreadPauseCtrl(fn func(string) *PauseController) *StepExecutor {
	e.makeThreadPauseCtrl = fn
	return e
}

// getActivityCtx returns the context to use when dispatching activities.
// Delegates to PauseController.GetActivityCtx with e.ctx as fallback.
func (e *StepExecutor) getActivityCtx() workflow.Context {
	return e.pauseCtrl.GetActivityCtx(e.ctx)
}

// GetThread returns the current thread from execContext.
// Returns empty string if execContext is not set.
func (e *StepExecutor) GetThread() string {
	if e.execContext != nil {
		return e.execContext.Thread
	}
	return ""
}

// GetChatID returns the chat ID from execContext (preferred) or falls back to stored field.
func (e *StepExecutor) GetChatID() string {
	if e.execContext != nil {
		return e.execContext.ChatID
	}
	return e.chatID
}

// GetWorkflowID returns the workflow ID from execContext (preferred) or falls back to stored field.
func (e *StepExecutor) GetWorkflowID() string {
	if e.execContext != nil {
		return e.execContext.WorkflowID
	}
	return e.workflowID
}

// RunningStep tracks an in-flight step execution.
// Returned by Start(), used by HandleCompletion().
type RunningStep struct {
	ActivityID   string
	StepID       string
	ActivityName string              // For schema lookup (e.g., "CallLLM")
	Node         *reliantv1.Node     // Proto node
	Event        *core.WorkflowEvent // Triggering event
	Future       workflow.Future
	EvalResult   *reliantv1.Node // Resolved node for save_message thread resolution
}

// removeRunningStep removes a step from the slice using pointer comparison.
// Returns a new slice with the step removed. Safe for use with nil pointers.
func removeRunningStep(steps []*RunningStep, toRemove *RunningStep) []*RunningStep {
	for i, s := range steps {
		if s == toRemove { // pointer comparison - always safe
			return append(steps[:i], steps[i+1:]...)
		}
	}
	return steps
}

// StepEvent is the result of a completed step, ready for edge routing.
type StepEvent struct {
	ID             string
	WorkflowID     string
	ChatID         string
	WorkflowName   string
	StepID         string
	Data           map[string]interface{}
	Error          error
	RetryExhausted bool // Set when activity failed after all Temporal retries - needs pause before retry
}

// ToEvent converts StepEvent to the Event type used by state machine.
func (se *StepEvent) ToEvent() *core.WorkflowEvent {
	data := se.Data
	if se.Error != nil {
		data = map[string]interface{}{"error": se.Error.Error()}
	}
	return &core.WorkflowEvent{
		ID:           se.ID,
		WorkflowID:   se.WorkflowID,
		ChatID:       se.ChatID,
		WorkflowName: se.WorkflowName,
		StepID:       se.StepID,
		Data:         data,
	}
}

// EnsureStepEventRoutable validates whether a step event is safe to emit into edge routing.
//
// For inline loop and inline workflow executors, any step failure must stop execution
// immediately. Emitting failed step events into the routing engine can cause downstream CEL
// conditions to evaluate against incomplete error-shaped output maps and produce misleading
// "no such key" failures (masking the real activity failure).
func EnsureStepEventRoutable(stepEvent *StepEvent) error {
	if stepEvent == nil {
		return fmt.Errorf("step event is nil")
	}
	if stepEvent.Error != nil {
		return stepEvent.Error
	}
	return nil
}

// Start begins execution of a step and returns a RunningStep to track it.
// The step runs asynchronously - use HandleCompletion() to process the result.
// StepID (node.ID) is used as the canonical identifier for tracking.
func (e *StepExecutor) Start(triggeredStep *core.TriggeredNode) *RunningStep {
	node := triggeredStep.Node
	event := triggeredStep.Event

	// Use helpers for consistent context building
	iterCtx := model.BuildIterContext(e.loopIteration)

	// Evaluate node config (resolves all CEL expressions in-place)
	evalResult, err := EvaluateNodeConfig(
		node,
		e.nodeOutputs,
		e.workflowID,
		event.WorkflowName,
		e.workflowInputs,
		iterCtx,
		nil, // loopOutputs - not in a loop
		e.execContext,
	)
	if err != nil {
		e.logger.Error("Failed to evaluate step config", "error", err, "stepID", node.GetId())
		return e.startFailedStep(node, event, err)
	}

	// Determine step type and dispatch
	stepType := node.GetType()
	logging.Info("[StepExecutor] Starting step",
		"stepID", node.GetId(),
		"stepType", stepType,
	)

	var future workflow.Future
	var activityName string

	switch stepType {
	case model.NodeTypeWorkflow:
		// NOTE: workflow nodes are now handled inline by InlineWorkflowExecutor.
		// This case should never be reached - if it is, it's a bug in the calling code.
		e.logger.Error("[StepExecutor] workflow nodes should be handled inline, not via StepExecutor",
			"stepID", node.GetId(),
			"stepType", stepType,
		)
		future = e.executeFailActivity(fmt.Sprintf("workflow node %s should be handled inline", node.GetId()))
		activityName = "InlineWorkflowError"

	case model.NodeTypeRun:
		future, activityName = e.startRun(node, evalResult)

	case model.NodeTypeApproval:
		// NOTE: approval nodes are now handled inline by InlineWorkflowExecutor.
		// This case should never be reached - if it is, it's a bug in the calling code.
		e.logger.Error("[StepExecutor] approval nodes should be handled inline, not via StepExecutor",
			"stepID", node.GetId(),
			"stepType", stepType,
		)
		future = e.executeFailActivity(fmt.Sprintf("approval node %s should be handled inline", node.GetId()))
		activityName = "InlineApprovalError"

	case model.NodeTypeAskQuestion:
		future, activityName = e.startAskQuestion(node, evalResult)

	default:
		// All other types are activities (e.g., call_llm, save_message, execute_tools)
		if isActivityType(stepType) {
			future, activityName = e.startAction(node, evalResult)
		} else {
			e.logger.Error("[StepExecutor] Unknown step type", "stepType", stepType, "stepID", node.GetId())
			future = e.executeFailActivity("unknown step type: " + stepType)
			activityName = "UnknownStepType"
		}
	}

	// Use StepID as canonical identifier - ActivityID kept for logging/backwards compat
	return &RunningStep{
		ActivityID:   node.GetId(), // Set to StepID for backwards compat
		StepID:       node.GetId(),
		ActivityName: activityName,
		Node:         node,
		Event:        event,
		Future:       future,
		EvalResult:   evalResult, // Store for save_message thread resolution
	}
}

// HandleCompletion processes a completed step and returns the resulting event.
// This handles:
// - Extracting and normalizing output
// - Executing save_message if configured
// - Storing output in nodeOutputs
// - Creating the completion event
func (e *StepExecutor) HandleCompletion(running *RunningStep) *StepEvent {
	// Use StepID for event ID - it's unique within an iteration
	eventID := fmt.Sprintf("event-%s", running.StepID)
	return e.handleActivityCompletion(running, eventID)
}

// handleActivityCompletion processes a completed regular activity.
func (e *StepExecutor) handleActivityCompletion(running *RunningStep, eventID string) *StepEvent {
	rawOutput, err := e.getRawOutput(running)
	if err != nil {
		// Check for CanceledError - this happens when the shared activity context
		// was cancelled (e.g., due to pause). Return as a regular error; the caller
		// (workflow.go) checks pauseRequested to decide if this is pause-related.
		var canceledErr *temporal.CanceledError
		if errors.As(err, &canceledErr) {
			e.logger.Info("[StepExecutor] Activity cancelled (context cancelled)",
				"activityID", running.ActivityID,
				"stepID", running.StepID,
			)
			return &StepEvent{
				ID:           eventID,
				WorkflowID:   e.workflowID,
				ChatID:       e.chatID,
				WorkflowName: e.workflowName,
				StepID:       running.StepID,
				Error:        err,
			}
		}

		// Check for TimeoutError - this happens when activity exhausts retries due to:
		// - Heartbeat timeout (activity didn't heartbeat in time, possibly due to pause)
		// - Start-to-Close timeout (activity took too long)
		// - Schedule-to-Start timeout (activity waited too long in queue)
		var timeoutErr *temporal.TimeoutError
		if errors.As(err, &timeoutErr) {
			timeoutType := timeoutErr.TimeoutType().String()
			e.logger.Warn("[StepExecutor] Activity timed out after retry exhaustion",
				"activityID", running.ActivityID,
				"stepID", running.StepID,
				"timeoutType", timeoutType,
				"error", err,
			)
			// For heartbeat timeouts during pause, this means the activity didn't exit cleanly
			// within WorkerStopTimeout (2s). Log this for debugging but proceed with retry.
			return &StepEvent{
				ID:             eventID,
				WorkflowID:     e.workflowID,
				ChatID:         e.chatID,
				WorkflowName:   e.workflowName,
				StepID:         running.StepID,
				Error:          err,
				RetryExhausted: true, // Will trigger pause, then retry on resume
			}
		}

		// All other errors after retry exhaustion (e.g., ApplicationError from
		// rate limits, transient failures). These are retryable errors that
		// exhausted Temporal's retry budget — mark as RetryExhausted so the
		// workflow can auto-pause and retry on user resume.
		e.logger.Error("[StepExecutor] Activity failed after retry exhaustion",
			"activityID", running.ActivityID,
			"stepID", running.StepID,
			"error", err,
			"errorType", fmt.Sprintf("%T", err),
		)
		return &StepEvent{
			ID:             eventID,
			WorkflowID:     e.workflowID,
			ChatID:         e.chatID,
			WorkflowName:   e.workflowName,
			StepID:         running.StepID,
			Error:          err,
			RetryExhausted: true, // Will trigger pause, then retry on resume
		}
	}

	// Normalize output against schema
	normalizedOutput := e.normalizeOutput(rawOutput, running.ActivityName)

	// Store output for downstream steps
	if normalizedOutput != nil {
		e.nodeOutputs[running.StepID] = normalizedOutput
	}

	// Execute save_message if configured
	if running.Node != nil && running.Node.GetSaveMessage() != nil {
		e.executeSaveMessage(running, normalizedOutput)
	}

	return &StepEvent{
		ID:           eventID,
		WorkflowID:   e.workflowID,
		ChatID:       e.chatID,
		WorkflowName: e.workflowName,
		StepID:       running.StepID,
		Data:         normalizedOutput,
	}
}

func (e *StepExecutor) getRawOutput(running *RunningStep) (map[string]interface{}, error) {
	// The flexible data converter (see internal/temporal/data_converter.go)
	// handles proto-encoded payloads transparently: activities can return any
	// proto.Message and Temporal will decode it into map[string]interface{}
	// via JSON fallback.
	var rawOutput map[string]interface{}
	if err := running.Future.Get(e.ctx, &rawOutput); err != nil {
		return nil, err
	}
	return rawOutput, nil
}

// executeSaveMessage runs the inline SaveMessage activity if configured.
// Returns the SaveMessage output so it can be merged into the step output.
func (e *StepExecutor) executeSaveMessage(running *RunningStep, output map[string]interface{}) map[string]interface{} {
	node := running.Node
	if node.GetSaveMessage() == nil {
		return nil
	}

	e.logger.Info("[StepExecutor] Executing inline save_message",
		"stepID", node.GetId(),
	)

	// Build workflow context for CEL evaluation
	workflowContext := buildWorkflowContext(
		e.workflowID,
		e.workflowName,
		e.chatID,
		e.workflowInputs,
	)

	// Use thread from execution context
	if e.execContext != nil && e.execContext.Thread != "" {
		if inputs, ok := workflowContext[workflowContextKeyInputs].(map[string]interface{}); ok {
			inputs["thread"] = e.execContext.Thread
		} else {
			workflowContext[workflowContextKeyInputs] = map[string]interface{}{"thread": e.execContext.Thread}
		}
	}

	saveOutput, err := executeSaveMessageInline(
		e.ctx,
		node,
		output,
		workflowContext,
		e.nodeOutputs,
		e.chatID,
		e.workflowID,
		e.loopNodeID,
		e.loopIteration,
		e.execContext, // Pass execContext for thread.* namespace access
	)
	if err != nil {
		e.logger.Error("[StepExecutor] Inline save_message failed",
			"stepID", node.GetId(),
			"error", err,
		)
		// Don't fail the step - save_message failure is logged but execution continues
		return nil
	}
	return saveOutput
}

// normalizeOutput ensures activity output has all schema fields present.
func (e *StepExecutor) normalizeOutput(rawOutput map[string]interface{}, activityName string) map[string]interface{} {
	if rawOutput == nil {
		rawOutput = make(map[string]interface{})
	}

	rawOutput = withRequiredActivityOutputFields(activityName, rawOutput)

	defaults := schema.GetOutputDefaults(activityName)
	if defaults == nil {
		return rawOutput
	}

	normalized := make(map[string]interface{})
	for field, defaultValue := range defaults {
		normalized[field] = defaultValue
	}
	for field, value := range rawOutput {
		normalized[field] = value
	}

	e.logger.Debug("[StepExecutor] Normalized output",
		"activityName", activityName,
		"rawFields", len(rawOutput),
		"normalizedFields", len(normalized),
	)

	return normalized
}

func withRequiredActivityOutputFields(activityName string, output map[string]interface{}) map[string]interface{} {
	if len(output) == 0 {
		output = map[string]interface{}{}
	}

	setDefault := func(key string, value interface{}) {
		if _, exists := output[key]; !exists {
			output[key] = value
		}
	}

	normalizedName := strings.ToLower(strings.ReplaceAll(activityName, "_", ""))
	switch normalizedName {
	case "callllm", "v2callllm":
		setDefault("message", map[string]interface{}{})
		setDefault("thinking", map[string]interface{}{})
		setDefault("response_text", "")
		setDefault("tool_calls", []interface{}{})
		setDefault("token_count", 0)

		// Ensure message map has required nested keys with defaults.
		// Proto3 serialization omits zero-value fields, so we need to ensure
		// all keys that CEL expressions reference exist in the map.
		if msgMap, ok := output["message"].(map[string]interface{}); ok {
			if _, exists := msgMap["text"]; !exists {
				msgMap["text"] = ""
			}
			if _, exists := msgMap["role"]; !exists {
				msgMap["role"] = ""
			}
		}
	}

	return output
}

// ============================================================================
// Step Type Dispatchers
// ============================================================================

// startFailedStep returns a RunningStep that will immediately fail.
func (e *StepExecutor) startFailedStep(node *reliantv1.Node, event *core.WorkflowEvent, err error) *RunningStep {
	future := e.executeFailActivity(fmt.Sprintf("CEL evaluation failed for step %s: %v", node.GetId(), err))
	return &RunningStep{
		ActivityID:   node.GetId(), // Set to StepID for backwards compat
		StepID:       node.GetId(),
		ActivityName: "FailStep",
		Node:         node,
		Event:        event,
		Future:       future,
	}
}

// startAction starts an activity node.
func (e *StepExecutor) startAction(
	node *reliantv1.Node,
	evalResult *reliantv1.Node,
) (workflow.Future, string) {
	activityName := nodeTypeToActivityName(node.GetType())

	// Special handling for ExecuteTools - intercept "spawn" tool calls
	if node.GetType() == model.NodeTypeExecuteTools {
		// Enrich the evaluated node with response tool info from upstream call_llm nodes
		if expectedTools, schemas := e.detectResponseToolInfo(node); len(expectedTools) > 0 {
			etArgs := evalResult.GetExecuteTools()
			// Filter out unresolved templates from expected_response_tools.
			// The field is `repeated string` (not CelX), so ResolveCELFields skips it.
			// Templates like "{{inputs.response_tool_name}}" remain as literal strings.
			// Remove them so auto-detection from upstream call_llm can fill in the
			// correct resolved tool names.
			if etArgs != nil {
				resolved := filterUnresolvedTemplates(etArgs.GetExpectedResponseTools())
				etArgs.ExpectedResponseTools = resolved
			}
			if etArgs != nil && len(etArgs.GetExpectedResponseTools()) == 0 {
				etArgs.ExpectedResponseTools = expectedTools
			}
			if etArgs != nil && len(schemas) > 0 {
				if etArgs.ResponseToolSchemas == nil {
					etArgs.ResponseToolSchemas = make(map[string]*structpb.Struct)
				}
				for name, schema := range schemas {
					if _, exists := etArgs.ResponseToolSchemas[name]; !exists {
						if s, err := mapToStruct(schema); err == nil {
							etArgs.ResponseToolSchemas[name] = s
						}
					}
				}
			}
		}

		rtx := e.buildRuntimeContext(node)

		logging.Info("[StepExecutor] startAction ExecuteTools",
			"stepID", node.GetId(),
			"loopNodeID", rtx.LoopNodeID,
			"loopIteration", rtx.LoopIteration,
		)

		future := executeToolsWithSpawnSupport(
			e.ctx,
			e.activityOptions(node),
			rtx,
			evalResult,
			e.workflowInputs,
			e.childTracker,
			e.makeThreadPauseCtrl,
		)
		return future, activityName
	}

	// Build structured input: pass the proto Node directly for proper protojson roundtrip
	rtx := e.buildRuntimeContext(node)
	input := types.ActivityInput{Runtime: rtx, Node: evalResult}

	future := workflow.ExecuteActivity(e.activityOptions(node), activityName, input)
	return future, activityName
}

// startAskQuestion runs the signal-backed ask_question node flow.
func (e *StepExecutor) startAskQuestion(node *reliantv1.Node, evalResult *reliantv1.Node) (workflow.Future, string) {
	resultFuture, resultSettable := workflow.NewFuture(e.ctx)
	threadID := ""
	if e.execContext != nil {
		threadID = e.execContext.Thread
	}

	metadata := ""
	if args := model.GetAskQuestionArgs(evalResult); args != nil {
		metadata = model.CelStringValue(args.GetMetadata())
	}

	workflow.Go(e.ctx, func(gCtx workflow.Context) {
		output, err := executeAskQuestionSignalFlow(gCtx, askQuestionExecution{
			ChatID:        e.chatID,
			WorkflowID:    e.workflowID,
			ThreadID:      threadID,
			StepID:        node.GetId(),
			LoopNodeID:    e.loopNodeID,
			LoopIteration: e.loopIteration,
			Metadata:      metadata,
			Logger:        workflow.GetLogger(gCtx),
		})
		if err != nil {
			resultSettable.SetError(err)
			return
		}
		resultSettable.SetValue(output)
	})

	return resultFuture, "AskQuestion"
}

// startRun starts a run (shell command) activity.
func (e *StepExecutor) startRun(node *reliantv1.Node, evalResult *reliantv1.Node) (workflow.Future, string) {
	runArgs, _ := model.NodeArgsAsMap(evalResult)
	runInputs := copyMap(runArgs)
	runInputs["command"] = model.NodeCommand(evalResult)
	if logFile := model.NodeLogFile(evalResult); logFile != "" {
		runInputs["log_file"] = logFile
	}

	// V2_ExecuteRunStep expects a flat map with runtime fields
	runInputs["workflow_id"] = e.workflowID
	runInputs["chat_id"] = e.chatID
	runInputs["step_id"] = node.GetId()
	if e.loopNodeID != "" {
		runInputs["loop_node_id"] = e.loopNodeID
		runInputs["loop_iteration"] = e.loopIteration
	}

	// Daemon selector: node-level override takes priority over workflow-level default
	if node.GetDaemon() != nil {
		if lit := node.GetDaemon().GetLiteral(); lit != nil {
			runInputs["daemon_selector"] = map[string]interface{}{
				"id":     lit.GetId(),
				"name":   lit.GetName(),
				"type":   lit.GetType(),
				"labels": lit.GetLabels(),
			}
		} else if node.GetDaemon().GetExpr() != "" {
			celCtx := buildWorkflowCELContext(e.workflowID, e.workflowName, e.workflowInputs, e.nodeOutputs)
			ds, err := ResolveCelDaemonSelector(node.GetDaemon(), celCtx)
			if err != nil {
				logging.Warn("[StepExecutor] failed to resolve node-level daemon CEL expression", "stepID", node.GetId(), "error", err)
			} else if ds != nil {
				runInputs["daemon_selector"] = map[string]interface{}{
					"id":     ds.ID,
					"name":   ds.Name,
					"type":   ds.Type,
					"labels": ds.Labels,
				}
			}
		}
	} else if e.execContext != nil && e.execContext.DaemonSelector != nil {
		ds := e.execContext.DaemonSelector
		runInputs["daemon_selector"] = map[string]interface{}{
			"id":     ds.ID,
			"name":   ds.Name,
			"type":   ds.Type,
			"labels": ds.Labels,
		}
	}

	logging.Info("[StepExecutor] startRun",
		"stepID", node.GetId(),
		"loopNodeID", e.loopNodeID,
		"loopIteration", e.loopIteration,
	)

	future := workflow.ExecuteActivity(e.activityOptions(node), "ExecuteRunStep", runInputs)
	return future, "ExecuteRunStep"
}

// activityOptions returns standard activity options, with optional node timeout override.
func (e *StepExecutor) activityOptions(node *reliantv1.Node) workflow.Context {
	// Default to 30 days - we have our own timeout mechanism via heartbeats
	timeout := 30 * 24 * time.Hour
	if node != nil {
		if timeoutStr := model.TimeoutExpr(node); timeoutStr != "" {
			if parsed, err := time.ParseDuration(timeoutStr); err == nil {
				timeout = parsed
			}
		}
	}

	// HeartbeatTimeout controls how quickly Temporal detects a dead activity.
	// We heartbeat every 500ms (registry.go), so the server sees regular heartbeats.
	// Using 30s gives enough room for worker restarts (Air hot-reload: compile + boot
	// typically takes 5-15s) without Temporal marking the activity as dead.
	// Let Temporal auto-generate ActivityID for deterministic replay.
	return workflow.WithActivityOptions(e.getActivityCtx(), workflow.ActivityOptions{
		StartToCloseTimeout: timeout,
		HeartbeatTimeout:    30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    time.Minute,
			MaximumAttempts:    5,
		},
	})
}

// executeFailActivity returns a future that will fail with the given message.
func (e *StepExecutor) executeFailActivity(errorMsg string) workflow.Future {
	return workflow.ExecuteActivity(
		workflow.WithActivityOptions(e.getActivityCtx(), workflow.ActivityOptions{
			StartToCloseTimeout: time.Second,
		}),
		"FailStep",
		map[string]interface{}{
			"chat_id": e.chatID,
			"error":   errorMsg,
		},
	)
}

// ============================================================================
// Helpers
// ============================================================================

// copyMap creates a shallow copy of a map.
func copyMap(m map[string]interface{}) map[string]interface{} {
	if m == nil {
		return make(map[string]interface{})
	}
	result := make(map[string]interface{}, len(m))
	for k, v := range m {
		result[k] = v
	}
	return result
}

// buildRuntimeContext constructs a types.RuntimeContext from StepExecutor fields.
func (e *StepExecutor) buildRuntimeContext(node *reliantv1.Node) types.RuntimeContext {
	rtx := types.RuntimeContext{
		ChatID:     e.chatID,
		WorkflowID: e.workflowID,
		StepID:     node.GetId(),
	}

	// Thread from execution context
	if e.execContext != nil {
		rtx.Thread = e.execContext.Thread
	}

	// Loop context
	if e.loopNodeID != "" {
		rtx.LoopNodeID = e.loopNodeID
		rtx.LoopIteration = e.loopIteration
	}

	// SpawnedBy from parent context
	if e.execContext != nil && e.execContext.Parent != nil && e.execContext.Parent.StepPath != "" {
		rtx.SpawnedBy = e.execContext.Parent.StepPath
	}

	// Spawn depth from execution context
	if e.execContext != nil {
		rtx.SpawnDepth = e.execContext.SpawnDepth
	}

	// Project path
	if e.execContext != nil && e.execContext.ProjectPath != "" {
		rtx.ProjectPath = e.execContext.ProjectPath
	}

	// Daemon selector: node-level override takes priority over workflow-level default
	if node.GetDaemon() != nil {
		if lit := node.GetDaemon().GetLiteral(); lit != nil {
			rtx.DaemonSelector = &types.DaemonSelector{
				ID:     lit.GetId(),
				Name:   lit.GetName(),
				Type:   lit.GetType(),
				Labels: lit.GetLabels(),
			}
		} else if node.GetDaemon().GetExpr() != "" {
			celCtx := buildWorkflowCELContext(e.workflowID, e.workflowName, e.workflowInputs, e.nodeOutputs)
			ds, err := ResolveCelDaemonSelector(node.GetDaemon(), celCtx)
			if err != nil {
				logging.Warn("[StepExecutor] failed to resolve node-level daemon CEL expression", "stepID", node.GetId(), "error", err)
			} else if ds != nil {
				rtx.DaemonSelector = &types.DaemonSelector{
					ID:     ds.ID,
					Name:   ds.Name,
					Type:   ds.Type,
					Labels: ds.Labels,
				}
			}
		}
	} else if e.execContext != nil && e.execContext.DaemonSelector != nil {
		// Fall back to workflow-level daemon selector
		rtx.DaemonSelector = &types.DaemonSelector{
			ID:     e.execContext.DaemonSelector.ID,
			Name:   e.execContext.DaemonSelector.Name,
			Type:   e.execContext.DaemonSelector.Type,
			Labels: e.execContext.DaemonSelector.Labels,
		}
	}

	return rtx
}

// mapToStruct converts a map[string]interface{} to a protobuf Struct.
func mapToStruct(m map[string]interface{}) (*structpb.Struct, error) {
	jsonBytes, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	s := &structpb.Struct{}
	if err := s.UnmarshalJSON(jsonBytes); err != nil {
		return nil, err
	}
	return s, nil
}

// filterUnresolvedTemplates removes entries that still contain unresolved {{...}} template
// expressions. This handles the case where `repeated string` proto fields (like
// expected_response_tools) are not processed by ResolveCELFields, leaving template
// strings as literals.
func filterUnresolvedTemplates(items []string) []string {
	var resolved []string
	for _, item := range items {
		if !strings.Contains(item, "{{") {
			resolved = append(resolved, item)
		}
	}
	return resolved
}

// detectResponseToolInfo finds response_tool names and schemas from upstream call_llm nodes.
// Delegates to the shared helper in response_tool_helpers.go.
func (e *StepExecutor) detectResponseToolInfo(node *reliantv1.Node) ([]string, map[string]map[string]interface{}) {
	if e.workflow == nil {
		return nil, nil
	}

	// Get the tool_calls field from execute_tools args
	etArgs := model.GetExecuteToolsArgs(node)
	if etArgs == nil {
		return nil, nil
	}
	toolCallsArg := model.CelStringRaw(etArgs.GetToolCalls())
	if toolCallsArg == "" {
		return nil, nil
	}

	return detectResponseToolsFromWorkflow(toolCallsArg, e.workflow.GetNodes(), e.workflowInputs)
}
