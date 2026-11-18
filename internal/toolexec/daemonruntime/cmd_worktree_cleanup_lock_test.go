package daemonruntime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

// Cleanup never removes a checkout an add may be making (obs feeb22c1).
//
// force_cleanup and delete_directory remove a checkout's directory and its
// registration. Before the fix they did so first and only then waited for
// the repository's add lock (at the prune), so a cleanup that overlapped a
// `git worktree add` deleted its destination mid-checkout. They now take the
// add lock, exclusive, before the first step and hold it through the prune.

// cleanupLockFixture is a repository, a checkout directory to clean up, and a
// registration whose directory is gone, which a prune removes.
type cleanupLockFixture struct {
	repo, dest, staleAdmin string
}

func newCleanupLockFixture(t *testing.T) cleanupLockFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	f := cleanupLockFixture{repo: filepath.Join(root, "repo"), dest: filepath.Join(root, "dest")}
	initRepo(t, f.repo)
	gone := filepath.Join(root, "gone")
	gitIn(t, f.repo, "worktree", "add", "-q", "-b", "gone", gone)
	require.NoError(t, os.RemoveAll(gone))
	f.staleAdmin = filepath.Join(f.repo, ".git", "worktrees", "gone")
	require.DirExists(t, f.staleAdmin)
	// What an add has written so far into its destination.
	require.NoError(t, os.MkdirAll(f.dest, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(f.dest, "README"), []byte("checking out"), 0o644))
	return f
}

func forceCleanupPayload(t *testing.T, f cleanupLockFixture) []byte {
	t.Helper()
	p, err := json.Marshal(worktreeForceCleanupRequest{ProjectPath: f.repo, WorktreePath: f.dest})
	require.NoError(t, err)
	return p
}

func TestWorktreeForceCleanup_WaitsForAnAddBeforeTouchingTheCheckout(t *testing.T) {
	testkit.Slow(t, "builds a real git repository and runs git against it")
	t.Parallel()
	f := newCleanupLockFixture(t)
	releaseAdd, err := worktreereclaim.LockWorktreeAdd(context.Background(), f.repo)
	require.NoError(t, err)

	// A cleanup whose wait ends while the add still runs touches nothing.
	gaveUp, cancel := context.WithCancel(context.Background())
	cancel()
	raw, err := handleWorktreeForceCleanup(gaveUp, forceCleanupPayload(t, f))
	require.NoError(t, err)
	var resp worktreeForceCleanupResponse
	require.NoError(t, json.Unmarshal(raw, &resp))
	require.FileExists(t, filepath.Join(f.dest, "README"), "cleanup removed a checkout while an add held the lock")
	require.DirExists(t, f.staleAdmin, "cleanup pruned while an add held the lock")
	require.False(t, resp.Success, "a cleanup that never got the add lock reported success")

	// A cleanup that waits proceeds once the add is over.
	payload := forceCleanupPayload(t, f)
	done := make(chan []byte, 1)
	go func() {
		raw, _ := handleWorktreeForceCleanup(context.Background(), payload)
		done <- raw
	}()
	require.FileExists(t, filepath.Join(f.dest, "README"), "cleanup removed a checkout while an add held the lock")
	releaseAdd()
	select {
	case raw = <-done:
	case <-time.After(time.Minute):
		t.Fatal("force cleanup never finished after the add released its lock")
	}
	require.NoError(t, json.Unmarshal(raw, &resp))
	require.True(t, resp.Success)
	require.NoDirExists(t, f.dest)
}

// The cleanup holds the add lock across its prune: the prune must not try to
// take it again, or it waits for itself until ctx ends and never prunes.
func TestWorktreeForceCleanup_PrunesUnderTheLockItHolds(t *testing.T) {
	testkit.Slow(t, "builds a real git repository and runs git against it")
	t.Parallel()
	f := newCleanupLockFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	raw, err := handleWorktreeForceCleanup(ctx, forceCleanupPayload(t, f))
	require.NoError(t, err)
	require.NoError(t, ctx.Err(), "force cleanup waited for its own lock")
	var resp worktreeForceCleanupResponse
	require.NoError(t, json.Unmarshal(raw, &resp))
	require.True(t, resp.Success)
	require.NoDirExists(t, f.dest)
	_, statErr := os.Stat(f.staleAdmin)
	require.True(t, errors.Is(statErr, os.ErrNotExist), "the stale registration was not pruned: %v", statErr)
}

func TestWorktreeDeleteDirectory_WaitsForAnAddBeforeTouchingTheCheckout(t *testing.T) {
	testkit.Slow(t, "builds a real git repository and runs git against it")
	t.Parallel()
	f := newCleanupLockFixture(t)
	releaseAdd, err := worktreereclaim.LockWorktreeAdd(context.Background(), f.repo)
	require.NoError(t, err)
	payload, err := json.Marshal(worktreeDeleteDirectoryRequest{ProjectPath: f.repo, WorktreePath: f.dest})
	require.NoError(t, err)

	gaveUp, cancel := context.WithCancel(context.Background())
	cancel()
	raw, err := handleWorktreeDeleteDirectory(gaveUp, payload)
	require.NoError(t, err)
	var resp worktreeDeleteDirectoryResponse
	require.NoError(t, json.Unmarshal(raw, &resp))
	require.FileExists(t, filepath.Join(f.dest, "README"), "delete removed a checkout while an add held the lock")
	require.False(t, resp.Deleted, "a delete that never got the add lock reported success")

	releaseAdd()
	raw, err = handleWorktreeDeleteDirectory(context.Background(), payload)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &resp))
	require.True(t, resp.Deleted)
	require.NoDirExists(t, f.dest)
}
