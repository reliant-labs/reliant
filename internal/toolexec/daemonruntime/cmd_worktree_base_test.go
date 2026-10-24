// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/worktreeerr"
)

// cmd_worktree_base_test.go — the branch a worktree is created on is the one
// that was asked for, whatever its base.
//
// git 2.43, given a start point that names no commit but matches one
// remote-tracking branch, runs `git worktree add -b worktree/new <path>
// feat/x` as if asked for a tracking branch named feat/x: it creates and
// checks out feat/x, never makes worktree/new, and exits 0. The worktree row
// then records a branch that does not exist. Any base that lived only on
// origin did this.

// baseRepos is a clone of a bare origin. origin has main and feat/x; the
// clone has seen both (refs/remotes/origin/*) but has no local feat/x.
type baseRepos struct {
	origin, clone string
	run           func(dir string, args ...string) string
}

func newBaseRepos(t *testing.T) baseRepos {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	// TestMain's HOME and git config (no automatic maintenance) are shared by
	// every test: these run in parallel and keep their worktrees apart by
	// workspace ID (create). A per-test HOME needed t.Setenv, which made all
	// five serial: ~95s of the package's 300s -race budget on a loaded host.
	t.Parallel()

	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e.com",
		)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
		return strings.TrimSpace(string(out))
	}

	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	seed := filepath.Join(root, "seed")
	clone := filepath.Join(root, "clone")
	run(root, "init", "--bare", "-b", "main", origin)
	run(root, "init", "-b", "main", seed)
	run(seed, "commit", "--allow-empty", "-m", "init")
	run(seed, "remote", "add", "origin", origin)
	run(seed, "push", "origin", "main")
	run(seed, "switch", "-c", "feat/x")
	run(seed, "commit", "--allow-empty", "-m", "feature work")
	run(seed, "push", "origin", "feat/x")
	run(root, "clone", origin, clone)
	return baseRepos{origin: origin, clone: clone, run: run}
}

// create runs worktree.create in the clone and returns its response.
func (r baseRepos) create(t *testing.T, workspaceID, branch, base string) worktreeCreateResponse {
	t.Helper()
	resp, _ := r.createAs(t, workspaceID, branch, base)
	return resp
}

// createAs is create, also returning the unique workspace ID it used.
func (r baseRepos) createAs(t *testing.T, workspaceID, branch, base string) (worktreeCreateResponse, string) {
	t.Helper()
	workspaceID = uniqueWorkspaceID(t, workspaceID)
	payload, err := json.Marshal(worktreeCreateRequest{
		ProjectPath: r.clone,
		WorkspaceID: workspaceID,
		SubPath:     "repo",
		Name:        workspaceID,
		Branch:      branch,
		BaseBranch:  base,
	})
	require.NoError(t, err)
	raw, err := handleWorktreeCreate(context.Background(), payload)
	require.NoError(t, err)
	var resp worktreeCreateResponse
	require.NoError(t, json.Unmarshal(raw, &resp))
	return resp, workspaceID
}

// refExists reports whether ref exists in dir.
func refExists(dir, ref string) bool {
	cmd := exec.Command("git", "rev-parse", "--verify", "--quiet", ref)
	cmd.Dir = dir
	return cmd.Run() == nil
}

func TestWorktreeCreate_BaseOnlyOnOriginMakesTheRequestedBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a bare origin, a clone and worktrees with git; runs in task test")
	}
	r := newBaseRepos(t)
	require.False(t, refExists(r.clone, "refs/heads/feat/x"), "precondition: feat/x is only on origin")

	resp := r.create(t, "ws-remote-base", "worktree/new", "feat/x")
	require.True(t, resp.Success, resp.Error)
	assert.Equal(t, "feat/x", resp.BaseBranch, "the recorded base is the branch name, not the ref it resolved to")

	assert.Equal(t, "worktree/new", r.run(resp.WorktreePath, "branch", "--show-current"),
		"the checkout is on the branch that was asked for")
	assert.Equal(t, r.run(r.clone, "rev-parse", "refs/remotes/origin/feat/x"), r.run(resp.WorktreePath, "rev-parse", "HEAD"),
		"and starts at origin's feat/x")
	assert.False(t, refExists(r.clone, "refs/heads/feat/x"), "git did not invent a local feat/x")
	assert.False(t, refExists(r.clone, "worktree/new@{upstream}"),
		"a base is a starting commit, not an upstream — as with a local base")
}

func TestWorktreeCreate_BaseNeverFetchedIsFetchedFromOrigin(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a bare origin, a clone and worktrees with git; runs in task test")
	}
	r := newBaseRepos(t)
	// Pushed after the clone was made: the clone has never seen it.
	seed := filepath.Join(filepath.Dir(r.clone), "seed")
	r.run(seed, "switch", "-c", "feat/late")
	r.run(seed, "commit", "--allow-empty", "-m", "late work")
	r.run(seed, "push", "origin", "feat/late")
	require.False(t, refExists(r.clone, "refs/remotes/origin/feat/late"), "precondition: the clone has not seen feat/late")

	resp := r.create(t, "ws-late-base", "worktree/late", "feat/late")
	require.True(t, resp.Success, resp.Error)
	assert.Equal(t, "worktree/late", r.run(resp.WorktreePath, "branch", "--show-current"))
	assert.Equal(t, r.run(seed, "rev-parse", "HEAD"), r.run(resp.WorktreePath, "rev-parse", "HEAD"))
	assert.True(t, refExists(r.clone, "refs/remotes/origin/feat/late"), "the one ref was fetched")
}

func TestWorktreeCreate_LocalBaseWinsOverOrigin(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a bare origin, a clone and worktrees with git; runs in task test")
	}
	r := newBaseRepos(t)
	// A local feat/x that differs from origin's: the user's own copy wins.
	r.run(r.clone, "branch", "feat/x", "main")
	require.NotEqual(t, r.run(r.clone, "rev-parse", "refs/heads/feat/x"), r.run(r.clone, "rev-parse", "refs/remotes/origin/feat/x"))

	resp := r.create(t, "ws-local-base", "worktree/local", "feat/x")
	require.True(t, resp.Success, resp.Error)
	assert.Equal(t, "worktree/local", r.run(resp.WorktreePath, "branch", "--show-current"))
	assert.Equal(t, r.run(r.clone, "rev-parse", "refs/heads/feat/x"), r.run(resp.WorktreePath, "rev-parse", "HEAD"))
}

func TestWorktreeCreate_CommitBaseIsUsedAsIs(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a bare origin, a clone and worktrees with git; runs in task test")
	}
	r := newBaseRepos(t)
	r.run(r.clone, "tag", "v1", "refs/remotes/origin/feat/x")

	resp := r.create(t, "ws-tag-base", "worktree/tagged", "v1")
	require.True(t, resp.Success, resp.Error)
	assert.Equal(t, "worktree/tagged", r.run(resp.WorktreePath, "branch", "--show-current"))
	assert.Equal(t, r.run(r.clone, "rev-parse", "v1^{commit}"), r.run(resp.WorktreePath, "rev-parse", "HEAD"))
}

// A base that exists neither here nor on origin (the fetch is tried and
// fails) is the caller's error: base_not_found, before anything is created
// on disk.
func TestWorktreeCreate_BaseNowhereIsBaseNotFound(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a bare origin, a clone and worktrees with git; runs in task test")
	}
	r := newBaseRepos(t)

	resp, workspaceID := r.createAs(t, "ws-missing-base", "worktree/doomed", "feat/nowhere")
	require.False(t, resp.Success)
	assert.Equal(t, worktreeerr.BaseNotFound, resp.ErrorCode)
	assert.Equal(t, "feat/nowhere", resp.ErrorDetail)
	assert.Contains(t, resp.Error, "feat/nowhere")

	home, err := os.UserHomeDir()
	require.NoError(t, err)
	_, statErr := os.Stat(filepath.Join(home, ".reliant", "worktrees", workspaceID))
	assert.True(t, os.IsNotExist(statErr), "nothing was created, so nothing is left behind")
	assert.False(t, refExists(r.clone, "refs/heads/worktree/doomed"))
	assert.False(t, refExists(r.clone, "refs/heads/feat/nowhere"))
}

// A base shaped like an option is never handed to git as one: it resolves to
// nothing, and the create answers base_not_found without running git on it.
func TestWorktreeCreate_RefusesOptionShapedBase(t *testing.T) {
	t.Parallel()
	_, found := resolveWorktreeStartPoint(context.Background(), t.TempDir(), "--upload-pack=x")
	assert.False(t, found)

	resp := createFor(t, map[string]any{
		"project_path": diagnoseRepo(t),
		"workspace_id": uniqueWorkspaceID(t, "ws-option-base"),
		"name":         "option",
		"branch":       "feat/new",
		"base_branch":  "--upload-pack=x",
	})
	require.False(t, resp.Success)
	assert.Equal(t, worktreeerr.BaseNotFound, resp.ErrorCode)
	assert.Equal(t, "--upload-pack=x", resp.ErrorDetail)
}
