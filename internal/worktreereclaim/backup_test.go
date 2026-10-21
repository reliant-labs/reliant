// Copyright (c) 2025 Reliant Labs
package worktreereclaim

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
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

// savedAt asserts one of refs is a saved-commit ref of kind ("head" or
// "reflog") holding exactly sha in the project, and returns it.
func savedAt(t *testing.T, f *fixture, refs []string, kind, sha string) string {
	t.Helper()
	for _, ref := range refs {
		if strings.Contains(ref, "/orphans/"+kind+"-") && run(t, f.project, "rev-parse", ref) == sha {
			if !strings.HasPrefix(ref, "refs/reliant/wip/") {
				t.Fatalf("%s is outside the snapshot namespace", ref)
			}
			return ref
		}
	}
	t.Fatalf("no %s ref holding %s among %v", kind, sha, refs)
	return ""
}

// A detached HEAD with commits no branch or remote has: the commits are saved
// under a ref, then the worktree is removed.
func TestReconcile_DetachedUnpushedCommitsAreSavedThenRemoved(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "~140 git processes: a full reclaim that saves a detached commit")
	f := newFixture(t)
	f.idle()
	w := f.worktree("det", "feat/det")
	w.Fence = "100"
	run(t, w.Path, "checkout", "-q", "--detach")
	sha := commit(t, w.Path, "only-here.txt", "exists nowhere else\n")

	first, second := f.both(w)
	if first.Outcome != OutcomeClaimed || second.Outcome != OutcomeRemoved || exists(w.Path) {
		t.Fatalf("got %+v then %+v, want claimed then removed", first, second)
	}
	ref := savedAt(t, f, first.SnapshotRefs, "head", sha)
	if !strings.HasPrefix(ref, SnapshotRefPrefix("det")+"root/orphans/") {
		t.Fatalf("ref %s is not this worktree's", ref)
	}
	if got := run(t, f.project, "show", ref+":only-here.txt"); got != "exists nowhere else" {
		t.Fatalf("saved commit has %q", got)
	}
	for _, r := range SnapshotRefsOnRow(first.SnapshotRefs) {
		t.Fatalf("a saved commit is never applied as a snapshot: %s", r)
	}
}

// A branch with commits on no remote: saved and removed; the branch survives.
func TestReconcile_UnpushedBranchCommitsAreSavedThenRemoved(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "~140 git processes: a full reclaim that saves an unpushed commit")
	f := newFixture(t)
	f.idle()
	w := f.worktree("br", "feat/br")
	w.Fence = "100"
	sha := commit(t, w.Path, "b.txt", "b\n")

	first, second := f.both(w)
	if second.Outcome != OutcomeRemoved {
		t.Fatalf("got %+v then %+v, want removed", first, second)
	}
	savedAt(t, f, first.SnapshotRefs, "head", sha)
	if run(t, f.project, "rev-parse", "refs/heads/feat/br") != sha {
		t.Fatal("the branch must survive")
	}
}

// Commits only the worktree's own reflog reaches (here: reset away) are saved
// before its admin directory, and with it the reflog, goes.
func TestReconcile_ReflogOnlyCommitsAreSavedThenRemoved(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "~100 git processes: a full reclaim that saves a reflog-only commit")
	f := newFixture(t)
	f.idle()
	w := f.worktree("rl", "feat/rl")
	w.Fence = "100"
	lost := commit(t, w.Path, "lost.txt", "reset away\n")
	run(t, w.Path, "reset", "-q", "--hard", "HEAD~1")

	first, second := f.both(w)
	if second.Outcome != OutcomeRemoved || exists(w.Path) {
		t.Fatalf("got %+v then %+v, want removed", first, second)
	}
	savedAt(t, f, first.SnapshotRefs, "reflog", lost)
}

// Saving is idempotent: a second pass reuses the refs it made.
func TestSaveCommits_ReusesItsRefs(t *testing.T) {
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
