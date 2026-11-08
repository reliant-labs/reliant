package daemonruntime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/require"
)

func TestWorktreeRefreshRefusesMovedHeadBeforeCheckout(t *testing.T) {
	if testing.Short() {
		t.Skip("real git origin, fetch and worktree; runs in full package tests")
	}
	r := newBaseRepos(t)
	old := r.run(r.clone, "rev-parse", "refs/remotes/origin/feat/x")
	// Origin moves; the clone still has old tracking/local information.
	r.run(r.clone, "checkout", "-b", "feat/x", old)
	r.run(r.clone, "commit", "--allow-empty", "-m", "new head")
	r.run(r.clone, "push", "origin", "feat/x")
	payload, err := json.Marshal(worktreeCreateRequest{
		ProjectPath: r.clone, WorkspaceID: "drift-refused", SubPath: "target",
		Branch: "worktree/review", BaseBranch: "feat/x", FetchBranch: "feat/x", ExpectedHead: old,
	})
	require.NoError(t, err)
	raw, err := handleWorktreeCreate(context.Background(), payload)
	require.NoError(t, err)
	var out worktreeCreateResponse
	require.NoError(t, json.Unmarshal(raw, &out))
	require.False(t, out.Success)
	require.Contains(t, out.Error, "head moved")
	require.False(t, refExists(r.clone, "refs/heads/worktree/review"))
}

func TestWorktreeRefreshUsesFetchedHeadNotStaleLocalBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("real git origin, fetch and worktree; runs in full package tests")
	}
	r := newBaseRepos(t)
	old := r.run(r.clone, "rev-parse", "refs/remotes/origin/main")
	head := r.run(r.clone, "rev-parse", "refs/remotes/origin/feat/x")
	r.run(r.clone, "branch", "feat/x", old) // local branch is stale, origin is authoritative
	payload, err := json.Marshal(worktreeCreateRequest{
		ProjectPath: r.clone, WorkspaceID: "fresh-target", SubPath: "target",
		Branch: "worktree/review", BaseBranch: "feat/x", FetchBranch: "feat/x", ExpectedHead: head,
	})
	require.NoError(t, err)
	raw, err := handleWorktreeCreate(context.Background(), payload)
	require.NoError(t, err)
	var out worktreeCreateResponse
	require.NoError(t, json.Unmarshal(raw, &out))
	require.True(t, out.Success, out.Error)
	require.Equal(t, head, r.run(out.WorktreePath, "rev-parse", "HEAD"))
}

// A checkout that lands on the wrong head (here a post-checkout hook commits in
// the new worktree, so HEAD moves after `git worktree add` passed the pre-add
// check) is refused AND taken back. Left behind, its registration would hold
// the branch at that path, so a retry could not add it again: the checkout was
// stranded for good.
func TestWorktreeCreateTakesBackACheckoutOnTheWrongHead(t *testing.T) {
	testkit.Slow(t, "real git origin, fetch, hooks and worktree")
	r := newBaseRepos(t)
	head := r.run(r.clone, "rev-parse", "refs/remotes/origin/feat/x")
	hook := filepath.Join(r.clone, ".git", "hooks", "post-checkout")
	require.NoError(t, os.MkdirAll(filepath.Dir(hook), 0o755))
	require.NoError(t, os.WriteFile(hook, []byte("#!/bin/sh\n"+
		"[ \"$(git rev-parse --git-dir)\" = \"$(git rev-parse --git-common-dir)\" ] && exit 0\n"+
		"git -c user.name=t -c user.email=t@e.com commit -q --allow-empty -m drift\n"), 0o755))
	// The test HOME is shared by every run of the package (TestMain), so the
	// workspace is named per run: a repeated run must not find the last one's.
	workspace := "wrong-head-" + filepath.Base(filepath.Dir(filepath.Dir(r.clone)))
	req := worktreeCreateRequest{
		ProjectPath: r.clone, WorkspaceID: workspace, SubPath: "target",
		Branch: "worktree/review", BaseBranch: "feat/x", FetchBranch: "feat/x", ExpectedHead: head,
	}
	create := func() worktreeCreateResponse {
		t.Helper()
		payload, err := json.Marshal(req)
		require.NoError(t, err)
		raw, err := handleWorktreeCreate(context.Background(), payload)
		require.NoError(t, err)
		var out worktreeCreateResponse
		require.NoError(t, json.Unmarshal(raw, &out))
		return out
	}

	out := create()
	require.False(t, out.Success)
	require.Contains(t, out.Error, "does not match verified expected head")
	require.NotContains(t, r.run(r.clone, "worktree", "list", "--porcelain"), workspace,
		"the wrong-head checkout must not stay registered")

	// The hook gone, the retried request finishes cleanly on the expected head.
	require.NoError(t, os.Remove(hook))
	out = create()
	require.True(t, out.Success, out.Error)
	require.Equal(t, head, r.run(out.WorktreePath, "rev-parse", "HEAD"))
}
