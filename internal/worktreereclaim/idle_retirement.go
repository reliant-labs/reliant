package worktreereclaim

import (
	"context"
	"fmt"
	"time"
)

// recoverableIdle is read-only whole-worktree eligibility, NOT live shedding.
// Only clean pushed/merged work can be recreated from its branch. Complete
// no-follow/mount-safe inspection includes fresh cache/tool/output writes.
//
// When it is not eligible, why names the first check that refused it, for
// Result.NotReclaimable: without it a server only ever saw "locked", and a
// finished workspace held by one stray file looked like one still in use.
func (r *Reclaimer) recoverableIdle(ctx context.Context, w Worktree, floor time.Duration) (ok bool, why string) {
	if floor <= 0 || r.ProcessUse == nil {
		return false, "process inspection unavailable"
	}
	budget := r.ProbeBudget
	if budget <= 0 {
		budget = DefaultProbeBudget
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	checkouts, _, hold := r.structure(ctx, w, false)
	if hold != nil {
		return false, string(hold.Reason) + ": " + hold.Detail
	}
	if len(checkouts) == 0 {
		return false, "no checkout found"
	}
	if v := classifyAll(ctx, w, checkouts, r.probe()); !v.safe() {
		return false, string(v.Reason) + ": " + v.Detail
	}
	last, err := idleSince(ctx, w.Path, checkouts)
	if err != nil {
		return false, "quiet time unknown: " + err.Error()
	}
	if age := r.now().Sub(last); age < floor {
		return false, fmt.Sprintf("touched %s ago", age.Round(time.Minute))
	}
	for _, c := range checkouts {
		tree, err := inspectRetirementTree(ctx, c)
		if err != nil {
			return false, relDetail(w.Path, c, "not retirable whole: "+err.Error())
		}
		age := r.now().Sub(tree.newest)
		tree.close()
		if age < floor {
			return false, relDetail(w.Path, c, fmt.Sprintf("a file was written %s ago", age.Round(time.Minute)))
		}
	}
	if h := r.inUse(ctx, w, true); h != nil {
		return false, string(h.Reason) + ": " + h.Detail
	}
	if err := r.ProcessUse(ctx, []string{w.Path}); err != nil {
		return false, "in use: " + err.Error()
	}
	if err := ctx.Err(); err != nil {
		return false, "probe ran out of time"
	}
	return true, ""
}
