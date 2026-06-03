// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/ptr"
	workflow_constants "github.com/reliant-labs/reliant/internal/workflow"
	"github.com/reliant-labs/reliant/internal/workflow/core"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"github.com/reliant-labs/reliant/internal/workflow/validation"
	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/types/known/structpb"
)

// reliantNamespace is a UUID v5 namespace for generating deterministic UUIDs
// This is a random UUID that serves as the namespace for all Reliant-generated UUIDs
var reliantNamespace = uuid.MustParse("f47ac10b-58cc-4372-a567-0e02b2c3d479")

// DeterministicWorkflowID generates a deterministic UUID based on parent workflow ID and a unique key.
// This is used for child workflows to ensure idempotency - if Temporal retries, we get the same ID.
// The key should be something stable across retries (e.g., tool_call_id, activity_id, step_id).
func DeterministicWorkflowID(parentWorkflowID, key string) string {
	return uuid.NewSHA1(reliantNamespace, []byte(parentWorkflowID+":"+key)).String()
}

// DeterministicThread generates a deterministic UUID for a thread path.
// Thread paths now correspond to child workflow UUIDs for message isolation.
func DeterministicThread(parentWorkflowID, key string) string {
	return DeterministicWorkflowID(parentWorkflowID, key)
}

// WorkflowInput contains everything needed to start a runtime workflow.
// ExecContext is the source of truth for execution state (thread, message, loop, parent).
// Inputs contains configuration parameters (model, temperature, tools, etc.)
type WorkflowInput struct {
	ChatID       string                 // Required: Chat this workflow operates on
	WorkflowName string                 // Required: Which workflow definition to load
	Inputs       map[string]interface{} // Configuration inputs (NOT trigger data)
	ExecContext  *ExecutionContext      // Required: Unified execution context
}

// ChatContext provides chat-level data for CEL evaluation (chat.id)
// NOTE: auto_approve was removed - use inputs.mode instead
type ChatContext struct {
	ID string `json:"id"`
}

// ModelContext provides model info for CEL evaluation
type ModelContext struct {
	MaxTokens     int    `json:"max_tokens"`
	ContextWindow int    `json:"context_window"`
	Name          string `json:"name"`
	ID            string `json:"id"`
}

// WorkflowInputData is a helper for building WorkflowInput.Inputs as map[string]interface{}
// Use NewWorkflowInputs() to create, then chain Set() calls to add inputs.
// All workflow inputs are dynamic - activities have strong types, workflows don't.
type WorkflowInputData struct {
	data map[string]interface{}
}

// NewWorkflowInputs creates a new WorkflowInputData builder
func NewWorkflowInputs() *WorkflowInputData {
	return &WorkflowInputData{data: make(map[string]interface{})}
}

// Set sets a key-value pair and returns the builder for chaining
func (d *WorkflowInputData) Set(key string, value interface{}) *WorkflowInputData {
	if value != nil {
		d.data[key] = value
	}
	return d
}

// SetIfNotEmpty sets a string value only if non-empty
func (d *WorkflowInputData) SetIfNotEmpty(key, value string) *WorkflowInputData {
	if value != "" {
		d.data[key] = value
	}
	return d
}

// ToMap returns the built map[string]interface{} for WorkflowInput.Inputs
func (d *WorkflowInputData) ToMap() map[string]interface{} {
	return d.data
}

// GetExecContext returns the ExecutionContext for this workflow input.
// Panics if ExecContext is nil - callers must ensure it's set.
func (input *WorkflowInput) GetExecContext() *ExecutionContext {
	if input.ExecContext == nil {
		panic("WorkflowInput.ExecContext is required")
	}
	return input.ExecContext
}

// NewChatContext creates chat context for CEL evaluation
func NewChatContext(chatID string) *ChatContext {
	return &ChatContext{ID: chatID}
}

// ChildWorkflowTracker tracks active child workflows for signal forwarding
// and per-thread input maps for thread-scoped param queries and updates.
type ChildWorkflowTracker struct {
	children     map[string]bool                   // Map of child workflow IDs that are currently active
	threadInputs map[string]map[string]interface{} // Map of thread -> subInputs for per-thread param access
}

// RegisterThreadInputs registers a thread's input map for later query/update access.
// The inputs map is the same pointer used by the inline executor, so updates are reflected live.
func (t *ChildWorkflowTracker) RegisterThreadInputs(thread string, inputs map[string]interface{}) {
	if t.threadInputs == nil {
		t.threadInputs = make(map[string]map[string]interface{})
	}
	t.threadInputs[thread] = inputs
}

// UnregisterThreadInputs removes a thread's input map when execution completes.
func (t *ChildWorkflowTracker) UnregisterThreadInputs(thread string) {
	delete(t.threadInputs, thread)
}

// GetThreadInputs returns the input map for a specific thread, or nil if not found.
func (t *ChildWorkflowTracker) GetThreadInputs(thread string) map[string]interface{} {
	if t.threadInputs == nil {
		return nil
	}
	return t.threadInputs[thread]
}

// GetAllThreadInputs returns a copy of all thread input maps for query handlers.
func (t *ChildWorkflowTracker) GetAllThreadInputs() map[string]map[string]interface{} {
	result := make(map[string]map[string]interface{}, len(t.threadInputs))
	for thread, inputs := range t.threadInputs {
		result[thread] = inputs
	}
	return result
}

// RunningInlineWorkflow tracks an in-flight inline workflow/agent execution.
// This enables parallel execution of workflow/agent steps using workflow.Go().
type RunningInlineWorkflow struct {
	StepID        string
	Node          *reliantv1.Node
	Event         *core.WorkflowEvent    // Triggering event
	DoneCh        workflow.Channel       // Completion signal channel
	Output        map[string]interface{} // Populated when done
	Error         error                  // Populated if execution failed
	EvalResult    *reliantv1.Node        // Resolved config for save_message resolution
	ChildExecCtx  *ExecutionContext      // Child execution context
	IsNodeRouting bool                   // True for node-routing routers (no child thread/context)
}

// removeRunningInlineWorkflow removes an inline workflow from the slice using pointer comparison.
// Returns a new slice with the workflow removed. Safe for use with nil pointers.
func removeRunningInlineWorkflow(workflows []*RunningInlineWorkflow, toRemove *RunningInlineWorkflow) []*RunningInlineWorkflow {
	for i, w := range workflows {
		if w == toRemove { // pointer comparison - always safe
			return append(workflows[:i], workflows[i+1:]...)
		}
	}
	return workflows
}

// DynamicWorkflow is the simplified runtime workflow engine.
// Philosophy: Keep workflows dead simple. Push complexity into activities.
//
// CEL Expression Evaluation:
// The workflow automatically evaluates CEL expressions in step inputs before execution.
// ALL string values are treated as CEL expressions. For literals, use quotes: "'literal'"
//
// CEL NAMESPACES:
//   - inputs.*             - Workflow input parameters
//   - workflow.*           - Workflow context (id, name, path, branch)
//   - nodes.<id>.*         - Outputs from previously completed nodes (nodes.call_llm.tool_calls)
//   - iter.*               - Loop iteration context (iter.iteration) - only inside loops
//   - output.*             - Current activity output (in save_message context)
//   - outputs.*            - Sub-workflow outputs (in loop while conditions)
//
// Examples:
//   - nodes.run_cmd.exit_code           → returns int (native type)
//   - nodes.run_cmd.exit_code == 0      → returns bool
//   - "'Error: ' + nodes.run_cmd.stderr" → returns string concatenation
//   - inputs.task                       → access workflow input parameters
//   - "'assistant'"                     → returns literal string
//
// See template_eval.go for implementation details.
// WorkflowResult contains the evaluated outputs from a workflow completion
type WorkflowResult struct {
	Outputs map[string]interface{} `json:"outputs"`
}

func DynamicWorkflow(ctx workflow.Context, input WorkflowInput) (result *WorkflowResult, retErr error) {
	logger := workflow.GetLogger(ctx)
	info := workflow.GetInfo(ctx)
	workflowID := info.WorkflowExecution.ID

	logger.Info("[Workflow Runtime] Starting",
		"workflowID", workflowID,
		"chatID", input.ChatID,
		"workflowName", input.WorkflowName,
	)

	// Track active child workflows for signal forwarding
	childTracker := &ChildWorkflowTracker{
		children: make(map[string]bool),
	}

	// NOTE: Signal handler and query handler are set up AFTER ApplyDefaults (below)
	// to ensure they reference the final input.Inputs map, not a pre-defaults copy.

	// Initialize inputs map if nil
	if input.Inputs == nil {
		input.Inputs = make(map[string]interface{})
	}

	// Get execution context (required)
	execCtx := input.GetExecContext()
	// Set workflow ID from Temporal (authoritative source)
	execCtx.WorkflowID = workflowID

	thread := execCtx.Thread
	forkedFromThread := execCtx.ForkedFrom

	logger.Info("[Workflow Runtime] Using execution context",
		"thread", thread,
		"mode", execCtx.ThreadMode,
		"forkedFrom", forkedFromThread,
	)

	// STEP 4: Defer cleanup for cancellation, errors, and completion
	parentWorkflowID := ""
	if execCtx.Parent != nil {
		parentWorkflowID = execCtx.Parent.WorkflowID
	}
	defer func() {
		handleWorkflowCompletion(ctx, workflowID, input.ChatID, input.WorkflowName, parentWorkflowID, thread, forkedFromThread, retErr)
	}()

	// STEP 5: Load workflow definition (YAML and JSON)
	loadedWf, err := loadWorkflowDefinition(ctx, input)
	if err != nil {
		// Notify user of the workflow parsing/loading error
		notifyWorkflowError(ctx, input.ChatID, workflowID, input.WorkflowName, "workflow_parse_error", err.Error())
		return nil, fmt.Errorf("failed to load workflow definition: %w", err)
	}

	// Parse the placeholder-resolved workflow to get input schemas
	wfWithPlaceholders, err := LoadWorkflow(loadedWf.WorkflowJSON)
	if err != nil {
		return nil, fmt.Errorf("parse workflow for validation: %w", err)
	}

	// STEP 5.5: Validate and apply defaults from workflow input schemas
	// This ensures all inputs with defaults are available for CEL evaluation
	// Use AllInputs() to include grouped inputs (e.g., Implementer.model, Reviewer.temperature)
	allSchemas := model.AllInputs(wfWithPlaceholders.GetInputs())
	if len(allSchemas) > 0 {
		// Preserve runtime-injected inputs before filtering
		// These are internal values that shouldn't be validated but are needed for CEL evaluation
		runtimeInputs := make(map[string]interface{})
		for key, value := range input.Inputs {
			if workflow_constants.RuntimeInjectedInputs[key] {
				runtimeInputs[key] = value
			}
		}

		// Filter out runtime-injected inputs before validation
		// These are internal values that shouldn't be validated against the workflow schema
		filteredInputs := make(map[string]interface{})
		for key, value := range input.Inputs {
			if !workflow_constants.RuntimeInjectedInputs[key] {
				filteredInputs[key] = value
			}
		}

		validationResult := validation.ValidateInputs(&reliantv1.Workflow{
			Name:   wfWithPlaceholders.GetName(),
			Inputs: wfWithPlaceholders.GetInputs(),
		}, filteredInputs)
		if validationResult.HasErrors() {
			errMsg := validationResult.Error()
			notifyWorkflowError(ctx, input.ChatID, workflowID, input.WorkflowName, "input_validation_error", errMsg)
			return nil, fmt.Errorf("workflow input validation failed: %s", errMsg)
		}

		// Apply defaults for any missing optional inputs (including grouped inputs)
		// ApplyDefaults produces nested structure directly for CEL access (inputs.agent.model)
		// IMPORTANT: Use original Inputs schema (with group structure) NOT allSchemas (flattened).
		// AllInputs() flattens groups to "GroupName.param" keys which ApplyDefaults can't reconstruct.
		// The original Inputs schema has group entries which ApplyDefaults handles correctly.
		input.Inputs = ApplyDefaultsForRuntime(filteredInputs, wfWithPlaceholders.GetInputs())

		// Restore runtime-injected inputs after ApplyDefaults
		// (they were filtered out for validation but are needed for CEL evaluation)
		for key, value := range runtimeInputs {
			input.Inputs[key] = value
		}

		logger.Debug("[Workflow Runtime] Applied input defaults",
			"workflowName", input.WorkflowName,
			"inputCount", len(input.Inputs),
			"schemaCount", len(allSchemas),
		)
	}

	// Inject runtime-provided values AFTER validation (these are internal, not user-provided)
	// These are needed by node inputs for CEL expressions like inputs.chat_id
	if input.Inputs["chat_id"] == nil || input.Inputs["chat_id"] == "" {
		input.Inputs["chat_id"] = input.ChatID
	}
	if input.Inputs["workflow_id"] == nil || input.Inputs["workflow_id"] == "" {
		input.Inputs["workflow_id"] = workflowID
	}
	// unique_activity_id will be set per-step, but we set a default here for any early access
	if input.Inputs["unique_activity_id"] == nil {
		input.Inputs["unique_activity_id"] = workflowID + "-init"
	}
	// Inject spawned_by from ExecutionContext.Parent.StepPath
	// This is used to prevent recursive spawn in child workflows
	if input.ExecContext != nil && input.ExecContext.Parent != nil && input.ExecContext.Parent.StepPath != "" {
		input.Inputs["spawned_by"] = input.ExecContext.Parent.StepPath
	}
	// Inject spawn_depth from ExecutionContext for depth-based spawn limiting
	if input.ExecContext != nil && input.ExecContext.SpawnDepth > 0 {
		input.Inputs["spawn_depth"] = input.ExecContext.SpawnDepth
	}

	// STEP 5.55: Set up signal handler and query handler NOW (after ApplyDefaults).
	// ApplyDefaults creates a NEW map, so we must set up handlers after it runs
	// to ensure they reference the final input.Inputs map.
	// Create a done channel to signal the handler to exit when workflow completes.
	// Without this, the signal handler goroutine blocks forever and prevents Temporal
	// from marking the workflow as completed.
	handlerDone := workflow.NewChannel(ctx)
	setupInputUpdateHandler(ctx, input.Inputs, childTracker, workflowID, handlerDone)
	defer func() {
		// Only send done signal if not cancelled - if cancelled, the handler
		// exits on its own via ctx.Err() check to avoid blocking on Send
		if ctx.Err() == nil {
			handlerDone.Send(ctx, true)
		}
	}()

	// NOTE: Pause/resume is handled via signals to the workflow.
	// The shared worker pool is always running; paused workflows block on a signal channel.

	// STEP 5.6: If workflow has templates, resolve them with actual inputs
	var wf *reliantv1.Workflow
	if loadedWf.HasTemplates {
		wf, err = ResolveAndParseWorkflow(loadedWf.YAML, input.Inputs)
		if err != nil {
			notifyWorkflowError(ctx, input.ChatID, workflowID, input.WorkflowName, "template_resolution_error", err.Error())
			return nil, fmt.Errorf("failed to resolve workflow templates: %w", err)
		}
		logger.Debug("[Workflow Runtime] Resolved workflow templates",
			"workflowName", input.WorkflowName,
		)
	} else {
		// No templates - use the placeholder-resolved workflow directly
		wf = wfWithPlaceholders
	}

	coreSemantics, err := CompileRuntimeSemantics(wf, input.WorkflowName)
	if err != nil {
		return nil, fmt.Errorf("compile runtime core semantics: %w", err)
	}

	// STEP 6: Initialize simplified state machine
	stateMachine := NewSimplifiedStateMachine(workflowID, wf)

	// STEP 6.05: Extract project path for preset loading in spawned workflows
	// Set on execCtx as the single source of truth - flows to all child executors
	projectPath := ""
	if path, ok := input.Inputs["project_path"].(string); ok {
		projectPath = path
		execCtx.ProjectPath = projectPath
	}

	// STEP 6.06: Evaluate workflow-level daemon selector
	// This sets the default daemon for all tool execution in this workflow.
	// Individual nodes can override with their own daemon field.
	if wf.Daemon != nil {
		celCtx := buildWorkflowCELContext(workflowID, input.WorkflowName, input.Inputs, nil)
		ds, err := ResolveCelDaemonSelector(wf.Daemon, celCtx)
		if err != nil {
			notifyWorkflowError(ctx, input.ChatID, workflowID, input.WorkflowName, "daemon_resolution_error", err.Error())
			return nil, fmt.Errorf("failed to resolve workflow daemon selector: %w", err)
		}
		if ds != nil {
			execCtx.DaemonSelector = ds
			logger.Info("[Workflow Runtime] Resolved daemon selector",
				"type", ds.Type,
				"name", ds.Name,
				"id", ds.ID,
			)
		}
	}

	// STEP 6.065: Session-level daemon fallback
	// If no workflow-level daemon was set, use the session's active daemon.
	// Priority: workflow daemon > session daemon > default resolution.
	if execCtx.DaemonSelector == nil {
		if sessionDaemonID, ok := input.Inputs["session_daemon_id"].(string); ok && sessionDaemonID != "" {
			execCtx.DaemonSelector = &DaemonSelectorValue{ID: sessionDaemonID}
			logger.Info("[Workflow Runtime] Using session daemon",
				"daemonID", sessionDaemonID,
			)
		}
	}

	// STEP 6.07: Preflight daemon check
	// If the workflow requires a daemon (run nodes, daemon tools, explicit daemon field),
	// verify that a daemon is available before starting execution. Fail fast with a
	// clear error message rather than failing mid-execution.
	preflightCfg := buildPreflightConfig()
	if RequiresDaemon(wf, preflightCfg) {
		preflightInput := map[string]interface{}{
			"chat_id": input.ChatID,
		}
		// Pass daemon selector if one was resolved at workflow level
		if execCtx.DaemonSelector != nil {
			preflightInput["daemon_selector"] = map[string]interface{}{
				"id":   execCtx.DaemonSelector.ID,
				"name": execCtx.DaemonSelector.Name,
				"type": execCtx.DaemonSelector.Type,
			}
		}
		// Also check session daemon from inputs
		if sessionDaemonID, ok := input.Inputs["session_daemon_id"].(string); ok && sessionDaemonID != "" {
			if _, hasDaemonSelector := preflightInput["daemon_selector"]; !hasDaemonSelector {
				preflightInput["daemon_selector"] = map[string]interface{}{
					"id": sessionDaemonID,
				}
			}
		}

		preflightCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			StartToCloseTimeout: 30 * time.Second,
			RetryPolicy: &temporal.RetryPolicy{
				MaximumAttempts: 1, // Don't retry — fail fast
			},
		})
		var preflightResult map[string]interface{}
		if err := workflow.ExecuteActivity(preflightCtx, "PreflightDaemonCheck", preflightInput).Get(ctx, &preflightResult); err != nil {
			notifyWorkflowError(ctx, input.ChatID, workflowID, input.WorkflowName, "daemon_unavailable", err.Error())
			return nil, fmt.Errorf("preflight daemon check failed: %w", err)
		}
		logger.Info("[Workflow Runtime] Preflight daemon check passed")
	}

	// STEP 6.1: Initialize thread tracker for runtime thread tracking
	threadTracker := NewThreadTracker()
	threadTracker.Mapping.RecordThreadResolution(ThreadRoot, thread)

	// Register thread status query handler
	err = workflow.SetQueryHandler(ctx, "get_thread_statuses", func() ([]*ThreadStatus, error) {
		return threadTracker.GetAllThreadStatuses(), nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to set thread liveness query handler: %w", err)
	}

	// Register workflow inputs query handler (for checking if params actually changed)
	err = workflow.SetQueryHandler(ctx, "get_workflow_inputs", func() (map[string]interface{}, error) {
		return input.Inputs, nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to set workflow inputs query handler: %w", err)
	}

	// Register per-thread inputs query handler.
	// Returns the subInputs map for a specific thread, or the root inputs if thread not found.
	err = workflow.SetQueryHandler(ctx, "get_thread_inputs", func(thread string) (map[string]interface{}, error) {
		if threadInputs := childTracker.GetThreadInputs(thread); threadInputs != nil {
			return threadInputs, nil
		}
		// Fall back to root inputs if thread not registered (e.g. root thread or completed)
		return input.Inputs, nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to set thread inputs query handler: %w", err)
	}

	// STEP 6.5: Notify UI that workflow has started and create workflow record
	var statusOpts *workflowStatusOpts
	if execCtx.Parent != nil || forkedFromThread != "" {
		// Extract title for child workflow thread updates
		// Prefer preset name over prompt for spawn tool calls
		title := ""
		if presetVal, ok := input.Inputs["preset"].(string); ok && presetVal != "" {
			// Use preset name as title for spawned workflows
			title = presetVal
		} else if promptVal, ok := input.Inputs["prompt"].(string); ok {
			title = promptVal
			if len(title) > 100 {
				title = title[:97] + "..."
			}
		}
		var loopIter *int64
		if execCtx.Loop != nil {
			iter := int64(execCtx.Loop.Iteration)
			loopIter = &iter
		}
		spawnedByNode := ""
		if execCtx.Parent != nil {
			spawnedByNode = execCtx.Parent.StepPath
		}
		// Determine thread title: use exec context's ThreadTitle if set,
		// otherwise default to the spawning node ID
		threadTitle := execCtx.ThreadTitle
		if threadTitle == "" && spawnedByNode != "" {
			threadTitle = spawnedByNode
		}
		statusOpts = &workflowStatusOpts{
			Title:           title,
			ThreadTitle:     threadTitle,
			SpawnedByNodeID: spawnedByNode,
			LoopIteration:   loopIter,
		}
	}
	notifyWorkflowStatus(ctx, input.ChatID, workflowID, input.WorkflowName, "started", parentWorkflowID, thread, statusOpts)

	// STEP 7: Construct initial event from workflow input
	// The initial event has StepID="" to indicate workflow start (matches "workflow.started" edge trigger)
	initialEvent := &core.WorkflowEvent{
		ID:           fmt.Sprintf("start-%s", workflowID),
		WorkflowID:   workflowID,
		ChatID:       input.ChatID,
		WorkflowName: input.WorkflowName,
		StepID:       "", // Empty StepID = workflow started
		Data:         input.Inputs,
	}

	events := []*core.WorkflowEvent{initialEvent}
	// Track outputs from completed nodes using NodeOutputStore for centralized management
	nodeOutputStore := NewNodeOutputStore(input.WorkflowName)
	nodeOutputs := nodeOutputStore.AsMap() // Backwards compatible - existing code can use the map directly

	// STEP 7.5: Initialize join state for synchronization nodes
	joinState := NewJoinState()
	joinState.InitializeJoins(wf)
	if len(joinState.Progress) > 0 {
		logger.Info("[Workflow Runtime] Initialized join nodes",
			"joinCount", len(joinState.Progress),
			"joinState", joinState.String())
	}

	// Track running steps (using RunningStep from StepExecutor)
	var runningSteps []*RunningStep

	// Track running inline workflows for parallel execution of workflow/agent steps
	var runningInlineWorkflows []*RunningInlineWorkflow

	// STEP 8: Set up signal-based pause infrastructure
	// Pause/resume coordination uses an epoch-based broadcast pattern.
	// A single resume-coordinator goroutine consumes the resume signal and
	// increments pauseEpoch. All goroutines (main loop + inline spawns) block
	// on workflow.Await() waiting for the epoch to advance, which wakes them
	// ALL — unlike resumeCh.Receive() which only unblocks one consumer.
	var pauseRequested bool
	var pauseEpoch int
	pauseCh := workflow.GetSignalChannel(ctx, "signal.pause")
	resumeCh := workflow.GetSignalChannel(ctx, "signal.resume")

	// Create a shared cancellable context for all activity dispatch.
	// One cancelAllActivities() call cancels every in-flight activity at any nesting
	// depth — including those in inline workflow executors and loop executors.
	activityCtx, cancelAllActivities := workflow.WithCancel(ctx)

	// getActivityCtx returns the current (possibly refreshed) cancellable context.
	// All executors call this when dispatching activities, so after resume they
	// automatically pick up the fresh context.
	getActivityCtx := func() workflow.Context {
		return activityCtx
	}

	// checkPause checks for a pending pause signal and blocks until resume if paused.
	// This is called at step boundaries to provide cooperative pause/resume.
	// Multiple goroutines can call this concurrently — they all block via
	// workflow.Await on the epoch counter rather than competing over a single
	// signal channel receive.
	checkPause := func(callerCtx workflow.Context) {
		// Non-blocking drain of any pending pause signals
		for pauseCh.ReceiveAsync(nil) {
			pauseRequested = true
		}
		if pauseRequested {
			// cancelAllActivities() is a Temporal SDK command (not a side effect)
			// and MUST execute during replay to maintain determinism.
			cancelAllActivities()
			if !workflow.IsReplaying(ctx) {
				logger.Info("[Workflow Runtime] Pause requested, cancelling activities and blocking until resume signal",
					"workflowID", workflowID,
				)
			}
			// Snapshot current epoch, then wait for the resume coordinator
			// to advance it. workflow.Await wakes ALL blocked goroutines.
			// IMPORTANT: callerCtx (not root ctx) is used here so that goroutines
			// spawned via workflow.Go() block on their own coroutine, avoiding
			// Temporal's "trying to block on coroutine which is already blocked" panic.
			myEpoch := pauseEpoch
			_ = workflow.Await(callerCtx, func() bool { return pauseEpoch > myEpoch })
			if !workflow.IsReplaying(ctx) {
				logger.Info("[Workflow Runtime] Resume signal received, continuing with fresh activity context",
					"workflowID", workflowID,
				)
			}
		}
	}

	// Background goroutine to listen for pause signals at any time.
	// This ensures the flag is set even while the workflow is blocked in waitForAnyCompletion().
	workflow.Go(ctx, func(gCtx workflow.Context) {
		for {
			pauseCh.Receive(gCtx, nil)
			pauseRequested = true
			// cancelAllActivities() is a Temporal SDK command (not a side effect)
			// and MUST execute during replay to maintain determinism.
			cancelAllActivities()
		}
	})

	// Resume coordinator goroutine: consumes the resume signal and broadcasts
	// to all paused goroutines by advancing the epoch counter and refreshing
	// the shared activity context.
	workflow.Go(ctx, func(gCtx workflow.Context) {
		for {
			resumeCh.Receive(gCtx, nil)
			pauseRequested = false
			// Always refresh the activity context and advance epoch for determinism.
			// These are Temporal SDK operations that must happen during replay too.
			activityCtx, cancelAllActivities = workflow.WithCancel(ctx)
			pauseEpoch++
			if !workflow.IsReplaying(gCtx) {
				logger.Info("[Workflow Runtime] Resume coordinator: broadcast resume to all goroutines",
					"workflowID", workflowID,
					"pauseEpoch", pauseEpoch,
				)
			}
		}
	})

	// requestPause triggers a self-pause from within the workflow.
	// Used by executors when a retryable error (like a rate limit) exhausts retries.
	requestPause := func() {
		pauseRequested = true
		cancelAllActivities()
	}

	// makeThreadPauseCtrl creates a per-thread PauseController that inherits
	// pause/activity-context from the shared one.
	makeThreadPauseCtrl := func(thread string) *PauseController {
		return &PauseController{
			CheckPause:    checkPause,
			ActivityCtxFn: getActivityCtx,
			RequestPause:  requestPause,
		}
	}

	// STEP 8.6: Create shared PauseController for all executors
	pauseCtrl := &PauseController{
		CheckPause:    checkPause,
		ActivityCtxFn: getActivityCtx,
		RequestPause:  requestPause,
	}

	// STEP 8.7: Create step executor for unified step lifecycle
	executor := NewStepExecutor(
		ctx,
		workflowID,
		input.ChatID,
		input.WorkflowName,
		input.Inputs,
		nodeOutputs,
		childTracker,
	).WithThreadTracker(threadTracker).
		WithExecContext(execCtx).
		WithProjectPath(projectPath).
		WithWorkflow(wf).
		WithPauseController(pauseCtrl).
		WithMakeThreadPauseCtrl(makeThreadPauseCtrl)

	// Create save message function for join nodes
	joinSaveMessageFunc := func(node *reliantv1.Node, output map[string]interface{}) {
		if node.GetSaveMessage() == nil {
			return
		}
		workflowContext := buildWorkflowContext(workflowID, input.WorkflowName, input.ChatID, input.Inputs)
		_, err := executeSaveMessageInline(
			ctx,
			node,
			output,
			workflowContext,
			nodeOutputs,
			input.ChatID,
			workflowID,
			"", // Join nodes don't have loop context
			0,
			nil, // No execContext for join nodes
		)
		if err != nil {
			logger.Error("[Workflow Runtime] Join save_message failed",
				"joinID", node.GetId(),
				"error", err,
			)
		}
	}

	// STEP 8.8: Initialize daemon-offline tracker.
	// Counts consecutive main-loop iterations where every daemon-targeted
	// activity returned "no daemon connected" and none succeeded. When the
	// streak meets DaemonOfflineHaltThreshold the workflow halts itself with
	// a terminal error carrying DaemonOfflineHaltMarker, so the frontend
	// surfaces a "Reconnect workspace" affordance instead of leaving the user
	// staring at a stuck thinking indicator.
	daemonOfflineTracker := NewDaemonOfflineTracker()

	// STEP 9: Main workflow loop
	for {
		// Yield to the Temporal scheduler to prevent deadlock detection during replay.
		// With large histories, replay can take several seconds of CPU-bound work.
		_ = workflow.Sleep(ctx, 0)

		// Check for workflow cancellation at the start of each iteration
		// This ensures we respond to CancelWorkflow requests promptly
		if ctx.Err() != nil {
			logger.Info("[Workflow Runtime] Cancelled, exiting main loop",
				"workflowID", workflowID,
				"chatID", input.ChatID,
			)
			return nil, ctx.Err()
		}

		// Reset per-turn daemon-offline observation flags. Any daemon-targeted
		// step completion observed BEFORE the next ObserveTurnBoundary call
		// (at the bottom of the iteration) contributes to THIS turn's verdict.
		daemonOfflineTracker.Reset()

		// Check for pause signal at step boundary
		checkPause(ctx)

		// STEP 9.1: Process events through join nodes first
		// This updates join state and may generate join completion events
		events = processJoinEvents(events, joinState, wf, workflowID, input.ChatID, input.WorkflowName, nodeOutputs, logger, joinSaveMessageFunc, workflow.Now(ctx))

		// Collect node router completion events before they're consumed by FindTriggeredNodes.
		// These need fallback dispatch if no edges match.
		var nodeRouterCompletionStepIDs []string
		for _, evt := range events {
			if evt.StepID != "" {
				if n := model.FindNode(wf, evt.StepID); n != nil && n.GetType() == model.NodeTypeRouter && model.IsNodeRouterMode(n) {
					nodeRouterCompletionStepIDs = append(nodeRouterCompletionStepIDs, evt.StepID)
				}
			}
		}

		// Find triggered steps from pending events
		triggeredSteps, err := stateMachine.FindTriggeredNodes(events, nodeOutputs, input.Inputs)
		if err != nil {
			return nil, fmt.Errorf("find triggered steps: %w", err)
		}
		events = nil // Clear processed events

		// For node routing routers: if edges produced triggered nodes, great. Otherwise,
		// dynamically dispatch to the selected_node from the router output.
		for _, routerStepID := range nodeRouterCompletionStepIDs {
			// Check if any triggered node came from an edge originating at this router
			edgeTriggered := false
			for _, step := range triggeredSteps {
				if step.Event != nil && step.Event.StepID == routerStepID {
					edgeTriggered = true
					break
				}
			}
			if edgeTriggered {
				continue
			}

			// No edges matched — dynamically dispatch to selected_node
			routerOutput, _ := nodeOutputs[routerStepID].(map[string]interface{})
			selectedNodeID, _ := routerOutput["selected_node"].(string)
			if selectedNodeID == "" {
				logger.Error("[Workflow Runtime] Node router completed but selected_node is empty and no edges matched",
					"routerStepID", routerStepID,
				)
				return nil, fmt.Errorf("node router %s completed with empty selected_node and no matching edges", routerStepID)
			}

			targetNode := model.FindNode(wf, selectedNodeID)
			if targetNode == nil {
				logger.Error("[Workflow Runtime] Node router selected_node not found in workflow",
					"routerStepID", routerStepID,
					"selectedNode", selectedNodeID,
				)
				return nil, fmt.Errorf("node router %s selected node %q not found in workflow", routerStepID, selectedNodeID)
			}

			logger.Info("[Workflow Runtime] Node router dynamic dispatch (no edges matched)",
				"routerStepID", routerStepID,
				"selectedNode", selectedNodeID,
			)
			triggeredSteps = append(triggeredSteps, &core.TriggeredNode{
				Node: targetNode,
				Event: &core.WorkflowEvent{
					ID:           fmt.Sprintf("node-router-dispatch-%s-%d", routerStepID, workflow.Now(ctx).UnixNano()),
					WorkflowID:   workflowID,
					ChatID:       input.ChatID,
					WorkflowName: input.WorkflowName,
					StepID:       routerStepID,
					Data:         routerOutput,
				},
			})
		}

		// Execute all triggered steps (activities or child workflows) in parallel
		// Filter out join steps and loop steps - they need special handling
		for _, step := range triggeredSteps {
			// Skip join steps - they are handled by processJoinEvents
			if step.Node.GetType() == model.NodeTypeJoin {
				continue
			}

			// Check node condition - if false, skip execution
			skipped, skipEvt, condErr := skipNodeIfConditionFalse(
				ctx, step.Node, nodeOutputs, input.Inputs,
				workflowID, input.ChatID, input.WorkflowName, logger,
			)
			if condErr != nil {
				return nil, condErr
			}
			if skipped {
				events = append(events, skipEvt)
				continue
			}

			// Handle loop steps - execute inline within this workflow
			if step.Node.GetType() == model.NodeTypeLoop {
				contract, contractErr := coreSemantics.RequireContractForNode(step.Node.GetId(), model.NodeTypeLoop)
				if contractErr != nil {
					return nil, contractErr
				}
				if contract.WorkflowIdentity == "" {
					return nil, fmt.Errorf("missing workflow identity in core semantics contract for loop node %q", step.Node.GetId())
				}

				loopArgs := model.GetLoopArgs(step.Node)
				logger.Info("[Workflow Runtime] ========== EXECUTING LOOP STEP (INLINE) ==========",
					"stepID", step.Node.GetId(),
					"while", model.DirectCelExpr(loopArgs.GetWhile()),
					"workflowIdentity", contract.WorkflowIdentity,
				)

				// Determine project path for the loop - check if node specifies one
				loopProjectPath := projectPath
				loopExecCtx := execCtx
				if p := loopArgs.GetProject(); p != nil && model.CelStringRaw(p.GetPath()) != "" {
					// Evaluate project path CEL expression
					evalResult, evalErr := EvaluateNodeConfig(
						step.Node,
						nodeOutputs,
						workflowID,
						input.WorkflowName,
						input.Inputs,
						nil, // Not in a nested loop
						nil, // loopOutputs - not in a loop
						execCtx,
					)
					if evalErr != nil {
						return nil, fmt.Errorf("failed to evaluate loop node project path for step %s: %w", step.Node.GetId(), evalErr)
					}
					if model.NodeProjectPath(evalResult) != "" {
						loopProjectPath = model.NodeProjectPath(evalResult)
						// Create a child context with the overridden project path
						loopExecCtx = execCtx.Clone()
						loopExecCtx.ProjectPath = loopProjectPath
						logger.Info("[Workflow Runtime] Loop using custom project path",
							"stepID", step.Node.GetId(),
							"projectPath", loopProjectPath,
						)
					}
				}

				// Create inline loop executor
				logger.Info("[SIGNAL_DEBUG] Creating loop executor from workflow.go",
					"stepID", step.Node.GetId(),
					"input.Inputs.addr", fmt.Sprintf("%p", input.Inputs),
					"input.Inputs.mode", input.Inputs["mode"],
				)
				loopExecutor, err := NewInlineLoopExecutor(
					ctx,
					workflowID,
					input.ChatID,
					input.WorkflowName,
					input.Inputs,
					nodeOutputs,
					childTracker,
					step,
				)
				if err != nil {
					logger.Error("[Workflow Runtime] Failed to create loop executor",
						"stepID", step.Node.GetId(),
						"error", err,
					)
					// Fail fast - loop executor creation errors should halt the workflow
					return nil, fmt.Errorf("failed to create loop executor for step %s: %w", step.Node.GetId(), err)
				}
				loopExecutor = loopExecutor.WithThreadTracker(threadTracker).
					WithExecContext(loopExecCtx).
					WithProjectPath(loopProjectPath).
					WithPauseController(makeThreadPauseCtrl(loopExecCtx.Thread)).
					WithMakeThreadPauseCtrl(makeThreadPauseCtrl).
					WithInvocationContract(contract)

				// Execute loop inline with retry-on-exhaustion support
				var loopOutput *reliantv1.LoopOutput
			retryLoop:
				for {
					var execErr error
					loopOutput, execErr = loopExecutor.Execute()
					if execErr != nil {
						// Handle CanceledError - activity inside loop was cancelled by pause.
						// Block until resume, then retry the loop execution.
						var canceledErr *temporal.CanceledError
						if errors.As(execErr, &canceledErr) {
							logger.Info("[Workflow Runtime] Loop cancelled (pause), blocking until resume then retrying",
								"stepID", step.Node.GetId(),
							)
							checkPause(ctx)
							// If root context is genuinely cancelled (CancelWorkflow, not just pause),
							// propagate the error instead of retrying to avoid an infinite loop.
							if ctx.Err() != nil {
								return nil, ctx.Err()
							}
							// Yield to scheduler after pause/resume to prevent deadlock detection during replay.
							_ = workflow.Sleep(ctx, 0)
							continue retryLoop
						}

						logger.Error("[Workflow Runtime] Loop step execution failed",
							"stepID", step.Node.GetId(),
							"error", execErr,
						)
						notifyWorkflowError(ctx, input.ChatID, workflowID, input.WorkflowName,
							"loop_execution_error",
							fmt.Sprintf("Loop step '%s' failed: %s", step.Node.GetId(), execErr.Error()))
						return nil, fmt.Errorf("loop step %s failed: %w", step.Node.GetId(), execErr)
					}

					// Success - break out of retry loop
					break retryLoop
				}

				// Store loop output for edge routing
				// Sub-workflow outputs are surfaced directly: nodes.loop_id.field
				// ProtoLoopOutputToMap handles both sequential and parallel loop output formats.
				loopOutputMap := model.ProtoLoopOutputToMap(loopOutput)
				nodeOutputStore.Set(step.Node.GetId(), loopOutputMap)

				// Execute save_message if configured on the loop node
				// Consistent with all other node types: save_message fires on completion.
				if step.Node.GetSaveMessage() != nil {
					_, err := ExecuteSaveMessageForNode(
						ctx,
						step.Node,
						loopOutputMap,
						nodeOutputs,
						workflowID,
						input.WorkflowName,
						input.ChatID,
						input.Inputs,
						execCtx,
						"", // Not inside a nested loop
						0,  // Not inside a nested loop
					)
					if err != nil {
						logger.Error("[Workflow Runtime] save_message failed for loop",
							"stepID", step.Node.GetId(),
							"error", err,
						)
						// Don't fail - save_message errors are logged but non-fatal
					}
				}

				// Update thread liveness - mark loop step as completed
				if threadTracker != nil {
					threadTracker.Mapping.MarkStepCompleted(step.Node.GetId())
				}

				// Create completion event for the loop
				events = append(events, &core.WorkflowEvent{
					ID:           fmt.Sprintf("loop-complete-%s-%d", step.Node.GetId(), workflow.Now(ctx).UnixNano()),
					WorkflowID:   workflowID,
					ChatID:       input.ChatID,
					WorkflowName: input.WorkflowName,
					StepID:       step.Node.GetId(),
					Data:         loopOutputMap,
				})
				continue
			}

			// Handle workflow steps - execute inline within this workflow
			// This replaces the old Temporal child workflow spawning
			if step.Node.GetType() == model.NodeTypeWorkflow {
				contract, contractErr := coreSemantics.RequireContractForNode(step.Node.GetId(), model.NodeTypeWorkflow)
				if contractErr != nil {
					return nil, contractErr
				}
				if contract.WorkflowIdentity == "" {
					return nil, fmt.Errorf("missing workflow identity in core semantics contract for workflow node %q", step.Node.GetId())
				}
				logger.Info("[Workflow Runtime] ========== EXECUTING WORKFLOW STEP (INLINE) ==========",
					"stepID", step.Node.GetId(),
					"stepType", string(step.Node.GetType()),
					"workflowIdentity", contract.WorkflowIdentity,
				)

				// Evaluate node config
				evalResult, err := EvaluateNodeConfig(
					step.Node,
					nodeOutputs,
					workflowID,
					input.WorkflowName,
					input.Inputs,
					nil, // Not in a loop
					nil, // loopOutputs - not in a loop
					execCtx,
				)
				if err != nil {
					logger.Error("[Workflow Runtime] Failed to evaluate workflow step config",
						"stepID", step.Node.GetId(),
						"error", err,
					)
					// Notify UI of the error
					notifyWorkflowError(ctx, input.ChatID, workflowID, input.WorkflowName,
						"config_evaluation_error",
						fmt.Sprintf("Step '%s' config evaluation failed: %s", step.Node.GetId(), err.Error()))
					// Fail fast - CEL evaluation errors should halt the workflow, not silently continue
					return nil, fmt.Errorf("step %s config evaluation failed: %w", step.Node.GetId(), err)
				}

				// Create inline workflow executor
				inlineExecutor, err := NewInlineWorkflowExecutor(
					ctx,
					workflowID,
					input.ChatID,
					input.WorkflowName,
					input.Inputs,
					nodeOutputs,
					childTracker,
					step.Node,
					evalResult,
					"", // No parent loop
					-1, // No loop iteration
				)
				if err != nil {
					logger.Error("[Workflow Runtime] Failed to create inline workflow executor",
						"stepID", step.Node.GetId(),
						"error", err,
					)
					// Notify UI of the error
					notifyWorkflowError(ctx, input.ChatID, workflowID, input.WorkflowName,
						"executor_creation_error",
						fmt.Sprintf("Failed to create executor for step '%s': %s", step.Node.GetId(), err.Error()))
					// Fail fast - executor creation errors should halt the workflow
					return nil, fmt.Errorf("failed to create executor for step %s: %w", step.Node.GetId(), err)
				}

				// Derive child context for the inline workflow (before launching so it's available for tracking)
				var childExecCtx *ExecutionContext
				inlineExecutor = inlineExecutor.WithInvocationContract(contract)
				childExecCtx = execCtx.ForChild(step.Node.GetId(), model.NodeThreadMode(evalResult), contract.WorkflowIdentity, true)

				// Create child thread and optionally save inject message
				// For non-inherit modes, this creates the thread with proper fork metadata
				if model.NodeThreadMode(evalResult) != model.ThreadModeInherit {
					var injectMsg *InjectMessageConfig
					if ic := model.NodeInjectConfig(evalResult); ic != nil && model.CelStringValue(ic.GetContent()) != "" {
						attIDs, attFiles := resolveInjectAttachments(ic, logger)
						injectMsg = &InjectMessageConfig{
							Role:        model.CelStringValue(ic.GetRole()),
							Content:     model.CelStringValue(ic.GetContent()),
							Attachments: attIDs,
							Files:       attFiles,
						}
					}

					// Determine thread title for child workflow: preset name > node ID
					var childThreadTitle *string
					if wfArgs := model.GetSubWorkflowArgs(step.Node); wfArgs != nil && len(wfArgs.GetPresets()) > 0 {
						if defaultPreset, ok := wfArgs.GetPresets()["default"]; ok && defaultPreset != "" {
							childThreadTitle = &defaultPreset
						}
					}
					if childThreadTitle == nil {
						nodeID := step.Node.GetId()
						childThreadTitle = &nodeID
					}

					if initErr := initChildWorkflow(ChildWorkflowInitOpts{
						Ctx:              ctx,
						ChatID:           input.ChatID,
						ParentWorkflowID: parentWorkflowID,
						ChildWorkflowID:  workflowID, // Inline workflows use parent's workflow ID
						ChildThreadID:    childExecCtx.Thread,
						WorkflowName:     contract.WorkflowIdentity,
						ThreadTitle:      childThreadTitle,
						ThreadMode:       model.NodeThreadMode(evalResult),
						ForkFromThread:   childExecCtx.ForkedFrom,
						ParentThread:     thread, // Current execution's thread
						SpawnedByNodeID:  step.Node.GetId(),
						InjectMessage:    injectMsg,
						Logger:           logger,
					}); initErr != nil {
						logger.Error("[Workflow Runtime] Failed to initialize child workflow thread",
							"stepID", step.Node.GetId(),
							"error", initErr,
						)
						return nil, fmt.Errorf("failed to initialize child workflow thread for step %s: %w", step.Node.GetId(), initErr)
					}
				} else if ic := model.NodeInjectConfig(evalResult); ic != nil && model.CelStringValue(ic.GetContent()) != "" {
					// For inherit mode, just save the inject message (thread already exists)
					activityCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
						StartToCloseTimeout: 30 * time.Second,
						RetryPolicy: &temporal.RetryPolicy{
							InitialInterval:    time.Second,
							BackoffCoefficient: 2.0,
							MaximumInterval:    10 * time.Second,
							MaximumAttempts:    3,
						},
					})
					attIDs, attFiles := resolveInjectAttachments(ic, logger)
					flatInput := &types.SaveMessageInput{
						ChatID:      input.ChatID,
						Thread:      childExecCtx.Thread,
						Role:        model.CelStringValue(ic.GetRole()),
						Content:     model.CelStringValue(ic.GetContent()),
						Attachments: attIDs,
						InjectFiles: injectFilesToData(attFiles),
						WorkflowID:  workflowID,
					}
					rtx := types.RuntimeContext{
						ChatID:     input.ChatID,
						Thread:     childExecCtx.Thread,
						WorkflowID: workflowID,
					}
					saveInput := types.ActivityInput{Runtime: rtx, Node: buildSaveMessageNode(flatInput)}
					if err := workflow.ExecuteActivity(activityCtx, "SaveMessage", saveInput).Get(ctx, nil); err != nil {
						logger.Error("[Workflow Runtime] Failed to save inject message to inherited thread",
							"stepID", step.Node.GetId(),
							"thread", childExecCtx.Thread,
							"error", err,
						)
						return nil, fmt.Errorf("failed to save inject message for step %s: %w", step.Node.GetId(), err)
					}
					logger.Info("[Workflow Runtime] Pre-saved inject message to inherited thread",
						"stepID", step.Node.GetId(),
						"thread", childExecCtx.Thread,
					)
				}

				// Override project path if the node specifies one
				// This allows sub-workflows to run in a different working directory
				childProjectPath := projectPath
				if model.NodeProjectPath(evalResult) != "" {
					childProjectPath = model.NodeProjectPath(evalResult)
					childExecCtx.ProjectPath = childProjectPath
				}
				inlineExecutor = inlineExecutor.WithThreadTracker(threadTracker).
					WithExecContext(childExecCtx).
					WithProjectPath(childProjectPath).
					WithPauseController(makeThreadPauseCtrl(childExecCtx.Thread)).
					WithMakeThreadPauseCtrl(makeThreadPauseCtrl)
				// Start workflow execution in parallel using workflow.Go()
				// This enables multiple workflow/agent steps to run concurrently
				doneCh := workflow.NewChannel(ctx)
				running := &RunningInlineWorkflow{
					StepID:       step.Node.GetId(),
					Node:         step.Node,
					Event:        step.Event,
					DoneCh:       doneCh,
					EvalResult:   evalResult,
					ChildExecCtx: childExecCtx,
				}

				// Capture variables for the goroutine closure
				executorCopy := inlineExecutor
				runningCopy := running

				workflow.Go(ctx, func(gCtx workflow.Context) {
					// CRITICAL: Override the workflow context to use the goroutine's context.
					// Each workflow.Go() goroutine must use its own context for blocking operations
					// to avoid "trying to block on coroutine which is already blocked" errors.
					executorCopy = executorCopy.WithWorkflowContext(gCtx)

					// Execute the inline workflow
					output, execErr := executorCopy.Execute()
					runningCopy.Output = output
					runningCopy.Error = execErr
					// Signal completion
					runningCopy.DoneCh.Send(gCtx, true)
				})

				// Track the running inline workflow for later completion handling
				runningInlineWorkflows = append(runningInlineWorkflows, running)
				logger.Info("[Workflow Runtime] Started inline workflow in parallel",
					"stepID", step.Node.GetId(),
					"totalRunningInline", len(runningInlineWorkflows),
				)
				continue
			}

			// Handle node routing routers - LLM-driven node selection (no child thread/sub-workflow)
			if step.Node.GetType() == model.NodeTypeRouter && model.IsNodeRouterMode(step.Node) {
				logger.Info("[Workflow Runtime] ========== EXECUTING NODE ROUTER STEP ===========",
					"stepID", step.Node.GetId(),
				)

				// Evaluate node config (resolve CEL expressions)
				evalResult, err := EvaluateNodeConfig(
					step.Node,
					nodeOutputs,
					workflowID,
					input.WorkflowName,
					input.Inputs,
					nil, // Not in a loop
					nil, // loopOutputs - not in a loop
					execCtx,
				)
				if err != nil {
					logger.Error("[Workflow Runtime] Failed to evaluate node router step config",
						"stepID", step.Node.GetId(),
						"error", err,
					)
					notifyWorkflowError(ctx, input.ChatID, workflowID, input.WorkflowName,
						"config_evaluation_error",
						fmt.Sprintf("Node router step '%s' config evaluation failed: %s", step.Node.GetId(), err.Error()))
					return nil, fmt.Errorf("node router step %s config evaluation failed: %w", step.Node.GetId(), err)
				}

				// Create router executor — node routing only needs CallLLM, no child thread/context
				routerExec := NewRouterExecutor(
					ctx,
					workflowID,
					input.ChatID,
					input.WorkflowName,
					input.Inputs,
					nodeOutputs,
					childTracker,
					step.Node,
					evalResult,
				)

				// Node routing needs the current execution context so the synthetic CallLLM
				// activity can load conversation history from the active thread, but it does
				// not create child threads or use the thread tracker.
				routerExec = routerExec.
					WithExecContext(execCtx).
					WithPauseController(makeThreadPauseCtrl(execCtx.Thread))

				// Launch in workflow.Go for proper Temporal activity context
				doneCh := workflow.NewChannel(ctx)
				running := &RunningInlineWorkflow{
					StepID:        step.Node.GetId(),
					Node:          step.Node,
					Event:         step.Event,
					DoneCh:        doneCh,
					EvalResult:    evalResult,
					IsNodeRouting: true,
				}

				routerCopy := routerExec
				runningCopy := running

				workflow.Go(ctx, func(gCtx workflow.Context) {
					routerCopy = routerCopy.WithWorkflowContext(gCtx)
					output, execErr := routerCopy.Execute()
					runningCopy.Output = output
					runningCopy.Error = execErr
					runningCopy.DoneCh.Send(gCtx, true)
				})

				runningInlineWorkflows = append(runningInlineWorkflows, running)
				logger.Info("[Workflow Runtime] Started node router in parallel",
					"stepID", step.Node.GetId(),
					"totalRunningInline", len(runningInlineWorkflows),
				)
				continue
			}

			// Handle workflow routing routers - LLM-driven workflow selection and execution
			if step.Node.GetType() == model.NodeTypeRouter {
				logger.Info("[Workflow Runtime] ========== EXECUTING ROUTER STEP ===========",
					"stepID", step.Node.GetId(),
				)

				// Evaluate node config (resolve CEL expressions)
				evalResult, err := EvaluateNodeConfig(
					step.Node,
					nodeOutputs,
					workflowID,
					input.WorkflowName,
					input.Inputs,
					nil, // Not in a loop
					nil, // loopOutputs - not in a loop
					execCtx,
				)
				if err != nil {
					logger.Error("[Workflow Runtime] Failed to evaluate router step config",
						"stepID", step.Node.GetId(),
						"error", err,
					)
					notifyWorkflowError(ctx, input.ChatID, workflowID, input.WorkflowName,
						"config_evaluation_error",
						fmt.Sprintf("Router step '%s' config evaluation failed: %s", step.Node.GetId(), err.Error()))
					return nil, fmt.Errorf("router step %s config evaluation failed: %w", step.Node.GetId(), err)
				}

				// Create router executor
				routerExec := NewRouterExecutor(
					ctx,
					workflowID,
					input.ChatID,
					input.WorkflowName,
					input.Inputs,
					nodeOutputs,
					childTracker,
					step.Node,
					evalResult,
				)

				// Derive child execution context
				childThreadMode := routerThreadMode(evalResult)
				childIdentity := routerWorkflowIdentity(step.Node)
				childExecCtx := execCtx.ForChild(step.Node.GetId(), childThreadMode, childIdentity, true)

				// Initialize child thread for non-inherit modes
				if childThreadMode != model.ThreadModeInherit {
					nodeID := step.Node.GetId()
					if initErr := initChildWorkflow(ChildWorkflowInitOpts{
						Ctx:              ctx,
						ChatID:           input.ChatID,
						ParentWorkflowID: parentWorkflowID,
						ChildWorkflowID:  workflowID,
						ChildThreadID:    childExecCtx.Thread,
						WorkflowName:     childIdentity,
						ThreadTitle:      &nodeID,
						ThreadMode:       childThreadMode,
						ForkFromThread:   childExecCtx.ForkedFrom,
						ParentThread:     thread,
						SpawnedByNodeID:  step.Node.GetId(),
						Logger:           logger,
					}); initErr != nil {
						logger.Error("[Workflow Runtime] Failed to initialize router child thread",
							"stepID", step.Node.GetId(),
							"error", initErr,
						)
						return nil, fmt.Errorf("failed to initialize router child thread for step %s: %w", step.Node.GetId(), initErr)
					}
				}

				// Override project path if needed
				childProjectPath := projectPath
				if model.NodeProjectPath(evalResult) != "" {
					childProjectPath = model.NodeProjectPath(evalResult)
					childExecCtx.ProjectPath = childProjectPath
				}

				routerExec = routerExec.
					WithExecContext(childExecCtx).
					WithProjectPath(childProjectPath).
					WithThreadTracker(threadTracker).
					WithPauseController(makeThreadPauseCtrl(childExecCtx.Thread)).
					WithMakeThreadPauseCtrl(makeThreadPauseCtrl)

				// Launch router execution in parallel (same pattern as workflow nodes)
				doneCh := workflow.NewChannel(ctx)
				running := &RunningInlineWorkflow{
					StepID:       step.Node.GetId(),
					Node:         step.Node,
					Event:        step.Event,
					DoneCh:       doneCh,
					EvalResult:   evalResult,
					ChildExecCtx: childExecCtx,
				}

				routerCopy := routerExec
				runningCopy := running

				workflow.Go(ctx, func(gCtx workflow.Context) {
					routerCopy = routerCopy.WithWorkflowContext(gCtx)
					output, execErr := routerCopy.Execute()
					runningCopy.Output = output
					runningCopy.Error = execErr
					runningCopy.DoneCh.Send(gCtx, true)
				})

				runningInlineWorkflows = append(runningInlineWorkflows, running)
				logger.Info("[Workflow Runtime] Started router workflow in parallel",
					"stepID", step.Node.GetId(),
					"totalRunningInline", len(runningInlineWorkflows),
				)
				continue
			}

			logger.Info("[Workflow Runtime] ========== EXECUTING STEP ==========",
				"stepID", step.Node.GetId(),
				"type", string(step.Node.GetType()))

			// Start step execution via StepExecutor
			running := executor.Start(step)
			runningSteps = append(runningSteps, running)
		}

		// If no running steps (activities or inline workflows) and no events, we're done!
		if len(runningSteps) == 0 && len(runningInlineWorkflows) == 0 && len(events) == 0 {
			logger.Info("[Workflow Runtime] Completed - evaluating outputs")

			// Build workflow context for output evaluation
			workflowContext := buildWorkflowContext(workflowID, input.WorkflowName, input.ChatID, input.Inputs)

			// Evaluate workflow outputs
			outputs, err := EvaluateWorkflowOutputs(wf.Outputs, nodeOutputs, workflowContext)
			if err != nil {
				logger.Error("[Workflow Runtime] Failed to evaluate outputs", "error", err)
				return nil, fmt.Errorf("failed to evaluate workflow outputs: %w", err)
			}

			return &WorkflowResult{Outputs: outputs}, nil
		}

		// Wait for at least one step (activity or inline workflow) to complete
		if len(runningSteps) > 0 || len(runningInlineWorkflows) > 0 {
			completedSteps, completedInline := waitForAnyCompletion(ctx, runningSteps, runningInlineWorkflows)

			// Check for pause signal after completion before processing results
			checkPause(ctx)

			// Process completed activity steps via StepExecutor
			for _, running := range completedSteps {
				stepEvent := executor.HandleCompletion(running)
				runningSteps = removeRunningStep(runningSteps, running)

				// Handle CanceledError - activity was cancelled by the shared activityCtx
				// (e.g., due to pause). Block until resume, then re-start the step.
				if stepEvent.Error != nil {
					var canceledErr *temporal.CanceledError
					if errors.As(stepEvent.Error, &canceledErr) {
						logger.Info("[Workflow Runtime] Activity cancelled (pause), blocking until resume then retrying",
							"stepID", running.StepID,
						)
						checkPause(ctx)
						// If root context is genuinely cancelled (CancelWorkflow, not just pause),
						// propagate the error instead of retrying to avoid an infinite loop.
						if ctx.Err() != nil {
							return nil, ctx.Err()
						}
						// Yield to scheduler after pause/resume to prevent deadlock detection during replay.
						_ = workflow.Sleep(ctx, 0)
						// Resumed - re-start the step with fresh activity context
						triggeredNode := &core.TriggeredNode{
							Node:  running.Node,
							Event: running.Event,
						}
						newRunning := executor.Start(triggeredNode)
						runningSteps = append(runningSteps, newRunning)
						continue
					}
				}

				// Handle retry exhaustion - pause workflow and retry on resume
				if stepEvent.RetryExhausted {
					logger.Info("[Workflow Runtime] *** RETRY EXHAUSTION DETECTED *** Activity exhausted retries, triggering pause",
						"stepID", running.StepID,
						"activityID", running.ActivityID,
						"error", stepEvent.Error,
					)

					// Emit error to UI so user knows what happened
					errorCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
						StartToCloseTimeout: 30 * time.Second,
						RetryPolicy: &temporal.RetryPolicy{
							MaximumAttempts: 3,
						},
					})
					var errorResult map[string]interface{}
					errStr := stepEvent.Error.Error()
					errorPayload := map[string]interface{}{
						"chat_id":       input.ChatID,
						"workflow_id":   workflowID,
						"workflow_name": input.WorkflowName,
						"error_message": humanizeRetryError(running.StepID, stepEvent.Error),
						"error_type":    "retry_exhaustion",
					}
					if summary := extractLLMErrorSummary(errStr); summary != "" {
						errorPayload["error_summary"] = summary + ". Workflow paused — send a message to retry."
					}
					_ = workflow.ExecuteActivity(errorCtx, "WorkflowError", errorPayload).Get(ctx, &errorResult)

					// Update DB status to paused so the UI reflects it and SendMessage
					// routes through the resume path instead of starting a new workflow.
					notifyWorkflowStatus(ctx, input.ChatID, workflowID, input.WorkflowName, "paused", parentWorkflowID, thread, nil)

					// Signal-based pause: set the pause flag directly and block until resume.
					pauseRequested = true
					checkPause(ctx)
					// Yield to scheduler after pause/resume to prevent deadlock detection during replay.
					_ = workflow.Sleep(ctx, 0)

					// Resumed! Update DB status back to running.
					notifyWorkflowStatus(ctx, input.ChatID, workflowID, input.WorkflowName, "started", parentWorkflowID, thread, nil)

					logger.Info("[Workflow Runtime] Resumed after pause, retrying step",
						"stepID", running.StepID,
					)

					triggeredNode := &core.TriggeredNode{
						Node:  running.Node,
						Event: running.Event,
					}
					newRunning := executor.Start(triggeredNode)
					runningSteps = append(runningSteps, newRunning)
					continue
				}

				events = append(events, stepEvent.ToEvent())

				// Observe daemon-offline signals from this completed step.
				// - Step error: e.g. ExecuteRunStep returns the daemon-offline
				//   error directly when RemoteRunExecutor can't reach the daemon.
				// - Step output: ExecuteTools wraps daemon-offline into the
				//   tool_results array (the activity itself succeeds), so we
				//   scan the payload for daemon-offline markers.
				if stepEvent.Error != nil {
					daemonOfflineTracker.ObserveStepError(stepEvent.Error)
				} else {
					daemonOfflineTracker.ObserveStepOutput(stepEvent.StepID, stepEvent.Data)
					// Run steps (ExecuteRunStep) return non-tool_results output
					// shapes when successful — record them as explicit daemon
					// liveness signals so a successful run step resets the streak.
					if running.ActivityName == "ExecuteRunStep" {
						daemonOfflineTracker.ObserveDaemonSuccess(running.StepID)
					}
				}

				// Update thread liveness - mark step as completed
				if threadTracker != nil && running.StepID != "" {
					threadTracker.Mapping.MarkStepCompleted(running.StepID)
				}
			}

			// Process completed inline workflows
			for _, running := range completedInline {
				logger.Info("[Workflow Runtime] Inline workflow completed",
					"stepID", running.StepID,
					"hasError", running.Error != nil,
				)

				// Remove from tracking slice using pointer comparison
				runningInlineWorkflows = removeRunningInlineWorkflow(runningInlineWorkflows, running)

				// Handle errors from inline workflow execution
				if running.Error != nil {
					// Handle CanceledError - activity inside inline workflow was cancelled by pause.
					// Block until resume, then re-launch the inline workflow.
					var canceledErr *temporal.CanceledError
					if errors.As(running.Error, &canceledErr) {
						logger.Info("[Workflow Runtime] Inline workflow cancelled (pause), blocking until resume then retrying",
							"stepID", running.StepID,
						)
						checkPause(ctx)
						// If root context is genuinely cancelled (CancelWorkflow, not just pause),
						// propagate the error instead of retrying to avoid an infinite loop.
						if ctx.Err() != nil {
							return nil, ctx.Err()
						}
						// Yield to scheduler after pause/resume to prevent deadlock detection during replay.
						_ = workflow.Sleep(ctx, 0)
						// Resumed - re-launch the inline workflow with fresh activity context
						doneCh := workflow.NewChannel(ctx)
						retryRunning := &RunningInlineWorkflow{
							StepID:        running.StepID,
							Node:          running.Node,
							Event:         running.Event,
							DoneCh:        doneCh,
							EvalResult:    running.EvalResult,
							ChildExecCtx:  running.ChildExecCtx,
							IsNodeRouting: running.IsNodeRouting,
						}

						// Distinguish between router nodes and regular workflow nodes
						nodeType := running.Node.GetType()
						if nodeType == model.NodeTypeRouter && running.IsNodeRouting {
							// Re-create RouterExecutor for node routing routers on the current thread.
							routerExec := NewRouterExecutor(
								ctx,
								workflowID,
								input.ChatID,
								input.WorkflowName,
								input.Inputs,
								nodeOutputs,
								childTracker,
								running.Node,
								running.EvalResult,
							)
							routerExec = routerExec.
								WithExecContext(execCtx).
								WithPauseController(makeThreadPauseCtrl(execCtx.Thread))

							routerCopy := routerExec
							runningCopy := retryRunning
							workflow.Go(ctx, func(gCtx workflow.Context) {
								routerCopy = routerCopy.WithWorkflowContext(gCtx)
								output, execErr := routerCopy.Execute()
								runningCopy.Output = output
								runningCopy.Error = execErr
								runningCopy.DoneCh.Send(gCtx, true)
							})
						} else if nodeType == model.NodeTypeRouter {
							// Re-create RouterExecutor for workflow routing routers
							routerExec := NewRouterExecutor(
								ctx,
								workflowID,
								input.ChatID,
								input.WorkflowName,
								input.Inputs,
								nodeOutputs,
								childTracker,
								running.Node,
								running.EvalResult,
							)
							routerExec = routerExec.
								WithExecContext(running.ChildExecCtx).
								WithProjectPath(running.ChildExecCtx.ProjectPath).
								WithThreadTracker(threadTracker).
								WithPauseController(makeThreadPauseCtrl(running.ChildExecCtx.Thread)).
								WithMakeThreadPauseCtrl(makeThreadPauseCtrl)

							routerCopy := routerExec
							runningCopy := retryRunning
							workflow.Go(ctx, func(gCtx workflow.Context) {
								routerCopy = routerCopy.WithWorkflowContext(gCtx)
								output, execErr := routerCopy.Execute()
								runningCopy.Output = output
								runningCopy.Error = execErr
								runningCopy.DoneCh.Send(gCtx, true)
							})
						} else {
							// Regular workflow node - use InlineWorkflowExecutor
							retryContract, contractErr := coreSemantics.RequireContractForNode(running.StepID, model.NodeTypeWorkflow)
							if contractErr != nil {
								return nil, contractErr
							}

							retryExecutor, retryErr := NewInlineWorkflowExecutor(
								ctx,
								workflowID,
								input.ChatID,
								input.WorkflowName,
								input.Inputs,
								nodeOutputs,
								childTracker,
								running.Node,
								running.EvalResult,
								"", // No parent loop
								-1, // No loop iteration
							)
							if retryErr != nil {
								return nil, fmt.Errorf("failed to re-create executor for step %s after pause: %w", running.StepID, retryErr)
							}
							retryExecutor = retryExecutor.WithInvocationContract(retryContract).
								WithThreadTracker(threadTracker).
								WithExecContext(running.ChildExecCtx).
								WithProjectPath(running.ChildExecCtx.ProjectPath).
								WithPauseController(makeThreadPauseCtrl(running.ChildExecCtx.Thread)).
								WithMakeThreadPauseCtrl(makeThreadPauseCtrl)

							executorCopy := retryExecutor
							runningCopy := retryRunning
							workflow.Go(ctx, func(gCtx workflow.Context) {
								executorCopy = executorCopy.WithWorkflowContext(gCtx)
								output, execErr := executorCopy.Execute()
								runningCopy.Output = output
								runningCopy.Error = execErr
								runningCopy.DoneCh.Send(gCtx, true)
							})
						}
						runningInlineWorkflows = append(runningInlineWorkflows, retryRunning)
						logger.Info("[Workflow Runtime] Re-launched inline workflow after pause",
							"stepID", running.StepID,
							"nodeType", nodeType,
						)
						continue
					}

					if running.Error != nil {
						logger.Error("[Workflow Runtime] Inline workflow execution failed",
							"stepID", running.StepID,
							"error", running.Error,
						)
						notifyWorkflowError(ctx, input.ChatID, workflowID, input.WorkflowName,
							"inline_workflow_error",
							fmt.Sprintf("Inline workflow '%s' failed: %s", running.StepID, running.Error.Error()))
						return nil, fmt.Errorf("inline workflow %s failed: %w", running.StepID, running.Error)
					}
				}

				// Store output for edge routing
				nodeOutputStore.Set(running.StepID, running.Output)

				// Execute save_message if configured on the workflow node
				// Use parent's execCtx so the message is saved to the parent's thread
				if running.Node.GetSaveMessage() != nil {
					_, err := ExecuteSaveMessageForNode(
						ctx,
						running.Node,
						running.Output,
						nodeOutputs,
						workflowID,
						input.WorkflowName,
						input.ChatID,
						input.Inputs,
						execCtx, // Parent's context
						"",      // Not in a loop
						0,       // Not in a loop
					)
					if err != nil {
						logger.Error("[Workflow Runtime] save_message failed for inline workflow",
							"stepID", running.StepID,
							"error", err,
						)
						// Don't fail - save_message errors are logged but non-fatal
					}
				}

				// Create completion event
				events = append(events, &core.WorkflowEvent{
					ID:           fmt.Sprintf("workflow-complete-%s-%d", running.StepID, workflow.Now(ctx).UnixNano()),
					WorkflowID:   workflowID,
					ChatID:       input.ChatID,
					WorkflowName: input.WorkflowName,
					StepID:       running.StepID,
					Data:         running.Output,
				})

				// Update thread liveness - mark step as completed
				if threadTracker != nil {
					threadTracker.Mapping.MarkStepCompleted(running.StepID)
				}
			}
		}

		// End-of-turn daemon-offline evaluation. This is the seam between
		// turns — every step completion from this iteration has been
		// observed; we now decide whether to bump the streak, reset it,
		// or leave it alone (CallLLM-only turns are neither).
		//
		// We deliberately halt LATE in the iteration so users still see the
		// final tool_result errors and the assistant's last reply in chat
		// before the workflow terminates. Returning here also exits the
		// main loop directly, so no subsequent turn will run.
		streak := daemonOfflineTracker.ObserveTurnBoundary()
		if streak >= DaemonOfflineHaltThreshold {
			logger.Error("[Workflow Runtime] Halting: daemon offline for consecutive turns",
				"workflowID", workflowID,
				"chatID", input.ChatID,
				"consecutiveTurns", streak,
				"threshold", DaemonOfflineHaltThreshold,
			)
			haltErr := HaltError(streak)
			// Surface to the chat-error UI via the same path used by other
			// terminal workflow errors. The error message embeds
			// DaemonOfflineHaltMarker so the frontend can render a structured
			// "Reconnect workspace" affordance instead of a generic toast.
			notifyWorkflowError(ctx, input.ChatID, workflowID, input.WorkflowName,
				"daemon_offline_halt", haltErr.Error())
			// Use NonRetryable so Temporal doesn't retry the workflow on a
			// halt that the user must remediate (reconnect the daemon).
			return nil, temporal.NewNonRetryableApplicationError(
				haltErr.Error(), "DaemonOfflineHalt", haltErr,
			)
		}
	}
}

// setupInputUpdateHandler starts a background goroutine that listens for
// "update_workflow_state" signals and applies them to the running workflow's
// input maps. It is the runtime counterpart to the two-model input propagation
// described in InlineWorkflowExecutor.buildSubWorkflowInputs.
//
// Signal dispatch follows two paths depending on the __thread key:
//
//  1. Thread-scoped updates (__thread present):
//     Applied directly to the registered thread input map
//     (ChildWorkflowTracker.GetThreadInputs). This is how the UI sends
//     per-thread param overrides to a specific ref-based sub-workflow.
//
//  2. Global updates (no __thread key):
//     Applied to the root workflowInputs map, then propagated to every
//     registered thread input map, then forwarded as signals to Temporal
//     child workflows. This three-layer fan-out ensures:
//     - Inline sub-workflows see the update immediately (they share the
//     root map by reference, so the first write is sufficient).
//     - Ref-based inline sub-workflows see the update because their
//     separate input maps are written to in the propagation loop.
//     - Temporal child workflows (non-inline) receive a forwarded signal
//     so their own setupInputUpdateHandler can apply it.
//
// The doneCh channel signals the handler to exit when the workflow completes.
func setupInputUpdateHandler(ctx workflow.Context, workflowInputs map[string]interface{}, childTracker *ChildWorkflowTracker, workflowID string, doneCh workflow.ReceiveChannel) {
	logger := workflow.GetLogger(ctx)
	updateSignal := workflow.GetSignalChannel(ctx, "update_workflow_state")

	// Log the map address for debugging reference identity
	logger.Info("[SIGNAL_DEBUG] Signal handler initialized",
		"workflowID", workflowID,
		"workflowInputsAddr", fmt.Sprintf("%p", workflowInputs),
	)

	workflow.Go(ctx, func(ctx workflow.Context) {
		for {
			// Check for workflow cancellation
			if ctx.Err() != nil {
				logger.Info("[SIGNAL_DEBUG] Signal handler exiting due to workflow cancellation",
					"workflowID", workflowID,
				)
				return
			}

			// Use a selector to wait for either a signal update or the done signal
			selector := workflow.NewSelector(ctx)
			done := false

			// Listen for workflow state updates
			selector.AddReceive(updateSignal, func(c workflow.ReceiveChannel, more bool) {
				var update map[string]interface{}
				c.Receive(ctx, &update)

				logger.Info("[SIGNAL_DEBUG] Signal received",
					"workflowID", workflowID,
					"workflowInputsAddr", fmt.Sprintf("%p", workflowInputs),
					"update", update,
				)

				// Check if this is a thread-scoped update (contains __thread key)
				targetThread, isThreadScoped := update["__thread"].(string)
				if isThreadScoped {
					delete(update, "__thread") // Remove meta key before applying
					threadInputs := childTracker.GetThreadInputs(targetThread)
					if threadInputs != nil {
						for key, value := range update {
							logger.Info("[Workflow Runtime] Thread input updated via signal",
								"thread", targetThread,
								"key", key,
								"old", threadInputs[key],
								"new", value,
							)
							threadInputs[key] = value
						}
					} else {
						logger.Warn("[Workflow Runtime] Thread-scoped update for unregistered thread, ignoring",
							"thread", targetThread,
						)
					}
				} else {
					// Global update: apply to root workflowInputs
					for key, value := range update {
						logger.Info("[Workflow Runtime] Input updated via signal",
							"key", key,
							"old", workflowInputs[key],
							"new", value,
							"mapAddr", fmt.Sprintf("%p", workflowInputs),
						)
						workflowInputs[key] = value
					}

					// Propagate global updates to all registered inline thread inputs.
					// Ref-based sub-workflows register their own input maps via
					// RegisterThreadInputs. Updating these maps directly ensures that
					// in-flight executors (e.g. a loop evaluating its while condition)
					// see the latest values without requiring a separate signal path.
					//
					// NOTE: Inlined sub-workflows share the parent's map reference
					// (see InlineWorkflowExecutor.buildSubWorkflowInputs) and see the
					// root update above automatically. This loop covers ref-based
					// children whose inputs are separate maps.
					for threadID, threadInputs := range childTracker.GetAllThreadInputs() {
						for key, value := range update {
							logger.Debug("[Workflow Runtime] Propagating global update to thread inputs",
								"thread", threadID,
								"key", key,
								"old", threadInputs[key],
								"new", value,
							)
							threadInputs[key] = value
						}
					}

					// Forward state updates to all active child workflows.
					// Sort child IDs for deterministic signal ordering across replays.
					childIDs := make([]string, 0, len(childTracker.children))
					for childWorkflowID := range childTracker.children {
						childIDs = append(childIDs, childWorkflowID)
					}
					sort.Strings(childIDs)
					for _, childWorkflowID := range childIDs {
						logger.Info("[Workflow Runtime] Forwarding state update to child workflow",
							"parentWorkflowID", workflowID,
							"childWorkflowID", childWorkflowID,
							"update", update,
						)
						workflow.SignalExternalWorkflow(ctx, childWorkflowID, "", "update_workflow_state", update)
					}
				}
			})

			// Listen for done signal to exit the handler
			selector.AddReceive(doneCh, func(c workflow.ReceiveChannel, more bool) {
				var v bool
				c.Receive(ctx, &v)
				logger.Info("[SIGNAL_DEBUG] Signal handler received done signal, exiting",
					"workflowID", workflowID,
				)
				done = true
			})

			selector.Select(ctx)

			if done {
				return
			}
		}
	})
}

// LoadedWorkflow contains both raw YAML and placeholder-resolved JSON from the activity
// Raw YAML is needed for runtime template resolution with actual inputs
type LoadedWorkflow struct {
	YAML         []byte `json:"yaml"`
	WorkflowJSON []byte `json:"workflow_json"`
	HasTemplates bool   `json:"has_templates"`
}

// loadWorkflowDefinition loads the workflow YAML definition and returns both raw YAML
// (for template resolution) and placeholder-resolved JSON (for input validation)
func loadWorkflowDefinition(ctx workflow.Context, input WorkflowInput) (*LoadedWorkflow, error) {
	logger := workflow.GetLogger(ctx)

	// Create activity context for loading workflow
	activityCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    30 * time.Second,
			MaximumAttempts:    3,
		},
	})

	// Load workflow from database/file
	// Pass ChatID to allow the activity to resolve project-relative workflow paths
	loadInput := map[string]string{
		"chat_id":       input.ChatID,
		"workflow_name": input.WorkflowName,
	}
	var result LoadedWorkflow
	if err := workflow.ExecuteActivity(activityCtx, "ActivityLoadWorkflow", loadInput).Get(ctx, &result); err != nil {
		logger.Error("[Workflow Runtime] Failed to load workflow", "error", err)
		return nil, err
	}

	return &result, nil
}

// buildWorkflowContext creates the workflow.* context map for node config evaluation
// This provides the workflow namespace available in all CEL expressions
//
// Thread is auto-generated by DynamicWorkflow and passed via ExecutionContext.
func buildWorkflowContext(
	workflowID string,
	workflowName string,
	chatID string,
	workflowInputs map[string]interface{},
) map[string]interface{} {
	context := make(map[string]interface{})
	context[workflowContextKeyID] = workflowID
	context[workflowContextKeyName] = workflowName
	context[workflowContextKeyChatID] = chatID

	// Expose workflow inputs - only set values that are actually present
	// CEL expressions should use has() to check for optional values
	if workflowInputs != nil {
		context[workflowContextKeyInputs] = workflowInputs

		// Mode is the single source of truth for execution mode
		if val, ok := workflowInputs[workflowContextKeyMode].(string); ok {
			context[workflowContextKeyMode] = val
		}

		if val, ok := workflowInputs[workflowContextKeyAgentName].(string); ok {
			context[workflowContextKeyAgentName] = val
		}

		if prompt, ok := workflowInputs[workflowContextKeyPrompt].(string); ok {
			context[workflowContextKeyPrompt] = prompt
		}

		if spawnedBy, ok := workflowInputs[workflowContextKeySpawnedBy].(string); ok {
			context[workflowContextKeySpawnedBy] = spawnedBy
		}
	} else {
		context[workflowContextKeyInputs] = map[string]interface{}{}
	}

	return context
}

// toolCallSplit holds the result of splitting tool calls into regular and spawn calls
type protoToolCallSplit struct {
	regularToolCalls []*reliantv1.ToolCallMsg
	spawnToolCalls   []*reliantv1.ToolCallMsg
	askUserToolCalls []*reliantv1.ToolCallMsg
}

// splitProtoToolCalls separates proto tool calls into regular tools and spawn tools.
func splitProtoToolCalls(toolCalls []*reliantv1.ToolCallMsg) protoToolCallSplit {
	var result protoToolCallSplit
	for _, tc := range toolCalls {
		switch tc.GetName() {
		case "spawn":
			result.spawnToolCalls = append(result.spawnToolCalls, tc)
		case "ask_user":
			result.askUserToolCalls = append(result.askUserToolCalls, tc)
		default:
			result.regularToolCalls = append(result.regularToolCalls, tc)
		}
	}
	return result
}

// buildSpawnChildInputs forwards only spawn-relevant parent inputs to the child agent.
//
// Model values must be selector objects ({"id":"gpt-5.3-codex"}, {"tags":[...]}).
// String model values are normalized to {"id": string} objects.
//
// parent_permission is propagated so the child's resolved permission is capped
// to be at most as permissive as the parent's.
func buildSpawnChildInputs(workflowInputs map[string]interface{}) map[string]interface{} {
	childInputs := map[string]interface{}{}

	mode := getModeFromInputs(workflowInputs)
	if mode != "" {
		childInputs["mode"] = mode
	}

	// Derive parent_permission so child permission is capped to parent's level.
	// If the parent already has a parent_permission (chained spawn), propagate the
	// most restrictive. Otherwise derive from mode.
	if parentPerm := resolveParentPermission(workflowInputs); parentPerm != "" {
		childInputs["parent_permission"] = parentPerm
	}

	if workflowInputs == nil {
		return childInputs
	}

	model, ok := workflowInputs["model"]
	if !ok || model == nil {
		return childInputs
	}

	// Normalize string model values to objects at this boundary
	if modelID, ok := model.(string); ok {
		if modelID != "" {
			childInputs["model"] = map[string]interface{}{"id": modelID}
		}
		return childInputs
	}

	childInputs["model"] = deepCopyJSONLike(model)
	return childInputs
}

// resolveParentPermission determines the effective permission level of the parent workflow.
// It checks for an explicitly inherited parent_permission first (chained spawns),
// then falls back to deriving from mode.
func resolveParentPermission(workflowInputs map[string]interface{}) string {
	if workflowInputs == nil {
		return ""
	}

	// If parent itself has a parent_permission constraint, propagate it
	if pp, ok := workflowInputs["parent_permission"].(string); ok && pp != "" {
		return pp
	}

	// Derive from mode: plan mode = readonly, otherwise mutating
	// These match the permission constants in internal/llm/tools/permissions.go
	mode := getModeFromInputs(workflowInputs)
	switch mode {
	case "plan":
		return "readonly"
	case "manual", "auto":
		return "mutating"
	default:
		return "" // Unknown mode, don't constrain
	}
}

// deepCopyJSONLike performs a deep copy for JSON-like values used in workflow inputs.
func deepCopyJSONLike(v interface{}) interface{} {
	switch val := v.(type) {
	case map[string]interface{}:
		copied := make(map[string]interface{}, len(val))
		for k, child := range val {
			copied[k] = deepCopyJSONLike(child)
		}
		return copied
	case []interface{}:
		copied := make([]interface{}, len(val))
		for i, child := range val {
			copied[i] = deepCopyJSONLike(child)
		}
		return copied
	default:
		return val
	}
}

// spawnChildWorkflowConfig holds configuration for spawning a child workflow via spawn tool
type spawnChildWorkflowConfig struct {
	childWorkflowID string
	childThread     string
	isResumption    bool
	promptStr       string
	toolCallID      string
	presetName      string // Preset name from spawn tool call
	title           string // Optional human-readable title for the thread
}

// parseSpawnToolCall parses a spawn tool call and returns the configuration for spawning a child workflow
func parseSpawnToolCall(ctx workflow.Context, spawnToolCall *reliantv1.ToolCallMsg, parentWorkflowID string) (*spawnChildWorkflowConfig, error) {
	logger := workflow.GetLogger(ctx)
	toolCallID := spawnToolCall.GetId()

	// Parse spawn tool input
	// The input may be wrapped in a metadata envelope: {"input": "<raw>", "__reliant_tool_meta__": {...}}
	// Unwrap it to get the actual LLM tool call input.
	inputStr := spawnToolCall.GetInput()
	var envelope map[string]interface{}
	if err := json.Unmarshal([]byte(inputStr), &envelope); err != nil {
		logger.Error("[ExecuteTools] Failed to parse spawn tool input", "error", err, "input", inputStr)
		return nil, err
	}
	// Unwrap metadata envelope if present
	if _, hasMeta := envelope["__reliant_tool_meta__"]; hasMeta {
		if rawInput, ok := envelope["input"].(string); ok {
			inputStr = rawInput
		}
	}
	var toolInput map[string]interface{}
	if err := json.Unmarshal([]byte(inputStr), &toolInput); err != nil {
		logger.Error("[ExecuteTools] Failed to parse unwrapped spawn tool input", "error", err, "input", inputStr)
		return nil, err
	}

	promptStr, _ := toolInput["prompt"].(string)
	presetName, _ := toolInput["preset"].(string)
	agentID, hasAgentID := toolInput["agent_id"].(string)
	titleOverride, _ := toolInput["title"].(string)

	// Prompt is always required, including for resumptions
	if promptStr == "" {
		logger.Warn("[ExecuteTools] Spawn tool called with empty prompt",
			"tool_call_id", toolCallID,
			"preset", presetName)
		return nil, fmt.Errorf("spawn tool requires a non-empty 'prompt' parameter")
	}

	config := &spawnChildWorkflowConfig{
		promptStr:  promptStr,
		toolCallID: toolCallID,
		presetName: presetName,
		title:      titleOverride, // May be empty - will default to preset name
	}

	if hasAgentID && agentID != "" {
		// Resuming an existing conversation
		config.childThread = agentID
		config.childWorkflowID = DeterministicWorkflowID(parentWorkflowID, toolCallID)
		config.isResumption = true
		logger.Info("[ExecuteTools] Resuming existing spawn conversation",
			"tool_call_id", toolCallID,
			"child_workflow_id", config.childWorkflowID,
			"resuming_thread", config.childThread,
			"preset", presetName,
			"prompt_preview", fmt.Sprintf("%.100s", promptStr))
	} else {
		// New conversation
		config.childWorkflowID = DeterministicWorkflowID(parentWorkflowID, toolCallID)
		config.childThread = config.childWorkflowID
		config.isResumption = false
		logger.Info("[ExecuteTools] Spawning new child workflow via spawn tool",
			"tool_call_id", toolCallID,
			"child_workflow_id", config.childWorkflowID,
			"child_thread", config.childThread,
			"preset", presetName,
			"prompt_preview", fmt.Sprintf("%.100s", promptStr))
	}

	return config, nil
}

// isTransientSpawnExecutionError returns true for errors that should trigger a retry
// in the spawn execution loop (e.g., worker restarts, heartbeat timeouts).
// Terminal errors (auth failures, validation errors) are not transient.
func isTransientSpawnExecutionError(err error) bool {
	if err == nil {
		return false
	}
	// Non-retryable ApplicationErrors are terminal
	var appErr *temporal.ApplicationError
	if errors.As(err, &appErr) {
		if appErr.Type() == "TerminalError" || appErr.NonRetryable() {
			return false
		}
	}
	return true
}

// spawnInlineResult is the typed result of an inline spawn execution.
type spawnInlineResult struct {
	ToolCallID string
	Content    string
	IsError    bool
}

// toToolResult converts to the map format expected by the tool result pipeline.
func (r *spawnInlineResult) toToolResult() map[string]interface{} {
	return map[string]interface{}{
		"tool_call_id": r.ToolCallID,
		"content":      r.Content,
		"is_error":     r.IsError,
	}
}

// executeSpawnInline runs a spawn tool call inline using InlineWorkflowExecutor.
// This replaces the previous child workflow approach, running the spawned workflow
// in the same Temporal workflow context as the parent. Benefits:
// - No orphaned child workflows on worker restart
// - Pause/resume automatically applies to spawned workflows
// - No per-workflow task queue issues
func executeSpawnInline(
	ctx workflow.Context,
	config *spawnChildWorkflowConfig,
	projectPath string,
	chatID string,
	parentWorkflowID string,
	parentThread string,
	workflowInputs map[string]interface{},
	childTracker *ChildWorkflowTracker,
	makeThreadPauseCtrl func(string) *PauseController,
) *spawnInlineResult {
	pauseCtrl := makeThreadPauseCtrl(config.childThread)
	logger := workflow.GetLogger(ctx)

	// Validate thread ownership for resumptions - threads cannot be resumed across chat branches
	if config.isResumption {
		validateCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			StartToCloseTimeout: 10 * time.Second,
			RetryPolicy: &temporal.RetryPolicy{
				InitialInterval:    time.Second,
				BackoffCoefficient: 2.0,
				MaximumInterval:    5 * time.Second,
				MaximumAttempts:    3,
			},
		})
		validateInput := map[string]interface{}{
			"thread_id":        config.childThread,
			"expected_chat_id": chatID,
		}
		var validateResult map[string]interface{}
		err := workflow.ExecuteActivity(validateCtx, "ValidateThreadOwnership", validateInput).Get(ctx, &validateResult)
		if err != nil {
			logger.Error("[SpawnInline] Failed to validate thread ownership",
				"toolCallID", config.toolCallID,
				"error", err,
			)
			return &spawnInlineResult{
				ToolCallID: config.toolCallID,
				Content:    fmt.Sprintf("Failed to validate thread ownership: %v", err),
				IsError:    true,
			}
		}
		valid, _ := validateResult["valid"].(bool)
		if !valid {
			errorMessage, _ := validateResult["error_message"].(string)
			if errorMessage == "" {
				errorMessage = "Thread ownership validation failed. Threads cannot be resumed across chat branches."
			}
			logger.Warn("[SpawnInline] Thread ownership validation failed",
				"toolCallID", config.toolCallID,
				"childThread", config.childThread,
				"chatID", chatID,
			)
			return &spawnInlineResult{
				ToolCallID: config.toolCallID,
				Content:    errorMessage,
				IsError:    true,
			}
		}
		logger.Info("[SpawnInline] Thread ownership validated",
			"toolCallID", config.toolCallID,
			"childThread", config.childThread,
			"chatID", chatID,
		)
	}

	targetWorkflow := "builtin://agent"

	// Build child inputs - only pass actual workflow inputs (mode, model)
	childInputs := buildSpawnChildInputs(workflowInputs)

	// Determine thread title: use explicit title if provided, otherwise preset name
	threadTitle := config.title
	if threadTitle == "" {
		threadTitle = config.presetName
	}

	// Build ExecContext for the spawned workflow
	// New spawns get fresh context (no inheritance from parent)
	// Resumptions use their existing thread
	threadMode := model.ThreadModeNew
	forkedFrom := ""
	if config.isResumption {
		threadMode = model.ThreadModeInherit
	}

	// Create child workflow+thread atomically and optionally save inject message
	// This ensures thread exists with proper fork metadata before any messages are saved
	var injectMsg *InjectMessageConfig
	if config.promptStr != "" {
		injectMsg = &InjectMessageConfig{
			Role:    "user",
			Content: config.promptStr,
		}
	}

	if err := initChildWorkflow(ChildWorkflowInitOpts{
		Ctx:              ctx,
		ChatID:           chatID,
		ParentWorkflowID: parentWorkflowID,
		ChildWorkflowID:  config.childWorkflowID,
		ChildThreadID:    config.childThread,
		WorkflowName:     targetWorkflow,
		ThreadTitle:      ptr.StringIfNotEmpty(threadTitle),
		ThreadMode:       threadMode,
		ForkFromThread:   forkedFrom,
		ParentThread:     parentThread,
		SpawnedByNodeID:  "spawn_tool",
		InjectMessage:    injectMsg,
		Logger:           logger,
	}); err != nil {
		logger.Error("[SpawnInline] Failed to initialize child workflow",
			"toolCallID", config.toolCallID,
			"childWorkflowID", config.childWorkflowID,
			"error", err,
		)
		return &spawnInlineResult{
			ToolCallID: config.toolCallID,
			Content:    fmt.Sprintf("Failed to initialize spawn: %v", err),
			IsError:    true,
		}
	}

	// Calculate child spawn depth: read parent's depth from workflowInputs and increment
	parentSpawnDepth := 0
	if sd, ok := workflowInputs["spawn_depth"]; ok {
		switch v := sd.(type) {
		case int:
			parentSpawnDepth = v
		case float64:
			parentSpawnDepth = int(v)
		case int64:
			parentSpawnDepth = int(v)
		}
	}

	// Resolve parent permission for the child to inherit
	parentPermission := resolveParentPermission(workflowInputs)

	childExecContext := &ExecutionContext{
		WorkflowID:       config.childWorkflowID,
		ChatID:           chatID,
		WorkflowName:     targetWorkflow,
		Thread:           config.childThread,
		ThreadMode:       threadMode,
		ThreadTitle:      threadTitle,
		ForkedFrom:       forkedFrom,
		ParentThread:     parentThread,
		ProjectPath:      projectPath, // Always inherit from parent
		SpawnDepth:       parentSpawnDepth + 1,
		ParentPermission: parentPermission,
		Parent: &ParentContext{
			WorkflowID: parentWorkflowID,
			StepPath:   "spawn_tool",
		},
	}

	// Build proto V2Node for the InlineWorkflowExecutor.
	spawnNode := &reliantv1.Node{
		Id:   "spawn-" + config.toolCallID,
		Type: model.NodeTypeWorkflow,
		Args: &reliantv1.Node_Workflow{Workflow: &reliantv1.SubWorkflowArgs{
			Ref: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: targetWorkflow}},
		}},
	}
	// Set args
	if childInputs != nil {
		protoArgs := make(map[string]*structpb.Value)
		for k, v := range childInputs {
			if val, err := structpb.NewValue(v); err == nil {
				protoArgs[k] = val
			}
		}
		spawnNode.GetWorkflow().Args = protoArgs
	}
	// Set preset if specified
	if config.presetName != "" {
		spawnNode.GetWorkflow().Presets = map[string]string{DefaultPresetGroup: config.presetName}
	}

	// Spawn nodes are already fully resolved (no CEL), so use the proto node directly.
	evalResult := spawnNode

	logger.Info("[SpawnInline] Starting inline spawn execution",
		"toolCallID", config.toolCallID,
		"childWorkflowID", config.childWorkflowID,
		"thread", config.childThread,
		"preset", config.presetName,
		"targetWorkflow", targetWorkflow,
	)

	// Notify workflow status "started" for UI swim lane
	// Thread and workflow already created by parent via initChildWorkflow/V2_CreateWorkflowWithThread
	notifyWorkflowStatus(ctx, chatID, config.childWorkflowID, targetWorkflow, "started", parentWorkflowID, config.childThread, &workflowStatusOpts{
		Title:               config.presetName,
		ThreadTitle:         threadTitle,
		SpawnedByNodeID:     "spawn_tool",
		SpawnedByToolCallID: config.toolCallID,
	})

	// Emit per-tool-call "executing" status so the UI shows this spawn as active
	notifyToolCallStatus(ctx, chatID, config.toolCallID, config.toolCallID, "spawn", "executing")

	// Retry loop for transient errors (worker restarts, heartbeat timeouts).
	// Spawn tool calls are long-running and must survive any number of worker restarts.
	// Unlike regular activities, workflow.Go() goroutines are NOT automatically retried
	// by Temporal — we must handle retries ourselves.
	backoff := time.Second
	const maxBackoff = 30 * time.Second

	var execErr error
	for attempt := 0; ; attempt++ {
		// Create a fresh inline executor for each attempt.
		// The executor is stateful, so we must re-create it after a transient error.
		// The thread's persisted messages ensure we resume from where we left off.
		inlineExecutor, err := NewInlineWorkflowExecutor(
			ctx,
			config.childWorkflowID,
			chatID,
			targetWorkflow,
			workflowInputs,
			make(map[string]interface{}), // Fresh node outputs for the spawned workflow
			childTracker,
			spawnNode,
			evalResult,
			"", // No parent loop node
			0,  // No loop iteration
		)
		if err != nil {
			logger.Error("[SpawnInline] Failed to create inline executor",
				"toolCallID", config.toolCallID,
				"error", err,
			)
			notifyWorkflowStatus(ctx, chatID, config.childWorkflowID, targetWorkflow, "failed", parentWorkflowID, config.childThread, &workflowStatusOpts{
				SpawnedByToolCallID: config.toolCallID,
			})
			notifyToolCallStatus(ctx, chatID, config.toolCallID, config.toolCallID, "spawn", "failed")
			return &spawnInlineResult{
				ToolCallID: config.toolCallID,
				Content:    fmt.Sprintf("Failed to create spawn executor: %v", err),
				IsError:    true,
			}
		}

		inlineExecutor = inlineExecutor.
			WithExecContext(childExecContext).
			WithProjectPath(projectPath).
			WithPauseController(pauseCtrl).
			WithMakeThreadPauseCtrl(makeThreadPauseCtrl)

		_, execErr = inlineExecutor.Execute()
		if execErr == nil {
			break // Success
		}

		// Transient errors (worker restart, heartbeat timeout) — retry with backoff.
		// Spawn tool calls must survive any number of worker restarts. The thread's
		// persisted messages ensure the agent resumes from where it left off.
		if isTransientSpawnExecutionError(execErr) {
			logger.Info("[SpawnInline] Transient error, retrying spawn execution",
				"toolCallID", config.toolCallID,
				"childWorkflowID", config.childWorkflowID,
				"attempt", attempt,
				"backoff", backoff,
				"error", execErr,
			)
			if sleepErr := workflow.Sleep(ctx, backoff); sleepErr != nil {
				// Sleep failed (workflow cancelled) — return the original transient error
				return &spawnInlineResult{
					ToolCallID: config.toolCallID,
					Content:    fmt.Sprintf("Spawned workflow failed: %v", execErr),
					IsError:    true,
				}
			}
			// Exponential backoff capped at maxBackoff
			backoff = time.Duration(float64(backoff) * 2)
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}

		// Non-transient error — permanent failure
		break
	}

	if execErr != nil {
		logger.Error("[SpawnInline] Inline execution failed",
			"toolCallID", config.toolCallID,
			"childWorkflowID", config.childWorkflowID,
			"error", execErr,
		)
		notifyWorkflowStatus(ctx, chatID, config.childWorkflowID, targetWorkflow, "failed", parentWorkflowID, config.childThread, &workflowStatusOpts{
			SpawnedByToolCallID: config.toolCallID,
		})
		notifyToolCallStatus(ctx, chatID, config.toolCallID, config.toolCallID, "spawn", "failed")
		return &spawnInlineResult{
			ToolCallID: config.toolCallID,
			Content:    fmt.Sprintf("Spawned workflow failed: %v", execErr),
			IsError:    true,
		}
	}

	// Notify workflow status "completed"
	notifyWorkflowStatus(ctx, chatID, config.childWorkflowID, targetWorkflow, "completed", parentWorkflowID, config.childThread, &workflowStatusOpts{
		SpawnedByToolCallID: config.toolCallID,
	})

	// Emit per-tool-call "completed" status so the UI marks this spawn as done
	notifyToolCallStatus(ctx, chatID, config.toolCallID, config.toolCallID, "spawn", "completed")

	// Fetch the last message from the child's thread as the spawn result
	result := fetchSpawnResult(ctx, chatID, config.childThread, config.toolCallID)

	logger.Info("[SpawnInline] Inline spawn completed",
		"toolCallID", config.toolCallID,
		"childWorkflowID", config.childWorkflowID,
	)

	return result
}

// fetchSpawnResult fetches the result from a completed spawn child workflow
func fetchSpawnResult(
	ctx workflow.Context,
	chatID string,
	childThread string,
	toolCallID string,
) *spawnInlineResult {
	logger := workflow.GetLogger(ctx)

	fetchInput := map[string]interface{}{
		"chat_id": chatID,
		"thread":  childThread,
	}

	// Use short timeout for this lightweight activity
	fetchCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    30 * time.Second,
			MaximumAttempts:    3,
		},
	})

	var fetchResult map[string]interface{}
	err := workflow.ExecuteActivity(fetchCtx, "FetchThreadResult", fetchInput).Get(ctx, &fetchResult)
	if err != nil {
		logger.Error("[ExecuteTools] Failed to fetch child thread result",
			"toolCallID", toolCallID,
			"error", err)
		return &spawnInlineResult{
			ToolCallID: toolCallID,
			Content:    fmt.Sprintf("Spawned workflow completed but failed to fetch result: %v", err),
			IsError:    true,
		}
	}

	content, _ := fetchResult["content"].(string)
	isError, _ := fetchResult["is_error"].(bool)
	// Prefix the result with the thread path for future resumption
	prefixedContent := fmt.Sprintf("<system>Use agent_id: %s for future resumption</system>\n\n%s", childThread, content)
	return &spawnInlineResult{
		ToolCallID: toolCallID,
		Content:    prefixedContent,
		IsError:    isError,
	}
}

// executeToolsWithSpawnSupport handles ExecuteTools action with special support for "spawn" tool calls.
// It splits tool calls into regular tools (executed via ExecuteTools activity) and spawn tools (spawned as child workflows).
// rtx is the pre-built RuntimeContext from the caller.
// evalNode is the CEL-evaluated proto node with resolved_tool_calls populated.
func executeToolsWithSpawnSupport(
	ctx workflow.Context,
	activityCtx workflow.Context,
	rtx types.RuntimeContext,
	evalNode *reliantv1.Node,
	workflowInputs map[string]interface{},
	childTracker *ChildWorkflowTracker,
	makeThreadPauseCtrl func(string) *PauseController,
) workflow.Future {
	logger := workflow.GetLogger(ctx)

	logger.Info("[executeToolsWithSpawnSupport] Received inputs",
		"step_id", rtx.StepID,
		"loop_node_id", rtx.LoopNodeID,
		"loop_iteration", rtx.LoopIteration,
	)

	// Helper to build ActivityInput from RuntimeContext + a proto Node.
	makeInput := func(node *reliantv1.Node) types.ActivityInput {
		return types.ActivityInput{Runtime: rtx, Node: node}
	}

	etArgs := evalNode.GetExecuteTools()
	if etArgs == nil {
		// No execute_tools args — pass through as-is
		return workflow.ExecuteActivity(activityCtx, "ExecuteTools", makeInput(evalNode))
	}

	toolCalls := etArgs.GetResolvedToolCalls()
	if len(toolCalls) == 0 {
		// No resolved tool calls — pass through as-is
		return workflow.ExecuteActivity(activityCtx, "ExecuteTools", makeInput(evalNode))
	}

	// Split tool calls into regular tools and spawn tools
	split := splitProtoToolCalls(toolCalls)

	var regularToolsFuture workflow.Future

	// Execute regular tools via ExecuteTools activity
	if len(split.regularToolCalls) > 0 {
		regularNode := &reliantv1.Node{
			Id:   evalNode.GetId(),
			Type: model.NodeTypeExecuteTools,
			Args: &reliantv1.Node_ExecuteTools{ExecuteTools: &reliantv1.ExecuteToolsArgs{
				ResolvedToolCalls:     split.regularToolCalls,
				ExpectedResponseTools: etArgs.GetExpectedResponseTools(),
				ResponseToolSchemas:   etArgs.GetResponseToolSchemas(),
			}},
		}

		logger.Info("[executeToolsWithSpawnSupport] Executing regular tools",
			"step_id", rtx.StepID,
			"count", len(split.regularToolCalls),
			"loop_node_id", rtx.LoopNodeID,
			"loop_iteration", rtx.LoopIteration,
		)

		regularToolsFuture = workflow.ExecuteActivity(activityCtx, "ExecuteTools", makeInput(regularNode))
	}

	// OPTIMIZATION: If only regular tools, return directly to avoid goroutine wrapper
	if len(split.spawnToolCalls) == 0 && len(split.askUserToolCalls) == 0 && regularToolsFuture != nil {
		return regularToolsFuture
	}

	// Execute spawn tool calls inline and combine with regular tool results
	var spawnConfigs []*spawnChildWorkflowConfig
	var spawnParseErrors []interface{}
	for _, spawnToolCall := range split.spawnToolCalls {
		config, err := parseSpawnToolCall(ctx, spawnToolCall, rtx.WorkflowID)
		if err != nil {
			// Return error as a tool result so the LLM can learn and retry
			spawnParseErrors = append(spawnParseErrors, map[string]interface{}{
				"tool_call_id": spawnToolCall.GetId(),
				"content":      fmt.Sprintf("Spawn failed: %v", err),
				"is_error":     true,
			})
			continue
		}
		spawnConfigs = append(spawnConfigs, config)
	}

	// Create a future that runs all spawns inline and combines results
	resultFuture, resultSettable := workflow.NewFuture(ctx)

	workflow.Go(ctx, func(gCtx workflow.Context) {
		var combinedResults []interface{}
		var messageOutput map[string]interface{}

		// Include any spawn parse errors as tool results
		combinedResults = append(combinedResults, spawnParseErrors...)

		// Wait for regular tools first (if any)
		if regularToolsFuture != nil {
			result, err := processRegularToolsFuture(gCtx, regularToolsFuture, logger)
			if err != nil {
				resultSettable.SetError(err)
				return
			}
			if result.messageOutput != nil {
				messageOutput = result.messageOutput
			}
			combinedResults = append(combinedResults, result.toolResults...)
		}

		// Execute spawn calls inline
		// For multiple spawns, run in parallel using workflow.Go + channel
		if len(spawnConfigs) == 1 {
			// Single spawn - run directly (no goroutine overhead)
			result := executeSpawnInline(gCtx, spawnConfigs[0], rtx.ProjectPath, rtx.ChatID, rtx.WorkflowID, rtx.Thread, workflowInputs, childTracker, makeThreadPauseCtrl)
			combinedResults = append(combinedResults, result.toToolResult())
		} else if len(spawnConfigs) > 1 {
			// Multiple spawns - run in parallel using Temporal channels
			resultCh := workflow.NewChannel(gCtx)
			for _, cfg := range spawnConfigs {
				cfg := cfg // Capture loop variable
				workflow.Go(gCtx, func(spawnCtx workflow.Context) {
					result := executeSpawnInline(spawnCtx, cfg, rtx.ProjectPath, rtx.ChatID, rtx.WorkflowID, rtx.Thread, workflowInputs, childTracker, makeThreadPauseCtrl)
					resultCh.Send(spawnCtx, result)
				})
			}
			// Collect all results
			for range spawnConfigs {
				var result *spawnInlineResult
				resultCh.Receive(gCtx, &result)
				combinedResults = append(combinedResults, result.toToolResult())
			}
		}

		// Execute ask_user tool calls inline (question system)
		for _, askTC := range split.askUserToolCalls {
			result := executeAskUserInline(
				gCtx, askTC,
				rtx.ChatID, rtx.WorkflowID, rtx.Thread,
				rtx.StepID, rtx.LoopNodeID, rtx.LoopIteration,
				logger,
			)
			combinedResults = append(combinedResults, result)
		}

		// Return in ExecuteToolsOutput format
		finalResult := buildFinalToolResult(combinedResults, messageOutput)
		resultSettable.SetValue(finalResult)
	})

	return resultFuture
}

// executeAskUserInline handles an ask_user tool call by creating a question record
// and waiting for a signal, using the same QuestionCreate/signal.question system
// as the DAG ask_question node.
func executeAskUserInline(
	ctx workflow.Context,
	tc *reliantv1.ToolCallMsg,
	chatID, workflowID, threadID string,
	stepID, loopNodeID string,
	loopIteration int,
	logger log.Logger,
) map[string]interface{} {
	const questionTimeout = 24 * time.Hour

	toolCallID := tc.GetId()
	rawInput := tc.GetInput()

	// Unwrap __reliant_tool_meta__ envelope if present
	var envelope map[string]interface{}
	if err := json.Unmarshal([]byte(rawInput), &envelope); err == nil {
		if _, hasMeta := envelope["__reliant_tool_meta__"]; hasMeta {
			if inner, ok := envelope["input"].(string); ok {
				rawInput = inner
			}
		}
	}

	// Build metadata JSON with question info and tool_call_id.
	// Parse the LLM's input to extract questions at the top level for the frontend.
	metaObj := map[string]interface{}{
		"type":         "ask_user",
		"tool_call_id": toolCallID,
	}
	// Try to merge questions into metadata directly for frontend consumption
	var toolInput map[string]interface{}
	if err := json.Unmarshal([]byte(rawInput), &toolInput); err == nil {
		if questions, ok := toolInput["questions"]; ok {
			metaObj["questions"] = questions
		}
	}
	metaBytes, _ := json.Marshal(metaObj)
	metadata := string(metaBytes)

	logger.Info("[AskUserInline] Built question metadata",
		"toolCallID", toolCallID,
		"rawInput", rawInput,
		"metadata", metadata,
		"hasQuestions", metaObj["questions"] != nil,
	)

	temporalWorkflowID := workflow.GetInfo(ctx).WorkflowExecution.ID

	// STEP 1: Call QuestionCreate activity
	createCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
	})

	questionInput := map[string]interface{}{
		"chat_id":              chatID,
		"workflow_id":          workflowID,
		"temporal_workflow_id": temporalWorkflowID,
		"thread_id":            threadID,
		"step_id":              stepID,
		"loop_node_id":         loopNodeID,
		"loop_iteration":       loopIteration,
		"metadata":             metadata,
		"tool_call_id":         toolCallID,
	}

	var createOutput struct {
		QuestionID      string `json:"question_id"`
		AlreadyResolved bool   `json:"already_resolved"`
		ResponseData    string `json:"response_data"`
	}
	if err := workflow.ExecuteActivity(createCtx, "QuestionCreate", questionInput).Get(ctx, &createOutput); err != nil {
		logger.Error("[AskUser] QuestionCreate failed", "error", err, "toolCallID", toolCallID)
		return map[string]interface{}{
			"tool_call_id": toolCallID,
			"content":      fmt.Sprintf("ask_user failed: %v", err),
			"is_error":     true,
		}
	}

	// STEP 2: If already resolved (replay), return immediately
	if createOutput.AlreadyResolved {
		logger.Info("[AskUser] Already resolved (replay)", "questionID", createOutput.QuestionID)
		return map[string]interface{}{
			"tool_call_id": toolCallID,
			"content":      formatAskUserResponse("reply", createOutput.ResponseData),
		}
	}

	// STEP 3: Wait for signal or timeout
	signalName := "signal.question." + createOutput.QuestionID
	signalCh := workflow.GetSignalChannel(ctx, signalName)
	timeoutCtx, cancelTimer := workflow.WithCancel(ctx)
	timeoutFuture := workflow.NewTimer(timeoutCtx, questionTimeout)

	selector := workflow.NewSelector(ctx)
	var action string
	var responseData string

	selector.AddReceive(signalCh, func(ch workflow.ReceiveChannel, more bool) {
		var signalData map[string]interface{}
		ch.Receive(ctx, &signalData)
		if a, ok := signalData["action"].(string); ok {
			action = a
		}
		if rd, ok := signalData["response_data"].(string); ok {
			responseData = rd
		}
		cancelTimer()
	})

	selector.AddFuture(timeoutFuture, func(f workflow.Future) {
		action = "timeout"
	})

	selector.Select(ctx)

	// STEP 4: On timeout, resolve in DB
	if action == "timeout" {
		resolveCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			StartToCloseTimeout: 30 * time.Second,
			RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
		})
		var resolveOutput map[string]interface{}
		if err := workflow.ExecuteActivity(resolveCtx, "QuestionResolve", map[string]interface{}{
			"question_id":   createOutput.QuestionID,
			"response_data": "",
		}).Get(ctx, &resolveOutput); err != nil {
			logger.Warn("[AskUser] Failed to resolve question as timeout",
				"questionID", createOutput.QuestionID, "error", err)
		}
		return map[string]interface{}{
			"tool_call_id": toolCallID,
			"content":      "The user did not respond in time.",
		}
	}

	// Format and return the response as a tool result
	if action == "" {
		action = "reply"
	}
	return map[string]interface{}{
		"tool_call_id": toolCallID,
		"content":      formatAskUserResponse(action, responseData),
	}
}

// formatAskUserResponse formats the question response data into a human-readable
// string for the LLM to consume as a tool result.
func formatAskUserResponse(action, responseData string) string {
	if action == "continue" && responseData == "" {
		return "The user continued without providing a specific answer."
	}
	if responseData == "" {
		return "The user replied via chat message. Check the conversation for their response."
	}

	// Try to parse structured answer
	var parsed struct {
		Answers []struct {
			Question string   `json:"question"`
			Selected []string `json:"selected"`
			Freetext string   `json:"freetext"`
		} `json:"answers"`
	}
	if err := json.Unmarshal([]byte(responseData), &parsed); err != nil || len(parsed.Answers) == 0 {
		// Not structured — return raw
		return responseData
	}

	// Format each Q&A pair
	var parts []string
	for _, a := range parsed.Answers {
		answer := strings.Join(a.Selected, ", ")
		if a.Freetext != "" {
			if answer != "" {
				answer += fmt.Sprintf(" (note: %s)", a.Freetext)
			} else {
				answer = fmt.Sprintf("(note: %s)", a.Freetext)
			}
		}
		parts = append(parts, fmt.Sprintf("Q: %s\nA: %s", a.Question, answer))
	}
	return strings.Join(parts, "\n\n")
}

// regularToolsResult holds the result of processing regular tools
type regularToolsResult struct {
	toolResults   []interface{}
	messageOutput map[string]interface{}
}

// processRegularToolsFuture waits for and processes the regular tools future
func processRegularToolsFuture(ctx workflow.Context, future workflow.Future, logger log.Logger) (*regularToolsResult, error) {
	var result map[string]interface{}
	if err := future.Get(ctx, &result); err != nil {
		logger.Error("[ExecuteTools] Regular tools failed", "error", err)
		return nil, err
	}

	res := &regularToolsResult{}

	// Preserve the message field
	if msg, hasMsg := result["message"]; hasMsg {
		if msgMap, ok := msg.(map[string]interface{}); ok {
			res.messageOutput = msgMap
		}
	}

	// Extract tool_results array
	if toolResults, ok := result["tool_results"].([]interface{}); ok {
		res.toolResults = toolResults
	}

	return res, nil
}

// buildFinalToolResult constructs the final combined tool result
func buildFinalToolResult(combinedResults []interface{}, messageOutput map[string]interface{}) map[string]interface{} {
	finalResult := map[string]interface{}{
		"tool_results": combinedResults,
	}

	// Include message field - either from ExecuteTools or create one for spawn-only results
	if messageOutput != nil {
		finalResult["message"] = messageOutput
	} else if len(combinedResults) > 0 {
		// Create a message output for spawn-only tool results
		finalResult["message"] = map[string]interface{}{
			"role": "tool",
			"text": "",
		}
	}

	return finalResult
}

// waitForStepCompletions waits for at least one step to complete using Selector
func waitForStepCompletions(ctx workflow.Context, runningSteps []*RunningStep) []*RunningStep {
	selector := workflow.NewSelector(ctx)
	var completed []*RunningStep

	// Add all running steps to selector
	for _, step := range runningSteps {
		s := step // Capture for closure
		selector.AddFuture(s.Future, func(f workflow.Future) {
			completed = append(completed, s)
		})
	}

	// Wait for at least one to complete
	selector.Select(ctx)

	return completed
}

// waitForAnyCompletion waits for either an activity step or an inline workflow to complete.
// Returns the completed activity steps and inline workflows.
// This enables unified waiting on both types of concurrent executions.
func waitForAnyCompletion(
	ctx workflow.Context,
	runningSteps []*RunningStep,
	runningInlineWorkflows []*RunningInlineWorkflow,
) ([]*RunningStep, []*RunningInlineWorkflow) {
	selector := workflow.NewSelector(ctx)
	var completedSteps []*RunningStep
	var completedInline []*RunningInlineWorkflow

	// Add activity futures to selector
	for _, step := range runningSteps {
		s := step // Capture for closure
		selector.AddFuture(s.Future, func(f workflow.Future) {
			completedSteps = append(completedSteps, s)
		})
	}

	// Add inline workflow channels to selector
	for _, iw := range runningInlineWorkflows {
		w := iw // Capture for closure
		selector.AddReceive(w.DoneCh, func(c workflow.ReceiveChannel, more bool) {
			completedInline = append(completedInline, w)
		})
	}

	// Wait for at least one to complete
	selector.Select(ctx)

	return completedSteps, completedInline
}

// workflowStatusOpts contains optional parameters for workflow status notifications
type workflowStatusOpts struct {
	Title               string
	ThreadTitle         string // Human-readable title for the thread (stored in threads table)
	SpawnedByToolCallID string
	SpawnedByNodeID     string // Node ID that spawned this child workflow
	LoopIteration       *int64 // Iteration index when spawned by a loop node
}

// notifyWorkflowStatus sends workflow status updates to chat_updates for UI notifications
// and tracks workflow hierarchy in the database.
// This is a fire-and-forget notification - errors are logged but don't fail the workflow
// opts can include agent info for UI swim lanes and fork metadata for context inheritance
func notifyWorkflowStatus(ctx workflow.Context, chatID, workflowID, workflowName, status, parentWorkflowID, thread string, opts *workflowStatusOpts) {
	logger := workflow.GetLogger(ctx)

	// If the workflow context is cancelled (e.g., during cleanup after cancellation),
	// we need a disconnected context to execute the status notification activity.
	// A disconnected context is not affected by the parent workflow's cancellation.
	baseCtx := ctx
	if ctx.Err() != nil {
		// Create a disconnected context for cleanup activities
		baseCtx, _ = workflow.NewDisconnectedContext(ctx)
		logger.Debug("[Workflow Runtime] Using disconnected context for status notification",
			"status", status,
			"workflowID", workflowID)
	}

	// Execute workflow status activity with short timeout
	activityCtx := workflow.WithActivityOptions(baseCtx, workflow.ActivityOptions{
		StartToCloseTimeout: time.Second * 5,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    5 * time.Second,
			MaximumAttempts:    3,
		},
	})

	// Use map with correct JSON field names (snake_case) to avoid import cycle
	input := map[string]interface{}{
		"chat_id":            chatID,
		"workflow_id":        workflowID,
		"workflow_name":      workflowName,
		"status":             status,
		"parent_workflow_id": parentWorkflowID,
		"thread":             thread,
	}

	// Add optional fields if provided
	if opts != nil {
		if opts.Title != "" {
			input["title"] = opts.Title
		}
		if opts.ThreadTitle != "" {
			input["thread_title"] = opts.ThreadTitle
		}
		if opts.SpawnedByToolCallID != "" {
			input["spawned_by_tool_call_id"] = opts.SpawnedByToolCallID
		}
		if opts.SpawnedByNodeID != "" {
			input["spawned_by_node_id"] = opts.SpawnedByNodeID
		}
		if opts.LoopIteration != nil {
			input["loop_iteration"] = *opts.LoopIteration
		}
	}

	var result map[string]interface{}
	// Use baseCtx (which may be disconnected) for the Get() call as well
	err := workflow.ExecuteActivity(activityCtx, "WorkflowStatus", input).Get(baseCtx, &result)
	if err != nil {
		// Log error but don't fail workflow
		logger.Warn("[Workflow Runtime] Failed to notify workflow status",
			"status", status,
			"workflowID", workflowID,
			"error", err)
	} else {
		logger.Info("[Workflow Runtime] Notified workflow status",
			"status", status,
			"workflowID", workflowID)
	}
}

// notifyToolCallStatus emits a tool call status update so the UI can show
// per-tool-call progress (e.g. spawn tool calls starting/completing independently).
// Fire-and-forget: errors are logged but don't fail the workflow.
func notifyToolCallStatus(ctx workflow.Context, chatID, contentBlockID, toolCallID, toolName, status string) {
	logger := workflow.GetLogger(ctx)

	activityCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 5 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    5 * time.Second,
			MaximumAttempts:    2,
		},
	})

	input := map[string]interface{}{
		"chat_id":          chatID,
		"content_block_id": contentBlockID,
		"tool_call_id":     toolCallID,
		"tool_name":        toolName,
		"status":           status,
	}

	var result map[string]interface{}
	err := workflow.ExecuteActivity(activityCtx, "EmitToolCallStatus", input).Get(ctx, &result)
	if err != nil {
		logger.Warn("[notifyToolCallStatus] Failed",
			"toolCallID", toolCallID, "status", status, "error", err)
	}
}

// notifyWorkflowError writes a workflow error to chat_updates for UI display.
// This is a fire-and-forget notification - errors are logged but don't fail the workflow.
func notifyWorkflowError(ctx workflow.Context, chatID, workflowID, workflowName, errorType, errorMessage string) {
	logger := workflow.GetLogger(ctx)

	// Execute workflow error activity with short timeout
	activityCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: time.Second * 5,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    5 * time.Second,
			MaximumAttempts:    3,
		},
	})

	// Use map with correct JSON field names (snake_case) to avoid import cycle
	input := map[string]interface{}{
		"chat_id":       chatID,
		"workflow_id":   workflowID,
		"workflow_name": workflowName,
		"error_type":    errorType,
		"error_message": errorMessage,
	}

	var result map[string]interface{}
	err := workflow.ExecuteActivity(activityCtx, "WorkflowError", input).Get(ctx, &result)
	if err != nil {
		// Log error but don't fail workflow
		logger.Warn("[Workflow Runtime] Failed to notify workflow error",
			"errorType", errorType,
			"workflowID", workflowID,
			"error", err)
	} else {
		logger.Info("[Workflow Runtime] Notified workflow error",
			"errorType", errorType,
			"workflowID", workflowID)
	}
}

// handleWorkflowCompletion handles workflow completion, errors, and cancellation
// Note: Temporal is the source of truth for workflow execution state.
// The workflows table tracks parent-child hierarchy for debugging and tracing.
// The retErr parameter captures the workflow's return error (via named returns),
// allowing us to distinguish between successful completion and error returns.
func handleWorkflowCompletion(ctx workflow.Context, workflowID, chatID, workflowName, parentWorkflowID, thread, forkedFromThread string, retErr error) {
	logger := workflow.GetLogger(ctx)

	// Create a disconnected context that will survive cancellation
	// This allows us to run cleanup activities even when the workflow is cancelled
	cleanupCtx, _ := workflow.NewDisconnectedContext(ctx)

	// Check if workflow was cancelled
	if ctx.Err() != nil {
		logger.Info("[Workflow Runtime] Cancelled", "workflowID", workflowID)
		// Run cleanup activities (cancel pending approvals, etc.)
		runCleanupActivities(cleanupCtx, chatID, workflowID, thread)
		// Notify UI that workflow was cancelled and update workflow record
		notifyWorkflowStatus(cleanupCtx, chatID, workflowID, workflowName, "cancelled", parentWorkflowID, thread, nil)
		return
	}

	// Check for panic/error recovery
	if r := recover(); r != nil {
		logger.Error("[Workflow Runtime] Panic recovered", "panic", r, "workflowID", workflowID)
		// Run cleanup activities
		runCleanupActivities(cleanupCtx, chatID, workflowID, thread)
		// Notify UI that workflow failed and update workflow record
		notifyWorkflowStatus(cleanupCtx, chatID, workflowID, workflowName, "failed", parentWorkflowID, thread, nil)
		panic(r) // Re-panic to maintain Temporal semantics
	}

	// Check if the workflow returned an error
	if retErr != nil {
		logger.Error("[Workflow Runtime] Failed with error", "workflowID", workflowID, "error", retErr)
		// Run cleanup activities for failed workflows
		runCleanupActivities(cleanupCtx, chatID, workflowID, thread)
		// Notify UI that workflow failed and update workflow record
		notifyWorkflowStatus(cleanupCtx, chatID, workflowID, workflowName, "failed", parentWorkflowID, thread, nil)
		return
	}

	// Normal completion - don't run cleanup for successful completions
	// Cleanup should only run for cancelled/failed workflows to avoid race conditions
	logger.Info("[Workflow Runtime] Completed successfully", "workflowID", workflowID)
	// Only run cleanup for root workflows (not child workflows) to avoid cleaning up parent workflows' tool calls
	if parentWorkflowID == "" {
		runCleanupActivities(cleanupCtx, chatID, workflowID, thread)
	}
	// Notify UI that workflow completed successfully and update workflow record
	notifyWorkflowStatus(cleanupCtx, chatID, workflowID, workflowName, "completed", parentWorkflowID, thread, nil)
}

// runCleanupActivities executes cleanup tasks such as cancelling pending approvals
// Only cleans up tool calls from the specified workflow/thread to avoid race conditions
func runCleanupActivities(ctx workflow.Context, chatID, workflowID, thread string) {
	logger := workflow.GetLogger(ctx)

	// Execute cleanup activity with short timeout
	activityCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: time.Second * 10,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    10 * time.Second,
			MaximumAttempts:    3,
		},
	})

	input := map[string]interface{}{
		"chat_id":     chatID,
		"workflow_id": workflowID,
		"thread":      thread,
	}

	var result map[string]interface{}
	err := workflow.ExecuteActivity(activityCtx, "Cleanup", input).Get(ctx, &result)
	if err != nil {
		// Log error but don't fail workflow - cleanup is best-effort
		logger.Warn("[Workflow Runtime] Failed to run cleanup activities",
			"chatID", chatID,
			"workflowID", workflowID,
			"thread", thread,
			"error", err)
	} else {
		logger.Info("[Workflow Runtime] Cleanup completed",
			"chatID", chatID,
			"workflowID", workflowID,
			"thread", thread,
			"result", result)
	}
}

// Helper functions to extract state from inputs with defaults
func getBoolInput(inputs map[string]interface{}, key string, defaultVal bool) bool {
	if val, ok := inputs[key]; ok {
		if boolVal, ok := val.(bool); ok {
			return boolVal
		}
	}
	return defaultVal
}

func getStringInput(inputs map[string]interface{}, key string, defaultVal string) string {
	if val, ok := inputs[key]; ok {
		if strVal, ok := val.(string); ok {
			return strVal
		}
	}
	return defaultVal
}

// humanizeRetryError extracts a clean, actionable error message from a retry exhaustion error.
func humanizeRetryError(stepID string, err error) string {
	errStr := err.Error()
	lower := strings.ToLower(errStr)

	var hint string
	switch {
	case strings.Contains(lower, "no such host") || strings.Contains(lower, "dns"):
		hint = "DNS resolution failed — check your internet connection and DNS settings"
	case strings.Contains(lower, "429") || strings.Contains(lower, "rate limit") || strings.Contains(lower, "too many requests"):
		hint = "Rate limited by API provider — wait a few minutes before retrying"
	case strings.Contains(lower, "timeout") || strings.Contains(lower, "deadline exceeded"):
		hint = "Request timed out — the API may be experiencing high load"
	case strings.Contains(lower, "connection refused") || strings.Contains(lower, "connection reset"):
		hint = "Connection failed — check your internet connection"
	case strings.Contains(lower, "unauthorized") || strings.Contains(lower, "forbidden") || strings.Contains(lower, "401") || strings.Contains(lower, "403"):
		hint = "Authentication failed — check your API key in Settings"
	default:
		hint = errStr
	}

	return fmt.Sprintf("Step '%s' failed: %s. Workflow paused — send a message to retry.", stepID, hint)
}

// getMapKeys returns the keys from a map for logging
func getMapKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
