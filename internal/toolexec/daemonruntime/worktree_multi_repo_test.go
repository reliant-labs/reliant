package daemonruntime

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Real git proof: only the target origin has feat/x. Both siblings have
// independent default branches and must still create successfully.
func TestWorktreeThreeReposFeatureOnlyInTarget(t *testing.T) {
	if testing.Short() {
		t.Skip("three real git origins, fetches and worktrees; runs in full package tests")
	}
	target := newBaseRepos(t)
	head := target.run(target.clone, "rev-parse", "refs/remotes/origin/feat/x")
	for _, name := range []string{"target", "forge", "reliant"} {
		repo := target.clone
		base, fetch, expected := "feat/x", "feat/x", head
		if name != "target" {
			repo = filepath.Join(t.TempDir(), name)
			target.run(filepath.Dir(repo), "init", "-b", "default-"+name, repo)
			target.run(repo, "commit", "--allow-empty", "-m", "sibling default")
			// Clone metadata normally records origin/HEAD. Give the fixture
			// that same authoritative default without any external lookup.
			target.run(repo, "update-ref", "refs/remotes/origin/default-"+name, target.run(repo, "rev-parse", "HEAD"))
			target.run(repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/default-"+name)
			require.False(t, refExists(repo, "refs/heads/feat/x"))
			base, fetch, expected = "", "", ""
		}
		payload, err := json.Marshal(worktreeCreateRequest{
			ProjectPath: repo, WorkspaceID: "three-repo-target", SubPath: name,
			Branch: "worktree/review", BaseBranch: base, FetchBranch: fetch, ExpectedHead: expected,
		})
		require.NoError(t, err)
		raw, err := handleWorktreeCreate(context.Background(), payload)
		require.NoError(t, err)
		var out worktreeCreateResponse
		require.NoError(t, json.Unmarshal(raw, &out))
		require.True(t, out.Success, "%s: %s", name, out.Error)
		if name == "target" {
			require.Equal(t, head, target.run(out.WorktreePath, "rev-parse", "HEAD"))
		} else {
			require.Equal(t, "default-"+name, out.BaseBranch)
		}
		// Remove only checkouts this test created; HOME belongs to this fixture.
		cmd := exec.Command("git", "worktree", "remove", out.WorktreePath)
		cmd.Dir = repo
		require.NoError(t, cmd.Run())
	}
}
