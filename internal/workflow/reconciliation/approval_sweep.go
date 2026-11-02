// Copyright (c) 2025 Reliant Labs
package reconciliation

import (
	"context"
	"fmt"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/logging"
)

// staleApprovalBatch bounds one pass's approval repairs; the next pass takes
// the rest.
const staleApprovalBatch = 200

// resolveStaleApprovals closes pending approvals nothing waits on any more:
// the run that asked finished or was cancelled, or the chat was archived or
// deleted (db.ListStaleApprovals). The run-end Cleanup closes a run's own
// approvals, but not every end runs it — a hard terminate, a reaped subtree,
// an archive — and an approval left pending keeps an Approve button on
// screen that can only fail. Each is closed as CANCELLED with the reason, and
// the close is announced on its chat's feed (db.ResolvePendingApproval). A
// failed or paused run is resumable and keeps its approval.
func (r *Reconciler) resolveStaleApprovals(ctx context.Context, stats *passStats) (int, error) {
	stale, err := r.repo.ListStaleApprovals(ctx, staleApprovalBatch)
	if err != nil {
		return 0, fmt.Errorf("failed to list stale approvals: %w", err)
	}
	closed := 0
	for _, a := range stale {
		ok, err := r.repo.ResolvePendingApproval(ctx, a.ID,
			int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_CANCELLED),
			"This request can no longer be answered: "+a.Why+". Nothing was approved.", "run_ended")
		if err != nil {
			logging.WarnContext(r.scope(ctx, a.ChatID, "", ""), "[Reconciler] Failed to close a stale approval",
				"approvalID", a.ID, "error", err)
			continue
		}
		if ok {
			closed++
			r.recordAnomaly(stats, anomalyStaleApprovalClosed)
		}
	}
	if closed > 0 {
		logging.InfoContext(ctx, "[Reconciler] Closed approvals nothing waits on any more", "count", closed)
	}
	return closed, nil
}

// announceApprovalResolutions repairs chat feeds that still announce a closed
// approval as pending (db.ListUnannouncedApprovalResolutions): before
// ApprovalResolve announced what it closed, every approval that timed out
// was one, and clients rebuild a chat's approvals from its feed. It runs to
// completion once per process; every close announces itself now, so a later
// pass would find nothing.
func (r *Reconciler) announceApprovalResolutions(ctx context.Context, stats *passStats) error {
	if r.approvalFeedRepaired.Load() {
		return nil
	}
	for {
		rows, err := r.repo.ListUnannouncedApprovalResolutions(ctx, staleApprovalBatch)
		if err != nil {
			return fmt.Errorf("failed to list unannounced approval resolutions: %w", err)
		}
		for _, a := range rows {
			if err := r.repo.AnnounceApprovalResolution(ctx, a); err != nil {
				// Retried on the next pass: the flag stays unset.
				return fmt.Errorf("failed to announce approval %s: %w", a.ID, err)
			}
			r.recordAnomaly(stats, anomalyApprovalResolutionAnnounced)
		}
		if len(rows) < staleApprovalBatch {
			r.approvalFeedRepaired.Store(true)
			return nil
		}
	}
}
