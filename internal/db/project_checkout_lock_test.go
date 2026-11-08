package db

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
		// A detach records a detachment, which names a real machine.
		require.NoError(t, repo.UpsertDaemon(ctx, &Daemon{ID: id, UserID: project.UserID}))
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
			t.Fatalf("a workspace of the checkout was inserted while its detach held the lock (err %v)", err)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// A detach runs its workspace guard under the checkout's exclusive lock; a
// branch workspace of that checkout is inserted under the shared side. So a
// create that arrives after the guard read "no workspaces" waits until the
// detach has committed, instead of landing a workspace of a checkout the
// detach then forgets; and one that committed before is what the guard reads.
func TestDetachProjectFromDaemon_HoldsBackWorkspaceInsertsUntilDone(t *testing.T) {
	t.Parallel()
	f := newCheckoutFixture(t)
	ctx := context.Background()

	// Committed before the detach takes the lock: its guard reads it.
	require.NoError(t, f.repo.CreateWorktree(ctx, f.workspace("before", f.elsewhere)))
	require.NoError(t, f.repo.DetachProjectFromDaemon(ctx, f.pid, f.elsewhere, "/clone", func(lockedCtx context.Context) error {
		assert.Equal(t, 1, f.countOn(t, lockedCtx, f.elsewhere), "the guard reads a workspace committed before the lock")
		return ErrNothingToDetach
	}))

	guarded, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- f.repo.DetachProjectFromDaemon(ctx, f.pid, f.here, "/clone", func(lockedCtx context.Context) error {
			if n := f.countOn(t, lockedCtx, f.here); n != 0 {
				t.Errorf("the guard read %d workspaces, want 0", n)
			}
			close(guarded)
			<-release // the rest of the detach's transaction runs after this
			return nil
		})
	}()
	<-guarded

	created := make(chan error, 1)
	go func() { created <- f.repo.CreateWorktree(ctx, f.workspace("during", f.here)) }()
	// A workspace of another machine's checkout does not wait on this one.
	require.NoError(t, f.repo.CreateWorktree(ctx, f.workspace("other-machine", f.elsewhere)))
	// The create is queued on the checkout's lock, not merely slow to start.
	f.waitForBlockedLockWaiter(t, created)
	assert.Equal(t, 0, f.countOn(t, ctx, f.here), "nothing landed between the guard and the detach's commit")

	close(release)
	require.NoError(t, <-done)
	require.NoError(t, <-created, "the create proceeds once the detach is done")
	assert.Equal(t, 1, f.countOn(t, ctx, f.here))
}
