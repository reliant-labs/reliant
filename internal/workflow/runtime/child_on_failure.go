// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"errors"
	"strings"

	"go.temporal.io/sdk/workflow"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/core"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// A ref'd child's on_failure.
//
// A `workflow:` node runs its child inline, in the parent's run, so the
// root's handler (on_failure.go) is the only one a run used to have: a child
// started by ref never ran its own, whatever it was written to clean up. The
// release workflow's reconcile_tag (file release:incomplete for a tag pushed
// before the run died) did not run under the release-* wrappers that start it.
//
// Now a child loaded by ref that declares on_failure runs it, once, when the
// child fails: an error nothing in the child handled, or a node of the child
// that declares outcome: failure. It runs inside the child's scope — the
// child's inputs, the child's node outputs, `failure` relative to the child —
// and BEFORE the error unwinds to the parent, so a parent's own handler runs
// after it. Like the root's, it cannot change the verdict: the child's error
// still propagates, and an error in the handler is logged and dropped.
//
// Inline bodies (`inline:`) carry no handler of their own: their failure is
// their parent's. A loop's `ref:` body is not covered here (it runs through
// the loop executor, not this one).

// childOnFailureChangeID gates running a ref'd child's handler: it adds
// commands to histories that, recorded before it, failed straight through.
const childOnFailureChangeID = "ref-child-on-failure"

// childFailureScope is what a ref'd child with a handler records while it
// runs: where it failed (its own recorder, chained to the run's) and the last
// verdict one of its nodes declared.
type childFailureScope struct {
	handler     *reliantv1.Workflow
	recorder    *failureRecorder
	outcome     string
	outcomeNode string
}

// beginChildFailureScope arms the scope for a child loaded by ref that
// declares on_failure, and attaches its recorder to the executor's context.
// nil (nothing to do) for an inline body, a handler, or a child with none.
func (e *InlineWorkflowExecutor) beginChildFailureScope() *childFailureScope {
	if e.subWorkflow == nil || e.loadStrategy() == core.LoadStrategyInlineEmbedded || e.pauseCtrl.failsFast() {
		return nil
	}
	handler := e.subWorkflow.GetOnFailure()
	if handler == nil || len(handler.GetNodes()) == 0 {
		return nil
	}
	scope := &childFailureScope{handler: handler, recorder: &failureRecorder{parent: failureRecorderFrom(e.ctx)}}
	e.ctx = withFailureRecorder(e.ctx, scope.recorder)
	e.childFailure = scope
	return scope
}

// noteOutcome records the verdict a node of the child declares, at entry
// (the same rule the root applies: a skipped node stamps nothing).
func (s *childFailureScope) noteOutcome(node *reliantv1.Node) {
	if s == nil {
		return
	}
	if outcome := model.NodeOutcome(node); outcome != "" {
		s.outcome, s.outcomeNode = outcome, node.GetId()
	}
}

// finishChildFailureScope runs the child's handler if the child failed. err
// is what the child's graph returned.
func (e *InlineWorkflowExecutor) finishChildFailureScope(scope *childFailureScope, err error) {
	if scope == nil || errors.Is(err, ErrThreadCancelled) || !runFailed(e.ctx, err, scope.outcome) {
		return
	}
	// Decided only on a failure, so a run that never fails records nothing.
	if workflow.GetVersion(e.ctx, childOnFailureChangeID, workflow.DefaultVersion, 1) == workflow.DefaultVersion {
		return
	}
	failure := failureFor(scope.recorder, err, scope.outcome, scope.outcomeNode)
	// The recorder holds run-wide paths ("release.count"); the child's
	// handler reads paths in the child ("count").
	failure.NodePath = strings.TrimPrefix(failure.NodePath, e.nodePath()+".")
	var record OnFailureRecord
	runOnFailureAt(e.ctx, scope.handler,
		WorkflowInput{ChatID: e.chatID, WorkflowName: e.subWorkflowName, Inputs: e.bodyInputs},
		e.workflowID, e.execContext, e.childTracker, e.bodyNodeOutputs, failure, &record, e.nodePath())
	e.logger.Info("[InlineWorkflow] Ran the ref'd child's on_failure handler",
		"nodeID", e.nodeID,
		"subWorkflow", e.subWorkflowName,
		"failedNode", failure.NodePath,
		"errorCode", failure.ErrorCode,
		"handlerError", record.HandlerError,
		"timedOut", record.TimedOut,
	)
}
