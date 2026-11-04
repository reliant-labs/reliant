package daemonruntime

import (
	"context"
	"encoding/json"
	"testing"

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
