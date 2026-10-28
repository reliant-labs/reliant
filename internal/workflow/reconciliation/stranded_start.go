// Copyright (c) 2025 Reliant Labs
package reconciliation

import (
	"context"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
)

// strandedStartBound is how long a committed launch's root may stay PENDING
// with no run in Temporal and no failed start recorded before this pass
// records the failure itself.
//
// It must outlast any start still in flight, or a slow start is recorded as
// failed and a trigger's next fire abandons it and runs beside it. A start
// attempt is bounded by core.StartAttemptDeadline (the launcher's deadline on
// the ExecuteWorkflow call), well inside it. It is core.StartAttemptBound, the
// same bound workflow-capacity recovery requires of a start attempt's stamp.
//
// The cost of the bound is the other side of the trade-off: a launch whose
// starter died between the commit and the start reads as in flight until the
// bound passes and the next pass finds it, so a trigger that skips overlaps
// skips its fires for up to strandedStartBound plus one poll interval. That is
// chosen over a shorter bound because the alternative failure, two runs of one
// trigger at once, is the one overlap=skip exists to prevent.
const strandedStartBound = core.StartAttemptBound

// strandedStartBatch caps one pass's sweep. Stranded starts are rare (each is
// a launcher that died or lost a write), so a full batch is itself a signal.
const strandedStartBatch = 100

// markStrandedStarts finds the PENDING roots whose launch committed more than
// strandedStartBound ago and that no failed start has been recorded for, and
// settles each against Temporal:
//
//   - Temporal has the run: the launch's mark-active was lost, so the row
//     moves to ACTIVE (and the drift scan that follows in this pass compares
//     it with the run like any other active row).
//   - Temporal has no run: the start never happened, so the failure is
//     recorded (Workflow.StartFailedAt). The row stays PENDING, so a retry of
//     the launch can still start it, but a trigger's next fire no longer
//     waits behind it.
//   - Temporal cannot be asked: left for the next pass.
func (r *Reconciler) markStrandedStarts(ctx context.Context, stats *passStats) (int, error) {
	cutoff := time.Now().Add(-strandedStartBound)
	stranded, err := r.repo.ListStrandedWorkflowStarts(ctx, cutoff, strandedStartBatch)
	if err != nil {
		logging.ErrorContext(ctx, "[Reconciler] Failed to list stranded workflow starts", "error", err)
		return 0, fmt.Errorf("failed to list stranded workflow starts: %w", err)
	}

	settled := 0
	for _, wf := range stranded {
		state, err := r.getTemporalWorkflowState(ctx, wf.ID)
		if err != nil {
			logging.WarnContext(ctx, "[Reconciler] Could not ask Temporal about a stranded start; retrying next pass",
				"workflowID", wf.ID, "chatID", wf.ChatID, "error", err)
			continue
		}

		if state.Exists {
			swapped, err := r.repo.CompareAndSwapWorkflowStatus(ctx, wf.ID, db.Active(), db.Pending())
			if err != nil {
				logging.WarnContext(ctx, "[Reconciler] Failed to mark a started run active", "workflowID", wf.ID, "error", err)
				continue
			}
			if swapped {
				settled++
				r.recordAnomaly(stats, anomalyStrandedStartRepaired)
				logging.ErrorContext(ctx, "[Reconciler] PENDING root has a run in Temporal; its launch's mark-active was lost. Marked active",
					"workflowID", wf.ID, "chatID", wf.ChatID, "anomaly", anomalyStrandedStartRepaired)
			}
			continue
		}

		marked, err := r.repo.MarkWorkflowStartFailed(ctx, wf.ID)
		if err != nil {
			logging.WarnContext(ctx, "[Reconciler] Failed to record a stranded start", "workflowID", wf.ID, "error", err)
			continue
		}
		if marked {
			settled++
			r.recordAnomaly(stats, anomalyStrandedStartFailed)
			logging.ErrorContext(ctx, "[Reconciler] Launch committed but its run never reached Temporal; recorded the start as failed",
				"workflowID", wf.ID, "chatID", wf.ChatID, "pendingSince", wf.CreatedAt,
				"bound", strandedStartBound, "anomaly", anomalyStrandedStartFailed)
		}
	}
	return settled, nil
}
