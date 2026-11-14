// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"errors"
	"strings"
	"time"

	"go.temporal.io/sdk/workflow"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/core"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// on_failure: a workflow's finally-style handler.
//
// A workflow that declares `on_failure:` gets ONE run of that inline graph when
// its run FAILS: an error nothing handled (a node whose activity failed for
// good, a config that would not evaluate, a sub-workflow that failed) or a
// verdict of `outcome: failure`. Not when it completes, pauses or is
// cancelled, because only a failure leaves work half done with nobody told.
//
// What the handler cannot do is change the verdict. The run still ends FAILED:
// the handler's job is to tell someone, release something, or record why, and
// a handler that could turn a failure into a success would make every failure
// ambiguous. An error inside the handler is logged and dropped, and the handler
// is never run for its own failure.

// onFailureChangeID gates running the handler. The handler adds commands
// (activities) to a run's tail, so a history recorded before it existed must
// replay without them.
const onFailureChangeID = "workflow-on-failure"

// FailureInputKey is the reserved input key the handler's `failure` namespace is
// read from. See wfcel.FailureInputKey.
const FailureInputKey = wfcel.FailureInputKey

// onFailureTimeout bounds the whole handler. A handler that cannot finish in
// this long is cut off: the run is already failed, and a hung notification must
// not keep it from ending.
const onFailureTimeout = 5 * time.Minute

// Failure is what an on_failure handler's nodes read as `failure`.
type Failure struct {
	// NodeID is the id of the node that failed ("" when the run failed outside
	// any node: a definition that would not load, a panic).
	NodeID string
	// NodePath is that node's fully-qualified graph path ("loop.body.node"), or
	// the node id for a top-level node.
	NodePath string
	// Error is the failure as one readable line.
	Error string
	// ErrorCode classifies it: "node_error" for an error from a node, "outcome"
	// for a node that declared outcome: failure, "panic" for an internal error.
	ErrorCode string
	// Attempt is the iteration of the loop the node sat in, 1-based, or 1 when
	// it was not in a loop.
	Attempt int
}

// OnFailureQuery reports whether the run's on_failure handler ran and what it
// ran for. It reads state the workflow already holds and writes nothing, so a
// scenario can assert on the failure context without the harness steering the
// run (see structural_observer.go for the same reasoning).
const OnFailureQuery = "get_on_failure"

// OnFailureRecord is the OnFailureQuery result.
type OnFailureRecord struct {
	Ran          bool   `json:"ran"`
	NodeID       string `json:"node_id,omitempty"`
	NodePath     string `json:"node_path,omitempty"`
	Error        string `json:"error,omitempty"`
	ErrorCode    string `json:"error_code,omitempty"`
	Attempt      int    `json:"attempt,omitempty"`
	HandlerError string `json:"handler_error,omitempty"`
	TimedOut     bool   `json:"timed_out,omitempty"`
}

// Failure error codes.
const (
	FailureCodeNodeError = "node_error"
	FailureCodeOutcome   = "outcome"
	FailureCodePanic     = "panic"
)

// CELValue is the `failure` namespace. Every key is always present, so a
// handler never fails with "no such key" while reporting a failure.
func (f Failure) CELValue() map[string]interface{} {
	attempt := f.Attempt
	if attempt < 1 {
		attempt = 1
	}
	return map[string]interface{}{
		"node_id":    f.NodeID,
		"node_path":  f.NodePath,
		"error":      f.Error,
		"error_code": f.ErrorCode,
		"attempt":    int64(attempt),
	}
}

// failureRecorder remembers where the run's first failure happened. The error
// that reaches the top of the run has been wrapped on the way up
// ("loop step x failed: sub-workflow y failed: step z exhausted retries"), and
// the node's identity is not recoverable from the text, so the executors record
// it where they see it.
//
// Only the FIRST failure is kept: the ones after it are the same failure
// unwinding.
type failureRecorder struct {
	nodeID, nodePath string
	attempt          int
	set              bool
	// parent is the enclosing scope's recorder: a ref'd child that has its
	// own on_failure records its failures for itself AND for the run, so
	// both handlers see where it happened.
	parent *failureRecorder
}

func (r *failureRecorder) record(nodePath string, attempt int) {
	if r == nil || nodePath == "" {
		return
	}
	r.parent.record(nodePath, attempt)
	if r.set {
		return
	}
	r.set = true
	r.nodePath = nodePath
	if i := strings.LastIndexByte(nodePath, '.'); i >= 0 {
		r.nodeID = nodePath[i+1:]
	} else {
		r.nodeID = nodePath
	}
	r.attempt = attempt
}

type failureRecorderKey struct{}

// withFailureRecorder attaches a recorder to the workflow context so every
// nested executor (loop bodies, inline sub-workflows, parallel iterations)
// reports into the same one.
func withFailureRecorder(ctx workflow.Context, rec *failureRecorder) workflow.Context {
	return workflow.WithValue(ctx, failureRecorderKey{}, rec)
}

// recordFailedNode notes that the node at this path is where the run failed.
// A no-op when no recorder is attached.
func recordFailedNode(ctx workflow.Context, nodePath string, attempt int) {
	if rec := failureRecorderFrom(ctx); rec != nil {
		rec.record(nodePath, attempt)
	}
}

// failureRecorderFrom is the recorder attached to ctx, or nil.
func failureRecorderFrom(ctx workflow.Context) *failureRecorder {
	if ctx == nil {
		return nil
	}
	rec, _ := ctx.Value(failureRecorderKey{}).(*failureRecorder)
	return rec
}

// failureFor builds the Failure of a run that ended with retErr and the
// verdict runOutcome. It never fails: a handler that is handed a poor failure
// still runs.
func failureFor(rec *failureRecorder, retErr error, runOutcome string, outcomeNode string) Failure {
	f := Failure{Attempt: 1}
	if rec != nil && rec.set {
		f.NodeID, f.NodePath, f.Attempt = rec.nodeID, rec.nodePath, rec.attempt
	}
	switch {
	case retErr != nil:
		var panicErr *WorkflowPanicError
		if errors.As(retErr, &panicErr) {
			f.ErrorCode = FailureCodePanic
		} else {
			f.ErrorCode = FailureCodeNodeError
		}
		f.Error = oneLineError(retErr)
	default:
		f.ErrorCode = FailureCodeOutcome
		f.Error = "the run reached a node that declares outcome: failure"
		if f.NodeID == "" {
			f.NodeID, f.NodePath = outcomeNode, outcomeNode
		}
	}
	return f
}

// runFailed reports whether a run that is completing with retErr and the
// verdict runOutcome FAILED, for the purpose of running its on_failure handler.
//
// It is deliberately narrower than "retErr != nil": a cancelled run, a run that
// is handing off to a successor (continue-as-new) and a run a user walked away
// from while it waited for a machine are all non-nil errors that are not
// failures, and a handler that fired for them would report problems that did
// not happen.
func runFailed(ctx workflow.Context, retErr error, runOutcome string) bool {
	if retErr != nil {
		if ctx.Err() != nil || isContinueAsNew(retErr) || isMachineWaitAbandoned(retErr) {
			return false
		}
		return true
	}
	return runOutcome == model.OutcomeFailure
}

// runOnFailure runs the workflow's on_failure handler, once, and swallows
// everything that goes wrong in it.
//
// The handler is an inline graph executed by the same inline executor a
// `workflow:` node's body is, which is what makes its nodes behave exactly like
// any other nodes (templates, save_message, actions, agents). Three things
// differ from a body, and each is deliberate:
//
//   - `nodes` starts as a SNAPSHOT of the main graph's outputs. A handler
//     reads what the run had produced, and must never write back into it.
//   - `failure` is injected as an input under a reserved key, so it exists
//     inside the handler and nowhere else (wfcel.FailureInputKey).
//   - The pause controller is fail-fast: no pause on retry exhaustion, no
//     daemon-offline breaker, no usage-limit wait. A handler that blocks
//     forever on a condition that needs a person would hold a FAILED run open,
//     which is the opposite of what it is for.
func runOnFailure(
	ctx workflow.Context,
	handler *reliantv1.Workflow,
	input WorkflowInput,
	workflowID string,
	execCtx *ExecutionContext,
	childTracker *ChildWorkflowTracker,
	nodeOutputs map[string]interface{},
	failure Failure,
	record *OnFailureRecord,
) {
	runOnFailureAt(ctx, handler, input, workflowID, execCtx, childTracker, nodeOutputs, failure, record, "")
}

// runOnFailureAt runs a handler whose workflow sits at scopePath in the run's
// graph: "" for the root workflow, the node path of the `workflow:` node that
// ran a ref'd child ("release"), so the handler's nodes are addressed
// "release.on_failure.<id>" and never collide with the root handler's.
func runOnFailureAt(
	ctx workflow.Context,
	handler *reliantv1.Workflow,
	input WorkflowInput,
	workflowID string,
	execCtx *ExecutionContext,
	childTracker *ChildWorkflowTracker,
	nodeOutputs map[string]interface{},
	failure Failure,
	record *OnFailureRecord,
	scopePath string,
) {
	logger := workflow.GetLogger(ctx)
	*record = OnFailureRecord{Ran: true, NodeID: failure.NodeID, NodePath: failure.NodePath, Error: failure.Error,
		ErrorCode: failure.ErrorCode, Attempt: failure.Attempt}
	if handler == nil || len(handler.GetNodes()) == 0 {
		return
	}
	// A disconnected context: the run may be ending with its own context
	// already cancelled by a failing step, and the handler must still be able
	// to dispatch.
	hctx, cancel := workflow.WithCancel(ctx)
	defer cancel()
	hctx, _ = workflow.NewDisconnectedContext(hctx)
	timerCtx, cancelTimer := workflow.WithCancel(hctx)
	defer cancelTimer()

	// The handler's inputs: the run's own, plus the failure. A copy, so the
	// reserved key never leaks into the run's live inputs.
	handlerInputs := make(map[string]interface{}, len(input.Inputs)+1)
	for k, v := range input.Inputs {
		handlerInputs[k] = v
	}
	handlerInputs[FailureInputKey] = failure.CELValue()

	// The snapshot: top-level keys copied, so adding to it cannot reach the
	// run's store. The values themselves are read-only by convention.
	snapshot := make(map[string]interface{}, len(nodeOutputs))
	for k, v := range nodeOutputs {
		snapshot[k] = v
	}

	node := &reliantv1.Node{
		Id:   OnFailureNodeID,
		Type: model.NodeTypeWorkflow,
		Args: &reliantv1.Node_Workflow{Workflow: &reliantv1.SubWorkflowArgs{Inline: handler}},
	}

	exec, err := NewInlineWorkflowExecutor(
		hctx, workflowID, input.ChatID, input.WorkflowName,
		handlerInputs, snapshot, childTracker,
		node, node, "", -1,
	)
	if err != nil {
		logger.Error("[Workflow Runtime] on_failure handler could not start", "workflowID", workflowID, "error", err)
		return
	}
	// The handler's thread is the run's own: it reports where the run reports.
	exec = exec.WithInvocationContract(inlineHandlerContract(input.WorkflowName, joinNodePath(scopePath, OnFailureNodeID))).
		WithNodePathPrefix(scopePath).
		WithSeedNodeOutputs(snapshot).
		WithExecContext(execCtx.ForChild(OnFailureNodeID, model.ThreadModeInherit, input.WorkflowName, true)).
		WithProjectPath(execCtx.ProjectPath).
		WithPauseController(failFastPauseController())

	logger.Info("[Workflow Runtime] Running on_failure handler",
		"workflowID", workflowID,
		"scope", scopePath,
		"failedNode", failure.NodePath,
		"errorCode", failure.ErrorCode,
	)

	done := workflow.NewChannel(hctx)
	var runErr error
	workflow.Go(hctx, func(gCtx workflow.Context) {
		exec = exec.WithWorkflowContext(gCtx)
		_, runErr = executeRecovering(gCtx, exec.Execute)
		done.Send(gCtx, true)
	})

	finished := false
	selector := workflow.NewSelector(hctx)
	selector.AddReceive(done, func(c workflow.ReceiveChannel, _ bool) {
		c.Receive(hctx, nil)
		finished = true
	})
	selector.AddFuture(workflow.NewTimer(timerCtx, onFailureTimeout), func(workflow.Future) {})
	selector.Select(hctx)

	switch {
	case !finished:
		record.TimedOut = true
		logger.Error("[Workflow Runtime] on_failure handler timed out; the run still fails", "workflowID", workflowID, "timeout", onFailureTimeout)
	case runErr != nil:
		record.HandlerError = oneLineError(runErr)
		logger.Error("[Workflow Runtime] on_failure handler failed; the run still fails", "workflowID", workflowID, "error", runErr)
	}
}

// OnFailureNodeID is the id the handler runs under, and so the prefix of its
// nodes' paths ("on_failure.notify"), which is how a scenario names them.
// Validation rejects a main-graph node with this id in a workflow that declares
// a handler.
const OnFailureNodeID = "on_failure"

// failFastPauseController is the handler's PauseController: every hook is
// absent, so nothing in it can pause, wait out a limit or trip a breaker, and
// FailFast makes a step that exhausts its retries end the handler instead of
// parking it.
func failFastPauseController() *PauseController {
	return &PauseController{FailFast: true}
}

// inlineHandlerContract is the semantic contract of the handler as an inline
// body: it inherits the run's inputs by reference and carries its own graph.
func inlineHandlerContract(workflowName, nodePath string) core.SubWorkflowContract {
	return core.SubWorkflowContract{
		NodePath:          nodePath,
		NodeID:            OnFailureNodeID,
		NodeType:          model.NodeTypeWorkflow,
		InvocationMode:    core.InvocationModeInline,
		WorkflowIdentity:  workflowName,
		ParentWorkflowRef: workflowName,
		InputPolicy:       core.InputPolicyInlineInheritParentInputs,
		LoadStrategy:      core.LoadStrategyInlineEmbedded,
		InputAssembly:     []core.InputAssemblyStage{core.InputAssemblyStageInheritParentInputs},
	}
}
