// Copyright (c) 2026 Reliant Labs
package worktreereclaim

import (
	"context"
	"os"
	"path/filepath"
	"slices"
)

// SettleParkedFor settles what an interrupted machine-sweep reclaim of dest
// left parked beside it, in <dir of dest>/.reliant-reclaim/, so dest can be
// put back: a parked copy goes home (back is true), and an interrupted
// deletion is finished once its remains are proven part of its capture
// (finishDisposal), after which the caller recreates dest from its branch and
// applies that capture. While a parked copy stays registered, `git worktree
// add` refuses dest's branch ("already used by worktree at …/.reliant-reclaim/
// …"), so a workspace that was being reclaimed when its chat came back could
// not be recreated until a later sweep pass got to it (obs 6d590acc). held is
// why a parked copy had to stay, "" when none did.
//
// It is exactly the recovery a sweep pass runs first (recoverReclaim, under
// the same key lock and admission lease), for the checkouts of one path. A
// Reclaimer without Acquire recovers nothing and reports the hold. stateDir
// is the sweep's (SweepOptions.StateDir; "" is DefaultStateDir), whose key
// locks a reclaim of a checkout outside Reliant's workspaces holds.
func (r *Reclaimer) SettleParkedFor(ctx context.Context, repo, dest, stateDir string) (back bool, held string) {
	markers, _ := filepath.Glob(filepath.Join(filepath.Dir(dest), reclaimQuarantineDir, "*", "*.origin"))
	var parked []string
	for _, mk := range markers {
		q := mk[:len(mk)-len(".origin")]
		if orig, _, _, ok := readReclaimMarkers(q); ok && samePath(orig, dest) {
			if fi, err := os.Lstat(q); err == nil && fi.IsDir() {
				parked = append(parked, resolvePath(q))
			}
		}
	}
	if len(parked) == 0 {
		return false, ""
	}
	common := repoCommonDir(ctx, repo)
	if common == "" {
		return false, "a reclaim of " + dest + " was interrupted, and its repository " + repo + " could not be read"
	}
	dir, err := stateDirOr(stateDir)
	if err != nil {
		return false, "a reclaim of " + dest + " was interrupted: " + err.Error()
	}
	found, anchors := discoverWorktrees(ctx, []string{common})
	// Discovery only returns anchors that Git successfully enumerated. An
	// incomplete listing cannot establish that the parked registration is gone.
	if !slices.Contains(anchors, common) {
		return false, "a reclaim of " + dest + " was interrupted, and its repository " + repo + " could not enumerate worktrees"
	}
	for _, d := range found {
		if !slices.Contains(parked, d.Path) {
			continue
		}
		e := newEntry(d)
		if !needsRecovery(e) {
			continue
		}
		r.recoverReclaim(ctx, &e, dir, true)
		switch {
		case e.Outcome == OutcomeMovedBack && samePath(e.Path, dest):
			back = true
		case e.Outcome == OutcomeReclaimed:
			// The remains are gone and their registration with them; dest's
			// capture is applied when it is recreated.
		default:
			held = e.Detail
		}
	}
	return back, held
}
