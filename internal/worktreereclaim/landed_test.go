// Copyright (c) 2025 Reliant Labs
package worktreereclaim

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Reclaim on merge: an ACTIVE worktree asked ProbeLanded reports Landed only
// when every checkout is clean and its HEAD is in its base branch as the
// remote has it; with ShedLanded it also sheds the regenerable output. The
// daemon never removes an active worktree.

// landed asks the daemon, as the sweep does for a live row.
func (f *fixture) landed(w Worktree, shed bool) Result {
	f.t.Helper()
	w.State, w.Fence, w.ProbeLanded, w.ShedLanded = StateActive, "", true, shed
	return f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}}).Results[0]
}

// mergeCommit is GitHub's "create a merge commit": main gets a two-parent
// commit over the branch, after main moved on by itself.
func (f *fixture) mergeCommit(branch string) {
	f.t.Helper()
	f.moveMain("main moved before the merge")
	tree := run(f.t, f.origin, "merge-tree", "--write-tree", "main", branch)
	merge := run(f.t, f.origin, "commit-tree", "-p", "main", "-p", branch, "-m", "Merge "+branch, tree)
	run(f.t, f.origin, "update-ref", "refs/heads/main", merge)
}

// rebaseMerge is GitHub's "rebase and merge": each of the branch's commits is
// re-applied on main with a new sha, and the branch is deleted.
func (f *fixture) rebaseMerge(branch string, commits int) {
	f.t.Helper()
	srv := filepath.Join(f.t.TempDir(), "server")
	run(f.t, filepath.Dir(srv), "clone", "-q", f.origin, srv)
	write(f.t, filepath.Join(srv, "newer.txt"), "main moved\n")
	run(f.t, srv, "add", ".")
	run(f.t, srv, "commit", "-q", "-m", "main moved first")
	for i := commits - 1; i >= 0; i-- {
		run(f.t, srv, "cherry-pick", "origin/"+branch+"~"+string(rune('0'+i)))
	}
	run(f.t, srv, "push", "-q", "origin", "main")
	run(f.t, f.origin, "branch", "-D", branch)
}

// moveMain lands an unrelated commit on origin's main.
func (f *fixture) moveMain(msg string) {
	f.t.Helper()
	tree := run(f.t, f.origin, "rev-parse", "main^{tree}")
	commit := run(f.t, f.origin, "commit-tree", "-p", "main", "-m", msg, tree)
	run(f.t, f.origin, "update-ref", "refs/heads/main", commit)
}

func (f *fixture) fetch(w Worktree) {
	f.t.Helper()
	run(f.t, w.Path, "fetch", "-q", "--prune", "origin")
}

func requireLanded(t *testing.T, got Result, want bool, why string) {
	t.Helper()
	if got.Outcome != OutcomeLocked || got.Landed != want {
		t.Fatalf("%s: got %+v, want locked with landed=%v", why, got, want)
	}
}

func requireStillActive(t *testing.T, f *fixture, w Worktree) {
	t.Helper()
	if !exists(w.Path) {
		t.Fatal("the daemon must never remove an active worktree")
	}
	if out := run(t, f.project, "worktree", "list", "--porcelain"); !strings.Contains(out, "locked reliant: worktree "+w.ID) {
		t.Fatalf("it must stay locked as active:\n%s", out)
	}
}

func TestLanded_MergeCommit(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "a merge commit on origin and a landed probe (~50 git processes)")
	f := newFixture(t)
	f.idle()
	w := f.worktree("mc", "feat/mc")
	f.commitIn(w, "feat/mc", "a.txt", "a\n", true)
	requireLanded(t, f.landed(w, false), false, "pushed but not merged")

	f.mergeCommit("feat/mc")
	f.fetch(w)
	requireLanded(t, f.landed(w, false), true, "merged with a merge commit")
	requireStillActive(t, f, w)
}

func TestLanded_SquashMergeSeenAfterTheDaemonFetches(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "a squash merge and a landed probe that fetches (~50 git processes)")
	f := newFixture(t)
	f.idle()
	f.r.FetchBase = FetchBaseBranch
	w := f.worktree("sq", "feat/sq")
	f.commitIn(w, "feat/sq", "one.txt", "1\n", true)
	f.commitIn(w, "feat/sq", "two.txt", "2\n", true)
	f.squashMerge("feat/sq")
	f.moveMain("main moved after the squash")
	// No fetch here: the probe must refresh origin/main itself.
	requireLanded(t, f.landed(w, false), true, "two commits squashed into one on main, PR branch deleted")
	requireStillActive(t, f, w)
}

func TestLanded_RebaseMerge(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "a rebase merge and a landed probe (~55 git processes)")
	f := newFixture(t)
	f.idle()
	w := f.worktree("rb", "feat/rb")
	f.commitIn(w, "feat/rb", "a.txt", "a\n", true)
	f.commitIn(w, "feat/rb", "b.txt", "b\n", true)
	f.rebaseMerge("feat/rb", 2)
	f.fetch(w)
	requireLanded(t, f.landed(w, false), true, "each commit re-applied on main with a new sha")
}

func TestLanded_MergedPullRequestHead(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "a PR merged with edits and a landed probe (~45 git processes)")
	f := newFixture(t)
	f.idle()
	w := f.worktree("pr", "feat/pr")
	f.commitIn(w, "feat/pr", "a.txt", "a\n", true)
	head := run(t, w.Path, "rev-parse", "HEAD")
	// The PR was squashed with a reviewer's edit, so no patch on main matches.
	srv := filepath.Join(t.TempDir(), "server")
	run(t, filepath.Dir(srv), "clone", "-q", f.origin, srv)
	write(t, filepath.Join(srv, "a.txt"), "a, as edited in review\n")
	run(t, srv, "add", ".")
	run(t, srv, "commit", "-q", "-m", "squash of feat/pr (#7)")
	run(t, srv, "push", "-q", "origin", "main")
	f.fetch(w)
	requireLanded(t, f.landed(w, false), false, "no merged PR known and no matching patch")

	f.r.PRMerged = func(_ context.Context, _, branch, h string) bool { return branch == "feat/pr" && h == head }
	f.r.memo, f.r.netCache = nil, nil
	requireLanded(t, f.landed(w, false), true, "the head of a merged pull request")
}

func TestLanded_UnpushedWorkIsNotLanded(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "a squash merge then an unpushed commit (~50 git processes)")
	f := newFixture(t)
	f.idle()
	w := f.worktree("up", "feat/up")
	f.commitIn(w, "feat/up", "pushed.txt", "p\n", true)
	f.squashMerge("feat/up")
	f.fetch(w)
	requireLanded(t, f.landed(w, false), true, "squash-merged")
	f.commitIn(w, "feat/up", "after.txt", "work that exists only here\n", false)
	requireLanded(t, f.landed(w, false), false, "a commit made after the merge, never pushed")
}

func TestLanded_DirtyTreeIsNotLanded(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "a merged worktree probed dirty three ways (~45 git processes)")
	f := newFixture(t)
	f.idle()
	w := f.worktree("dt", "feat/dt")
	f.commitIn(w, "feat/dt", "a.txt", "a\n", true)
	f.mergeCommit("feat/dt")
	f.fetch(w)
	requireLanded(t, f.landed(w, false), true, "clean and merged")

	write(t, filepath.Join(w.Path, "a.txt"), "edited, not committed\n")
	requireLanded(t, f.landed(w, false), false, "a modified tracked file")
	run(t, w.Path, "checkout", "--", "a.txt")
	write(t, filepath.Join(w.Path, "new.txt"), "untracked\n")
	requireLanded(t, f.landed(w, false), false, "an untracked file")
	if err := os.Remove(filepath.Join(w.Path, "new.txt")); err != nil {
		t.Fatal(err)
	}
	requireLanded(t, f.landed(w, false), true, "clean again")
}

func TestLanded_NoCommitsBeyondBaseCounts(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "a fresh worktree and a landed probe (~25 git processes)")
	f := newFixture(t)
	f.idle()
	w := f.worktree("fresh", "feat/fresh")
	f.moveMain("main moved on")
	f.fetch(w)
	requireLanded(t, f.landed(w, false), true, "HEAD is an ancestor of origin/main")
}

func TestLanded_MultiRepoNeedsEveryCheckoutLanded(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "a two-checkout workspace landing one checkout at a time (~80 git processes)")
	f := newFixture(t)
	f.idle()
	w := f.multiRepo("mr")
	api, web := Worktree{Path: filepath.Join(w.Path, "api")}, Worktree{Path: filepath.Join(w.Path, "web")}
	f.commitIn(api, "feat/mra", "api.txt", "api\n", true)
	f.commitIn(web, "feat/mrb", "web.txt", "web\n", true)

	f.mergeCommit("feat/mra")
	f.fetch(api)
	requireLanded(t, f.landed(w, false), false, "api merged, web only pushed")

	f.squashMerge("feat/mrb")
	f.fetch(web)
	requireLanded(t, f.landed(w, false), true, "both checkouts merged")
	requireStillActive(t, f, Worktree{ID: w.ID, Path: api.Path})
}

func TestLanded_ProbeNeverDeletesFromLiveWorktree(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "a landed worktree probed for shedding five ways (~70 git processes)")
	f := newFixture(t)
	ignore(t, f, "node_modules/\n")
	w := f.worktree("sh", "feat/sh")
	nm := filepath.Join(w.Path, "node_modules", "left-pad", "index.js")
	write(t, nm, "module.exports = 1\n")
	tracked := filepath.Join(w.Path, ".gitignore")

	// Just touched: landed, but nothing is shed inside the shed floor.
	got := f.landed(w, true)
	requireLanded(t, got, true, "fresh worktree")
	if !exists(nm) || got.StrippedBytes != 0 {
		t.Fatalf("shed inside the quiet floor: %+v", got)
	}

	f.idle()
	// ShedLanded unset (dry run, or a chat run is going): never shed.
	if got := f.landed(w, false); !exists(nm) || len(got.Stripped) != 0 {
		t.Fatalf("shed without ShedLanded: %+v", got)
	}
	// A process works in it.
	f.r.Cwds = func(context.Context) ([]string, error) { return []string{w.Path}, nil }
	if got := f.landed(w, true); !exists(nm) {
		t.Fatalf("shed while a process has its cwd there: %+v", got)
	}
	f.r.Cwds = func(context.Context) ([]string, error) { return nil, nil }
	// A process has a file in it open (or the inspection failed).
	f.r.ProcessUse = func(context.Context, []string) error { return errors.New("workspace open by process 42") }
	if got := f.landed(w, true); !exists(nm) {
		t.Fatalf("shed while a file in it is open: %+v", got)
	}
	// Unsupported platforms cannot prove that open/executable/mapped files
	// are unused. A clear cwd snapshot alone never authorizes shedding.
	f.r.ProcessUse = func(context.Context, []string) error { return ErrProcessUseUnsupported }
	if got := f.landed(w, true); !exists(nm) || len(got.Stripped) != 0 {
		t.Fatalf("shed without complete process inspection: %+v", got)
	}
	f.r.ProcessUse = func(context.Context, []string) error { return nil }

	got = f.landed(w, true)
	requireLanded(t, got, true, "quiet, unused, landed")
	if !exists(filepath.Join(w.Path, "node_modules")) || len(got.Stripped) != 0 || got.StrippedBytes != 0 {
		t.Fatalf("live landed probing must preserve all files until whole archival: %+v", got)
	}
	if !exists(tracked) {
		t.Fatal("a tracked file was removed")
	}
	requireStillActive(t, f, w)
}

func TestLanded_DataKeepsEntireWorktreeLive(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "a landed worktree with ignored data (~35 git processes)")
	f := newFixture(t)
	ignore(t, f, "node_modules/\n.env\n")
	f.idle()
	w := f.worktree("dh", "feat/dh")
	write(t, filepath.Join(w.Path, ".env"), "SECRET=1\n")
	write(t, filepath.Join(w.Path, "node_modules", "x", "i.js"), "x\n")

	got := f.landed(w, true)
	if got.Outcome != OutcomeLocked || got.Landed {
		t.Fatalf("got %+v: a workspace holding data must not be offered for archiving", got)
	}
	if !strings.Contains(got.Detail, "landed, but kept") {
		t.Fatalf("detail %q should say why it stays", got.Detail)
	}
	if !exists(filepath.Join(w.Path, "node_modules")) || !exists(filepath.Join(w.Path, ".env")) {
		t.Fatal("held live worktree must retain both output and protected data")
	}
}

func TestLanded_NotAskedForARetireOrWithoutTheFlag(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "~20 git processes")
	f := newFixture(t)
	f.idle()
	w := f.worktree("nf", "feat/nf")
	w.State, w.Fence = StateActive, ""
	if got := f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}}).Results[0]; got.Landed {
		t.Fatalf("landed reported without ProbeLanded: %+v", got)
	}
	w.ProbeLanded, w.Retire = true, "100"
	if got := f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}}).Results[0]; got.Landed {
		t.Fatalf("landed reported on an unarchive's re-lock: %+v", got)
	}
}

func TestSelfWithin_TheDaemonNeverTouchesWhereItRuns(t *testing.T) {
	t.Parallel()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if !selfWithin(wd) || !selfWithin(filepath.Dir(wd)) {
		t.Fatal("this process's working directory, and any directory above it, must count")
	}
	if selfWithin(t.TempDir()) {
		t.Fatal("an unrelated directory must not")
	}
	r := &Reclaimer{Cwds: func(context.Context) ([]string, error) { return nil, nil }, Now: func() time.Time { return time.Now() }}
	if hold := r.inUse(context.Background(), Worktree{ID: "self", Path: filepath.Dir(wd)}, true); hold == nil || hold.Reason != ReasonInUse {
		t.Fatalf("inUse = %+v, want held in use", hold)
	}
}
