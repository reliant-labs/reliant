package daemonruntime

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

// Keep the real admission gate, but inject process inspection through
// Reclaimer.ProcessUse rather than sampling unrelated processes on the host.
// Production WorktreeRecoveryLease.Check's uncertainty paths are tested in
// worktree_activity_test.go and worktreereclaim/process_use_linux_test.go.
type admissionTestLease struct{ *WorktreeRecoveryLease }

func (l admissionTestLease) Check(ctx context.Context) error { return ctx.Err() }

func TestLandedStripExcludesNewCommandThroughDeletion(t *testing.T) {
	testkit.Slow(t, "creates a temporary git worktree and probes landed status")
	t.Parallel()
	base := t.TempDir()
	project, root := filepath.Join(base, "project"), filepath.Join(base, "worktrees")
	newRepo(t, project)
	if err := os.WriteFile(filepath.Join(project, ".gitignore"), []byte("node_modules/\n"), 0600); err != nil {
		t.Fatal(err)
	}
	rcGit(t, project, "add", ".gitignore")
	rcGit(t, project, "commit", "-qm", "ignore output")
	rcGit(t, project, "update-ref", "refs/remotes/origin/main", "HEAD")
	path := filepath.Join(root, "workspace")
	rcGit(t, project, "worktree", "add", "-b", "feature", path)
	if err := worktreereclaim.LockCheckout(context.Background(), path, "test-landed"); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(path, "node_modules", "keep-until-strip")
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("rebuildable"), 0600); err != nil {
		t.Fatal(err)
	}
	g := newTestRecoveryGate()
	r := &worktreereclaim.Reclaimer{Root: root, Now: func() time.Time { return time.Now().Add(48 * time.Hour) }, Cwds: func(context.Context) ([]string, error) { return nil, nil }}
	r.Acquire = func(_ context.Context, paths []string) (worktreereclaim.RecoveryLease, error) {
		lease, err := g.acquire(paths)
		if err != nil {
			return nil, err
		}
		return admissionTestLease{lease}, nil
	}
	checks := 0
	r.ProcessUse = func(context.Context, []string) error {
		checks++
		// Attempt new command admission after eligibility, after parking and
		// immediately before every descriptor-relative unlink.

		if done, err := g.begin(nil); err == nil {
			done()
			t.Fatal("new command admitted during stripping")
		}
		return nil
	}
	w := worktreereclaim.Worktree{ID: "test-landed", Path: path, State: worktreereclaim.StateActive, ProbeLanded: true, ShedLanded: true, Checkouts: []worktreereclaim.Checkout{{RepoPath: project, Rel: "", BaseBranch: "main"}}}
	// Ordinary reconcile must not self-admit as unknown tool work.
	if !recoveryLifecycleCommand("worktree.reconcile") {
		t.Fatal("reconcile would veto its own lease")
	}
	result := r.Reconcile(context.Background(), worktreereclaim.Request{Worktrees: []worktreereclaim.Worktree{w}}).Results[0]
	if !result.Landed {
		t.Fatalf("landed proof: %+v", result)
	}
	w.State, w.Fence, w.ProbeLanded, w.ShedLanded = worktreereclaim.StateArchived, "123", false, false
	result = r.Reconcile(context.Background(), worktreereclaim.Request{Worktrees: []worktreereclaim.Worktree{w}}).Results[0]
	if result.Outcome != worktreereclaim.OutcomeClaimed {
		t.Fatalf("archive claim: %+v", result)
	}
	w.Remove = true
	result = r.Reconcile(context.Background(), worktreereclaim.Request{Worktrees: []worktreereclaim.Worktree{w}}).Results[0]
	if checks < 2 || result.Outcome != worktreereclaim.OutcomeRemoved {
		t.Fatalf("whole retirement was not fenced through deletion: checks=%d result=%+v", checks, result)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("temporary rebuildable fixture should be removed", err)
	}
	done, err := g.begin(nil)
	if err != nil {
		t.Fatal("command admission not released after strip", err)
	}
	done()
}

func TestEnsureDeferredRetirementNeverReportsPresent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	target := filepath.Join(root, "preserve")
	if err := os.WriteFile(target, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, outcome := range []worktreereclaim.Outcome{worktreereclaim.OutcomeDeferred, worktreereclaim.OutcomeHeld, worktreereclaim.OutcomeError} {
		e := newWorkspaceEnsurer()
		calls := 0
		e.retire = func(context.Context, worktreereclaim.Worktree) worktreereclaim.Result {
			calls++
			return worktreereclaim.Result{Outcome: outcome}
		}
		req := toolexec.WorkspaceEnsureRequest{Path: root, WorktreeID: "retiring", Retire: "123"}
		for i := 0; i < 2; i++ {
			got := e.ensure(context.Background(), req)
			if got.Status != toolexec.WorkspaceBlocked || got.Path != "" {
				t.Fatalf("admitted before retirement acknowledgement: %+v", got)
			}
		}
		if calls != 2 {
			t.Fatal("retirement must be retried by every batch")
		}
		if _, err := os.Stat(target); err != nil {
			t.Fatal(err)
		}
		e.retire = func(context.Context, worktreereclaim.Worktree) worktreereclaim.Result {
			return worktreereclaim.Result{Outcome: worktreereclaim.OutcomeLocked}
		}
		if got := e.ensure(context.Background(), req); got.Status != toolexec.WorkspacePresent {
			t.Fatalf("positive retirement did not unblock: %+v", got)
		}
	}
}

func TestEnsureOldRemoverPathLockMustRetireBeforeRevival(t *testing.T) {
	testkit.Slow(t, "creates temporary git worktree and serializes old remover with retirement")
	t.Parallel()
	f := newEnsureFixture(t, "")
	r := &worktreereclaim.Reclaimer{Root: filepath.Dir(f.root), Cwds: func(context.Context) ([]string, error) { return nil, nil }}
	w := worktreereclaim.Worktree{ID: f.worktreeID, Path: f.root, State: worktreereclaim.StateArchived, Fence: "123", Checkouts: []worktreereclaim.Checkout{{RepoPath: f.project, BaseBranch: "main"}}}
	claim := r.Reconcile(context.Background(), worktreereclaim.Request{Worktrees: []worktreereclaim.Worktree{w}}).Results[0]
	if claim.Outcome != worktreereclaim.OutcomeClaimed {
		t.Fatalf("claim: %+v", claim)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	r.Cwds = func(context.Context) ([]string, error) { close(entered); <-release; return []string{f.root}, nil }
	w.Remove = true
	oldDone := make(chan worktreereclaim.Result, 1)
	go func() {
		oldDone <- r.Reconcile(context.Background(), worktreereclaim.Request{Worktrees: []worktreereclaim.Worktree{w}}).Results[0]
	}()
	<-entered // old remover owns the path lock, not a timing assumption
	e := newWorkspaceEnsurer()
	e.retire = func(ctx context.Context, active worktreereclaim.Worktree) worktreereclaim.Result {
		return r.Reconcile(ctx, worktreereclaim.Request{Worktrees: []worktreereclaim.Worktree{active}}).Results[0]
	}
	req := f.request("thread")
	req.Retire = w.Fence
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // retirement budget expires while the older remover still owns it
	for i := 0; i < 2; i++ {
		if got := e.ensure(ctx, req); got.Status != toolexec.WorkspaceBlocked {
			t.Fatalf("present directory was admitted past old remover: %+v", got)
		}
	}
	close(release)
	if got := <-oldDone; got.Outcome != worktreereclaim.OutcomeHeld {
		t.Fatalf("old remover should preserve cwd user: %+v", got)
	}
	r.Cwds = func(context.Context) ([]string, error) { return nil, nil }
	if got := e.ensure(context.Background(), req); got.Status != toolexec.WorkspacePresent {
		t.Fatalf("retirement: %+v", got)
	}
	// Once acknowledgement unlocks tools, an already issued old request can
	// no longer claim or remove the revived checkout.
	if got := r.Reconcile(context.Background(), worktreereclaim.Request{Worktrees: []worktreereclaim.Worktree{w}}).Results[0]; got.Outcome != worktreereclaim.OutcomeHeld || got.Reason != worktreereclaim.ReasonStale {
		t.Fatalf("old remover survived retirement: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(f.root, "a.txt")); err != nil {
		t.Fatal("revived workspace was removed", err)
	}
}

func TestEnsureRecreatedWorkspaceRequiresRestoredLockAcknowledgement(t *testing.T) {
	testkit.Slow(t, "recreates temporary git checkout and verifies restored lock acknowledgement")
	t.Parallel()
	f := newEnsureFixture(t, "")
	if err := os.RemoveAll(f.root); err != nil {
		t.Fatal(err)
	}
	e := newWorkspaceEnsurer()
	calls := 0
	e.retire = func(context.Context, worktreereclaim.Worktree) worktreereclaim.Result {
		calls++
		if calls == 1 {
			return worktreereclaim.Result{Outcome: worktreereclaim.OutcomeGone}
		}
		return worktreereclaim.Result{Outcome: worktreereclaim.OutcomeDeferred}
	}
	req := f.request("thread")
	req.Retire = "123"
	got := e.ensure(context.Background(), req)
	if got.Status != toolexec.WorkspaceBlocked || calls != 2 {
		t.Fatalf("recreation bypassed positive restored-lock check: calls=%d got=%+v", calls, got)
	}
	if _, err := os.Stat(filepath.Join(f.root, "a.txt")); err != nil {
		t.Fatal("fixture was not recreated", err)
	}
}

func TestLandedAdmissionRefusesExistingUnknownWork(t *testing.T) {
	g := newTestRecoveryGate()
	done, err := g.begin(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	if lease, err := g.acquire([]string{t.TempDir()}); err == nil {
		lease.Release()
		t.Fatal("existing unknown work must prevent cleanup")
	}
}
