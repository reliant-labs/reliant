// Copyright (c) 2025 Reliant Labs
package worktreereclaim

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// both plays the server for an automatic pass and returns the two phases'
// answers: what phase one saved and claimed, then what phase two did.
func (f *fixture) both(w Worktree) (first, second Result) {
	f.t.Helper()
	first = f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}}).Results[0]
	if first.Outcome != OutcomeClaimed {
		return first, first
	}
	w.Remove = true
	return first, f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}}).Results[0]
}

// commit makes a commit in the checkout and returns it.
func commit(t *testing.T, checkout, file, content string) string {
	t.Helper()
	write(t, filepath.Join(checkout, file), content)
	run(t, checkout, "add", ".")
	run(t, checkout, "commit", "-q", "-m", "local work: "+file)
	return run(t, checkout, "rev-parse", "HEAD")
}

// A detached unpublished HEAD must not be reclaimed using a local-only ref.
func TestReconcile_DetachedUnpushedCommitsRequireRemoteRecovery(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "detached unpushed commits saved, then a full reclaim (~62 git processes)")
	f := newFixture(t)
	f.idle()
	w := f.worktree("det", "feat/det")
	w.Fence = "100"
	run(t, w.Path, "checkout", "-q", "--detach")
	sha := commit(t, w.Path, "only-here.txt", "exists nowhere else\n")

	first, second := f.both(w)
	if first.Outcome != OutcomeHeld || second.Outcome != OutcomeHeld || !exists(w.Path) {
		t.Fatalf("got %+v then %+v, want held with original retained", first, second)
	}
	if len(first.SnapshotRefs) != 0 || run(t, w.Path, "rev-parse", "HEAD") != sha {
		t.Fatal("automatic reclaim must not substitute local snapshots for remote proof")
	}
	if got := run(t, w.Path, "show", "HEAD:only-here.txt"); got != "exists nowhere else" {
		t.Fatalf("original content changed: %q", got)
	}
}

// An unpublished branch and its original checkout survive absent remote proof.
func TestReconcile_UnpushedBranchCommitsRequireRemoteRecovery(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "unpushed branch commits saved, then a full reclaim (~61 git processes)")
	f := newFixture(t)
	f.idle()
	w := f.worktree("br", "feat/br")
	w.Fence = "100"
	sha := commit(t, w.Path, "b.txt", "b\n")

	first, second := f.both(w)
	if first.Outcome != OutcomeHeld || second.Outcome != OutcomeHeld || !exists(w.Path) {
		t.Fatalf("got %+v then %+v, want held with original retained", first, second)
	}
	if len(first.SnapshotRefs) != 0 || run(t, w.Path, "rev-parse", "HEAD") != sha {
		t.Fatal("local ref must not replace remote recovery proof")
	}
	if run(t, f.project, "rev-parse", "refs/heads/feat/br") != sha {
		t.Fatal("the branch must survive")
	}
}

// Reflog-only history must remain in its original admin directory without proof.
func TestReconcile_ReflogOnlyCommitsRequireRemoteRecovery(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "reflog-only commits saved, then a full reclaim (~73 git processes)")
	f := newFixture(t)
	f.idle()
	w := f.worktree("rl", "feat/rl")
	w.Fence = "100"
	lost := commit(t, w.Path, "lost.txt", "reset away\n")
	run(t, w.Path, "reset", "-q", "--hard", "HEAD~1")

	first, second := f.both(w)
	if first.Outcome != OutcomeHeld || second.Outcome != OutcomeHeld || !exists(w.Path) {
		t.Fatalf("got %+v then %+v, want held with original retained", first, second)
	}
	if len(first.SnapshotRefs) != 0 {
		t.Fatal("automatic local snapshot fallback is forbidden")
	}
	if got := run(t, w.Path, "reflog", "--format=%H"); !strings.Contains(got, lost) {
		t.Fatal("original reflog history was lost")
	}
	if got := run(t, w.Path, "show", lost+":lost.txt"); got != "reset away" {
		t.Fatalf("original history content changed: %q", got)
	}
}

// Saving is idempotent: a second pass reuses the refs it made.
func TestSaveCommits_ReusesItsRefs(t *testing.T) {
	testkit.Slow(t, "builds real git repositories and runs git against them")
	t.Parallel()
	f := newFixture(t)
	w := f.worktree("idem", "feat/idem")
	commit(t, w.Path, "x.txt", "x\n")
	a, err := saveCommits(context.Background(), w, []string{w.Path})
	if err != nil {
		t.Fatal(err)
	}
	b, err := saveCommits(context.Background(), w, []string{w.Path})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(a, ",") != strings.Join(b, ",") || len(a) != 1 {
		t.Fatalf("first %v, second %v: want the same single ref", a, b)
	}
}

// Uncommitted work still holds: nothing is saved and nothing removed.
func TestReconcile_DirtyStillHoldsWithUnpushedCommits(t *testing.T) {
	testkit.Slow(t, "builds real git repositories and runs git against them")
	t.Parallel()
	f := newFixture(t)
	f.idle()
	w := f.worktree("dirty-up", "feat/dirty-up")
	w.Fence = "100"
	commit(t, w.Path, "c.txt", "c\n")
	write(t, filepath.Join(w.Path, "wip.txt"), "not committed\n")

	got, _ := f.both(w)
	if got.Outcome != OutcomeHeld || got.Reason != ReasonDirty || !exists(w.Path) {
		t.Fatalf("got %+v, want held/dirty", got)
	}
	if out := run(t, f.project, "for-each-ref", "refs/reliant/"); out != "" {
		t.Fatalf("nothing is saved for a dirty worktree, found:\n%s", out)
	}
}

// "Keep everything" removes nothing, so nothing is saved either.
func TestReconcile_KeepEverythingDoesNotSaveOrRemove(t *testing.T) {
	testkit.Slow(t, "builds real git repositories and runs git against them")
	t.Parallel()
	f := newFixture(t)
	f.idle()
	w := f.worktree("keep-up", "feat/keep-up")
	commit(t, w.Path, "k.txt", "k\n")
	if got := f.verdict(w); got.Outcome != OutcomeHeld || got.Reason != ReasonUnpushed || !exists(w.Path) {
		t.Fatalf("got %+v, want held/unpushed", got)
	}
	if out := run(t, f.project, "for-each-ref", "refs/reliant/"); out != "" {
		t.Fatalf("nothing is saved under keep everything, found:\n%s", out)
	}
}
