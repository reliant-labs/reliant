package daemonruntime

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWorktreeRefreshSHANameCannotShadowVerifiedCommit(t *testing.T) {
	if testing.Short() {
		t.Skip("real git origin, fetch and checkout; full scoped test")
	}
	r := newBaseRepos(t)
	expected := r.run(r.clone, "rev-parse", "refs/remotes/origin/feat/x")
	other := r.run(r.clone, "rev-parse", "refs/remotes/origin/main")
	r.run(r.clone, "branch", expected, other)
	for _, reuse := range []bool{false, true} {
		payload, err := json.Marshal(worktreeCreateRequest{ProjectPath: r.clone, WorkspaceID: "sha-shadow", SubPath: "repo", Branch: "worktree/review", BaseBranch: "feat/x", FetchBranch: "feat/x", ExpectedHead: expected, ReuseBranch: reuse})
		require.NoError(t, err)
		raw, err := handleWorktreeCreate(context.Background(), payload)
		require.NoError(t, err)
		var out worktreeCreateResponse
		require.NoError(t, json.Unmarshal(raw, &out))
		if reuse {
			require.False(t, out.Success)
			require.Contains(t, out.Error, "reuse")
		} else {
			require.True(t, out.Success, out.Error)
			require.Equal(t, expected, r.run(out.WorktreePath, "rev-parse", "HEAD"))
		}
	}
}
