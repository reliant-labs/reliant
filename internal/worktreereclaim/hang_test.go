// Copyright (c) 2025 Reliant Labs
package worktreereclaim

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// bareReclaimer is a Reclaimer over an empty root, for tests that never reach
// git: the lock and deadline paths run before anything touches a repository.
func bareReclaimer(t *testing.T) (*Reclaimer, Worktree) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := &Reclaimer{Root: root, Cwds: func(context.Context) ([]string, error) { return nil, nil }}
	return r, Worktree{ID: "row", Path: filepath.Join(root, "proj", "row"), State: StateArchived, Fence: "100"}
}

func cancelled() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// ---- lock acquisition honours the deadline ------------------------------

func TestPollLock_StopsWhenContextEnds(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	tries := 0
	err := pollLock(ctx, func() (bool, error) {
		tries++
		if tries == 3 {
			cancel()
		}
		return false, nil
	})
	if !errors.Is(err, context.Canceled) || tries != 3 {
		t.Fatalf("err=%v tries=%d, want Canceled after 3 tries", err, tries)
	}
}

func TestPollLock_TakesAFreeLockEvenWhenTheContextIsDone(t *testing.T) {
	t.Parallel()
	if err := pollLock(cancelled(), func() (bool, error) { return true, nil }); err != nil {
		t.Fatalf("an uncontended lock must not be refused for want of time: %v", err)
	}
}

func TestLockFile_ContendedLockGivesUpWithTheContext(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "row.lock")
	release, err := lockFile(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockFile(cancelled(), path); !errors.Is(err, context.Canceled) {
		t.Fatalf("a held lock with a done context: err=%v, want Canceled", err)
	}
	release()
	again, err := lockFile(cancelled(), path)
	if err != nil {
		t.Fatalf("a released lock is free even to a done context: %v", err)
	}
	again()
}

func TestPathLock_WaiterGivesUpWithTheContext(t *testing.T) {
	t.Parallel()
	l := newPathLock()
	if err := l.lock(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := l.lock(cancelled()); !errors.Is(err, context.Canceled) {
		t.Fatalf("held path lock with a done context: err=%v, want Canceled", err)
	}
	if l.tryLock() {
		t.Fatal("tryLock took a held lock")
	}
	l.unlock()
	if !l.tryLock() {
		t.Fatal("tryLock refused a free lock")
	}
}

func TestSettle_PathLockHeldDefersInsteadOfBlocking(t *testing.T) {
	t.Parallel()
	r, w := bareReclaimer(t)
	l := r.pathLock(w.Path)
	if err := l.lock(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := r.settle(cancelled(), w, false)
	if got.Outcome != OutcomeDeferred || got.Detail != DetailTimedOut {
		t.Fatalf("a worktree another request holds: %+v, want deferred/timed out", got)
	}
	l.unlock()
	if got := r.settle(context.Background(), w, false); got.Outcome != OutcomeGone {
		t.Fatalf("once free it settles normally: %+v", got)
	}
}

func TestSettle_RowFileLockHeldElsewhereDefersInsteadOfBlocking(t *testing.T) {
	t.Parallel()
	r, w := bareReclaimer(t)
	release, err := NewReclaimer(r.Root).rowLock(context.Background(), w.ID) // another process on the same root
	if err != nil {
		t.Fatal(err)
	}
	got := r.settle(cancelled(), w, false)
	if got.Outcome != OutcomeDeferred || got.Detail != DetailTimedOut {
		t.Fatalf("a row another process holds: %+v, want deferred/timed out", got)
	}
	release()
	if got := r.settle(context.Background(), w, false); got.Outcome != OutcomeGone {
		t.Fatalf("once free it settles normally: %+v", got)
	}
}

func TestReconcile_RowLockHeldElsewhereRepliesAtTheDeadline(t *testing.T) {
	t.Parallel()
	r, w := bareReclaimer(t)
	r.Budget = 200 * time.Millisecond
	release, err := NewReclaimer(r.Root).rowLock(context.Background(), w.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	done := make(chan Response, 1)
	go func() { done <- r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}}) }()
	select {
	case resp := <-done:
		if got := resp.Results[0]; got.Outcome != OutcomeDeferred {
			t.Fatalf("got %+v, want deferred", got)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Reconcile did not reply: a held row lock outlasted the deadline")
	}
}

// ---- Reconcile replies at its deadline ----------------------------------

func TestReconcile_RepliesAtTheDeadlineWithWhatFinished(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.idle()
	f.r.Budget = 200 * time.Millisecond
	f.r.Workers = 1
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	// A process list that never answers and ignores its context: the one
	// worker wedges there, as a git process stuck on a dead disk would.
	f.r.Cwds = func(context.Context) ([]string, error) { <-release; return nil, nil }

	gone := Worktree{ID: "gone", Path: filepath.Join(f.root, "proj", "gone"), State: StateArchived, Fence: "100"}
	stuck := f.worktree("stuck", "feat/stuck")
	queued := Worktree{ID: "queued", Path: filepath.Join(f.root, "proj", "queued"), State: StateArchived, Fence: "100"}
	req := Request{Worktrees: []Worktree{gone, stuck, queued}}

	done := make(chan Response, 1)
	go func() { done <- f.r.Reconcile(context.Background(), req) }()
	var resp Response
	select {
	case resp = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Reconcile did not reply at its deadline: it waited for a wedged worker")
	}
	if len(resp.Results) != len(req.Worktrees) {
		t.Fatalf("got %d results for %d worktrees", len(resp.Results), len(req.Worktrees))
	}
	for i, r := range resp.Results {
		if r.ID != req.Worktrees[i].ID {
			t.Fatalf("result %d is for %q, want %q: results follow the request's order", i, r.ID, req.Worktrees[i].ID)
		}
	}
	if r := resp.Results[0]; r.Outcome != OutcomeGone {
		t.Errorf("a worktree that finished before the deadline keeps its answer: %+v", r)
	}
	if r := resp.Results[1]; r.Outcome != OutcomeDeferred || r.Detail != DetailTimedOut {
		t.Errorf("the wedged worktree: %+v, want deferred/timed out", r)
	}
	if r := resp.Results[2]; r.Outcome != OutcomeDeferred || r.Detail != DetailNotReached {
		t.Errorf("a worktree never started: %+v, want deferred/not reached", r)
	}
	unblock()
}

// ---- the probe says when it could not tell ------------------------------

func TestIdleDone_AProbeOutOfTimeIsInconclusiveNotLocked(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.idle()
	f.r.ProbeBudget = time.Nanosecond
	w := f.worktree("x", "feat/x")
	got := f.probeIdle(w)
	if got.Outcome != OutcomeProbeInconclusive {
		t.Fatalf("got %+v, want probe_inconclusive", got)
	}
	if out := run(t, f.project, "worktree", "list", "--porcelain"); !strings.Contains(out, "locked reliant: worktree x") {
		t.Fatalf("an inconclusive probe still leaves the worktree locked as active:\n%s", out)
	}
}

// ---- policy -------------------------------------------------------------

func TestRebuildable_ForgeStateAndAtomicTempFiles(t *testing.T) {
	t.Parallel()
	for p, want := range map[string]bool{
		".forge/state/":                      true,
		".forge/state/build-dev.json":        true,
		".forge/.forge-tmp-generate-cache-1": true,
		".forge/hostinfra/pg/":               false,
		".forge/other.json":                  false,
		"state/build.json":                   false,
	} {
		if got := rebuildablePath(p); got != want {
			t.Errorf("rebuildablePath(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestDataWalk_AnIgnoredFileListedUnderItsDirectoryIsReportedOnce(t *testing.T) {
	t.Parallel()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run(t, dir, "init", "-q")
	write(t, filepath.Join(dir, ".gitignore"), "secrets/*\n!secrets/keep\n")
	write(t, filepath.Join(dir, "secrets", "a.yaml"), "a\n")
	write(t, filepath.Join(dir, "secrets", "b.yaml"), "b\n")
	v := dataHold(context.Background(), dir, nil)
	if v.Reason != ReasonData {
		t.Fatalf("got %+v, want data", v)
	}
	for _, name := range []string{"secrets/a.yaml", "secrets/b.yaml"} {
		if n := strings.Count(v.Detail, name); n != 1 {
			t.Errorf("%s appears %d times in %q", name, n, v.Detail)
		}
	}
}

func TestDataWalk_VisitingAPathTwiceCountsItOnce(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "secrets", "dev.yaml"), "x\n")
	d := newDataWalk(context.Background(), dir, nil)
	d.visit("secrets/")
	d.visit("secrets/dev.yaml")
	d.visit("secrets/dev.yaml")
	if len(d.out) != 1 || d.out[0] != "secrets/dev.yaml" {
		t.Fatalf("out = %v, want [secrets/dev.yaml]", d.out)
	}
}

func TestIdleSince_IgnoresTheIndexButNotHeadOrTheReflog(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	path := filepath.Join(base, "w")
	admin := filepath.Join(base, "admin")
	write(t, filepath.Join(path, ".git"), "gitdir: "+admin+"\n")
	for _, n := range []string{"index", "HEAD", filepath.Join("logs", "HEAD")} {
		write(t, filepath.Join(admin, n), "x")
	}
	old := time.Now().Add(-72 * time.Hour)
	for _, p := range []string{path, filepath.Join(path, ".git"), admin, filepath.Join(admin, "index"), filepath.Join(admin, "HEAD"), filepath.Join(admin, "logs", "HEAD"), filepath.Join(admin, "logs")} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	before := idleSince(path, []string{path})
	if before.After(old.Add(time.Second)) {
		t.Fatalf("fixture: idle clock = %v, want about %v", before, old)
	}
	now := time.Now()
	if err := os.Chtimes(filepath.Join(admin, "index"), now, now); err != nil {
		t.Fatal(err)
	}
	if got := idleSince(path, []string{path}); !got.Equal(before) {
		t.Fatalf("touching the index moved the idle clock to %v: a plain `git status` resets every clock", got)
	}
	if err := os.Chtimes(filepath.Join(admin, "logs", "HEAD"), now, now); err != nil {
		t.Fatal(err)
	}
	if got := idleSince(path, []string{path}); got.Before(now.Add(-time.Second)) {
		t.Fatalf("a new reflog entry must count as activity: %v", got)
	}
}
