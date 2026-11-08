package db

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A remove or reclone runs its workspace guard and its daemon command under
// the checkout's exclusive lock; a branch workspace of that checkout is
// inserted under the shared side. So a create that arrives after the guard
// read "no workspaces" waits until the destroyer is done, instead of landing
// a workspace whose common repository the command then deletes; and one that
// committed before is what the guard reads.
func TestWithProjectCheckoutExclusive_HoldsBackWorkspaceInsertsUntilDone(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	project, err := repo.GetProject(ctx, "test-project")
	require.NoError(t, err)
	pid := project.ID
	here, elsewhere := "d-here", "d-elsewhere"
	now := time.Now().UTC()
	workspace := func(id, daemonID string) *Worktree {
		return &Worktree{
			ID: id, Name: id, Path: "/ws/" + id, Branch: "b-" + id, BaseBranch: "main",
			ProjectID: pid, Status: 1, DaemonID: &daemonID,
			CreatedAt: now, UpdatedAt: now, LastActive: now,
		}
	}
	countOn := func(ctx context.Context, daemonID string) int {
		rows, err := repo.ListWorktrees(ctx, WorktreeFilters{ProjectID: &pid, DaemonID: &daemonID})
		require.NoError(t, err)
		return len(rows)
	}

	// Committed before the destroyer takes the lock: its guard reads it.
	require.NoError(t, repo.CreateWorktree(ctx, workspace("before", elsewhere)))
	require.NoError(t, repo.WithProjectCheckoutExclusive(ctx, pid, elsewhere, func(lockedCtx context.Context) error {
		assert.Equal(t, 1, countOn(lockedCtx, elsewhere), "the guard reads a workspace committed before the lock")
		return nil
	}))

	guarded, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- repo.WithProjectCheckoutExclusive(ctx, pid, here, func(lockedCtx context.Context) error {
			if n := countOn(lockedCtx, here); n != 0 {
				t.Errorf("the guard read %d workspaces, want 0", n)
			}
			close(guarded)
			<-release // the daemon command deleting the checkout runs here
			return nil
		})
	}()
	<-guarded

	created := make(chan error, 1)
	go func() { created <- repo.CreateWorktree(ctx, workspace("during", here)) }()
	// A workspace of another machine's checkout does not wait on this one.
	require.NoError(t, repo.CreateWorktree(ctx, workspace("other-machine", elsewhere)))
	select {
	case err := <-created:
		t.Fatalf("a workspace of the checkout was inserted while its remove held the lock (err %v)", err)
	case <-time.After(300 * time.Millisecond):
	}
	assert.Equal(t, 0, countOn(ctx, here), "nothing landed between the guard and the delete")

	close(release)
	require.NoError(t, <-done)
	require.NoError(t, <-created, "the create proceeds once the remove is done")
	assert.Equal(t, 1, countOn(ctx, here))
}
