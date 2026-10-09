// Copyright (c) 2025 Reliant Labs
package services

import (
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// The user's way out of a chat stuck on "working directory does not exist":
// RecreateWorktree on an ACTIVE worktree whose directory vanished rebuilds it
// in place from its branch, on the machine that owns it, through the daemon's
// real handler and real git.
func TestRecreateWorktree_RepairsAnActiveWorktreeWhoseDirectoryVanished(t *testing.T) {
	e := newReclaimEnv(t)
	wt, path := e.worktree(t, "vanished")
	require.NoError(t, os.RemoveAll(path))

	resp, err := e.svc.RecreateWorktree(e.ctx, connect.NewRequest(&reliantv1.RecreateWorktreeRequest{WorktreeId: wt.ID}))
	require.NoError(t, err)
	assert.Contains(t, resp.Msg.Message, "recreated from branch feat/vanished")
	assert.Equal(t, "feat/vanished", gitIn(t, path, "rev-parse", "--abbrev-ref", "HEAD"))
	assert.Contains(t, e.router.calls, toolexec.WorkspaceEnsureCommand)

	// The row is untouched: still active, same path. The chat keeps its binding.
	after, err := e.repo.GetWorktree(e.ctx, wt.ID)
	require.NoError(t, err)
	assert.Nil(t, after.DeletedAt)
	assert.Equal(t, path, after.Path)
}

// Pressing the button on a healthy workspace is harmless.
func TestRecreateWorktree_ActiveWorktreeThatIsPresentIsANoOp(t *testing.T) {
	e := newReclaimEnv(t)
	wt, path := e.worktree(t, "healthy")
	require.NoError(t, os.WriteFile(filepath.Join(path, "dirty.txt"), []byte("keep"), 0o644))

	resp, err := e.svc.RecreateWorktree(e.ctx, connect.NewRequest(&reliantv1.RecreateWorktreeRequest{WorktreeId: wt.ID}))
	require.NoError(t, err)
	assert.Contains(t, resp.Msg.Message, "nothing needed recreating")
	b, err := os.ReadFile(filepath.Join(path, "dirty.txt"))
	require.NoError(t, err)
	assert.Equal(t, "keep", string(b))
}

// Nothing to rebuild from: the user is told why, and nothing is moved.
func TestRecreateWorktree_ReportsWhyAnActiveWorktreeCannotBeRebuilt(t *testing.T) {
	e := newReclaimEnv(t)
	wt, path := e.worktree(t, "unrecoverable")
	require.NoError(t, os.RemoveAll(path))
	gitIn(t, e.project.Path, "worktree", "remove", "--force", "--force", path)
	gitIn(t, e.project.Path, "branch", "-D", "feat/unrecoverable")

	_, err := e.svc.RecreateWorktree(e.ctx, connect.NewRequest(&reliantv1.RecreateWorktreeRequest{WorktreeId: wt.ID}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "no longer exists")
}
