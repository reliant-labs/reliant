// Copyright (c) 2025 Reliant Labs
package worktreereclaim

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func (f *fixture) probeIdle(w Worktree) Result {
	f.t.Helper()
	w.State, w.Fence, w.ProbeIdle = StateActive, "", true
	return f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}}).Results[0]
}

// An active worktree is reported finished only when everything the automatic
// removal needs holds, with the longer floor. The daemon never removes it.
func TestIdleDone_ReportsOnlyAFinishedWorktree(t *testing.T) {
	f := newFixture(t)
	f.idle()
	w := f.worktree("done", "feat/done")
	if got := f.probeIdle(w); got.Outcome != OutcomeIdleDone {
		t.Fatalf("clean, merged, idle: got %+v, want idle_done", got)
	}
	if !exists(w.Path) {
		t.Fatal("the daemon must never remove an active worktree")
	}
	if out := run(t, f.project, "worktree", "list", "--porcelain"); !strings.Contains(out, "locked reliant: worktree done") {
		t.Fatalf("it must stay locked as active:\n%s", out)
	}
}

func TestIdleDone_RefusesEverythingNotFinished(t *testing.T) {
	cases := map[string]func(t *testing.T, f *fixture, w Worktree){
		"untracked file": func(t *testing.T, f *fixture, w Worktree) { write(t, filepath.Join(w.Path, "wip.txt"), "x\n") },
		"modified file":  func(t *testing.T, f *fixture, w Worktree) { write(t, filepath.Join(w.Path, "a.txt"), "changed\n") },
		"unpushed commit": func(t *testing.T, f *fixture, w Worktree) {
			f.commitIn(w, "feat/x", "n.txt", "n\n", false)
		},
		"ignored data": func(t *testing.T, f *fixture, w Worktree) {
			ignore(t, f, ".env\n")
			run(t, w.Path, "merge", "-q", "--ff-only", "main")
			write(t, filepath.Join(w.Path, ".env"), "SECRET=1\n")
		},
		"process inside": func(t *testing.T, f *fixture, w Worktree) {
			f.r.Cwds = func(context.Context) ([]string, error) { return []string{filepath.Join(w.Path, "sub")}, nil }
		},
		"process list unreadable": func(t *testing.T, f *fixture, w Worktree) {
			f.r.Cwds = func(context.Context) ([]string, error) { return nil, os.ErrPermission }
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.idle()
			w := f.worktree("x", "feat/x")
			setup(t, f, w)
			got := f.probeIdle(w)
			if got.Outcome == OutcomeIdleDone {
				t.Fatalf("%s: reported finished: %+v", name, got)
			}
			if !exists(w.Path) {
				t.Fatalf("%s: directory removed", name)
			}
		})
	}
}

func TestIdleDone_RecentlyTouchedIsNotFinished(t *testing.T) {
	f := newFixture(t)
	f.r.Now = func() time.Time { return time.Now().Add(2 * time.Hour) } // past the removal floor, well short of the active one
	f.r.IdleFloor = time.Hour
	w := f.worktree("recent", "feat/recent")
	if got := f.probeIdle(w); got.Outcome != OutcomeLocked {
		t.Fatalf("2h idle is not finished for an ACTIVE worktree (floor %v): %+v", DefaultActiveIdleFloor, got)
	}
}

func TestIdleDone_NeedsTheServerToAsk(t *testing.T) {
	f := newFixture(t)
	f.idle()
	w := f.worktree("quiet", "feat/quiet")
	w.State, w.Fence = StateActive, ""
	got := f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}}).Results[0]
	if got.Outcome != OutcomeLocked {
		t.Fatalf("without ProbeIdle an active worktree is only locked: %+v", got)
	}
}

func TestIdleDone_AWorktreeBeingRestoredIsNeverProbed(t *testing.T) {
	f := newFixture(t)
	f.idle()
	w := f.worktree("rest", "feat/rest")
	w.State, w.Fence, w.ProbeIdle, w.Retire = StateActive, "", true, "100"
	if got := f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}}).Results[0]; got.Outcome != OutcomeLocked {
		t.Fatalf("a restore must only re-lock: %+v", got)
	}
}

// One worktree stalled inside a check must cost its own row, not the budget the
// others need: the request ends by its deadline and the stalled one is
// reported as timed out, not held.
func TestReconcile_AStalledWorktreeIsDeferredAndTheOthersFinish(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out a real request deadline; skipped under -short")
	}
	f := newFixture(t)
	f.idle()
	f.r.Budget = 6 * time.Second
	var first atomic.Bool
	f.r.Cwds = func(ctx context.Context) ([]string, error) {
		if first.CompareAndSwap(false, true) {
			<-ctx.Done() // the stalled check
			return nil, ctx.Err()
		}
		return nil, nil
	}
	var ws []Worktree
	for _, id := range []string{"s1", "s2", "s3"} {
		w := f.worktree(id, "feat/"+id)
		w.Fence = "100"
		ws = append(ws, w)
	}
	start := time.Now()
	resp := f.r.Reconcile(context.Background(), Request{Worktrees: ws})
	if d := time.Since(start); d > 20*time.Second {
		t.Fatalf("the request overran its budget: %v", d)
	}
	var deferred, claimed int
	for _, res := range resp.Results {
		switch {
		case res.Outcome == OutcomeDeferred && res.Detail == DetailTimedOut:
			deferred++
		case res.Outcome == OutcomeClaimed:
			claimed++
		default:
			t.Errorf("unexpected %+v (a stalled check is not a hold)", res)
		}
	}
	if deferred != 1 || claimed != 2 {
		t.Fatalf("deferred=%d claimed=%d, want 1 and 2: %+v", deferred, claimed, resp.Results)
	}
	for _, w := range ws {
		if !exists(w.Path) {
			t.Fatalf("%s: phase one must not remove anything", w.ID)
		}
	}
}

func TestReconcile_ResultsKeepRequestOrder(t *testing.T) {
	f := newFixture(t)
	f.idle()
	var ws []Worktree
	for _, id := range []string{"o1", "o2", "o3", "o4", "o5", "o6"} {
		w := f.worktree(id, "feat/"+id)
		w.Fence = "100"
		ws = append(ws, w)
	}
	resp := f.r.Reconcile(context.Background(), Request{Worktrees: ws})
	for i, res := range resp.Results {
		if res.ID != ws[i].ID {
			t.Fatalf("result %d is %s, want %s", i, res.ID, ws[i].ID)
		}
	}
}

// A rebase or amend leaves the old tip in the worktree's reflog, on no branch.
// Its change is on main, so losing the name loses nothing.
func TestOrphan_TipWhoseWorkIsOnBaseDoesNotHold(t *testing.T) {
	f := newFixture(t)
	f.idle()
	w := f.worktree("rb", "feat/rb")
	f.commitIn(w, "feat/rb", "one.txt", "1\n", true)
	old := run(t, w.Path, "rev-parse", "HEAD")
	f.squashMerge("feat/rb")
	run(t, w.Path, "fetch", "-q", "--prune", "origin")     // the PR branch is gone from origin
	run(t, w.Path, "reset", "-q", "--hard", "origin/main") // the "git pull" after merge: the old tip survives only in the reflog
	if run(t, w.Path, "cat-file", "-t", old) != "commit" {
		t.Fatal("setup: old tip gone")
	}
	got := f.one(w)
	if got.Outcome != OutcomeRemoved {
		t.Fatalf("a reflog tip whose change is on main must not hold the worktree: %+v", got)
	}
}

func TestOrphan_TipWithWorkNotOnBaseStillHolds(t *testing.T) {
	f := newFixture(t)
	f.idle()
	w := f.worktree("lost", "feat/lost")
	run(t, w.Path, "checkout", "-q", "--detach")
	write(t, filepath.Join(w.Path, "lost.txt"), "only here\n")
	run(t, w.Path, "add", ".")
	run(t, w.Path, "commit", "-q", "-m", "detached work")
	run(t, w.Path, "checkout", "-q", "feat/lost")
	got := f.one(w)
	if got.Outcome != OutcomeHeld || got.Reason != ReasonUnreachable || !exists(w.Path) {
		t.Fatalf("work that is nowhere else must still hold: %+v", got)
	}
}

// With three repos per workspace and dozens of workspaces, the same origin was
// fetched and the same PR looked up over and over inside one pass.
func TestProbe_NetworkChecksAreSharedPerRepo(t *testing.T) {
	f := newFixture(t)
	var fetches, lookups atomic.Int32
	f.r.FetchBase = func(context.Context, string, string) { fetches.Add(1) }
	f.r.PRMerged = func(context.Context, string, string, string) bool { lookups.Add(1); return false }
	a := f.worktree("n1", "feat/n1")
	b := f.worktree("n2", "feat/n2")
	f.commitIn(a, "feat/n1", "a.txt", "a\n", false)
	f.commitIn(b, "feat/n2", "b.txt", "b\n", false)
	f.idle()
	f.r.FetchBase = func(context.Context, string, string) { fetches.Add(1) }
	f.r.PRMerged = func(context.Context, string, string, string) bool { lookups.Add(1); return false }
	f.one(a)
	f.one(b)
	f.one(a)
	if n := fetches.Load(); n != 1 {
		t.Fatalf("one origin's base branch fetched %d times in a pass, want 1", n)
	}
	if n := lookups.Load(); n != 2 {
		t.Fatalf("PR lookups = %d, want 2 (one per branch, repeated asks cached)", n)
	}
}

func TestProbe_AMergedAnswerIsKeptButANoIsRetried(t *testing.T) {
	f := newFixture(t)
	now := time.Now()
	f.r.Now = func() time.Time { return now }
	answer := false
	var calls atomic.Int32
	f.r.PRMerged = func(context.Context, string, string, string) bool { calls.Add(1); return answer }
	p := f.r.probe()
	ctx := context.Background()
	if p.prMerged(ctx, f.project, "b", "h") {
		t.Fatal("want no")
	}
	answer = true
	if p.prMerged(ctx, f.project, "b", "h") {
		t.Fatal("a recent no is reused: GitHub is not asked on every check")
	}
	now = now.Add(netTTL + time.Second)
	if !p.prMerged(ctx, f.project, "b", "h") {
		t.Fatal("after the TTL the question is asked again")
	}
	answer = false
	if !p.prMerged(ctx, f.project, "b", "h") || calls.Load() != 2 {
		t.Fatalf("a merged PR never un-merges for the same head; calls=%d", calls.Load())
	}
}

func TestReasonRank_EveryReasonIsRanked(t *testing.T) {
	for _, r := range []HeldReason{ReasonUnmanaged, ReasonNestedRepo, ReasonInUse, ReasonOutsideFiles, ReasonData, ReasonUnverified, ReasonTooLarge, ReasonDirty, ReasonUnpushed, ReasonUnreachable} {
		if reasonRank(r) == 0 {
			t.Errorf("%s has no rank, so a multi-repo worktree could report it as safe", r)
		}
	}
	if reasonRank(ReasonDirty) <= reasonRank(ReasonUnpushed) || reasonRank(ReasonUnpushed) <= reasonRank(ReasonUnreachable) {
		t.Error("dirty must outrank unpushed must outrank unreachable_history")
	}
}
