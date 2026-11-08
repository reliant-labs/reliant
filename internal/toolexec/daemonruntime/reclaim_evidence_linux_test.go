//go:build linux

package daemonruntime

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

// Owner decision, 2026-11-07: a clean, pushed worktree is retired on evidence
// about ITS paths, not refused because some managed background process or
// terminal exists elsewhere on the machine. These run the real admission
// gate and the real /proc inspection (cwd, exe, open fds, mappings).

var errUnrelatedShell = errors.New("managed background process has unknown filesystem scope")

// evidenceReclaimer is a Reclaimer wired the way sharedWorktreeReclaimer
// wires the daemon's, against gate g, and an archived worktree of a project
// whose remote has its HEAD.
func evidenceReclaimer(t *testing.T, g *recoveryAdmission) (*worktreereclaim.Reclaimer, worktreereclaim.Worktree) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	project, root := filepath.Join(base, "project"), filepath.Join(base, "worktrees")
	newRepo(t, project)
	rcGit(t, project, "update-ref", "refs/remotes/origin/main", "HEAD")
	path := filepath.Join(root, "proj", "workspace")
	rcGit(t, project, "worktree", "add", "-q", "-b", "feature", path)
	if err := worktreereclaim.LockCheckout(context.Background(), path, "evidence"); err != nil {
		t.Fatal(err)
	}
	// A fixed clock decades past the fixture's file times: every idle floor
	// is met without reading the wall clock.
	idleNow := time.Date(2100, time.January, 1, 0, 0, 0, 0, time.UTC)
	r := &worktreereclaim.Reclaimer{Root: root, Now: func() time.Time { return idleNow },
		Cwds: worktreereclaim.ProcessCwds, ProcessUse: worktreereclaim.CheckProcessUse}
	r.Acquire = func(_ context.Context, paths []string) (worktreereclaim.RecoveryLease, error) {
		lease, err := g.acquireLease(paths, false)
		return acquireChecked(context.Background(), lease, err)
	}
	r.AcquireClean = func(_ context.Context, paths []string) (worktreereclaim.RecoveryLease, error) {
		lease, err := g.acquireLease(paths, true)
		return acquireChecked(context.Background(), lease, err)
	}
	w := worktreereclaim.Worktree{ID: "evidence", Path: path, State: worktreereclaim.StateArchived, Fence: "123", BaseBranch: "main",
		Checkouts: []worktreereclaim.Checkout{{RepoPath: project, Rel: "", BaseBranch: "main"}}}
	return r, w
}

// retire plays the server: phase one claims, phase two removes.
func retire(r *worktreereclaim.Reclaimer, w worktreereclaim.Worktree) worktreereclaim.Result {
	got := r.Reconcile(context.Background(), worktreereclaim.Request{Worktrees: []worktreereclaim.Worktree{w}}).Results[0]
	if got.Outcome != worktreereclaim.OutcomeClaimed {
		return got
	}
	w.Remove = true
	return r.Reconcile(context.Background(), worktreereclaim.Request{Worktrees: []worktreereclaim.Worktree{w}}).Results[0]
}

func TestCleanPushedWorktreeRetiresBesideUnrelatedManagedWork(t *testing.T) {
	testkit.Slow(t, "creates a git worktree and inspects every process in /proc")
	g := newTestRecoveryGate()
	g.managedUse = func() error { return errUnrelatedShell } // a dev server runs elsewhere
	done, err := g.begin(nil)                                // and an exec of unknown scope is in flight
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	r, w := evidenceReclaimer(t, g)
	got := retire(r, w)
	if got.Outcome != worktreereclaim.OutcomeRemoved {
		t.Fatalf("clean pushed worktree not retired beside unrelated work: %+v", got)
	}
	if _, err := os.Stat(w.Path); !os.IsNotExist(err) {
		t.Fatalf("directory still exists: %v", err)
	}
	// The machine-wide rule is untouched for everything else.
	if lease, err := g.acquire([]string{t.TempDir()}); err == nil {
		lease.Release()
		t.Fatal("strict admission lost its managed-use veto")
	}
}

func TestCleanPushedWorktreeHeldWhileAProcessUsesIt(t *testing.T) {
	testkit.Slow(t, "creates git worktrees, starts a process and inspects /proc")
	for _, kind := range []string{"cwd", "open-fd"} {
		t.Run(kind, func(t *testing.T) {
			g := newTestRecoveryGate()
			g.managedUse = func() error { return errUnrelatedShell }
			r, w := evidenceReclaimer(t, g)
			switch kind {
			case "cwd":
				cmd := exec.Command("sleep", "60")
				cmd.Dir = w.Path
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
			case "open-fd":
				f, err := os.Open(filepath.Join(w.Path, "a.txt"))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { f.Close() })
			}
			got := retire(r, w)
			if got.Outcome != worktreereclaim.OutcomeHeld || got.Reason != worktreereclaim.ReasonInUse {
				t.Fatalf("a worktree a process uses (%s) was not held in_use: %+v", kind, got)
			}
			if _, err := os.Stat(filepath.Join(w.Path, "a.txt")); err != nil {
				t.Fatalf("worktree touched while in use: %v", err)
			}
		})
	}
}

func TestUncommittedWorktreeHeldWhileAShellIsOpen(t *testing.T) {
	testkit.Slow(t, "creates a git worktree and inspects /proc")
	g := newTestRecoveryGate()
	g.managedUse = func() error { return errors.New("open terminal session has unknown filesystem scope") }
	r, w := evidenceReclaimer(t, g)
	p := filepath.Join(w.Path, "notes.md")
	if err := os.WriteFile(p, []byte("uncommitted\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := retire(r, w); got.Outcome != worktreereclaim.OutcomeHeld || got.Reason != worktreereclaim.ReasonDirty {
		t.Fatalf("uncommitted work was not held dirty: %+v", got)
	}
	if b, err := os.ReadFile(p); err != nil || string(b) != "uncommitted\n" {
		t.Fatalf("uncommitted work touched: %q %v", b, err)
	}
}

// The evidence lease ignores the managed-use veto and unknown-scope work,
// and still excludes, and is excluded by, work whose paths overlap it.
func TestEvidenceLeaseScopesExclusionToItsPaths(t *testing.T) {
	g := newTestRecoveryGate()
	g.managedUse = func() error { return errUnrelatedShell }
	root, other := t.TempDir(), t.TempDir()
	unknown, err := g.begin(nil)
	if err != nil {
		t.Fatal(err)
	}
	overlapping, err := g.begin([]string{filepath.Join(root, "file")})
	if err != nil {
		t.Fatal(err)
	}
	if lease, err := g.acquireEvidence([]string{root}); err == nil {
		lease.Release()
		t.Fatal("evidence lease admitted beside overlapping known work")
	}
	overlapping()
	lease, err := g.acquireEvidence([]string{root})
	if err != nil {
		t.Fatalf("evidence lease refused for unrelated or unknown-scope work: %v", err)
	}
	unknown()
	if done, err := g.begin([]string{filepath.Join(root, "x")}); err == nil {
		done()
		t.Fatal("overlapping work admitted during a clean retirement")
	}
	for _, paths := range [][]string{nil, {other}} {
		done, err := g.begin(paths)
		if err != nil {
			t.Fatalf("work %v refused during an unrelated clean retirement: %v", paths, err)
		}
		done()
	}
	if strict, err := g.acquire([]string{other}); err == nil {
		strict.Release()
		t.Fatal("strict lease lost its managed-use veto")
	}
	lease.Release()
}
