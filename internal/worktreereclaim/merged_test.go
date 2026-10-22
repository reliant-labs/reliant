// Copyright (c) 2025 Reliant Labs
package worktreereclaim

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// A worktree whose branch was squash-merged and whose PR branch was then
// deleted has no remote ref naming its HEAD and is an ancestor of nothing, so
// "pushed" alone never frees it. The reaper treats such work as done when its
// patch is already on the base branch, or when its PR is merged.

// squashMerge plays what GitHub does when a PR is squash-merged: the branch's
// whole change lands on main as ONE new commit, and the PR branch is deleted.
func (f *fixture) squashMerge(branch string) {
	f.t.Helper()
	// Merge on the server itself, so the project's own checkout, which the
	// worktrees hang off, is untouched. Plumbing in the bare origin makes the
	// commit `merge --squash` would (the same three-way tree, one parent) in
	// four processes; a clone, merge, commit and push was nineteen.
	tree := run(f.t, f.origin, "merge-tree", "--write-tree", "main", branch)
	squash := run(f.t, f.origin, "commit-tree", "-p", "main", "-m", "squash of "+branch+" (#1)", tree)
	run(f.t, f.origin, "update-ref", "refs/heads/main", squash)
	run(f.t, f.origin, "branch", "-D", branch)
}

// commitIn adds a file in the worktree as its own commit and pushes the branch,
// as a PR author does.
func (f *fixture) commitIn(w Worktree, branch, file, body string, push bool) {
	f.t.Helper()
	write(f.t, filepath.Join(w.Path, file), body)
	run(f.t, w.Path, "add", ".")
	run(f.t, w.Path, "commit", "-q", "-m", "add "+file)
	if push {
		run(f.t, w.Path, "push", "-q", "origin", branch)
	}
}

// pruned is what the daemon's next fetch does once the PR branch is gone: it
// drops the remote-tracking ref, so nothing but local commits name HEAD.
func (f *fixture) pruned(w Worktree) {
	f.t.Helper()
	run(f.t, w.Path, "fetch", "-q", "--prune", "origin")
	if out := run(f.t, w.Path, "branch", "-r", "--contains", "HEAD"); out != "" {
		f.t.Fatalf("a remote ref still names HEAD:\n%s", out)
	}
}

func TestMerged_SquashMergedBranchWithItsRemoteBranchDeletedIsReaped(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "a squash merge, a deleted remote branch and a full reclaim (~66 git processes)")
	f := newFixture(t)
	f.idle()
	w := f.worktree("sq", "feat/sq")
	f.commitIn(w, "feat/sq", "one.txt", "1\n", true)
	f.commitIn(w, "feat/sq", "two.txt", "2\n", true)
	f.squashMerge("feat/sq")
	f.pruned(w)

	// The base moved on after the squash, as it does on a busy repository.
	other := filepath.Join(t.TempDir(), "other")
	run(t, filepath.Dir(other), "clone", "-q", f.origin, other)
	run(t, other, "config", "user.name", "x")
	run(t, other, "config", "user.email", "x@e.com")
	write(t, filepath.Join(other, "later.txt"), "later\n")
	run(t, other, "add", ".")
	run(t, other, "commit", "-q", "-m", "later work")
	run(t, other, "push", "-q", "origin", "main")
	run(t, w.Path, "fetch", "-q", "origin")

	if got := f.one(w); got.Outcome != OutcomeRemoved || exists(w.Path) {
		t.Fatalf("got %+v, want removed: its two commits are on main as one squash", got)
	}
	run(t, f.project, "rev-parse", "--verify", "refs/heads/feat/sq") // the branch itself is never deleted
}

func TestMerged_RebaseMergedBranchIsReaped(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "~59 git processes")
	f := newFixture(t)
	f.idle()
	w := f.worktree("rb", "feat/rb")
	f.commitIn(w, "feat/rb", "a.txt", "a\n", true)
	f.commitIn(w, "feat/rb", "b.txt", "b\n", true)

	// GitHub's "rebase and merge": each commit is re-applied on main with a new
	// sha but the same patch, and the PR branch is deleted.
	srv := filepath.Join(t.TempDir(), "server")
	run(t, filepath.Dir(srv), "clone", "-q", f.origin, srv)
	run(t, srv, "config", "user.name", "gh")
	run(t, srv, "config", "user.email", "gh@e.com")
	write(t, filepath.Join(srv, "newer.txt"), "main moved\n")
	run(t, srv, "add", ".")
	run(t, srv, "commit", "-q", "-m", "main moved first")
	run(t, srv, "cherry-pick", "origin/feat/rb~1", "origin/feat/rb")
	run(t, srv, "push", "-q", "origin", "main")
	run(t, f.origin, "branch", "-D", "feat/rb")
	f.pruned(w)
	run(t, w.Path, "fetch", "-q", "origin")

	if got := f.one(w); got.Outcome != OutcomeRemoved {
		t.Fatalf("got %+v, want removed", got)
	}
}

func TestMerged_AnUnpushedLocalCommitIsStillHeld(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "a merge, an unpushed commit, and two verdicts (~71 git processes)")
	f := newFixture(t)
	f.idle()
	w := f.worktree("lc", "feat/lc")
	f.commitIn(w, "feat/lc", "pushed.txt", "p\n", true)
	f.squashMerge("feat/lc")
	// One more commit made after the squash, never pushed: it is not on main.
	f.commitIn(w, "feat/lc", "after.txt", "work that exists only here\n", false)
	f.pruned(w)
	run(t, w.Path, "fetch", "-q", "origin")

	got := f.verdict(w)
	if got.Outcome != OutcomeHeld || got.Reason != ReasonUnpushed || !exists(w.Path) {
		t.Fatalf("got %+v, want held/unpushed: after.txt was never merged or pushed", got)
	}
	if got := f.confirmed(w); got.Outcome == OutcomeRemoved && !snapshotHas(t, f, got, "after.txt", "exists only here") {
		t.Fatalf("confirmed clean-up removed it without saving after.txt: %+v", got)
	}
}

func TestMerged_APurelyLocalBranchNeverPushedIsHeld(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.idle()
	w := f.worktree("lo", "feat/lo")
	f.commitIn(w, "feat/lo", "only-here.txt", "x\n", false)
	if got := f.verdict(w); got.Outcome != OutcomeHeld || got.Reason != ReasonUnpushed {
		t.Fatalf("got %+v, want held/unpushed", got)
	}
}

func TestMerged_ADirtyCheckoutIsHeldEvenWhenItsBranchWasSquashMerged(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.idle()
	w := f.worktree("dm", "feat/dm")
	f.commitIn(w, "feat/dm", "m.txt", "m\n", true)
	f.squashMerge("feat/dm")
	f.pruned(w)
	run(t, w.Path, "fetch", "-q", "origin")
	write(t, filepath.Join(w.Path, "scratch.txt"), "uncommitted\n")

	got := f.one(w)
	if got.Outcome != OutcomeHeld || got.Reason != ReasonDirty || !exists(filepath.Join(w.Path, "scratch.txt")) {
		t.Fatalf("got %+v, want held/dirty with the file intact", got)
	}
}

func TestMerged_InUseAndRecentlyActiveStillHoldAMergedWorktree(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.idle()
	w := f.worktree("iu", "feat/iu")
	f.commitIn(w, "feat/iu", "i.txt", "i\n", true)
	f.squashMerge("feat/iu")
	f.pruned(w)
	run(t, w.Path, "fetch", "-q", "origin")

	f.r.Cwds = func(context.Context) ([]string, error) { return []string{filepath.Join(w.Path, "sub")}, nil }
	if got := f.one(w); got.Outcome != OutcomeHeld || got.Reason != ReasonInUse {
		t.Fatalf("got %+v, want held/in_use", got)
	}
	f.r.Cwds = func(context.Context) ([]string, error) { return nil, nil }
	f.r.Now = nil // the real clock: it was touched seconds ago
	if got := f.one(w); got.Outcome != OutcomeHeld || got.Reason != ReasonRecentlyActive {
		t.Fatalf("got %+v, want held/recently_active", got)
	}
}

// A user-locked worktree (someone else's `git worktree lock`) is never theirs
// to reap, merged or not.
func TestMerged_ALockedByTheUserWorktreeIsNotReaped(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.idle()
	w := f.worktree("ul", "feat/ul")
	f.commitIn(w, "feat/ul", "u.txt", "u\n", true)
	f.squashMerge("feat/ul")
	f.pruned(w)
	run(t, w.Path, "fetch", "-q", "origin")
	run(t, f.project, "worktree", "unlock", w.Path)
	run(t, f.project, "worktree", "lock", "--reason", "keep: my experiment", w.Path)
	if got := f.one(w); got.Outcome != OutcomeHeld || got.Reason != ReasonUnmanaged || !exists(w.Path) {
		t.Fatalf("got %+v, want held/unmanaged", got)
	}
}

// With a PR lookup, a merged PR whose head is exactly this HEAD frees the
// worktree even when no patch on the local base matches (the clone has not
// fetched the merge); a different head does not.
func TestMerged_AMergedPullRequestForExactlyThisHeadFreesTheWorktree(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "a merged pull request's head through a verdict and a full reclaim (~62 git processes)")
	f := newFixture(t)
	f.idle()
	w := f.worktree("pr", "feat/pr")
	f.commitIn(w, "feat/pr", "p.txt", "p\n", true)
	head := run(t, w.Path, "rev-parse", "HEAD")
	run(t, f.origin, "branch", "-D", "feat/pr")
	f.pruned(w) // nothing local names it and main has not got it

	asked := 0
	f.r.PRMerged = func(_ context.Context, _, branch, sha string) bool {
		asked++
		return branch == "feat/pr" && sha == "somebody-elses-head"
	}
	if got := f.verdict(w); got.Outcome != OutcomeHeld || got.Reason != ReasonUnpushed {
		t.Fatalf("a merged PR with another head must not free it: %+v", got)
	}
	f.r.netCache = nil // the first answer was a (cached) no for this head
	f.r.PRMerged = func(_ context.Context, _, branch, sha string) bool {
		asked++
		return branch == "feat/pr" && sha == head
	}
	if got := f.one(w); got.Outcome != OutcomeRemoved {
		t.Fatalf("got %+v, want removed: its PR merged with exactly this head", got)
	}
	if asked == 0 {
		t.Fatal("the PR lookup was never consulted")
	}
}

// The base is refreshed before concluding the work is unmerged: the merge may
// have happened since this clone last fetched.
func TestMerged_TheBaseIsFetchedBeforeConcludingItIsUnmerged(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "a merge on the origin, a fetch and a full reclaim (~64 git processes)")
	f := newFixture(t)
	f.idle()
	w := f.worktree("ft", "feat/ft")
	f.commitIn(w, "feat/ft", "f.txt", "f\n", true)
	stale := run(t, w.Path, "rev-parse", "refs/remotes/origin/main")
	f.squashMerge("feat/ft")
	f.pruned(w)
	// This clone last fetched before the merge: put origin/main back where it was.
	run(t, w.Path, "update-ref", "refs/remotes/origin/main", stale)

	fetched := 0
	f.r.FetchBase = func(ctx context.Context, checkout, base string) { fetched++; FetchBaseBranch(ctx, checkout, base) }
	if got := f.one(w); got.Outcome != OutcomeRemoved {
		t.Fatalf("got %+v, want removed after refreshing origin/main", got)
	}
	if fetched == 0 {
		t.Fatal("origin/main was never refreshed")
	}
}

// A branch deleted on GitHub after merge may still have patch-equal changes
// reverted since; what counts is the patch, so a squash of DIFFERENT content
// must not match.
func TestMerged_ADifferentChangeOnMainDoesNotCountAsMerged(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "~35 git processes")
	f := newFixture(t)
	f.idle()
	w := f.worktree("df", "feat/df")
	f.commitIn(w, "feat/df", "mine.txt", "my change\n", false)

	srv := filepath.Join(t.TempDir(), "server")
	run(t, filepath.Dir(srv), "clone", "-q", f.origin, srv)
	run(t, srv, "config", "user.name", "gh")
	run(t, srv, "config", "user.email", "gh@e.com")
	write(t, filepath.Join(srv, "mine.txt"), "someone else wrote a different mine.txt\n")
	run(t, srv, "add", ".")
	run(t, srv, "commit", "-q", "-m", "unrelated work with the same filename")
	run(t, srv, "push", "-q", "origin", "main")
	run(t, w.Path, "fetch", "-q", "origin")

	if got := f.verdict(w); got.Outcome != OutcomeHeld || got.Reason != ReasonUnpushed {
		t.Fatalf("got %+v, want held/unpushed: main has a different change to that file", got)
	}
}

// A multi-repo workspace is removed only when EVERY checkout is done: one
// squash-merged and one with unpushed work holds the whole thing, and naming
// the checkout that is not done.
func TestMerged_AMultiRepoWorkspaceNeedsEveryCheckoutDone(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "two checkouts, two squash merges, a verdict and a full reclaim (~130 git processes)")
	f := newFixture(t)
	f.idle()
	ws := filepath.Join(f.root, "proj", "multi")
	checkouts := map[string]string{"api": "feat/api", "web": "feat/web"}
	for c, b := range checkouts {
		run(t, f.project, "worktree", "add", "-q", "-b", b, filepath.Join(ws, c), "main")
		if err := LockCheckout(context.Background(), filepath.Join(ws, c), "multi"); err != nil {
			t.Fatal(err)
		}
	}
	w := Worktree{ID: "multi", Path: ws, State: StateArchived, BaseBranch: "main", Fence: "9"}
	api, web := Worktree{Path: filepath.Join(ws, "api")}, Worktree{Path: filepath.Join(ws, "web")}
	f.commitIn(api, "feat/api", "api.txt", "a\n", true)
	f.squashMerge("feat/api")
	f.pruned(api)
	f.commitIn(web, "feat/web", "web.txt", "never pushed\n", false)
	run(t, api.Path, "fetch", "-q", "origin")

	got := f.verdict(w)
	if got.Outcome != OutcomeHeld || got.Reason != ReasonUnpushed || !strings.Contains(got.Detail, "web") || strings.Contains(got.Detail, "api: ") {
		t.Fatalf("got %+v, want held/unpushed naming web only", got)
	}
	if !exists(filepath.Join(ws, "api")) || !exists(filepath.Join(ws, "web")) {
		t.Fatal("a checkout was removed from a workspace that is not done")
	}

	// Once web is merged too, the whole workspace goes.
	run(t, web.Path, "push", "-q", "origin", "feat/web")
	f.squashMerge("feat/web")
	f.pruned(web)
	run(t, web.Path, "fetch", "-q", "origin")
	if got := f.one(w); got.Outcome != OutcomeRemoved || exists(ws) {
		t.Fatalf("got %+v exists=%v, want the whole workspace removed", got, exists(ws))
	}
}

// A patch-id does not see a binary file's bytes, so two different binary
// changes to one path would look identical. A branch that touches a binary file
// is therefore never recognised by patch, and stays held.
func TestMerged_ABranchThatTouchesABinaryFileIsNotRecognisedByPatch(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.idle()
	w := f.worktree("bn", "feat/bn")
	write(t, filepath.Join(w.Path, "blob.bin"), "\x00\x01mine\x00")
	run(t, w.Path, "add", ".")
	run(t, w.Path, "commit", "-q", "-m", "add blob")

	srv := filepath.Join(t.TempDir(), "server")
	run(t, filepath.Dir(srv), "clone", "-q", f.origin, srv)
	run(t, srv, "config", "user.name", "gh")
	run(t, srv, "config", "user.email", "gh@e.com")
	write(t, filepath.Join(srv, "blob.bin"), "\x00\x02theirs\x00") // a different binary at the same path
	run(t, srv, "add", ".")
	run(t, srv, "commit", "-q", "-m", "someone else's blob")
	run(t, srv, "push", "-q", "origin", "main")
	run(t, w.Path, "fetch", "-q", "origin")

	if got := f.verdict(w); got.Outcome != OutcomeHeld || got.Reason != ReasonUnpushed {
		t.Fatalf("got %+v, want held/unpushed", got)
	}
}

func TestMerged_PatchesOnBaseHandlesWhatItCannotCompare(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	w := f.worktree("pb", "feat/pb")
	ctx := context.Background()
	head := run(t, w.Path, "rev-parse", "HEAD")

	// Nothing the base lacks: an ancestor, which inBase answers before this.
	if ok, err := patchesOnBase(ctx, w.Path, "refs/heads/main", head); err != nil || ok {
		t.Fatalf("an empty range is not evidence of a merge: ok=%v err=%v", ok, err)
	}
	// A base ref that does not exist can prove nothing, so it is never "merged":
	// the worktree stays held.
	if ok, err := patchesOnBase(ctx, w.Path, "refs/heads/nope", head); err != nil || ok {
		t.Fatalf("ok=%v err=%v, want false: an unknown base proves nothing", ok, err)
	}
}
