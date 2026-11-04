package worktreereclaim

import (
	"context"
	"time"
)

// recoverableIdle is read-only whole-worktree eligibility, NOT live shedding.
// Only clean pushed/merged work can be recreated from its branch. Complete
// no-follow/mount-safe inspection includes fresh cache/tool/output writes.
func (r *Reclaimer) recoverableIdle(ctx context.Context, w Worktree, floor time.Duration) bool {
	if floor <= 0 || r.ProcessUse == nil {
		return false
	}
	budget := r.ProbeBudget
	if budget <= 0 {
		budget = DefaultProbeBudget
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	checkouts, _, hold := r.structure(ctx, w, false)
	if hold != nil || len(checkouts) == 0 {
		return false
	}
	if v := classifyAll(ctx, w, checkouts, r.probe()); !v.safe() {
		return false
	}
	if last, err := idleSince(ctx, w.Path, checkouts); err != nil || r.now().Sub(last) < floor {
		return false
	}
	for _, c := range checkouts {
		tree, err := inspectRetirementTree(ctx, c)
		if err != nil {
			return false
		}
		fresh := r.now().Sub(tree.newest) < floor
		tree.close()
		if fresh {
			return false
		}
	}
	if h := r.inUse(ctx, w, true); h != nil {
		return false
	}
	return r.ProcessUse(ctx, []string{w.Path}) == nil && ctx.Err() == nil
}
