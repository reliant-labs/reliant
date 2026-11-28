// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"errors"
	"time"

	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	types "github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// A node that declares `tolerate_failure: true` is optional (reliant#2752):
// when it fails, the graph goes on as though it had completed, with outputs
// that say it did not:
//
//	nodes.<id>.failed  true
//	nodes.<id>.error   why, one line
//
// so an edge or a later node branches on it. An optional reader agent (the
// pr-review cp_reader) that hits a bad path no longer fails the whole run.
//
// The node's own executor decides, at the point where its failure would
// otherwise become the graph's: an activity step in StepExecutor.HandleCompletion
// (every graph, loop body and sub-workflow runs its steps there), and a loop,
// sub-workflow or router node where its executor returns its error. A
// workflow definition is loaded by a recorded activity, so a history recorded
// before the field existed has no node that sets it, and replays unchanged.

// Output keys of a tolerated failure.
const (
	toleratedFailedKey = "failed"
	toleratedErrorKey  = "error"
)

// toleratedFailure returns the outputs that stand for node's failure with err,
// and true, when node declares tolerate_failure and err is a failure it may
// tolerate. A cancel (a pause, a CancelWorkflow), a panic, and a run that is
// already ending (a continue-as-new handoff, a gave-up self-pause) are not the
// node failing: they are never tolerated, so they unwind as they always have.
func toleratedFailure(node *reliantv1.Node, err error) (map[string]interface{}, bool) {
	if err == nil || !node.GetTolerateFailure() || !tolerable(err) {
		return nil, false
	}
	return map[string]interface{}{
		toleratedFailedKey: true,
		toleratedErrorKey:  oneLineError(err),
	}, true
}

// tolerable reports whether err is the node failing, as opposed to the run
// being stopped or handed off around it.
func tolerable(err error) bool {
	var canceled *temporal.CanceledError
	if errors.As(err, &canceled) {
		return false
	}
	var panicErr *WorkflowPanicError
	if errors.As(err, &panicErr) {
		return false
	}
	if isContinueAsNew(err) || isMachineWaitAbandoned(err) {
		return false
	}
	return true
}

// logToleratedFailure says, once, that nodeID failed and the run went on. A
// warning: the failure is real, and the run's outcome will say it was handled.
func logToleratedFailure(logger log.Logger, nodeID string, err error) {
	logger.Warn("[Workflow Runtime] Node failed; it tolerates failure, so the run continues",
		"stepID", nodeID, "error", err)
}

// toleratedFailureRecordChangeID gates recording a tolerated failure as the
// node's failed attempt (recordToleratedFailure). It adds an activity command,
// and is consulted only where a tolerated failure happens.
const toleratedFailureRecordChangeID = "tolerated-failure-record"

// recordToleratedFailureActivityName is handlers.RecordToleratedFailureActivityName,
// spelled here as the runtime spells "FailStep": the runtime does not import
// the handlers.
const recordToleratedFailureActivityName = "RecordToleratedFailure"

// toleratedScope is where a tolerated node sits, for its attempt row.
type toleratedScope struct {
	WorkflowID    string
	StepID        string
	NodePath      string
	LoopNodeID    string
	LoopIteration int
}

// recordToleratedFailure puts a tolerated node's failure on record as the
// node's failed attempt (handlers.RecordToleratedFailureActivity): a
// step_executions row with the error, which is what the run's outcome and its
// diagnostics read. Without it, a failure decided in the workflow — the
// repeated-tool-error breaker ending an agent, a loop or sub-workflow that
// ended — left every attempt successful, and a run that went on past it read
// as having succeeded.
//
// The activity always fails, by design; its error is expected and dropped.
// Best effort: a record that cannot be written never changes the run.
func recordToleratedFailure(ctx workflow.Context, scope toleratedScope, err error) {
	if ctx == nil || scope.StepID == "" || err == nil {
		return
	}
	if workflow.GetVersion(ctx, toleratedFailureRecordChangeID, workflow.DefaultVersion, 1) == workflow.DefaultVersion {
		return
	}
	recordNodeFailure(ctx, scope, oneLineError(err))
}

// recordNodeFailure writes the failed attempt recordToleratedFailure
// describes, ungated: each caller gates it under its own change id, since it
// adds an activity command where none was. It is also how a sub-workflow's
// declared failure is put on record (child_outcome.go).
func recordNodeFailure(ctx workflow.Context, scope toleratedScope, errText string) {
	if ctx == nil || scope.StepID == "" || errText == "" {
		return
	}
	input := map[string]interface{}{
		"workflow_id": scope.WorkflowID,
		"step_id":     scope.StepID,
		"node_path":   scope.NodePath,
		"error":       errText,
	}
	if scope.LoopNodeID != "" {
		input["loop_node_id"] = scope.LoopNodeID
		input["loop_iteration"] = scope.LoopIteration
	}
	// The run the node belongs to, recorded on its attempt row: the run's
	// outcome reads only the attempts under its id
	// (store.FirstHandledNodeFailure).
	if root := rootRunIDFrom(ctx); root != "" {
		input["telemetry"] = &types.RunTelemetry{RootRunID: root}
	}
	actCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 1},
	})
	_ = workflow.ExecuteActivity(actCtx, recordToleratedFailureActivityName, input).Get(ctx, nil)
}
