// Copyright (c) 2026 Reliant Labs
package daemonruntime

import (
	"context"
	"time"

	"github.com/reliant-labs/reliant/internal/diskwatch"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/version"
	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

// worktreeSweep is the disk watcher's hook for the machine-wide worktree
// reclaim (worktreereclaim.SweepMachine). It runs in this process, through
// the same Reclaimer as the server's reconciles, so it holds the daemon's
// admission lease (AcquireWorktreeRecovery) and the rows' locks, which the
// forge subprocess it replaces could not.
func worktreeSweep(cfg worktreereclaim.SweepConfig) diskwatch.WorktreeSweep {
	return diskwatch.WorktreeSweep{
		Mode: string(cfg.Mode),
		Run: func(ctx context.Context, trigger string, pressure, approvedOnly bool) {
			r, err := sharedWorktreeReclaimer()
			if err != nil {
				logging.WarnContext(ctx, "[worktreereclaim] Sweep skipped: worktree root unavailable", "error", err)
				return
			}
			rep := r.SweepMachine(ctx, worktreereclaim.SweepOptions{
				Mode: cfg.Mode, Roots: cfg.Roots, CleanIdle: cfg.CleanIdle, ApprovalIdle: cfg.ApprovalIdle,
				Trigger: trigger, Pressure: pressure, ApprovedOnly: approvedOnly, Version: version.Get().Version,
			})
			logSweep(ctx, rep)
		},
		// No ApprovalsSince: an approval in approvals.json is not verified as
		// a person's (any process running as the user can write it), so the
		// sweep never acts on one (SweepOptions.ApprovalsVerified stays
		// false) and an approved-only pass would do nothing.
	}
}

// logSweep logs one line per worktree it reclaimed, would reclaim, or
// proposes for approval, and a summary. Holds are in the journal
// (~/.reliant/reclaim/journal.jsonl), not the log: there are hundreds a pass.
func logSweep(ctx context.Context, rep worktreereclaim.SweepReport) {
	for _, e := range rep.Entries {
		attrs := []any{"mode", rep.Mode, "path", e.Path, "owner", e.Owner, "class", e.Class, "bytes", e.Bytes}
		switch e.Outcome {
		case worktreereclaim.OutcomeReclaimed:
			logging.InfoContext(ctx, "[worktreereclaim] reclaimed", append(attrs, "freed_bytes", e.FreedBytes, "capture_ref", e.CaptureRef)...)
		case worktreereclaim.OutcomeWouldReclaim:
			logging.InfoContext(ctx, "[worktreereclaim] would reclaim", attrs...)
		case worktreereclaim.OutcomeAwaiting:
			logging.InfoContext(ctx, "[worktreereclaim] needs approval", append(attrs, "reason", e.Reason)...)
		case worktreereclaim.OutcomeSweepError:
			logging.WarnContext(ctx, "[worktreereclaim] error", append(attrs, "detail", e.Detail)...)
		}
	}
	t := rep.Totals
	logging.InfoContext(ctx, "[worktreereclaim] Sweep finished", "mode", rep.Mode, "trigger", rep.Trigger, "approved_only", rep.ApprovedOnly,
		"seen", t.Seen, "auto_safe", t.AutoSafe, "auto_safe_bytes", t.AutoSafeBytes, "needs_approval", t.NeedsApproval,
		"needs_approval_bytes", t.NeedsApprovalBytes, "held", t.Held, "deferred", t.Deferred, "reclaimed", t.Reclaimed,
		"reclaimed_bytes", t.ReclaimedBytes, "clones", len(rep.Clones), "duration", rep.FinishedAt.Sub(rep.StartedAt).Round(time.Second))
}
