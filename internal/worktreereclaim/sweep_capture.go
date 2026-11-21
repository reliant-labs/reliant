// Copyright (c) 2026 Reliant Labs
package worktreereclaim

import (
	"context"
	"errors"
)

// A capture-only pass (SweepOptions.CaptureOnly, `reliant worktree reclaim
// scan --capture`) takes, for each worktree it would reclaim, the steps of
// reclaimEntry that come before anything moves: the checkout is evaluated
// again from scratch under its locks, captured in place, and the capture is
// checked against the files exactly as reclaimEntry checks the parked copy
// (captureIntact, verifyCapture). Nothing is locked, moved or deleted, so a
// person can try `reliant worktree restore` on a real capture.
//
// Only auto-safe worktrees are captured: what a pass in mode on would
// reclaim without asking. Their capture holds nothing HEAD does not.
//
// The capture is keyed by the checkout's path (CaptureKey), never by a
// Reliant row id, and marked CaptureOnly. worktree.ensure applies a row's
// newest capture when it recreates a missing workspace (LatestCapture); a
// capture of a checkout that was never removed must not be that, on this
// daemon or an older one that does not know the mark.

// captureOnlyEntry captures and verifies one auto-safe entry. It leaves the
// entry would_reclaim with its CaptureRef on success, and holds it, for the
// reason a reclaim would have, otherwise. A capture that fails its check is
// deleted again: nothing else ever pointed at it.
func (r *Reclaimer) captureOnlyEntry(ctx context.Context, e *SweepEntry, opts SweepOptions, dir string) {
	m := r.pathLock(e.Path)
	if m.lock(ctx) != nil {
		e.Detail = "not captured: " + DetailTimedOut + "; " + e.Detail
		return
	}
	defer m.unlock()
	lctx, cancel := context.WithTimeout(ctx, recoverLockWait)
	unlock, err := r.keyLock(lctx, dir, e)
	cancel()
	if err != nil {
		e.Detail = "not captured: a reclaim of it is running (its lock is held); " + e.Detail
		return
	}
	defer unlock()

	// Again, from scratch, under the locks.
	fresh := SweepEntry{Key: e.Key, Path: e.Path, CommonDir: e.CommonDir, Repo: e.Repo, Branch: e.Branch, Head: e.Head, Base: e.Base, Owner: e.Owner, RowID: e.RowID}
	if info, err := inspectLock(ctx, e.Path); err == nil && info.Locked {
		fresh.Lock = info.Reason
	} else if err != nil {
		e.hold(ReasonUnverified, err.Error())
		return
	}
	r.evaluate(ctx, &fresh, opts)
	if fresh.Class != ClassAutoSafe {
		*e = fresh
		switch {
		case fresh.Class == ClassNeedsApproval:
			e.Outcome = OutcomeAwaiting
		case e.Outcome == "":
			e.Outcome = OutcomeSweepHeld
		}
		return
	}
	*e = fresh
	e.Outcome = OutcomeWouldReclaim

	meta := CaptureMeta{Path: e.Path, Branch: e.Branch, RowID: e.RowID, Class: string(ClassAutoSafe), IdleSeconds: e.IdleSeconds,
		Version: opts.Version, CapturedAt: r.now().UTC(), CaptureOnly: true}
	capt, err := captureCheckout(ctx, e.Path, CaptureKey(e.Path), meta, opts.Capture)
	if err != nil {
		if errors.Is(err, errCaptureEnvelope) {
			e.hold(ReasonTooLarge, err.Error())
		} else {
			e.hold(ReasonUnverified, "capture failed: "+err.Error())
		}
		return
	}
	if capt.DiskTree != headTree(ctx, e.Path) || capt.IndexTree != capt.DiskTree || len(capt.Orphans) != 0 {
		dropCapture(ctx, capt)
		e.hold(ReasonDirty, "it changed before it could be captured; it needs approval now")
		return
	}
	if err := captureIntact(ctx, capt); err != nil {
		dropCapture(ctx, capt)
		e.hold(ReasonCaptureMissing, err.Error())
		return
	}
	if err := verifyCapture(ctx, e.Path, capt); err != nil {
		dropCapture(ctx, capt)
		e.hold(ReasonRecentlyActive, "changed while it was captured: "+err.Error())
		return
	}
	e.CaptureRef = capt.Ref
	e.Detail = "captured at " + capt.Ref + " and verified; nothing was removed. Try a restore elsewhere: reliant worktree restore " +
		capt.Ref + " --repo " + capt.CommonDir + " --to <new dir>"
}

// dropCapture deletes a capture this process just made, if the ref still
// names the commit it made.
func dropCapture(ctx context.Context, c Capture) {
	_, _ = git(context.WithoutCancel(ctx), "", nil, "--git-dir="+c.CommonDir, "update-ref", "-d", c.Ref, c.Commit)
}
