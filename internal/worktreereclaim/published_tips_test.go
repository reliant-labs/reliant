// Copyright (c) 2026 Reliant Labs
package worktreereclaim

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// orphansOf is the checkout's orphan tips before and after the base and
// published-branch filter (tipsNotOnBase).
func orphansOf(t *testing.T, w Worktree) (tips, lost []string) {
	t.Helper()
	ctx := context.Background()
	tips, err := orphanTips(ctx, w.Path, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	lost, err = tipsNotOnBase(ctx, w.Path, w.BaseBranch, tips, nil)
	if err != nil {
		t.Fatal(err)
	}
	return tips, lost
}

// A pushed commit rewritten in place (an amend or rebase that keeps its
// change) and force-pushed leaves the old commit in the worktree's reflog
// only. Its change is on the pushed branch, so it no longer holds a finished
// worktree (2026-11-22: such tips held 19 of 37 finished workspaces).
func TestOrphan_TipRewrittenOnThePushedBranchDoesNotHold(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "pushes, amends and force-pushes a branch, then classifies (~40 git processes)")
	f := newFixture(t)
	f.idle()
	w := f.worktree("rb", "feat/rb")
	old := commit(t, w.Path, "work.txt", "pushed work\n")
	run(t, w.Path, "push", "-q", "origin", "feat/rb")
	run(t, w.Path, "commit", "-q", "--amend", "-m", "the same change, reworded")
	run(t, w.Path, "push", "-q", "-f", "origin", "feat/rb")

	tips, lost := orphansOf(t, w)
	if !slices.Contains(tips, old) {
		t.Fatalf("the rewritten commit should be an orphan tip: %v", tips)
	}
	if len(lost) != 0 {
		t.Fatalf("a tip whose change is on origin/feat/rb is not lost work: %v", lost)
	}
	if got := f.verdict(w); got.Reason != ReasonKept {
		t.Fatalf("work published on the pushed branch must not hold: %+v", got)
	}
}

// A commit amended into a DIFFERENT change before anything was pushed: the
// pushed branch holds only the new version, so the old one is work that
// exists nowhere else and still holds the worktree.
func TestOrphan_AmendedAwayUnpushedTipStillHolds(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "amends and pushes a branch, then classifies (~40 git processes)")
	f := newFixture(t)
	f.idle()
	w := f.worktree("am", "feat/am")
	old := commit(t, w.Path, "work.txt", "first draft, never pushed\n")
	write(t, w.Path+"/work.txt", "second draft\n")
	run(t, w.Path, "add", ".")
	run(t, w.Path, "commit", "-q", "--amend", "-m", "second draft")
	run(t, w.Path, "push", "-q", "origin", "feat/am")

	if _, lost := orphansOf(t, w); !slices.Contains(lost, old) {
		t.Fatalf("an amended-away change that was never pushed is lost work: %v", lost)
	}
	if got := f.verdict(w); got.Outcome != OutcomeHeld || got.Reason != ReasonUnreachable || !exists(w.Path) {
		t.Fatalf("unpublished history must still hold: %+v", got)
	}
	// Asked live whether it is reclaimable, the answer says what holds it.
	live := w
	live.State, live.Fence, live.ProbeRecoverableAfterSeconds = StateActive, "", 3600
	got := f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{live}}).Results[0]
	if got.Outcome != OutcomeLocked || got.RecoverableIdle || !strings.HasPrefix(got.NotReclaimable, string(ReasonUnreachable)+": ") {
		t.Fatalf("a live answer must name the unreachable history: %+v", got)
	}
}

// Only remote-tracking refs vouch for a tip: the same change on a LOCAL
// branch alone is not a durable receipt (#1590).
func TestOrphan_TipOnALocalBranchOnlyStillHolds(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "amends an unpushed branch, then classifies (~30 git processes)")
	f := newFixture(t)
	w := f.worktree("lb", "feat/lb")
	old := commit(t, w.Path, "work.txt", "local only\n")
	run(t, w.Path, "commit", "-q", "--amend", "-m", "the same change, never pushed")

	if _, lost := orphansOf(t, w); !slices.Contains(lost, old) {
		t.Fatalf("a tip whose change is only on a local branch is still lost work: %v", lost)
	}
	if got := publishedRefs(context.Background(), w.Path); len(got) != 0 {
		t.Fatalf("an unpushed branch has no published refs: %v", got)
	}
}
