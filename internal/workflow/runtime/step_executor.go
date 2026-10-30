// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/logging"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/core"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
	"github.com/reliant-labs/reliant/internal/workflow/stopreason"
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
	// nodePathPrefix is the fully-qualified dotted path of the scope this
	// executor runs nodes in ("impl_loop.attempt" for a sub-workflow body
	// inside a loop). A node's own NodePath is this prefix plus its id.
	//
	// Distinct from loopNodeID on purpose: loopNodeID is loop-scoped identity
	// and is persisted; this is graph position and is not. See
	// types.RuntimeContext.NodePath.
	nodePathPrefix string
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
	// threadInterrupt observes interrupts for the thread this executor is running.
	threadInterrupt *ThreadInterrupt
	// makeThreadInterrupt creates per-thread interrupt handles for spawned/nested threads.
	makeThreadInterrupt func(string) *ThreadInterrupt
	// saveInputRefs caches, per node id, which workflow inputs the node's
	// save_message reads (see saveMessageInputRefs).
	saveInputRefs map[string]wfcel.NamespaceRefs
	// loopOutputs is the `outputs` namespace for this executor's nodes: the
	// enclosing loop's previous-iteration outputs when it runs a loop body
	// (never nil there — see WithLoopBodyOutputs), nil otherwise.
	loopOutputs map[string]interface{}
	// enclosingIter is the `iter` of the loop enclosing a sub-workflow body
	// this executor runs, when that body's own inputs do not carry it.
	enclosingIter map[string]interface{}
	// askUserHandoff is set on the iterations of a background spawn's own
	// agent loop: a blocking ask_user in its execute_tools batch can park
	// the spawn for a continue-as-new handoff (ask_user_handoff.go).
	askUserHandoff *toolBatchHandoff
}

// WithAskUserHandoff lets this executor's execute_tools batches park their
// spawn inside a blocking ask_user wait (ask_user_handoff.go).
func (e *StepExecutor) WithAskUserHandoff(h *toolBatchHandoff) *StepExecutor {
	e.askUserHandoff = h
	return e
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

// WithEnclosingIter sets the enclosing loop's iteration context for a
// sub-workflow body whose inputs were not inherited from the loop.
func (e *StepExecutor) WithEnclosingIter(iter map[string]interface{}) *StepExecutor {
	e.enclosingIter = iter
	return e
}

// WithLoopBodyOutputs marks this executor as running a loop body and sets the
// enclosing loop's previous-iteration outputs (nil = no previous iteration,
// normalized to the empty map so `outputs` is declared on every iteration).
func (e *StepExecutor) WithLoopBodyOutputs(prev map[string]interface{}) *StepExecutor {
	e.loopOutputs = loopBodyOutputs(prev)
	return e
}

// WithNodePathPrefix sets the fully-qualified dotted path of the scope whose
// nodes this executor runs. Callers compose it with joinNodePath so nesting
// accumulates instead of being overwritten.
func (e *StepExecutor) WithNodePathPrefix(prefix string) *StepExecutor {
	e.nodePathPrefix = prefix
	return e
}

// iterContext builds the `iter` namespace for node config evaluation.
//
// The loop executors already publish the authoritative iteration context into
// the iteration inputs they hand this executor — including `item` and `key`,
// which only they can know because only they resolved the items expression.
// Preferring that map is what makes `iter.item` work for ORDINARY nodes.
// Rebuilding it from the iteration counter alone yielded a two-key map of
// iteration and index, so `iter.item.num` failed to resolve with
// "no such key: num" (docs/workflows/patterns.mdx:436 documents the contract).
//
// Falls back to the counter-only context when there is no loop context to read:
// a top-level step has no `iter` in its inputs, and a loop body whose items
// were never resolved (a `while` loop) has no item to expose.
func (e *StepExecutor) iterContext() map[string]interface{} {
	if iter, ok := e.workflowInputs["iter"].(map[string]interface{}); ok {
		// Trust the loop's iteration/index over the published map only if the
		// map omits them; the executor and its inputs are built together, so a
		// disagreement means the inputs were assembled for a different scope.
		if _, hasIteration := iter["iteration"]; hasIteration {
			return iter
		}
	}
	if e.enclosingIter != nil {
		return e.enclosingIter
	}
	return model.BuildIterContext(e.loopIteration)
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

// WithThreadInterrupts sets the interrupt handle for this executor's thread.
func (e *StepExecutor) WithThreadInterrupts(interrupt *ThreadInterrupt) *StepExecutor {
	e.threadInterrupt = interrupt
	return e
}

// WithMakeThreadInterrupt sets the per-thread interrupt handle factory for spawn support.
func (e *StepExecutor) WithMakeThreadInterrupt(fn func(string) *ThreadInterrupt) *StepExecutor {
	e.makeThreadInterrupt = fn
	return e
}

// getActivityCtx returns the context to use when dispatching activities.
// It composes pause/resume cancellation with same-thread interrupts so a thread
// can abandon the work in front of its mailbox without cancelling sibling
// threads that share the same Temporal workflow.
func (e *StepExecutor) getActivityCtx() workflow.Context {
	base := e.pauseCtrl.GetActivityCtx(e.ctx)
	thread := e.GetThread()
	interrupt := resolveThreadInterrupt(e.makeThreadInterrupt, e.threadInterrupt, thread)
	return interrupt.ActivityContext(base)
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
	// PreallocatedMessageID is the assistant message id minted before a
	// call_llm dispatch (delta identity protocol); "" for other step types
	// and for pre-gate histories.
	PreallocatedMessageID string
	// ThreadInterruptEpoch is the same-thread interrupt epoch observed before
	// dispatch. Cancellation details settle only when this epoch advances.
	ThreadInterruptEpoch int64
	// SaveDelegated records the dispatch-time decision that the executing
	// activity writes this node's save_message (a types.SaveMessageRequest was
	// attached to its input). When false and the node has a save_message, the
	// workflow assembled the output itself and writes the message on
	// completion.
	SaveDelegated bool
	// ProviderPinsKey identifies the dispatch routing epoch; an old in-flight
	// call completing after a switch must not acknowledge the new cleanup.
	ProviderPinsKey string
}

func (e *StepExecutor) providerPinsKey() string {
	if e.childTracker == nil || e.childTracker.chatProvider == "" {
		return ""
	}
	return fmt.Sprintf("%s/%s/%d", e.GetThread(), e.childTracker.chatProvider, e.childTracker.providerUpdates)
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
	iterCtx := e.iterContext()

	// Evaluate node config (resolves all CEL expressions in-place)
	evalResult, err := EvaluateNodeConfig(
		node,
		e.nodeOutputs,
		e.workflowID,
		event.WorkflowName,
		e.workflowInputs,
		iterCtx,
		e.loopOutputs,
		e.execContext,
	)
	if err != nil {
		e.logger.Error("Failed to evaluate step config", "error", err, "stepID", node.GetId())
		return e.startFailedStep(node, event, err)
	}

	// Determine step type and dispatch
	stepType := node.GetType()
	logging.Debug("[StepExecutor] Starting step",
		"stepID", node.GetId(),
		"stepType", stepType,
	)

	var future workflow.Future
	var activityName string
	saveDelegated := false

	// Delta identity: mint the assistant message id BEFORE dispatching a
	// call_llm activity so retries re-stream under the same id. Gated on
	// GetVersion inside preallocateAssistantMessageID — replaying pre-change
	// histories yields "" and adds no commands.
	preallocatedMessageID := ""
	if stepType == model.NodeTypeCallLLM {
		thread := ""
		if e.execContext != nil {
			thread = e.execContext.Thread
		}
		preallocatedMessageID = preallocateAssistantMessageID(e.ctx, e.chatID, thread)
	}
	threadInterruptEpoch := int64(0)
	if interrupt := resolveThreadInterrupt(e.makeThreadInterrupt, e.threadInterrupt, e.GetThread()); interrupt != nil {
		threadInterruptEpoch = interrupt.Epoch()
	}

	// A machine step that follows a hold first makes sure the machine is
	// awake (machine_resume_wake.go): the idle policy may have suspended it
	// while the run waited, and the step's own calls never wake it.
	if machine, toolNames, toolsKnown := machineStepTools(stepType, evalResult); machine {
		e.pauseCtrl.WakeMachineBeforeStep(e.ctx, e.getActivityCtx(), toolNames, toolsKnown)
	}

	switch stepType {
	case model.NodeTypeWorkflow:
		// NOTE: workflow nodes are now handled inline by InlineWorkflowExecutor.
		// This case should never be reached - if it is, it's a bug in the calling code.
		e.logger.Error("[StepExecutor] workflow nodes should be handled inline, not via StepExecutor",
			"stepID", node.GetId(),
			"stepType", stepType,
		)
		future = e.executeFailActivity(node, fmt.Sprintf("workflow node %s should be handled inline", node.GetId()))
		activityName = "InlineWorkflowError"

	case model.NodeTypeRun:
		future, activityName, saveDelegated = e.startRun(node, evalResult)

	case model.NodeTypeApproval:
		// NOTE: approval nodes are now handled inline by InlineWorkflowExecutor.
		// This case should never be reached - if it is, it's a bug in the calling code.
		e.logger.Error("[StepExecutor] approval nodes should be handled inline, not via StepExecutor",
			"stepID", node.GetId(),
			"stepType", stepType,
		)
		future = e.executeFailActivity(node, fmt.Sprintf("approval node %s should be handled inline", node.GetId()))
		activityName = "InlineApprovalError"

	case model.NodeTypeAskQuestion:
		future, activityName = e.startAskQuestion(node, evalResult)

	case model.NodeTypeCompute:
		future, activityName = e.startCompute(node, evalResult)

	default:
		// All other types are activities (e.g., call_llm, save_message, execute_tools)
		if isActivityType(stepType) {
			future, activityName, saveDelegated = e.startAction(node, evalResult, preallocatedMessageID, event)
		} else {
			e.logger.Error("[StepExecutor] Unknown step type", "stepType", stepType, "stepID", node.GetId())
			future = e.executeFailActivity(node, "unknown step type: "+stepType)
			activityName = "UnknownStepType"
		}
	}

	// Use StepID as canonical identifier - ActivityID kept for logging/backwards compat
	return &RunningStep{
		ActivityID:            node.GetId(), // Set to StepID for backwards compat
		StepID:                node.GetId(),
		ActivityName:          activityName,
		Node:                  node,
		Event:                 event,
		Future:                future,
		EvalResult:            evalResult, // Store for save_message thread resolution
		PreallocatedMessageID: preallocatedMessageID,
		ThreadInterruptEpoch:  threadInterruptEpoch,
		SaveDelegated:         saveDelegated,
		ProviderPinsKey:       e.providerPinsKey(),
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
	stepEvent := e.handleActivityCompletion(running, eventID)
	if machine, _, _ := machineStepTools(running.Node.GetType(), nil); machine {
		e.pauseCtrl.MachineStepDone(e.ctx)
	}

	// Daemon-offline circuit breaker: observe the outcome AFTER save_message
	// ran (inside handleActivityCompletion), so the failing tool results are
	// already persisted to the chat if the breaker decides to pause. This is
	// the one chokepoint every step completion passes through — main loop,
	// inline workflows, and loop iterations alike — which is what lets agent
	// loops trip the breaker. May block until resume.
	e.pauseCtrl.ObserveDaemonOfflineStep(e.ctx, running.ActivityName, stepEvent)

	// A run that gave up (an unattended self-pause outlived its ladder) is
	// ending: a step that completes now fails with that, so whichever
	// executor holds it returns it instead of carrying on — the pause that
	// gave up may have been armed by another thread, or by the breaker above.
	if gaveUp := e.pauseCtrl.gaveUp(); gaveUp != nil && stepEvent != nil {
		stepEvent.Data = nil
		stepEvent.Error = gaveUp
		stepEvent.RetryExhausted = true
	}

	// Repeated-identical-tool-error breaker: an unattended agent that has been
	// told the same thing three times and keeps asking is not going to get
	// another answer. Ending its step makes the surrounding loop and run fail
	// with the reason, instead of spending max_turns on it. The failing
	// results are already persisted (save_message ran above), so the
	// transcript shows exactly what it kept hitting. Attended chats are left
	// alone: a person is there to redirect them.
	if IsUnattended(e.workflowInputs) && stepEvent != nil && stepEvent.Error == nil {
		if err := e.pauseCtrl.ObserveRepeatedToolErrors(e.GetThread(), running.ActivityName, stepEvent); err != nil &&
			workflow.GetVersion(e.ctx, repeatedToolErrorChangeID, workflow.DefaultVersion, 1) != workflow.DefaultVersion {
			// Reported here, once: the loop, the inline workflow and the run
			// pass it up and log it at DEBUG (markReported).
			e.logger.Error("[StepExecutor] Ending the agent: its tool calls keep failing identically",
				"stepID", running.StepID, "thread", e.GetThread(), "error", err)
			stepEvent.Error = markReported(err)
		}
	}

	return stepEvent
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
			// Delta identity: the stream under this id is dead. A pause-retry
			// re-Start()s the step and mints a fresh id, so finalize this one
			// now (no-op when no id was pre-allocated).
			emitStreamFinalized(e.ctx, e.chatID, running.PreallocatedMessageID, e.threadForFinalize(), streamReasonCancelled, 0)
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
			// Delta identity: finalize before the caller pauses; the retry
			// after resume re-Start()s with a fresh id.
			emitStreamFinalized(e.ctx, e.chatID, running.PreallocatedMessageID, e.threadForFinalize(), streamReasonAborted, 0)
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
		// Delta identity: finalize before the caller pauses (aborted). The
		// retry after resume re-Start()s the step with a fresh id.
		emitStreamFinalized(e.ctx, e.chatID, running.PreallocatedMessageID, e.threadForFinalize(), streamReasonAborted, 0)
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

	if (running.ActivityName == "CallLLM" || running.ActivityName == "Compact") && running.ProviderPinsKey != "" {
		if e.childTracker.providerPinCleanups == nil {
			e.childTracker.providerPinCleanups = map[string]bool{}
		}
		e.childTracker.providerPinCleanups[running.ProviderPinsKey] = true
	}
	// Normalize output against schema
	normalizedOutput := e.normalizeOutput(rawOutput, running.ActivityName)

	// Store output for downstream steps
	if normalizedOutput != nil {
		e.nodeOutputs[running.StepID] = normalizedOutput
	}

	// Record what this batch's load_tool calls granted, for the thread's next
	// call_llm. From the recorded result, so replay rebuilds the same grants.
	if running.ActivityName == executeToolsActivityName {
		e.childTracker.recordToolGrants(e.GetThread(), grantedToolsFromOutput(normalizedOutput))
	}

	// A node's save_message is written by whoever executed the node. When the
	// activity executed it, the wrapper already wrote the message before
	// returning; only an output the workflow assembled itself (ask_question,
	// an execute_tools batch with spawn/ask_user calls) is saved here.
	//
	// A save that fails fails the step, exactly as the wrapper fails the
	// activity: the step surfaces as retry-exhausted, so the caller pauses the
	// chat with the error and re-runs the step on resume.
	if running.Node != nil && running.Node.GetSaveMessage() != nil && !running.SaveDelegated {
		if _, saveErr := e.executeSaveMessage(running, normalizedOutput); saveErr != nil {
			emitStreamFinalized(e.ctx, e.chatID, running.PreallocatedMessageID, e.threadForFinalize(), streamReasonAborted, 0)
			return &StepEvent{
				ID:             eventID,
				WorkflowID:     e.workflowID,
				ChatID:         e.chatID,
				WorkflowName:   e.workflowName,
				StepID:         running.StepID,
				Error:          saveErr,
				RetryExhausted: true, // Will trigger pause, then retry on resume
			}
		}
	}

	// Delta identity: success-path finalize marker. Fires AFTER the save —
	// the marker's contract is "no more deltas under this id". No-op when no
	// id was pre-allocated.
	emitStreamFinalized(e.ctx, e.chatID, running.PreallocatedMessageID, e.threadForFinalize(),
		streamReasonCompleted, extractLastStreamSeq(normalizedOutput))

	return &StepEvent{
		ID:           eventID,
		WorkflowID:   e.workflowID,
		ChatID:       e.chatID,
		WorkflowName: e.workflowName,
		StepID:       running.StepID,
		Data:         normalizedOutput,
	}
}

// threadForFinalize returns the thread to record on stream_finalized markers.
func (e *StepExecutor) threadForFinalize() string {
	if e.execContext != nil {
		return e.execContext.Thread
	}
	return ""
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

// executeSaveMessage writes, from the workflow, the save_message of a node
// whose output the workflow assembled itself (the save was not delegated to an
// activity). Returns the SaveMessage output, or the error that kept the
// message from being written.
func (e *StepExecutor) executeSaveMessage(running *RunningStep, output map[string]interface{}) (map[string]interface{}, error) {
	node := running.Node
	if node.GetSaveMessage() == nil {
		return nil, nil
	}

	e.logger.Debug("[StepExecutor] Executing inline save_message",
		"stepID", node.GetId(),
	)

	workflowContext := buildSaveMessageWorkflowContext(e.workflowID, e.workflowName, e.chatID, e.workflowInputs, e.GetThread(), e.execContext)
	saveOutput, err := executeSaveMessageInline(
		e.ctx,
		node,
		output,
		workflowContext,
		e.chatID,
		e.workflowID,
		e.loopNodeID,
		e.loopIteration,
		e.saveIter(),
		running.PreallocatedMessageID,
	)
	if err != nil {
		e.logger.Error("[StepExecutor] Inline save_message failed",
			"stepID", node.GetId(),
			"error", err,
		)
		return nil, fmt.Errorf("save_message for %s: %w", node.GetId(), err)
	}
	return saveOutput, nil
}

// nodeScope is the NodeResolutionContext for this executor's nodes — the same
// scope EvaluateNodeConfig resolves their config against: the scope's
// completed nodes, inputs, workflow, and, inside a loop body, iter and the
// enclosing loop's previous-iteration outputs.
func (e *StepExecutor) nodeScope() *wfcel.NodeResolutionContext {
	scope := &wfcel.NodeResolutionContext{
		Inputs:   e.workflowInputs,
		Nodes:    e.nodeOutputs,
		Workflow: workflowContextToTyped(buildWorkflowContext(e.workflowID, e.workflowName, e.chatID, e.workflowInputs, e.execContext)),
		Outputs:  e.loopOutputs,
	}
	if e.loopNodeID != "" {
		scope.Iter = iterContextFromMap(e.iterContext())
	}
	return scope
}

// saveIter is the `iter` a workflow-side save_message sees: nil outside a
// loop, otherwise the scope's full iteration context (item/key included).
func (e *StepExecutor) saveIter() *model.IterContext {
	if e.loopNodeID == "" {
		return nil
	}
	return iterContextFromMap(e.iterContext())
}

// normalizeOutput ensures activity output has all schema fields present.
func (e *StepExecutor) normalizeOutput(rawOutput map[string]interface{}, activityName string) map[string]interface{} {
	return normalizeActivityOutput(rawOutput, activityName)
}

// normalizeActivityOutput fills an activity's decoded result out to its full
// output field set. It is the ONE definition of "the output a node's CEL
// sees": the workflow applies it to every step result, and the ActivityWrapper
// applies it to the result before evaluating the node's save_message, so a
// template reads the same map in both places.
//
// The activity result arrives as protojson without unpopulated fields, so every
// unset field — including unset sub-messages and fields inside present ones —
// is absent. schema.FillOutputDefaults restores each declared field as its zero
// value, recursively, from the output message's descriptor. message_only fields
// are NOT restored: they were cleared on purpose before the result entered
// history. The input map is not mutated.
//
// A CallLLM result additionally gets a stop_reason when it has none (see
// stopreason.FillDefault), so a loop condition never reads it empty — a
// result recorded before the field existed keeps its meaning.
func normalizeActivityOutput(rawOutput map[string]interface{}, activityName string) map[string]interface{} {
	output := schema.FillOutputDefaults(activityName, deepCopyJSONMap(rawOutput))
	if activityName == callLLMActivityName {
		stopreason.FillDefault(output)
	}
	return output
}

// callLLMActivityName is the registered name of the call_llm node's activity.
const callLLMActivityName = "CallLLM"

// deepCopyJSONMap copies the maps and slices of a decoded-JSON value so
// normalization can fill it in without writing through to the caller's map.
func deepCopyJSONMap(source map[string]interface{}) map[string]interface{} {
	if source == nil {
		return nil
	}
	copied := make(map[string]interface{}, len(source))
	for key, value := range source {
		copied[key] = deepCopyJSONValue(value)
	}
	return copied
}

func deepCopyJSONValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		return deepCopyJSONMap(typed)
	case []interface{}:
		items := make([]interface{}, len(typed))
		for index, item := range typed {
			items[index] = deepCopyJSONValue(item)
		}
		return items
	default:
		return value
	}
}

// ============================================================================
// Step Type Dispatchers
// ============================================================================

// startFailedStep returns a RunningStep that will immediately fail.
func (e *StepExecutor) startFailedStep(node *reliantv1.Node, event *core.WorkflowEvent, err error) *RunningStep {
	future := e.executeFailActivity(node, fmt.Sprintf("CEL evaluation failed for step %s: %v", node.GetId(), err))
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
	preallocatedMessageID string,
	event *core.WorkflowEvent,
) (workflow.Future, string, bool) {
	activityName := nodeTypeToActivityName(node.GetType())

	// Special handling for ExecuteTools - intercept "spawn" tool calls
	if node.GetType() == model.NodeTypeExecuteTools {
		// expected_response_tools is `repeated string`, so ResolveCELFields
		// leaves "{{ inputs.response_tool_name }}" as literal text. Resolve the
		// simple input references here, from the same inputs the call_llm's
		// response tool name resolves from: a literal template never matches a
		// tool call, which sent the model's response-tool call down the
		// regular tool path ("unknown tool: no placement") and discarded the
		// result.
		if etArgs := evalResult.GetExecuteTools(); etArgs != nil {
			etArgs.ExpectedResponseTools = resolveInputTemplates(etArgs.GetExpectedResponseTools(), e.workflowInputs)
		}

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

		// The capability set of the call_llm turn that produced these calls,
		// copied from its recorded output: execute_tools refuses anything
		// outside it, and load_tool grants against it. Owned by the runtime —
		// it is never authored — and nil only when that call_llm recorded no
		// set (research/TOOL_CAPABILITIES.md §4.1).
		if etArgs := evalResult.GetExecuteTools(); etArgs != nil {
			etArgs.Capabilities = e.upstreamToolCapabilities(node, etArgs.GetResolvedToolCalls())
		}

		rtx := e.buildRuntimeContext(node)
		// A batch with spawn/ask_user calls is merged workflow-side, so the
		// workflow is the executor of that output and saves it itself; any
		// other batch's result IS the ExecuteTools activity's result.
		delegated := false
		if !toolBatchAssembledByWorkflow(evalResult) {
			rtx.SaveMessage = e.saveMessageRequest(node)
			delegated = rtx.SaveMessage != nil
		}

		logging.Debug("[StepExecutor] startAction ExecuteTools",
			"stepID", node.GetId(),
			"loopNodeID", rtx.LoopNodeID,
			"loopIteration", rtx.LoopIteration,
			"saveDelegated", delegated,
		)

		// Always set, nil included: a spawn dispatched from this batch runs
		// on a context derived from this one and must not inherit it.
		toolsCtx := withToolBatchHandoff(e.ctx, e.askUserHandoff.forBatch(node.GetId(), event, e.nodeOutputs))
		future := executeToolsWithSpawnSupport(
			toolsCtx,
			e.activityOptions(node),
			rtx,
			evalResult,
			e.workflowInputs,
			e.childTracker,
			e.makeThreadPauseCtrl,
			e.makeThreadInterrupt,
		)
		return future, activityName, delegated
	}

	// Build structured input: pass the proto Node directly for proper protojson roundtrip
	rtx := e.buildRuntimeContext(node)
	rtx.AssistantMessageID = preallocatedMessageID
	rtx.SaveMessage = e.saveMessageRequest(node)
	input := types.ActivityInput{Runtime: rtx, Node: evalResult}

	future := workflow.ExecuteActivity(e.activityOptions(node), activityName, input)
	return future, activityName, rtx.SaveMessage != nil
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
			NodePath:      joinNodePath(e.nodePathPrefix, node.GetId()),
			Metadata:      metadata,
			Unattended:    IsUnattended(e.workflowInputs),
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

// startCompute completes a compute node on the spot. Its outputs are the node's
// config, and EvaluateNodeConfig has already resolved every {{ }} in them in
// this scope, so there is nothing left to run: no activity, no worker, no
// history beyond the workflow's own. An expression that fails never gets here;
// it fails the step in EvaluateNodeConfig like any other node's config.
func (e *StepExecutor) startCompute(node *reliantv1.Node, evalResult *reliantv1.Node) (workflow.Future, string) {
	future, settable := workflow.NewFuture(e.ctx)
	outputs := make(map[string]interface{})
	for name, value := range evalResult.GetCompute().GetOutputs() {
		outputs[name] = value.AsInterface()
	}
	// A structural node runs no activity, so nothing at the activity layer sees
	// its completion; report it the way loops and sub-workflows do.
	recordStructuralCompleted(e.ctx, joinNodePath(e.nodePathPrefix, node.GetId()), outputs)
	settable.SetValue(outputs)
	return future, computeActivityName
}

// computeActivityName labels a compute step in logs and output normalization.
// It is not a registered activity: nothing is dispatched under it.
const computeActivityName = "Compute"

// startRun starts a run (shell command) activity.
func (e *StepExecutor) startRun(node *reliantv1.Node, evalResult *reliantv1.Node) (workflow.Future, string, bool) {
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
	runInputs["node_path"] = joinNodePath(e.nodePathPrefix, node.GetId())
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
			ds, err := ResolveCelDaemonSelector(node.GetDaemon(), e.nodeScope())
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

	logging.Debug("[StepExecutor] startRun",
		"stepID", node.GetId(),
		"loopNodeID", e.loopNodeID,
		"loopIteration", e.loopIteration,
	)

	// The run step's input is a flat map, so the delegated save_message
	// request rides at its top level rather than on a RuntimeContext.
	saveReq := e.saveMessageRequest(node)
	if saveReq != nil {
		runInputs[types.RunStepSaveMessageKey] = saveReq
	}

	// The node's timeout bounds the COMMAND: it is the command timeout the
	// machine enforces (ExecuteRunStepInput.Timeout, in ms). The activity gets
	// RunStepMachineWaitBudget on top of it, for a command that never reached
	// its machine to wait for it inside the attempt — an allowance for that
	// wait only, never extra time for a command running on a healthy machine.
	// The resolved node first, so a timeout written as an expression counts.
	if timeout, ok := nodeTimeout(evalResult); ok {
		runInputs["timeout"] = int(timeout / time.Millisecond)
	} else if timeout, ok := nodeTimeout(node); ok {
		runInputs["timeout"] = int(timeout / time.Millisecond)
	}
	future := workflow.ExecuteActivity(e.activityOptionsWithSlack(node, types.RunStepMachineWaitBudget), "ExecuteRunStep", runInputs)
	return future, "ExecuteRunStep", saveReq != nil
}

// nodeTimeout is the node's own `timeout`, when it sets a positive one.
func nodeTimeout(node *reliantv1.Node) (time.Duration, bool) {
	if node == nil {
		return 0, false
	}
	timeoutStr := model.TimeoutExpr(node)
	if timeoutStr == "" {
		return 0, false
	}
	parsed, err := time.ParseDuration(timeoutStr)
	if err != nil || parsed <= 0 {
		return 0, false
	}
	return parsed, true
}

// activityOptions returns standard activity options, with optional node timeout override.
func (e *StepExecutor) activityOptions(node *reliantv1.Node) workflow.Context {
	return e.activityOptionsWithSlack(node, 0)
}

// activityOptionsWithSlack is activityOptions with slack added to a node's own
// timeout, for an activity that may legitimately wait before its work starts.
func (e *StepExecutor) activityOptionsWithSlack(node *reliantv1.Node, slack time.Duration) workflow.Context {
	// Default to 30 days - we have our own timeout mechanism via heartbeats
	timeout := 30 * 24 * time.Hour
	if parsed, ok := nodeTimeout(node); ok {
		timeout = parsed + slack
	}

	// HeartbeatTimeout controls how quickly Temporal detects a worker that died
	// without releasing its activities; see activityHeartbeatTimeout (registry.go).
	//
	// WaitForCancellation is deliberately unset, so a cancelled step's future
	// resolves at cancel-request time (~10ms) instead of waiting for the
	// activity's own return — which only reaches the worker on a heartbeat,
	// throttled to 3s. Setting it cost the user 1-3s on every pause and every
	// interrupt (measured live: 1.05s, 1.97s, 2.81s).
	//
	// Nothing is lost by not waiting, because no step depends on the workflow
	// receiving anything from a cancelled activity any more:
	//   - execute_tools writes each tool's outcome durably as it goes, through a
	//     context detached from cancellation (detachedForTerminalWrite), and a
	//     re-dispatch returns the recorded outcome rather than re-running
	//     (checkPriorTerminalResult).
	//   - call_llm persists its own partial turn the same way
	//     (persistInterruptedTurn), keyed on its position in the graph
	//     (callLLMIdempotencyKey) so the re-dispatch converges on one row.
	// The SDK discards a cancelled activity's late return in any case — it marks
	// the future handled the moment cancellation is requested — so harvesting
	// output from one was never something a waiting workflow could rely on.
	//
	// Let Temporal auto-generate ActivityID for deterministic replay.
	return workflow.WithActivityOptions(e.getActivityCtx(), workflow.ActivityOptions{
		StartToCloseTimeout: timeout,
		HeartbeatTimeout:    activityHeartbeatTimeout,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    time.Minute,
			MaximumAttempts:    stepActivityMaxAttempts,
		},
	})
}

// threadInterruptedSince reports whether this executor's thread was interrupted
// since the given epoch. Used to tell a per-thread interrupt apart from a
// chat-wide pause when reporting why a step was cancelled — the two arrive as
// the same CanceledError, and a log that guessed "pause" for both sent an
// investigation down the wrong path for hours.
func (e *StepExecutor) threadInterruptedSince(epoch int64) bool {
	interrupt := resolveThreadInterrupt(e.makeThreadInterrupt, e.threadInterrupt, e.GetThread())
	return interrupt != nil && interrupt.InterruptedSince(epoch)
}

// callLLMIdempotencyKey identifies a call_llm turn by its position in the graph
// — the same turn re-dispatched after an interrupt produces the same key, while
// a different node or a later loop iteration produces a different one.
//
// Length-prefixed per component (as injectIdempotencyKey does) so that no two
// different component triples can render to the same string.
func callLLMIdempotencyKey(workflowID, stepID, loopNodeID string, loopIteration int) string {
	// "noloop" rather than iter:0 when the step is not inside a loop, matching
	// injectIdempotencyKey. Outside a loop the iteration counter is not
	// meaningful, and spelling that distinction out keeps a top-level node from
	// sharing a key shape with the first iteration of a loop that reuses its id.
	loop := "noloop"
	if loopNodeID != "" {
		loop = fmt.Sprintf("iter:%d", loopIteration)
	}
	var b strings.Builder
	b.WriteString(model.NodeTypeCallLLM)
	for _, part := range []string{workflowID, stepID, loopNodeID, loop} {
		fmt.Fprintf(&b, "|%d:%s", len(part), part)
	}
	return b.String()
}

// executeFailActivity returns a future that will fail with the given message.
func (e *StepExecutor) executeFailActivity(node *reliantv1.Node, errorMsg string) workflow.Future {
	return workflow.ExecuteActivity(
		workflow.WithActivityOptions(e.getActivityCtx(), workflow.ActivityOptions{
			StartToCloseTimeout: time.Second,
		}),
		"FailStep",
		e.failStepInput(node, errorMsg),
	)
}

// failStepInput is FailStep's input. Beyond the message, it names the failing
// node exactly as a graph activity's runtime context does — step id, loop
// scope and node_path — so the activity wrapper files the failure under that
// node: a failed node event and a step row carrying the error. Without them a
// step whose {{ }} expression failed to evaluate had no row and no event, and
// the canvas could not say which step broke. The FailStep handler itself reads
// only the message.
func (e *StepExecutor) failStepInput(node *reliantv1.Node, errorMsg string) map[string]interface{} {
	input := map[string]interface{}{
		"chat_id":     e.chatID,
		"workflow_id": e.workflowID,
		"step_id":     node.GetId(),
		"error":       errorMsg,
		"node_path":   joinNodePath(e.nodePathPrefix, node.GetId()),
	}
	if e.loopNodeID != "" {
		input["loop_node_id"] = e.loopNodeID
		input["loop_iteration"] = e.loopIteration
	}
	return input
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
		NodePath:   joinNodePath(e.nodePathPrefix, node.GetId()),
	}

	// Thread from execution context
	if e.execContext != nil {
		rtx.Thread = e.execContext.Thread
	}

	// Who this run's integration writes speak as (see InputKeyActingIdentity).
	if e.childTracker != nil && e.childTracker.chatProvider != "" && (model.NodeType(node) == model.NodeTypeCallLLM || model.NodeType(node) == model.NodeTypeCompact) {
		key := e.providerPinsKey()
		rtx.ClearProviderPins = !e.childTracker.providerPinCleanups[key]
	}
	rtx.Unattended = IsUnattended(e.workflowInputs)
	rtx.ActingIdentity = ActingIdentity(e.workflowInputs)
	rtx.Automation, _ = e.workflowInputs[InputKeyAutomation].(string)

	// What load_tool has granted on this thread so far. call_llm offers it,
	// within what the node declares; this is how a tool loaded on one turn is
	// callable on the next, whichever worker runs either.
	if model.NodeType(node) == model.NodeTypeCallLLM {
		rtx.ToolGrants = e.childTracker.toolGrantsFor(rtx.Thread)
		// Whether anyone is attending decides which tools the turn may be
		// handed. Read off this executor's inputs, which every sub-workflow,
		// loop body and spawned sub-agent inherits unattended into
		// (propagateUnattended).
		rtx.Unattended = IsUnattended(e.workflowInputs)
		// The provider the user chose for the run: what a tier selector with
		// no provider of its own resolves on (propagateProviderPreference).
		rtx.ProviderPreference = ProviderPreference(e.workflowInputs)
		if driver := e.childTracker.chatProvider; driver != "" {
			rtx.ChatProvider = driver
			rtx.Descendant = rtx.Thread != e.childTracker.rootThread
			rtx.PresetModelTags = presetModelTags(e.workflowInputs)
			// Kept for query/observability consumers too; strict enforcement
			// happens at the activity's model-resolution boundary.
			rtx.ProviderPreference = []string{driver}
		}
		rtx.Failover = failoverPolicy(e.workflowInputs)
		// While the thread is on a fallback model (a usage limit on its own
		// provider), the lease replaces the chat's selector; the first turn
		// after it says the chat is back. Compared in the workflow, so a
		// replay derives the same model.
		rtx.FallbackModel, rtx.FallbackReturn = e.pauseCtrl.fallbackFor(e.ctx, rtx.Thread)
		// Whether the loop took this turn on a hint (see
		// RuntimeContext.SpeculativeTurn).
		rtx.SpeculativeTurn = e.childTracker.isSpeculativeTurn(rtx.Thread)
		// The catalog the run started with: the spawn tool describes its
		// presets from it (catalog_pin.go).
		rtx.CatalogPin = catalogPinFrom(e.ctx)
	}

	// Compaction summarizes on the model the thread is on, so a chat on a
	// fallback provider is not sent back to the limited one to compact. It is
	// refused by the same limit call_llm is, so it decides the same way: it
	// carries the chat's failover policy too.
	if model.NodeType(node) == model.NodeTypeCompact {
		rtx.ChatProvider = e.childTracker.chatProvider
		rtx.Descendant = rtx.Thread != e.childTracker.rootThread
		rtx.PresetModelTags = presetModelTags(e.workflowInputs)
		rtx.FallbackModel = e.pauseCtrl.liveFallback(e.ctx, rtx.Thread)
		rtx.Failover = failoverPolicy(e.workflowInputs)
	}

	// Loop context
	if e.loopNodeID != "" {
		rtx.LoopNodeID = e.loopNodeID
		rtx.LoopIteration = e.loopIteration
	}

	// A call_llm turn is identified by its POSITION in the graph, not by the
	// attempt that happened to run it.
	//
	// SaveMessage's default idempotency key is scoped to the Temporal ActivityID,
	// which is minted fresh on every dispatch. That is right for most steps, but
	// it means a call_llm re-dispatched after an interrupt writes a SECOND
	// assistant message for the same turn instead of recognising its own earlier
	// partial. Keying on (workflow, step, iteration) — all stable across a
	// re-dispatch — makes the save converge on one row, which is what lets the
	// activity persist its partial durably rather than handing it back through
	// Temporal's cancellation payload.
	//
	// Gated on the same version as the pre-allocated message id: histories
	// recorded before that gate replay with the old RunID-scoped key and are
	// unaffected. Format mirrors injectIdempotencyKey — length-prefixed, so no
	// combination of components can collide by concatenation.
	if model.NodeType(node) == model.NodeTypeCallLLM &&
		workflow.GetVersion(e.ctx, streamFinalizedVersionGate, workflow.DefaultVersion, 1) >= 1 {
		rtx.MessageIdempotencyKey = callLLMIdempotencyKey(rtx.WorkflowID, rtx.StepID, e.loopNodeID, e.loopIteration)
	}

	// SpawnedBy from parent context
	if e.execContext != nil && e.execContext.Parent != nil && e.execContext.Parent.StepPath != "" {
		rtx.SpawnedBy = e.execContext.Parent.StepPath
	}

	// Spawn depth and parent permission from execution context
	if e.execContext != nil {
		rtx.SpawnDepth = e.execContext.SpawnDepth
		rtx.ParentPermission = e.execContext.ParentPermission
	}

	// Project path
	if e.execContext != nil && e.execContext.ProjectPath != "" {
		rtx.ProjectPath = e.execContext.ProjectPath
	}

	// User JWT — propagated so activity workers in a separate process can
	// hydrate the in-memory auth map and resolve the Reliant LLM driver.
	if e.execContext != nil && e.execContext.UserJWT != "" {
		rtx.UserJWT = e.execContext.UserJWT
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
			ds, err := ResolveCelDaemonSelector(node.GetDaemon(), e.nodeScope())
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

// ============================================================================
// Delegated save_message
// ============================================================================

// saveMessageRequest builds the request that delegates node's save_message to
// the activity executing it, or nil when the node has none.
func (e *StepExecutor) saveMessageRequest(node *reliantv1.Node) *types.SaveMessageRequest {
	config := node.GetSaveMessage()
	if config == nil {
		return nil
	}
	workflowContext := buildWorkflowContext(e.workflowID, e.workflowName, e.chatID, e.workflowInputs, e.execContext)
	req := &types.SaveMessageRequest{
		Config:    config,
		Inputs:    e.saveMessageInputs(node),
		Workflow:  *workflowContextToTyped(workflowContext),
		AgentName: saveMessageAgentName(workflowContext),
	}
	// The same full iter (iteration, index, item, key) the node's config and
	// the workflow-side save see. The item ships only when the save_message
	// actually reads it: it can be arbitrarily large and would otherwise ride
	// along on every dispatch.
	if e.loopNodeID != "" {
		req.Iter = e.saveMessageIter(node)
	}
	return req
}

// saveMessageIter is the `iter` a delegated save_message sees: this scope's
// full iteration context, minus `item` when no expression in the save reads
// iter at all (an unparseable expression keeps it — under-shipping would turn
// a template that works workflow-side into a "no such key" in the worker).
func (e *StepExecutor) saveMessageIter(node *reliantv1.Node) *model.IterContext {
	iter := iterContextFromMap(e.iterContext())
	exprs, err := saveMessageExpressions(node.GetSaveMessage())
	if err != nil {
		return iter
	}
	refs, err := wfcel.ReferencesOf(exprs, wfcel.CELIter)
	if err == nil && !refs.Referenced {
		iter.Item = nil
	}
	return iter
}

// saveMessageInputs is the subset of workflow inputs a save_message reads,
// plus `thread`, which every save needs.
func (e *StepExecutor) saveMessageInputs(node *reliantv1.Node) map[string]interface{} {
	refs := e.saveMessageInputRefs(node)
	var inputs map[string]interface{}
	if refs.Whole {
		inputs = copyMap(e.workflowInputs)
	} else {
		inputs = make(map[string]interface{}, len(refs.Fields)+1)
		for _, field := range refs.Fields {
			if v, ok := e.workflowInputs[field]; ok {
				inputs[field] = v
			}
		}
	}
	if thread := e.GetThread(); thread != "" {
		inputs["thread"] = thread
	}
	return inputs
}

// saveMessageInputRefs reports which workflow inputs node's save_message reads,
// computed once per node. An expression that cannot be parsed ships the whole
// inputs map: under-shipping would turn a template that works workflow-side
// into a "no such key" in the worker.
func (e *StepExecutor) saveMessageInputRefs(node *reliantv1.Node) wfcel.NamespaceRefs {
	if refs, ok := e.saveInputRefs[node.GetId()]; ok {
		return refs
	}
	var refs wfcel.NamespaceRefs
	exprs, err := saveMessageExpressions(node.GetSaveMessage())
	if err == nil {
		refs, err = wfcel.ReferencesOf(exprs, wfcel.CELInputs)
	}
	if err != nil {
		e.logger.Warn("[StepExecutor] Could not analyze save_message input references; sending all inputs",
			"stepID", node.GetId(), "error", err)
		refs = wfcel.NamespaceRefs{Referenced: true, Whole: true}
	}
	if e.saveInputRefs == nil {
		e.saveInputRefs = make(map[string]wfcel.NamespaceRefs)
	}
	e.saveInputRefs[node.GetId()] = refs
	return refs
}

// saveMessageExpressions lists every CEL expression in a save_message: the
// {{...}} expressions of its templated fields and its raw-CEL condition.
func saveMessageExpressions(config *reliantv1.SaveMessageConfig) ([]string, error) {
	if config == nil {
		return nil, nil
	}
	var exprs []string
	for _, field := range []*reliantv1.CelString{
		config.GetRole(), config.GetContent(), config.GetToolCalls(),
		config.GetToolResults(), config.GetAttachments(), config.GetDisplayStyle(),
	} {
		fieldExprs, err := wfcel.TemplateExpressions(model.CelStringRaw(field), false)
		if err != nil {
			return nil, err
		}
		exprs = append(exprs, fieldExprs...)
	}
	if cond := model.DirectCelExpr(config.GetCondition()); cond != "" {
		exprs = append(exprs, cond)
	}
	return exprs, nil
}
