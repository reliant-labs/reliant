// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/require"
)

// Prod, 2026-11-03..06, one saturated cloud machine: worktree.create took
// 20-135s against its 2m budget. A create that answered after the server gave
// up, or whose connection dropped under it, left a checkout nobody owned,
// holding its branch: the user's retry failed with "branch is already checked
// out at <that checkout>". A `git worktree add` killed at the deadline left a
// registration locked "initializing" that held the branch the same way.

func guardRepoBranches(t *testing.T, projectPath string) string {
	t.Helper()
	return gitOutIn(t, projectPath, "branch", "--list", "--format=%(refname:short)")
}

func guardWorktrees(t *testing.T, projectPath string) string {
	t.Helper()
	return gitOutIn(t, projectPath, "worktree", "list", "--porcelain")
}

// The connection dropped while the first attempt ran, so the router sends the
// same request again. The second attempt must answer with the checkout the
// first made, not fail on the branch the first one holds.
func TestWorktreeCreate_ResentRequestAnswersWithTheFirstAttemptsCheckout(t *testing.T) {
	testkit.Slow(t, "creates real git checkouts and worktrees on disk")
	t.Parallel()
	projectPath := diagnoseRepo(t)
	workspaceID := uniqueWorkspaceID(t, "ws-resend")
	req := map[string]any{
		"project_path": projectPath,
		"workspace_id": workspaceID,
		"name":         "resend",
		"branch":       "feat/resend",
		"base_branch":  "main",
		"worktree_id":  "wt-resend-1",
	}

	first := createFor(t, req)
	require.True(t, first.Success, "first attempt: %s", first.Error)
	head := gitOutIn(t, testWorktreePath(t, workspaceID), "rev-parse", "HEAD")

	again := createFor(t, req)
	require.True(t, again.Success, "the re-sent request must answer with the first attempt's checkout: %s", again.Error)
	require.Equal(t, head, gitOutIn(t, testWorktreePath(t, workspaceID), "rev-parse", "HEAD"))
	require.Equal(t, 1, strings.Count(guardWorktrees(t, projectPath), "branch refs/heads/feat/resend"))

	// A DIFFERENT worktree asking for the same checkout path is not a
	// re-send: it is refused, never handed someone else's checkout.
	req["worktree_id"] = "wt-resend-2"
	require.False(t, createFor(t, req).Success)
}

// `git worktree add` failing after it populated the checkout (here a
// post-checkout hook; in prod, the deadline's SIGKILL) must take back the
// checkout, its registration and the branch it made.
func TestWorktreeCreate_FailedAddTakesBackItsCheckoutAndBranch(t *testing.T) {
	testkit.Slow(t, "creates real git checkouts and worktrees on disk")
	t.Parallel()
	projectPath := diagnoseRepo(t)
	hooks := filepath.Join(t.TempDir(), "hooks")
	require.NoError(t, os.MkdirAll(hooks, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(hooks, "post-checkout"), []byte("#!/bin/sh\nexit 1\n"), 0o755))
	gitOutIn(t, projectPath, "config", "core.hooksPath", hooks)

	workspaceID := uniqueWorkspaceID(t, "ws-failed-add")
	resp := createFor(t, map[string]any{
		"project_path": projectPath,
		"workspace_id": workspaceID,
		"sub_path":     "repo",
		"name":         "failed",
		"branch":       "feat/failed-add",
		"base_branch":  "main",
		"worktree_id":  "wt-failed-add",
	})
	require.False(t, resp.Success, "the failing post-checkout hook must fail the create")

	_, err := os.Stat(testWorktreePath(t, workspaceID))
	require.True(t, os.IsNotExist(err), "the checkout and the workspace root it made must be gone: %v", err)
	require.NotContains(t, guardWorktrees(t, projectPath), "feat/failed-add", "registration left behind")
	require.NotContains(t, guardRepoBranches(t, projectPath), "feat/failed-add", "branch left behind")
}

// A checkout that predates the call is never taken back, nor a branch the
// call did not make.
func TestAbandonCheckout_KeepsABranchThatMoved(t *testing.T) {
	testkit.Slow(t, "creates real git checkouts and worktrees on disk")
	t.Parallel()
	projectPath := diagnoseRepo(t)
	checkout := filepath.Join(t.TempDir(), "wt")
	gitOutIn(t, projectPath, "worktree", "add", "-q", "-b", "feat/moved", checkout, "main")
	require.NoError(t, os.WriteFile(filepath.Join(checkout, "g.txt"), []byte("x\n"), 0o644))
	gitOutIn(t, checkout, "add", "g.txt")
	gitOutIn(t, checkout, "-c", "user.name=t", "-c", "user.email=t@e.com", "commit", "-q", "-m", "work")

	abandonCheckout(context.Background(), projectPath, checkout, "feat/moved", "refs/heads/main")

	require.NotContains(t, guardWorktrees(t, projectPath), checkout)
	require.Contains(t, guardRepoBranches(t, projectPath), "feat/moved", "a branch with commits past the start point is someone's work")
}

// What SIGKILL leaves mid-checkout: a registration still locked
// "initializing". It is unlocked, removed, and the branch freed.
func TestAbandonCheckout_RemovesAKilledAddsRegistration(t *testing.T) {
	testkit.Slow(t, "creates real git checkouts and worktrees on disk")
	t.Parallel()
	projectPath := diagnoseRepo(t)
	checkout := filepath.Join(t.TempDir(), "killed")
	gitOutIn(t, projectPath, "worktree", "add", "-q", "-b", "feat/killed", checkout, "main")
	admin := registrationOf(context.Background(), projectPath, checkout)
	require.NotEmpty(t, admin)
	require.NoError(t, os.WriteFile(filepath.Join(admin, "locked"), []byte("initializing\n"), 0o644))

	abandonCheckout(context.Background(), projectPath, checkout, "feat/killed", "refs/heads/main")

	_, err := os.Stat(admin)
	require.True(t, os.IsNotExist(err), "registration left: %v", err)
	require.NotContains(t, guardRepoBranches(t, projectPath), "feat/killed")
	// The branch is free for the user's retry.
	gitOutIn(t, projectPath, "worktree", "add", "-q", "-b", "feat/killed", filepath.Join(t.TempDir(), "retry"), "main")
}

// The checkout path is built from the request and a failed create removes it
// whole, so a workspace_id or sub_path that leaves the workspace's directory
// is refused before anything on disk is touched.
func TestWorktreeCreate_RefusesAPathOutsideTheWorkspace(t *testing.T) {
	t.Parallel()
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	victim := filepath.Join(home, "victim-"+uniqueWorkspaceID(t, "x"))
	require.NoError(t, os.MkdirAll(victim, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(victim, "keep.txt"), []byte("x\n"), 0o644))
	rel, err := filepath.Rel(filepath.Join(home, ".reliant", "worktrees", "ws"), victim)
	require.NoError(t, err)

	cases := []struct{ workspaceID, subPath string }{
		{"ws", rel},
		{"ws", victim},
		{"ws", "repo/../../other"},
		{"..", ""},
		{".", "repo"},
		{"ws/../..", ""},
		{"proj/..", "repo"},
		{"", "repo"},
		{filepath.Dir(victim), ""},
	}
	for _, c := range cases {
		_, err := worktreeCreatePath(c.workspaceID, c.subPath)
		require.Error(t, err, "workspace_id=%q sub_path=%q", c.workspaceID, c.subPath)

		resp := createFor(t, map[string]any{
			"project_path": t.TempDir(),
			"workspace_id": c.workspaceID,
			"sub_path":     c.subPath,
			"name":         "escape",
			"branch":       "feat/escape",
			"worktree_id":  "wt-escape",
		})
		require.False(t, resp.Success, "workspace_id=%q sub_path=%q", c.workspaceID, c.subPath)
		_, err = os.Stat(filepath.Join(victim, "keep.txt"))
		require.NoError(t, err, "a refused create touched %s", victim)
	}

	for _, ok := range []struct{ workspaceID, subPath string }{{"ws", ""}, {"ws", "repo"}, {"ws", "nested/repo"}, {"ws", "."}, {"ws/wt-1", "repo"}} {
		p, err := worktreeCreatePath(ok.workspaceID, ok.subPath)
		require.NoError(t, err, "workspace_id=%q sub_path=%q", ok.workspaceID, ok.subPath)
		require.True(t, strings.HasPrefix(p, filepath.Join(home, ".reliant", "worktrees", "ws")), p)
	}
}

// Attempts at one checkout path run one at a time; a waiter whose deadline
// passes gives up rather than blocking past it.
func TestLockCreatePath_SerializesAndHonorsTheDeadline(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "p")
	unlock, err := lockCreatePath(context.Background(), path)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = lockCreatePath(ctx, path)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	unlock()
	unlock2, err := lockCreatePath(context.Background(), path)
	require.NoError(t, err)
	unlock2()
}
