// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"errors"

	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/temporal"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
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
