package db

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// checkoutFixture is a project with a checkout recorded on two machines.
type checkoutFixture struct {
	repo            *Repo
	pid             string
	here, elsewhere string
}

func newCheckoutFixture(t *testing.T) checkoutFixture {
	t.Helper()
	repo, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.Background()
	project, err := repo.GetProject(ctx, "test-project")
	require.NoError(t, err)
	f := checkoutFixture{repo: repo, pid: project.ID, here: "d-here", elsewhere: "d-elsewhere"}
	for _, id := range []string{f.here, f.elsewhere} {
		require.NoError(t, repo.UpsertProjectDaemon(ctx, f.pid, id, "/clone", nil))
	}
	return f
}

func (f checkoutFixture) workspace(id, daemonID string) *Worktree {
	at := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	return &Worktree{
		ID: id, Name: id, Path: "/ws/" + id, Branch: "b-" + id, BaseBranch: "main",
		ProjectID: f.pid, Status: 1, DaemonID: &daemonID,
		CreatedAt: at, UpdatedAt: at, LastActive: at,
	}
}

func (f checkoutFixture) countOn(t *testing.T, ctx context.Context, daemonID string) int {
	t.Helper()
	rows, err := f.repo.ListWorktrees(ctx, WorktreeFilters{ProjectID: &f.pid, DaemonID: &daemonID})
	require.NoError(t, err)
	return len(rows)
}

// waitForBlockedLockWaiter returns once a session of this test's database is
// queued on an advisory lock it has not been granted: the create under test
// reached the shared lock and is held back by the exclusive one.
func (f checkoutFixture) waitForBlockedLockWaiter(t *testing.T, created <-chan error) {
	t.Helper()
	ctx := context.Background()
	for {
		var waiting int
		require.NoError(t, f.repo.ORM().Bun().NewRaw(`SELECT count(*) FROM pg_locks l
			JOIN pg_database d ON d.oid = l.database
			WHERE l.locktype = 'advisory' AND NOT l.granted AND d.datname = current_database()`).Scan(ctx, &waiting))
		if waiting > 0 {
			return
		}
		select {
		case err := <-created:
			t.Fatalf("a workspace of the checkout was inserted while its remove held the lock (err %v)", err)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// A remove or reclone runs its workspace guard and its daemon command under
// the checkout's exclusive lock; a branch workspace of that checkout is
// inserted under the shared side. So a create that arrives after the guard
// read "no workspaces" waits until the destroyer is done, instead of landing
// a workspace whose common repository the command then deletes; and one that
// committed before is what the guard reads.
func TestTearDownProjectCheckout_HoldsBackWorkspaceInsertsUntilDone(t *testing.T) {
	t.Parallel()
	f := newCheckoutFixture(t)
	ctx := context.Background()

	// Committed before the destroyer takes the lock: its guard reads it.
	require.NoError(t, f.repo.CreateWorktree(ctx, f.workspace("before", f.elsewhere)))
	require.NoError(t, f.repo.TearDownProjectCheckout(ctx, f.pid, f.elsewhere, func(lockedCtx context.Context) error {
		assert.Equal(t, 1, f.countOn(t, lockedCtx, f.elsewhere), "the guard reads a workspace committed before the lock")
		return nil
	}, func(context.Context) bool { return true }))

	guarded, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- f.repo.TearDownProjectCheckout(ctx, f.pid, f.here, func(lockedCtx context.Context) error {
			if n := f.countOn(t, lockedCtx, f.here); n != 0 {
				t.Errorf("the guard read %d workspaces, want 0", n)
			}
			return nil
		}, func(context.Context) bool {
			close(guarded)
			<-release // the daemon command deleting the checkout runs here
			return true
		})
	}()
	<-guarded

	created := make(chan error, 1)
	go func() { created <- f.repo.CreateWorktree(ctx, f.workspace("during", f.here)) }()
	// A workspace of another machine's checkout does not wait on this one.
	require.NoError(t, f.repo.CreateWorktree(ctx, f.workspace("other-machine", f.elsewhere)))
	// The create is queued on the checkout's lock, not merely slow to start.
	f.waitForBlockedLockWaiter(t, created)
	assert.Equal(t, 0, f.countOn(t, ctx, f.here), "nothing landed between the guard and the delete")

	close(release)
	require.NoError(t, <-done)
	require.NoError(t, <-created, "the create proceeds once the remove is done")
	assert.Equal(t, 1, f.countOn(t, ctx, f.here))
}

// The machine does not stop deleting when the server stops waiting for it. A
// remove whose wait ended without an answer (destroy reports it unsettled: a
// timeout, a cancelled wait) releases the lock, but the checkout stays marked:
// a workspace created afterwards, while the machine is still deleting, and
// after it finished, is refused. Only a remove or reclone run again whose
// outcome is known clears it; the other machine's checkout never waited.
func TestTearDownProjectCheckout_KeepsWorkspacesRefusedWhileTheOutcomeIsUnknown(t *testing.T) {
	t.Parallel()
	f := newCheckoutFixture(t)
	ctx := context.Background()

	stillDeleting, deleted := make(chan struct{}), make(chan struct{})
	require.NoError(t, f.repo.TearDownProjectCheckout(ctx, f.pid, f.here, nil, func(context.Context) bool {
		// The request budget ran out; the machine's delete runs on.
		go func() {
			<-stillDeleting
			close(deleted)
		}()
		return false
	}))

	err := f.repo.CreateWorktree(ctx, f.workspace("while-deleting", f.here))
	require.ErrorIs(t, err, core.ErrCheckoutTeardownPending, "admitted while the machine may still be deleting")
	close(stillDeleting)
	<-deleted
	err = f.repo.CreateWorktree(ctx, f.workspace("after-deleting", f.here))
	require.ErrorIs(t, err, core.ErrCheckoutTeardownPending, "nothing has told the server the delete is over")
	assert.Equal(t, 0, f.countOn(t, ctx, f.here))
	require.NoError(t, f.repo.CreateWorktree(ctx, f.workspace("other-machine", f.elsewhere)))

	// A second unsettled attempt keeps the mark; a settled one clears it.
	require.NoError(t, f.repo.TearDownProjectCheckout(ctx, f.pid, f.here, nil, func(context.Context) bool { return false }))
	require.ErrorIs(t, f.repo.CreateWorktree(ctx, f.workspace("still-unknown", f.here)), core.ErrCheckoutTeardownPending)
	require.NoError(t, f.repo.TearDownProjectCheckout(ctx, f.pid, f.here, nil, func(context.Context) bool { return true }))
	require.NoError(t, f.repo.CreateWorktree(ctx, f.workspace("settled", f.here)))
	assert.Equal(t, 1, f.countOn(t, ctx, f.here))
}

// A guard that refuses sends nothing and marks nothing: the checkout's
// workspaces are not held back by a remove that never ran.
func TestTearDownProjectCheckout_ARefusedGuardMarksNothing(t *testing.T) {
	t.Parallel()
	f := newCheckoutFixture(t)
	ctx := context.Background()
	refused := core.ErrCheckoutTeardownPending // any error
	err := f.repo.TearDownProjectCheckout(ctx, f.pid, f.here, func(context.Context) error { return refused },
		func(context.Context) bool {
			t.Error("destroy ran after its guard refused")
			return false
		})
	require.ErrorIs(t, err, refused)
	require.NoError(t, f.repo.CreateWorktree(ctx, f.workspace("ws", f.here)))
}

// The caller going away does not cut the wait for the machine short, nor
// abandon the mark: destroy runs on a context that is not cancelled with the
// caller's.
func TestTearDownProjectCheckout_OutlivesTheCallersCancellation(t *testing.T) {
	t.Parallel()
	f := newCheckoutFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, f.repo.TearDownProjectCheckout(ctx, f.pid, f.here, nil, func(lockedCtx context.Context) bool {
		cancel()
		assert.NoError(t, lockedCtx.Err(), "the wait for the machine is not cancelled with the caller")
		return false
	}))
	require.ErrorIs(t, f.repo.CreateWorktree(context.Background(), f.workspace("ws", f.here)), core.ErrCheckoutTeardownPending)
}
