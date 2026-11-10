// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"errors"
	"strings"

	"go.temporal.io/sdk/workflow"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
)

// finally: a workflow's handler for however a run ends.
//
// A workflow that declares `finally:` gets ONE run of that inline graph when a
// run of it ENDS: it completed, it failed, or it was cancelled. It exists for
// what on_failure cannot do: on_failure runs only on a failure, by contract,
// so a workflow that takes a lease, a claim or a label had to release it on
// the success path AND in on_failure — and a cancelled run released it
// nowhere, leaving it held until its TTL. finally is the one place it goes.
//
// A cancel reaches it because the run service now cancels cooperatively
// (runs.Service.Terminate): Temporal cancels the run's context, the graph
// unwinds, and the completion defer runs this on a disconnected context.
//
// It does not run when the run pauses (it is coming back) or hands off to a
// successor run (continue-as-new: the work goes on). When the run failed, it
// runs AFTER on_failure. Like on_failure it cannot change how the run ended,
// it is bounded by onFailureTimeout, nothing in it can pause, and an error in
// it is logged and dropped.
//
// Its nodes read `run.status` — RunStatusCompleted, RunStatusFailed or
// RunStatusCancelled — and `failure.*`, every key present and empty unless the
// run failed, so one handler can guard on either without a has().
//
// A child loaded by ref that declares finally runs its own when the child
// ends, in the child's scope, before the parent's (child_on_failure.go).

// finallyChangeID gates running the handler: it adds commands to the tail of
// histories that, recorded before it, ended without them.
const finallyChangeID = "workflow-finally"

// FinallyNodeID is the id the handler runs under, and so the prefix of its
// nodes' paths ("finally.release"). Validation rejects a main-graph node with
// this id in a workflow that declares a handler.
const FinallyNodeID = "finally"

// RunInputKey is the reserved input key the handler's `run` namespace is read
// from. See wfcel.RunInputKey.
const RunInputKey = wfcel.RunInputKey

// The values of run.status.
const (
	RunStatusCompleted = "completed"
	RunStatusFailed    = "failed"
	RunStatusCancelled = "cancelled"
)

// FinallyQuery reports whether the run's finally handler ran, and for what
// ending; it reads state the workflow already holds and writes nothing (see
// OnFailureQuery).
const FinallyQuery = "get_finally"

// FinallyRecord is the FinallyQuery result.
type FinallyRecord struct {
	Ran          bool   `json:"ran"`
	Status       string `json:"status,omitempty"`
	HandlerError string `json:"handler_error,omitempty"`
	TimedOut     bool   `json:"timed_out,omitempty"`
}

// runEnding classifies how a run that is returning retErr with the verdict
// runOutcome ends, for its finally handler. ok is false when the run is not
// ending at all: it is handing off to a successor (continue-as-new).
//
// A cancel is read first: a cancelled run unwinds with whatever error its
// cancelled step returned, and that error is the cancel, not a failure. A
// thread that was cancelled (ErrThreadCancelled, a ref'd child running as a
// spawned agent the user stopped) ended cancelled too.
func runEnding(ctx workflow.Context, retErr error, runOutcome string) (status string, ok bool) {
	if retErr != nil && isContinueAsNew(retErr) {
		return "", false
	}
	var panicErr *WorkflowPanicError
	if errors.As(retErr, &panicErr) {
		return RunStatusFailed, true
	}
	if ctx.Err() != nil || isMachineWaitAbandoned(retErr) || errors.Is(retErr, ErrThreadCancelled) {
		return RunStatusCancelled, true
	}
	if runFailed(ctx, retErr, runOutcome) {
		return RunStatusFailed, true
	}
	return RunStatusCompleted, true
}

// runCELValue is the `run` namespace.
func runCELValue(status string) map[string]interface{} {
	return map[string]interface{}{"status": status}
}

// runFinallyAt runs a finally handler whose workflow sits at scopePath ("" for
// the root, the `workflow:` node's path for a ref'd child). failure is the
// run's failure when status is RunStatusFailed, and ignored otherwise.
func runFinallyAt(
	ctx workflow.Context,
	handler *reliantv1.Workflow,
	input WorkflowInput,
	workflowID string,
	execCtx *ExecutionContext,
	childTracker *ChildWorkflowTracker,
	nodeOutputs map[string]interface{},
	status string,
	failure Failure,
	record *FinallyRecord,
	scopePath string,
) {
	*record = FinallyRecord{Ran: true, Status: status}
	if status != RunStatusFailed {
		failure = Failure{}
	}
	workflow.GetLogger(ctx).Info("[Workflow Runtime] Running finally handler",
		"workflowID", workflowID,
		"scope", scopePath,
		"status", status,
	)
	record.TimedOut, record.HandlerError = runInlineHandler(ctx, FinallyNodeID, handler, input, workflowID, execCtx, childTracker,
		nodeOutputs, map[string]interface{}{
			FailureInputKey: failure.CELValue(),
			RunInputKey:     runCELValue(status),
		}, scopePath)
}

// finallyGate decides, once and only for a run that has a handler, whether
// this history runs it. A disconnected context, so a cancelled run can still
// record the decision.
func finallyGate(ctx workflow.Context) bool {
	dctx, _ := workflow.NewDisconnectedContext(ctx)
	return workflow.GetVersion(dctx, finallyChangeID, workflow.DefaultVersion, 1) != workflow.DefaultVersion
}

// childFinallyPath is a ref'd child's failure path relative to the child.
func childFinallyPath(path, childPath string) string {
	return strings.TrimPrefix(path, childPath+".")
}
