// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/rundiagnostics"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// runStartSkew is how far before the run's start (workflow time) a node
// attempt (database time) may be stamped and still be the run's. The two
// clocks are different machines'. It applies only to a run with no id (a
// workflow that predates it): a run with an id reads only the attempts
// recorded under it, which a previous run that ended within the skew does not
// share.
const runStartSkew = 5 * time.Second

// runOutcome is how an ending run ended for whoever judges whether it worked
// (core.Workflow.RunOutcome, reliant#2753), and, for a handled failure, the
// node whose failure the run went on past.
type runOutcome struct {
	outcome string
	handled *store.HandledNodeFailure
}

// resolveRunOutcome judges input's run as it ends. Lifecycle status is not
// enough: a run whose own failure path ran — a parallel iteration that failed
// under on_failure: continue, a tolerated node, a finally handler's node — is
// COMPLETED, and read as a success to the run list and its trigger's health.
//
//   - failed, or completed into a declared outcome: failure -> failed
//   - completed, with a node whose last attempt in this run failed ->
//     failure_handled. "In this run" is the attempts recorded under its run
//     id (input.RunID), so the previous run on the same reused workflow id is
//     never read, nor an attempt recorded with no run id. Only a run with
//     no id (a workflow that predates it) falls back to the attempts
//     recorded since it began.
//   - completed into a declared outcome: skipped -> skipped
//   - completed otherwise -> succeeded
//
// Anything else (started, paused, cancelled) has no outcome. A failed read of
// the node attempts is logged and judged succeeded, as the run was before the
// outcome existed: the status write it rides with must not fail over it.
func (a *WorkflowStatusActivity) resolveRunOutcome(ctx context.Context, input WorkflowStatusInput) runOutcome {
	switch {
	case input.Status == "failed",
		input.Status == "completed" && input.Outcome == model.OutcomeFailure:
		return runOutcome{outcome: core.RunOutcomeFailed}
	case input.Status != "completed":
		return runOutcome{}
	case input.Outcome == model.OutcomeSkipped:
		// Declared, so it wins over a node failure the run went on past, as
		// a declared failure does: the workflow said it did not do its work.
		return runOutcome{outcome: core.RunOutcomeSkipped}
	}
	var since time.Time
	if input.RunStartedAtMs > 0 {
		since = time.UnixMilli(input.RunStartedAtMs).Add(-runStartSkew)
	}
	handled, err := a.runs.FirstHandledNodeFailure(ctx, input.WorkflowID, input.RunID, since)
	if err != nil {
		logging.WarnContext(ctx, "[WorkflowStatus] Could not read the run's node attempts; judging it succeeded",
			"workflow_id", input.WorkflowID, "error", err)
		return runOutcome{outcome: core.RunOutcomeSucceeded}
	}
	if handled != nil {
		return runOutcome{outcome: core.RunOutcomeFailureHandled, handled: handled}
	}
	return runOutcome{outcome: core.RunOutcomeSucceeded}
}

// handledFailureDetail describes a handled failure for whoever reads the
// trigger's health: the run completed, and which node failed on the way.
func handledFailureDetail(h *store.HandledNodeFailure) string {
	if h == nil {
		return ""
	}
	return fmt.Sprintf("the run completed, but node %q failed and its failure path ran: %s", h.Node, rundiagnostics.ErrorText(h.Error))
}
