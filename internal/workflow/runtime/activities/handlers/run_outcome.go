// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/rundiagnostics"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// runStartSkew is how far before the run's start (workflow time) a node
// attempt (database time) may be stamped and still be the run's. The two
// clocks are different machines'.
const runStartSkew = 5 * time.Second

// runOutcome is how an ending run ended for whoever judges whether it worked
// (core.Workflow.RunOutcome, reliant#2753), and, for a handled failure, the
// node whose failure the run went on past.
type runOutcome struct {
	outcome string
	handled *db.HandledNodeFailure
}

// resolveRunOutcome judges input's run as it ends. Lifecycle status is not
// enough: a run whose own failure path ran — a parallel iteration that failed
// under on_failure: continue, a tolerated node, a finally handler's node — is
// COMPLETED, and read as a success to the run list and its trigger's health.
//
//   - failed, or completed into a declared outcome: failure -> failed
//   - completed, with a node whose last attempt since the run began failed ->
//     failure_handled
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
	}
	var since time.Time
	if input.RunStartedAtMs > 0 {
		since = time.UnixMilli(input.RunStartedAtMs).Add(-runStartSkew)
	}
	handled, err := a.repo.FirstHandledNodeFailure(ctx, input.WorkflowID, since)
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
func handledFailureDetail(h *db.HandledNodeFailure) string {
	if h == nil {
		return ""
	}
	return fmt.Sprintf("the run completed, but node %q failed and its failure path ran: %s", h.Node, rundiagnostics.ErrorText(h.Error))
}
