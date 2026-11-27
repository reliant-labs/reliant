// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"errors"
	"fmt"

	"go.temporal.io/sdk/workflow"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// A sub-workflow's declared verdict, as its parent sees it (agent report
// 7011a947; owner decision 2026-11-27, "record + opt-in").
//
// A verdict belongs to the run, and only the run's own top-level nodes stamp
// it: a node of a `workflow:` node's body that declares `outcome: failure`
// ends that body's graph, runs the child's own on_failure (child_on_failure.go),
// and the parent's node completes. Until this, nothing else remembered it:
// every attempt of the run had succeeded, so the run was judged `succeeded`
// (run_outcome.go) and looked green in the run list and its trigger's health.
//
// Now, when a body's graph ends on a declared failure:
//
//   - by default the declaring node is recorded as a failed attempt (the
//     tolerated-failure record, tolerate_failure.go), so a run that completes
//     anyway is judged failure_handled — "completed, but a node failed and
//     its failure path ran" — naming that node. Control flow is unchanged.
//   - with `propagate_outcome: true` on the `workflow:` node, the node FAILS,
//     exactly as on an error nothing in the body handled: tolerate_failure,
//     the parent's on_failure and the run's verdict treat it as one.
//
// Loop bodies run through the loop executor, not this one, and are not
// covered; an on_failure or finally handler cannot change a verdict and is
// never judged here.

// childDeclaredFailureRecordChangeID gates recording a body's declared
// failure as its node's failed attempt: it adds an activity command, and is
// consulted only when a body ends on a declared failure, so a history
// recorded before it replays without one.
const childDeclaredFailureRecordChangeID = "child-declared-failure-record"

// ChildDeclaredFailureError is a `workflow:` node's error when its body
// declared outcome: failure and the node sets propagate_outcome.
type ChildDeclaredFailureError struct {
	// NodePath is the declaring node's run-wide path ("release.blocked").
	NodePath string
}

func (e *ChildDeclaredFailureError) Error() string {
	return fmt.Sprintf("node %s declared outcome: failure, and the workflow node propagates its outcome", e.NodePath)
}

// IsChildDeclaredFailure reports whether err is a propagated declared failure.
func IsChildDeclaredFailure(err error) bool {
	var d *ChildDeclaredFailureError
	return errors.As(err, &d)
}

// childDeclaredFailureText is the error the declaring node's recorded attempt
// carries.
const childDeclaredFailureText = "declared the sub-workflow's outcome: failure"

// noteOutcome records the verdict a node of the body declares, at entry (the
// root's rule: a skipped node stamps nothing; the last one wins), for this
// executor and for the child's failure scope.
func (e *InlineWorkflowExecutor) noteOutcome(node *reliantv1.Node) {
	if outcome := model.NodeOutcome(node); outcome != "" {
		e.outcome, e.outcomeNode = outcome, node.GetId()
	}
	e.childFailure.noteOutcome(node)
}

// propagatesOutcome reports whether the `workflow:` node sets
// propagate_outcome. The evaluated node is read when there is one, as every
// other field of the node is.
func (e *InlineWorkflowExecutor) propagatesOutcome() bool {
	source := e.evalResult
	if source == nil {
		source = e.node
	}
	return model.GetSubWorkflowArgs(source).GetPropagateOutcome()
}

// settleDeclaredOutcome is what a body that ended on a declared failure does
// to its node, once its own handlers have run: the error the node fails with
// (propagate_outcome), or nil after recording the declaring node's failure.
// nil, with nothing recorded, for a body that declared none, and for a
// handler's graph.
func (e *InlineWorkflowExecutor) settleDeclaredOutcome() error {
	if e.outcome != model.OutcomeFailure || e.outcomeNode == "" || e.pauseCtrl.failsFast() {
		return nil
	}
	path := joinNodePath(e.nodePath(), e.outcomeNode)
	if e.propagatesOutcome() {
		// Where the run failed, for a handler's failure.node_path: the
		// declaring node, not the `workflow:` node around it.
		recordFailedNode(e.ctx, path, 1)
		return &ChildDeclaredFailureError{NodePath: path}
	}
	if workflow.GetVersion(e.ctx, childDeclaredFailureRecordChangeID, workflow.DefaultVersion, 1) == workflow.DefaultVersion {
		return nil
	}
	e.logger.Info("[InlineWorkflow] The sub-workflow declared outcome: failure; recording it, the node completes",
		"nodeID", e.nodeID,
		"subWorkflow", e.subWorkflowName,
		"declaringNode", path,
	)
	recordNodeFailure(e.ctx, e.toleratedScope(e.outcomeNode), childDeclaredFailureText)
	return nil
}
