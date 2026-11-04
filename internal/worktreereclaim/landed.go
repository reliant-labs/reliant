// Copyright (c) 2025 Reliant Labs
package worktreereclaim

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
)

// Reclaim on merge. Most worktrees are made for one pull request; once it
// lands, the checkout is a copy of the base branch plus a node_modules, a
// tool install and caches nobody will use again, held for as long as its chat
// is not archived. ProbeLanded reports whether the complete checkout can be
// retired after row-locked archival. Live probing never deletes files; whole
// retirement is the server's existing fenced removal path.

// ErrProcessUseUnsupported is CheckProcessUse on a platform it cannot inspect
// completely. It is uncertainty, never proof that destructive cleanup is safe.
var ErrProcessUseUnsupported = errors.New("process-use inspection unsupported on this platform")

// landedAnswer is what the landed probe concluded.
type landedAnswer int

const (
	// landedNo: a check ran and said some work has not landed.
	landedNo landedAnswer = iota
	landedYes
	// landedInconclusive: a check could not run to completion (out of time,
	// a git failure), so nothing is known.
	landedInconclusive
)

// probeLanded reports complete safe retirement eligibility without deleting
// anything. Protected, mounted, changed or uncertain layouts remain live.
func (r *Reclaimer) probeLanded(ctx context.Context, w Worktree, res *Result) {
	budget := r.ProbeBudget
	if budget <= 0 {
		budget = DefaultProbeBudget
	}
	pctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	// Cheap git questions first: most live worktrees have not landed, and
	// for those the workspace scan below is never paid for.
	switch r.allLanded(pctx, w, shallowCheckouts(w.Path, w.Checkouts)) {
	case landedNo:
		return
	case landedInconclusive:
		res.Outcome = OutcomeProbeInconclusive
		return
	}
	_, _, hold := r.structure(pctx, w, false)
	switch {
	case hold == nil:
		// The old data classifier may accept an ignored ancestor wholesale.
		// Whole retirement requires complete mount-safe descendant inspection
		// before even offering the row for automatic archival.
		for _, checkout := range shallowCheckouts(w.Path, w.Checkouts) {
			tree, err := inspectRetirementTree(pctx, checkout)
			if err != nil {
				res.Detail = "worktree retained: " + err.Error()
				return
			}
			tree.close()
		}
		res.Landed = true
	case hold.Reason == ReasonUnverified:
		res.Outcome = OutcomeProbeInconclusive
		return
	case !shedsFor(hold.Reason):
		return // not provably this row's to touch
	default:
		res.Detail = "landed, but kept on disk: " + hold.Detail
	}
	// Landed cleanup retires the whole checkout after the server archives it.
	// Never recursively shed a subset out of a still-live worktree.
}

// allLanded reports whether every checkout's work has landed. There must be
// at least one; a described checkout that is missing from disk has nothing in
// it to lose and is not asked about.
func (r *Reclaimer) allLanded(ctx context.Context, w Worktree, checkouts []string) landedAnswer {
	if len(checkouts) == 0 {
		return landedNo
	}
	probe := r.probe()
	answers := make([]landedAnswer, len(checkouts))
	var wg sync.WaitGroup
	for i, c := range checkouts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			answers[i] = landedCheckout(ctx, c, baseFor(w, c), nestedOf(c, checkouts), w.ID, probe)
		}()
	}
	wg.Wait()
	out := landedYes
	for _, a := range answers {
		switch a {
		case landedNo:
			return landedNo // a definite no outranks an unknown
		case landedInconclusive:
			out = landedInconclusive
		}
	}
	if out == landedYes && ctx.Err() != nil {
		return landedInconclusive
	}
	return out
}

// landedCheckout decides whether one checkout's work has landed. It must be
// safe to remove by the automatic path's own rules (classifyCheckout: clean,
// nothing hidden, no history on no branch), and its HEAD must be in its base
// branch as the REMOTE has it: pushed alone is not landed, and neither is a
// merge into a local base branch nobody pushed. A HEAD with no commits beyond
// its base is an ancestor of it, so a checkout nobody committed in counts.
func landedCheckout(ctx context.Context, checkout, baseBranch string, nested []string, worktreeID string, probe mergeProbe) landedAnswer {
	v := classifyCheckout(ctx, checkout, baseBranch, nested, worktreeID, probe)
	switch {
	case v.Reason == ReasonUnverified:
		return landedInconclusive
	case !v.safe():
		return landedNo
	}
	head, err := git(ctx, checkout, nil, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return landedInconclusive
	}
	names := landingBases(ctx, checkout, baseBranch)
	if len(names) == 0 {
		return landedNo // no remote base to have landed in
	}
	landed := func() (bool, error) {
		return inCommits(ctx, checkout, head, remoteBaseCommits(ctx, checkout, names), probe.memo)
	}
	merged, err := landed()
	if err != nil {
		return landedInconclusive
	}
	if merged {
		return landedYes
	}
	if probe.prMerged != nil {
		if branch, err := git(ctx, checkout, nil, "symbolic-ref", "--short", "-q", "HEAD"); err == nil && branch != "" && probe.prMerged(ctx, checkout, branch, head) {
			return landedYes
		}
	}
	if probe.fetchBase != nil {
		for _, name := range names {
			probe.fetchBase(ctx, checkout, name)
		}
		if merged, err = landed(); err != nil {
			return landedInconclusive
		}
		if merged {
			return landedYes
		}
	}
	return landedNo
}

// landingBases names the branches a checkout's work may land in: its own
// base branch, and the remote's default branch (origin/HEAD), which is where
// a pull request from a branch based on an older or deleted feature branch
// usually ends up.
func landingBases(ctx context.Context, checkout, baseBranch string) []string {
	var names []string
	if baseBranch != "" {
		names = append(names, baseBranch)
	}
	if def, err := git(ctx, checkout, nil, "symbolic-ref", "-q", "refs/remotes/origin/HEAD"); err == nil {
		if name := strings.TrimPrefix(def, "refs/remotes/origin/"); name != def && name != "" && name != baseBranch {
			names = append(names, name)
		}
	}
	return names
}

// remoteBaseCommits resolves refs/remotes/origin/<name> for each name to the
// commit it names now; a ref that does not exist is left out.
func remoteBaseCommits(ctx context.Context, checkout string, names []string) []string {
	var shas []string
	for _, name := range names {
		sha, err := git(ctx, checkout, nil, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+name+"^{commit}")
		if err == nil && sha != "" && !slices.Contains(shas, sha) {
			shas = append(shas, sha)
		}
	}
	return shas
}

// selfWithin reports whether this process runs from inside path: its
// executable or its working directory is there. The daemon never touches the
// worktree it is itself executing in, whatever the process list says.
func selfWithin(path string) bool {
	for _, get := range []func() (string, error){os.Executable, os.Getwd} {
		if p, err := get(); err == nil && p != "" && (samePath(p, path) || within(path, p)) {
			return true
		}
	}
	return false
}
