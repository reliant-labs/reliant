// Copyright (c) 2025 Reliant Labs
package worktreereclaim

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

// Reclaim on merge. Most worktrees are made for one pull request; once it
// lands, the checkout is a copy of the base branch plus a node_modules, a
// tool install and caches nobody will use again, held for as long as its chat
// is not archived, which for an agent's chat is forever. ProbeLanded asks the
// daemon whether that has happened, and ShedLanded lets it delete the
// regenerable bulk while the worktree is still live. Archiving and removing
// the checkout stay the server's call and the existing fenced path's job.

// DefaultLandedShedFloor is how long a landed active worktree must have been
// quiet (idleSince) before its regenerable output is shed.
const DefaultLandedShedFloor = 30 * time.Minute

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

// probeLanded answers ProbeLanded for an active worktree that was just
// locked, setting res.Landed, and sheds its regenerable output when allowed
// (ShedLanded). A landed worktree whose structure keeps it on disk anyway
// (ignored data, files outside its checkouts, another repository inside it)
// is not reported landed, so the server leaves it live, but it is still shed:
// those holds are about files shedding never touches. Anything the probe
// could not settle makes the outcome OutcomeProbeInconclusive.
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
	_, sc, hold := r.structure(pctx, w, false)
	switch {
	case hold == nil:
		res.Landed = true
	case hold.Reason == ReasonUnverified:
		res.Outcome = OutcomeProbeInconclusive
		return
	case !shedsFor(hold.Reason):
		return // not provably this row's to touch
	default:
		res.Detail = "landed, but kept on disk: " + hold.Detail
	}
	if w.ShedLanded {
		r.shedLanded(ctx, w, sc, res)
	}
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
		switch {
		case a == landedNo:
			return landedNo // a definite no outranks an unknown
		case a == landedInconclusive:
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

// shedLanded deletes the regenerable output of a landed ACTIVE worktree. It
// is shed's live counterpart and stricter than it: the server has seen no
// chat run in the worktree, the worktree has been quiet for the shed floor,
// exclusive runtime admission excludes new tools, and a complete fresh
// process inspection proves no cwd, executable, open file or mapped file
// uses it. Unsupported inspection holds it. Only paths in checkouts this row's
// lock proves are its own are touched (strip).
func (r *Reclaimer) shedLanded(ctx context.Context, w Worktree, sc scan, res *Result) {
	if w.KeepFiles || sc.Truncated {
		return
	}
	if dl, ok := ctx.Deadline(); ok && time.Until(dl) < shedMinBudget {
		return // the next pass sheds it
	}
	checkouts := ownCheckouts(ctx, w, sc)
	if len(checkouts) == 0 {
		return
	}
	floor := r.LandedShedFloor
	if floor <= 0 {
		floor = DefaultLandedShedFloor
	}
	last, err := idleSince(ctx, w.Path, checkouts)
	if err != nil || r.now().Sub(last) < floor {
		return
	}
	r.stripUnused(ctx, w, sc.Repos, checkouts, res)
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
