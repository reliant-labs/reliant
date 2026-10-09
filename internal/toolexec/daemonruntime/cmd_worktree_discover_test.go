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

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/workspacecreate"
)

func TestParseWorktreePorcelain(t *testing.T) {
	out := "worktree /r/main\nHEAD aaa\nbranch refs/heads/main\n\n" +
		"worktree /r/locked\nHEAD bbb\nbranch refs/heads/feat/x\nlocked\n\n" +
		"worktree /r/locked-why\nHEAD ccc\nbranch refs/heads/y\nlocked reclaimable by reliant\n\n" +
		"worktree /r/gone\nHEAD ddd\nbranch refs/heads/z\nprunable gitdir file points to non-existent location\n\n" +
		"worktree /r/det\nHEAD eee\ndetached\n\n" +
		"worktree /r/bare\nbare\n"
	got := parseWorktreePorcelain(out)
	require.Len(t, got, 6)
	assert.Equal(t, porcelainWorktree{Path: "/r/main", Head: "aaa", Branch: "main"}, got[0])
	assert.True(t, got[1].Locked)
	assert.Equal(t, "", got[1].LockReason)
	assert.Equal(t, "feat/x", got[1].Branch)
	assert.Equal(t, "reclaimable by reliant", got[2].LockReason)
	assert.True(t, got[3].Prunable)
	assert.Equal(t, "gitdir file points to non-existent location", got[3].PrunableReason)
	assert.True(t, got[4].Detached)
	assert.Equal(t, "", got[4].Branch)
	assert.True(t, got[5].Bare)
}

func TestDiscoverRepos_MissingAndNonGitYieldNothing(t *testing.T) {
	root := t.TempDir()
	payload, _ := json.Marshal(worktreeDiscoverReposRequest{Repos: []worktreeRepoRef{
		{RepoID: "x", Path: filepath.Join(root, "missing")},
		{RepoID: "y", Path: root},
	}})
	raw, err := handleWorktreeDiscoverRepos(context.Background(), payload)
	require.NoError(t, err)
	var resp worktreeDiscoverReposResponse
	require.NoError(t, json.Unmarshal(raw, &resp))
	assert.Empty(t, resp.Worktrees)
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
}

func initRepo(t *testing.T, dir string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
}

type discoverFixture struct {
	root, a, b, aWT, bGone string
}

// A non-git root holding repos a/ and b/, a linked worktree of a, and a
// linked worktree of b whose directory was then removed.
func newDiscoverFixture(t *testing.T) discoverFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	f := discoverFixture{root: root, a: filepath.Join(root, "a"), b: filepath.Join(root, "b"),
		aWT: filepath.Join(root, "a-wt"), bGone: filepath.Join(root, "b-gone")}
	initRepo(t, f.a)
	initRepo(t, f.b)
	gitIn(t, f.a, "worktree", "add", "-q", "-b", "feat-a", f.aWT)
	gitIn(t, f.b, "worktree", "add", "-q", "-b", "feat-b", f.bGone)
	require.NoError(t, os.RemoveAll(f.bGone))
	return f
}

func (f discoverFixture) repos() []worktreeRepoRef {
	return []worktreeRepoRef{{RepoID: "ra", Path: f.a}, {RepoID: "rb", Path: f.b}}
}

func discoverCall(t *testing.T, req worktreeDiscoverReposRequest) worktreeDiscoverReposResponse {
	t.Helper()
	payload, _ := json.Marshal(req)
	raw, err := handleWorktreeDiscoverRepos(context.Background(), payload)
	require.NoError(t, err)
	var resp worktreeDiscoverReposResponse
	require.NoError(t, json.Unmarshal(raw, &resp))
	return resp
}

func TestDiscoverRepos_RealGit(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: spawns git; runs in the full lane")
	}
	f := newDiscoverFixture(t)
	t.Setenv("HOME", t.TempDir())

	resp := discoverCall(t, worktreeDiscoverReposRequest{Repos: f.repos()})
	require.Len(t, resp.Worktrees, 2)
	byRepo := map[string]discoveredWorktreeEntry{}
	for _, e := range resp.Worktrees {
		byRepo[e.RepoID] = e
	}
	live := byRepo["ra"]
	assert.Equal(t, f.aWT, live.Path)
	assert.Equal(t, "feat-a", live.Branch)
	assert.False(t, live.Prunable)
	assert.NotEmpty(t, live.Head)
	stale := byRepo["rb"]
	assert.Equal(t, f.bGone, stale.Path)
	assert.True(t, stale.Prunable)
	assert.NotEmpty(t, stale.PrunableReason)

	t.Run("exclude_roots drops tracked live checkouts", func(t *testing.T) {
		resp := discoverCall(t, worktreeDiscoverReposRequest{Repos: f.repos(), ExcludeRoots: []string{f.root + "/a-wt"}})
		require.Len(t, resp.Worktrees, 1)
		assert.True(t, resp.Worktrees[0].Prunable)
	})
	t.Run("exclude root containing the checkout", func(t *testing.T) {
		resp := discoverCall(t, worktreeDiscoverReposRequest{Repos: f.repos()[:1], ExcludeRoots: []string{f.root}})
		assert.Empty(t, resp.Worktrees)
	})
	t.Run("reliant worktrees root is excluded", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		inside := filepath.Join(home, ".reliant", "worktrees", "p", "ws-1", "a")
		gitIn(t, f.a, "worktree", "add", "-q", "-b", "reliant-made", inside)
		resp := discoverCall(t, worktreeDiscoverReposRequest{Repos: f.repos()[:1]})
		require.Len(t, resp.Worktrees, 1)
		assert.Equal(t, f.aWT, resp.Worktrees[0].Path)
		assert.Equal(t, filepath.Join(home, ".reliant", "worktrees"), resp.WorktreesRoot)
	})
}

func TestPrune_RealGit(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: spawns git; runs in the full lane")
	}
	f := newDiscoverFixture(t)
	payload, _ := json.Marshal(worktreePruneRequest{Repos: f.repos()})
	raw, err := handleWorktreePrune(context.Background(), payload)
	require.NoError(t, err)
	var resp worktreePruneResponse
	require.NoError(t, json.Unmarshal(raw, &resp))
	require.Empty(t, resp.Errors)
	require.Len(t, resp.Pruned, 1)
	assert.Equal(t, f.bGone, resp.Pruned[0].Path)
	assert.Equal(t, "rb", resp.Pruned[0].RepoID)

	// The live worktree and its directory are untouched.
	_, err = os.Stat(f.aWT)
	assert.NoError(t, err)
	after := discoverCall(t, worktreeDiscoverReposRequest{Repos: f.repos()})
	require.Len(t, after.Worktrees, 1)
	assert.Equal(t, f.aWT, after.Worktrees[0].Path)

	// Nothing left to prune.
	raw, err = handleWorktreePrune(context.Background(), payload)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &resp))
	assert.Empty(t, resp.Pruned)
}

// ---- adopt ----

type adoptStore struct{ rows map[string]db.Worktree }

func (s *adoptStore) CreateWorktree(_ context.Context, w *db.Worktree) error {
	s.rows[w.ID] = *w
	return nil
}
func (s *adoptStore) UpdateWorktree(_ context.Context, w *db.Worktree) error {
	s.rows[w.ID] = *w
	return nil
}
func (s *adoptStore) ArchiveWorktree(context.Context, string) error { return nil }
func (s *adoptStore) GetLiveWorktreeByName(context.Context, string, string) (*db.Worktree, error) {
	return nil, nil
}
func (s *adoptStore) ListWorktreesByBranch(context.Context, string, string) ([]*db.Worktree, error) {
	return nil, nil
}

// handlerMachine runs workspacecreate commands through the daemon handlers.
type handlerMachine struct{ t *testing.T }

func (m handlerMachine) Send(ctx context.Context, cmd string, payload, resp any, _ int32) error {
	b, _ := json.Marshal(payload)
	out, err := DefaultRegistry().Handle(ctx, cmd, b)
	if err != nil {
		return err
	}
	if resp != nil {
		return json.Unmarshal(out, resp)
	}
	return nil
}

func adoptMove(t *testing.T, repoPath, src, ws, sub, id string) worktreeAdoptMoveResponse {
	t.Helper()
	payload, _ := json.Marshal(worktreeAdoptMoveRequest{RepoPath: repoPath, Src: src, WorkspaceID: ws, SubPath: sub, WorktreeID: id})
	raw, err := handleWorktreeAdoptMove(context.Background(), payload)
	require.NoError(t, err)
	var resp worktreeAdoptMoveResponse
	require.NoError(t, json.Unmarshal(raw, &resp))
	return resp
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	require.NoError(t, err, string(out))
	return strings.TrimSpace(string(out))
}

func TestAdoptMove_RealGit(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: spawns git; runs in the full lane")
	}
	f := newDiscoverFixture(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.WriteFile(filepath.Join(f.aWT, "dirty.txt"), []byte("wip"), 0o644))
	headBefore := gitOut(t, f.aWT, "rev-parse", "HEAD")

	t.Run("moves, keeps branch/HEAD/dirty file, locks", func(t *testing.T) {
		resp := adoptMove(t, f.a, f.aWT, "proj/ws-1", "a", "row-1")
		require.True(t, resp.Success, resp.Error)
		dest := filepath.Join(home, ".reliant", "worktrees", "proj", "ws-1", "a")
		assert.Equal(t, dest, resp.WorktreePath)
		assert.NoDirExists(t, f.aWT)
		assert.Equal(t, "feat-a", gitOut(t, dest, "rev-parse", "--abbrev-ref", "HEAD"))
		assert.Equal(t, headBefore, gitOut(t, dest, "rev-parse", "HEAD"))
		b, err := os.ReadFile(filepath.Join(dest, "dirty.txt"))
		require.NoError(t, err)
		assert.Equal(t, "wip", string(b))
		list := gitOut(t, f.a, "worktree", "list", "--porcelain")
		assert.Contains(t, list, "worktree "+dest)
		assert.Contains(t, list, "locked")

		// restore puts it back, unlocked, intact.
		payload, _ := json.Marshal(worktreeAdoptRestoreRequest{RepoPath: f.a, Src: f.aWT, Dest: dest})
		raw, err := handleWorktreeAdoptRestore(context.Background(), payload)
		require.NoError(t, err)
		var rr worktreeAdoptRestoreResponse
		require.NoError(t, json.Unmarshal(raw, &rr))
		require.True(t, rr.Success, rr.Error)
		assert.NoDirExists(t, dest)
		assert.Equal(t, headBefore, gitOut(t, f.aWT, "rev-parse", "HEAD"))
		_, err = os.Stat(filepath.Join(f.aWT, "dirty.txt"))
		assert.NoError(t, err)
	})

	t.Run("refuses an existing destination", func(t *testing.T) {
		dest := filepath.Join(home, ".reliant", "worktrees", "proj", "ws-2", "a")
		require.NoError(t, os.MkdirAll(dest, 0o755))
		resp := adoptMove(t, f.a, f.aWT, "proj/ws-2", "a", "row-2")
		assert.False(t, resp.Success)
		assert.Contains(t, resp.Error, "already exists")
		assert.DirExists(t, f.aWT)
	})

	t.Run("refuses a main checkout and a foreign path", func(t *testing.T) {
		assert.False(t, adoptMove(t, f.a, f.a, "proj/ws-3", "a", "r").Success)
		assert.False(t, adoptMove(t, f.a, t.TempDir(), "proj/ws-3", "a", "r").Success)
		assert.DirExists(t, f.a)
	})

	t.Run("surfaces git's refusal for a locked worktree", func(t *testing.T) {
		gitIn(t, f.a, "worktree", "lock", "--reason", "hand lock", f.aWT)
		resp := adoptMove(t, f.a, f.aWT, "proj/ws-4", "a", "row-4")
		assert.False(t, resp.Success)
		assert.Contains(t, resp.Error, "locked")
		assert.DirExists(t, f.aWT)
		gitIn(t, f.a, "worktree", "unlock", f.aWT)
	})
}

// A multi-repo adopt whose LATER repo fails must put the adopted checkout
// back where it was, dirty file and all, and remove only what Finish made.
func TestAdoptFinish_RollbackMovesCheckoutBack(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: spawns git; runs in the full lane")
	}
	f := newDiscoverFixture(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.WriteFile(filepath.Join(f.aWT, "dirty.txt"), []byte("wip"), 0o644))
	headBefore := gitOut(t, f.aWT, "rev-parse", "HEAD")
	// b already has the adopted branch name AND it is checked out elsewhere:
	// the adopt must fail without touching that branch.
	gitIn(t, f.b, "branch", "feat-a")
	bWT := filepath.Join(f.root, "b-has-feat-a")
	gitIn(t, f.b, "worktree", "add", "-q", bWT, "feat-a")
	bTip := gitOut(t, f.b, "rev-parse", "feat-a")

	runAdopt := func(t *testing.T, ws string) (*db.Worktree, error, workspacecreate.Request) {
		project := &db.Project{ID: "p", Name: "proj", Path: f.root}
		repos := []*core.Repo{{ID: "ra", Name: "a", RelativePath: "a"}, {ID: "rb", Name: "b", RelativePath: "b"}}
		store := &adoptStore{rows: map[string]db.Worktree{}}
		req := workspacecreate.Request{Project: project, Repos: repos, Name: "adopted", Branch: "feat-a", OwnerDaemonID: "d", WorkspaceID: ws}
		wt, err := workspacecreate.Insert(context.Background(), store, req)
		require.NoError(t, err)
		moved := adoptMove(t, f.a, f.aWT, ws, "a", wt.ID)
		require.True(t, moved.Success, moved.Error)
		req.Existing = map[string]workspacecreate.ExistingCheckout{"ra": {Path: moved.WorktreePath, Base: "main", OriginalPath: f.aWT}}
		ferr := workspacecreate.Finish(context.Background(), store, handlerMachine{t}, req, wt)
		return wt, ferr, req
	}

	wt, ferr, _ := runAdopt(t, "proj/ws-fail")
	require.Error(t, ferr)
	assert.Contains(t, ferr.Error(), "already checked out in another worktree")
	assert.Equal(t, int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_FAILED), wt.Status)
	assert.Empty(t, wt.Path)
	// Back at its original path, intact.
	assert.Equal(t, "feat-a", gitOut(t, f.aWT, "rev-parse", "--abbrev-ref", "HEAD"))
	assert.Equal(t, headBefore, gitOut(t, f.aWT, "rev-parse", "HEAD"))
	b, err := os.ReadFile(filepath.Join(f.aWT, "dirty.txt"))
	require.NoError(t, err)
	assert.Equal(t, "wip", string(b))
	assert.NoDirExists(t, filepath.Join(home, ".reliant", "worktrees", "proj", "ws-fail"))
	// b's branch and its existing checkout were not reset or removed.
	assert.Equal(t, bTip, gitOut(t, f.b, "rev-parse", "feat-a"))
	assert.DirExists(t, bWT)
}

func TestAdoptFinish_ReusesExistingBranchAsIs(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: spawns git; runs in the full lane")
	}
	f := newDiscoverFixture(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	// b has feat-a, one commit ahead of main, checked out nowhere.
	gitIn(t, f.b, "checkout", "-q", "-b", "feat-a")
	gitIn(t, f.b, "commit", "-q", "--allow-empty", "-m", "b work")
	gitIn(t, f.b, "checkout", "-q", "main")
	bTip := gitOut(t, f.b, "rev-parse", "feat-a")

	project := &db.Project{ID: "p", Name: "proj", Path: f.root}
	repos := []*core.Repo{{ID: "ra", Name: "a", RelativePath: "a"}, {ID: "rb", Name: "b", RelativePath: "b"}}
	store := &adoptStore{rows: map[string]db.Worktree{}}
	req := workspacecreate.Request{Project: project, Repos: repos, Name: "adopted", Branch: "feat-a", OwnerDaemonID: "d", WorkspaceID: "proj/ws-ok"}
	wt, err := workspacecreate.Insert(context.Background(), store, req)
	require.NoError(t, err)
	moved := adoptMove(t, f.a, f.aWT, "proj/ws-ok", "a", wt.ID)
	require.True(t, moved.Success, moved.Error)
	req.Existing = map[string]workspacecreate.ExistingCheckout{"ra": {Path: moved.WorktreePath, Base: "main", OriginalPath: f.aWT}}
	require.NoError(t, workspacecreate.Finish(context.Background(), store, handlerMachine{t}, req, wt))

	root := filepath.Join(home, ".reliant", "worktrees", "proj", "ws-ok")
	assert.Equal(t, root, wt.Path)
	assert.Equal(t, int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE), wt.Status)
	assert.Equal(t, bTip, gitOut(t, filepath.Join(root, "b"), "rev-parse", "HEAD"), "b's branch was checked out as it was, not reset to main")
	assert.Equal(t, bTip, gitOut(t, f.b, "rev-parse", "feat-a"))
	assert.Equal(t, "feat-a", gitOut(t, filepath.Join(root, "a"), "rev-parse", "--abbrev-ref", "HEAD"))
	assert.Equal(t, "main", wt.BaseBranches["ra"])
}

func TestAdoptFinish_CreatesBranchWhenAbsent(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: spawns git; runs in the full lane")
	}
	f := newDiscoverFixture(t)
	t.Setenv("HOME", t.TempDir())
	project := &db.Project{ID: "p", Name: "proj", Path: f.root}
	repos := []*core.Repo{{ID: "ra", Name: "a", RelativePath: "a"}, {ID: "rb", Name: "b", RelativePath: "b"}}
	store := &adoptStore{rows: map[string]db.Worktree{}}
	req := workspacecreate.Request{Project: project, Repos: repos, Name: "adopted", Branch: "feat-a", OwnerDaemonID: "d", WorkspaceID: "proj/ws-new"}
	wt, err := workspacecreate.Insert(context.Background(), store, req)
	require.NoError(t, err)
	moved := adoptMove(t, f.a, f.aWT, "proj/ws-new", "a", wt.ID)
	require.True(t, moved.Success, moved.Error)
	req.Existing = map[string]workspacecreate.ExistingCheckout{"ra": {Path: moved.WorktreePath, Base: "main", OriginalPath: f.aWT}}
	require.NoError(t, workspacecreate.Finish(context.Background(), store, handlerMachine{t}, req, wt))
	assert.Equal(t, "feat-a", gitOut(t, filepath.Join(wt.Path, "b"), "rev-parse", "--abbrev-ref", "HEAD"))
}
