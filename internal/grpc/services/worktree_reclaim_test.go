// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/toolexec/daemonruntime"
	"github.com/reliant-labs/reliant/internal/worktreereclaim"
	"github.com/reliant-labs/reliant/internal/worktreesweep"
)

// realDaemonRouter sends every command to the daemon's real handler (the same
// registry the daemon dispatches from), against real git repositories. The
// worktree root is redirected to a temp HOME.
type realDaemonRouter struct {
	worktreeTestDaemonRouter
	calls []string
}

func (r *realDaemonRouter) SendDaemonCommandToDaemon(ctx context.Context, userID, _ string, cmd string, payload []byte, to int32) ([]byte, error) {
	return r.SendDaemonCommand(ctx, userID, cmd, payload, to)
}

func (r *realDaemonRouter) SendDaemonCommand(ctx context.Context, _ string, cmd string, payload []byte, _ int32) ([]byte, error) {
	r.calls = append(r.calls, cmd)
	return daemonruntime.DefaultRegistry().Handle(ctx, cmd, payload)
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e.com")
	out, err := c.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

type reclaimEnv struct {
	svc     *WorktreeService
	sweeper *worktreesweep.Sweeper
	repo    *db.Repo
	router  *realDaemonRouter
	userID  string
	daemon  string
	ctx     context.Context
	project *db.Project
	root    string
}

func newReclaimEnv(t *testing.T) *reclaimEnv {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	// The daemon's worktree root is <home>/.reliant/worktrees; the shared
	// reclaimer reads it once, so this test process must use one HOME. Skip the
	// sharing by pointing the registered handlers at the same root.
	root := filepath.Join(home, ".reliant", "worktrees")
	require.NoError(t, os.MkdirAll(root, 0o755))

	repo := db.NewTestRepo(t)
	userID := uuid.NewString()
	e := &reclaimEnv{repo: repo, userID: userID, daemon: "d-" + uuid.NewString(), root: root,
		ctx: context.WithValue(context.Background(), auth.UserIDContextKey, userID), router: &realDaemonRouter{}}

	projectPath := filepath.Join(t.TempDir(), "project")
	gitIn(t, filepath.Dir(projectPath), "init", "-q", "-b", "main", projectPath)
	require.NoError(t, os.WriteFile(filepath.Join(projectPath, "a.txt"), []byte("one\n"), 0o644))
	gitIn(t, projectPath, "add", ".")
	gitIn(t, projectPath, "commit", "-q", "-m", "init")

	now := time.Now().UTC()
	e.project = &db.Project{ID: "p-" + userID, UserID: userID, Name: "proj", Path: projectPath, CreatedAt: now, UpdatedAt: now, LastActive: now}
	require.NoError(t, repo.CreateProject(e.ctx, e.project))
	require.NoError(t, repo.UpsertDaemon(e.ctx, &db.Daemon{ID: e.daemon, UserID: userID}))
	require.NoError(t, repo.UpsertDaemonAttachment(e.ctx, &db.DaemonAttachment{DaemonID: e.daemon, UserID: userID, Source: db.DaemonAttachmentSourceInbound}))

	e.sweeper = worktreesweep.New(repo, e.router)
	e.svc = NewWorktreeService(repo, nil, e.router).WithSettler(e.sweeper)
	return e
}

// worktree makes a real locked checkout under the daemon's root and its row.
func (e *reclaimEnv) worktree(t *testing.T, name string) (*db.Worktree, string) {
	t.Helper()
	id := uuid.NewString()
	path := filepath.Join(e.root, "proj", name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	gitIn(t, e.project.Path, "worktree", "add", "-q", "-b", "feat/"+name, path, "main")
	require.NoError(t, worktreereclaim.LockCheckout(e.ctx, path, id))
	now := time.Now().UTC()
	wt := &db.Worktree{ID: id, Name: name, Path: path, Branch: "feat/" + name, BaseBranch: "main", ProjectID: e.project.ID,
		DaemonID: &e.daemon, Status: 1, CreatedAt: now, UpdatedAt: now, LastActive: now}
	require.NoError(t, e.repo.CreateWorktree(e.ctx, wt))
	return wt, path
}

// age makes a worktree look untouched for two days: its directory, and the
// index, HEAD and reflog of its git admin directory.
func age(t *testing.T, path string) {
	t.Helper()
	old := time.Now().Add(-48 * time.Hour)
	_ = os.Chtimes(path, old, old)
	if b, err := os.ReadFile(filepath.Join(path, ".git")); err == nil {
		dir := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(b)), "gitdir:"))
		for _, n := range []string{"index", "HEAD", "logs/HEAD", "."} {
			_ = os.Chtimes(filepath.Join(dir, n), old, old)
		}
	}
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

// S1: Unarchive re-locks as active BEFORE flipping the row, so a removal that
// was claimed under the archive's fence can no longer proceed.
func TestUnarchive_RelocksAsActiveSoAStaleClaimCannotRemove(t *testing.T) {
	e := newReclaimEnv(t)
	wt, path := e.worktree(t, "unarch")
	require.NoError(t, e.repo.ArchiveWorktree(e.ctx, wt.ID))
	// Claim it, as the daemon does for an archived row.
	archived, err := e.repo.GetWorktree(e.ctx, wt.ID)
	require.NoError(t, err)
	fence := worktreesweep.Fence(archived)
	claimReq := worktreereclaim.Request{Worktrees: []worktreereclaim.Worktree{{ID: wt.ID, Path: path, State: worktreereclaim.StateArchived, Fence: fence, BaseBranch: "main"}}}
	r := worktreereclaim.NewReclaimer(e.root)
	r.IdleFloor = 0
	r.Cwds = func(context.Context) ([]string, error) { return nil, nil }
	require.Equal(t, worktreereclaim.OutcomeClaimed, r.Reconcile(e.ctx, claimReq).Results[0].Outcome)

	_, err = e.svc.UnarchiveWorktree(e.ctx, connect.NewRequest(&reliantv1.UnarchiveWorktreeRequest{WorktreeId: wt.ID}))
	require.NoError(t, err)
	assert.Contains(t, e.router.calls, "worktree.reconcile", "unarchive asked the machine to re-lock")

	claimReq.Worktrees[0].Remove = true
	got := r.Reconcile(e.ctx, claimReq).Results[0]
	assert.NotEqual(t, worktreereclaim.OutcomeRemoved, got.Outcome, "a stale claim removed a restored workspace")
	assert.True(t, exists(path))
}

// S5: restore after removal. Unarchive refuses; recreate rebuilds and re-applies
// the saved work, and keeps the refs until it has.
func TestRecreate_AppliesTheSavedSnapshotAndKeepsRefsUntilThen(t *testing.T) {
	e := newReclaimEnv(t)
	wt, path := e.worktree(t, "restore")
	require.NoError(t, os.WriteFile(filepath.Join(path, "wip.txt"), []byte("precious\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(path, "a.txt"), []byte("edited\n"), 0o644))
	require.NoError(t, e.repo.ArchiveWorktree(e.ctx, wt.ID))
	age(t, path)
	require.NoError(t, e.sweeper.Sweep(e.ctx)) // holds it: dirty
	held, _ := e.repo.GetWorktree(e.ctx, wt.ID)
	require.Equal(t, "dirty", held.CleanupMetadata.HeldReason)

	// A confirmed clean-up saves and removes it.
	results, _, err := e.sweeper.Cleanup(e.ctx, e.repo, e.userID, e.daemon, []string{wt.ID})
	require.NoError(t, err)
	require.Equal(t, worktreesweep.CleanupRemoved, results[0].Outcome, "%+v", results[0])
	require.False(t, exists(path))
	gone, _ := e.repo.GetWorktree(e.ctx, wt.ID)
	require.True(t, gone.CleanupMetadata.DirectoryDeleted)
	require.Len(t, gone.CleanupMetadata.SnapshotRefs, 1)

	// Plain unarchive cannot work: there is no directory.
	_, err = e.svc.UnarchiveWorktree(e.ctx, connect.NewRequest(&reliantv1.UnarchiveWorktreeRequest{WorktreeId: wt.ID}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	_, err = e.svc.RecreateWorktree(e.ctx, connect.NewRequest(&reliantv1.RecreateWorktreeRequest{WorktreeId: wt.ID}))
	require.NoError(t, err)
	b, err := os.ReadFile(filepath.Join(path, "wip.txt"))
	require.NoError(t, err)
	assert.Equal(t, "precious\n", string(b), "untracked work is back")
	b, _ = os.ReadFile(filepath.Join(path, "a.txt"))
	assert.Equal(t, "edited\n", string(b), "tracked edits are back")
	assert.Equal(t, "", gitIn(t, path, "diff", "--cached", "--name-only"), "the index is left at HEAD")
	after, _ := e.repo.GetWorktree(e.ctx, wt.ID)
	assert.Nil(t, after.DeletedAt)
	assert.True(t, after.CleanupMetadata == nil || len(after.CleanupMetadata.SnapshotRefs) == 0, "refs are dropped only after they were re-applied")
}

// S4: permanent delete must not orphan a held directory.
func TestPermanentDelete_RefusesWhileTheDirectoryIsStillOnDisk(t *testing.T) {
	e := newReclaimEnv(t)
	wt, path := e.worktree(t, "pd")
	require.NoError(t, os.WriteFile(filepath.Join(path, "wip.txt"), []byte("x"), 0o644))
	require.NoError(t, e.repo.ArchiveWorktree(e.ctx, wt.ID))
	age(t, path)
	_, err := e.svc.DeleteWorktree(e.ctx, connect.NewRequest(&reliantv1.DeleteWorktreeRequest{WorktreeId: wt.ID}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	_, getErr := e.repo.GetWorktree(e.ctx, wt.ID)
	assert.NoError(t, getErr, "the row must survive so the directory can still be found")
	assert.True(t, exists(path))
}

func TestPermanentDelete_SucceedsOnceTheMachineRemovedTheDirectory(t *testing.T) {
	e := newReclaimEnv(t)
	wt, path := e.worktree(t, "pd2")
	require.NoError(t, e.repo.ArchiveWorktree(e.ctx, wt.ID))
	age(t, path)
	_, err := e.svc.DeleteWorktree(e.ctx, connect.NewRequest(&reliantv1.DeleteWorktreeRequest{WorktreeId: wt.ID}))
	require.NoError(t, err)
	assert.False(t, exists(path))
}

// S2: an import may not claim an archived workspace's directory.
func TestImportWorktree_RejectsAnArchivedRowsPath(t *testing.T) {
	e := newReclaimEnv(t)
	wt, path := e.worktree(t, "imp")
	require.NoError(t, e.repo.ArchiveWorktree(e.ctx, wt.ID))
	_, err := e.svc.ImportWorktree(e.ctx, connect.NewRequest(&reliantv1.ImportWorktreeRequest{ProjectId: e.project.ID, Path: path}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))
}

// NIT: storeCleanupMetadata merges instead of wiping held state.
func TestStoreCleanupMetadata_MergesInsteadOfOverwriting(t *testing.T) {
	e := newReclaimEnv(t)
	wt, _ := e.worktree(t, "merge")
	require.NoError(t, e.repo.UpdateWorktreeCleanupMetadata(e.ctx, wt.ID, &db.CleanupMetadata{HeldReason: "dirty", SnapshotRefs: []string{"refs/x"}}))
	e.svc.storeCleanupMetadata(e.ctx, wt.ID, true)
	got, _ := e.repo.GetWorktree(e.ctx, wt.ID)
	assert.Equal(t, "dirty", got.CleanupMetadata.HeldReason)
	assert.Equal(t, []string{"refs/x"}, got.CleanupMetadata.SnapshotRefs)
	assert.True(t, got.CleanupMetadata.DeleteBranch)
}

// multiRepoEnv builds a project whose ROOT is not a git repository and which has
// two nested repos, like the owner's reliant-labs checkout.
func newMultiRepoEnv(t *testing.T) (*reclaimEnv, []string) {
	t.Helper()
	e := newReclaimEnv(t)
	root := t.TempDir() // the project root: not a repo
	var repoPaths []string
	now := time.Now().UTC()
	e.project = &db.Project{ID: "mp-" + e.userID, UserID: e.userID, Name: "multi", Path: root, CreatedAt: now, UpdatedAt: now, LastActive: now}
	require.NoError(t, e.repo.CreateProject(e.ctx, e.project))
	for _, name := range []string{"api", "web"} {
		p := filepath.Join(root, name)
		gitIn(t, root, "init", "-q", "-b", "main", p)
		require.NoError(t, os.WriteFile(filepath.Join(p, "a.txt"), []byte(name+"\n"), 0o644))
		gitIn(t, p, "add", ".")
		gitIn(t, p, "commit", "-q", "-m", "init")
		require.NoError(t, e.repo.CreateRepo(e.ctx, &core.Repo{ID: uuid.NewString(), ProjectID: e.project.ID, Name: name, RelativePath: name, CreatedAt: now, UpdatedAt: now}))
		repoPaths = append(repoPaths, p)
	}
	return e, repoPaths
}

// R-S4: restoring a removed multi-repo workspace rebuilds EVERY repo and puts
// back each one's saved work.
func TestRecreate_RestoresEveryRepoOfAMultiRepoWorkspace(t *testing.T) {
	e, repoPaths := newMultiRepoEnv(t)
	ws := filepath.Join(e.root, "multi", "ws")
	for i, name := range []string{"api", "web"} {
		gitIn(t, repoPaths[i], "worktree", "add", "-q", "-b", "feat/ws", filepath.Join(ws, name), "main")
		require.NoError(t, worktreereclaim.LockCheckout(e.ctx, filepath.Join(ws, name), "wsid"))
	}
	now := time.Now().UTC()
	wt := &db.Worktree{ID: "wsid", Name: "ws", Path: ws, Branch: "feat/ws", BaseBranch: "main", ProjectID: e.project.ID,
		DaemonID: &e.daemon, Status: 1, CreatedAt: now, UpdatedAt: now, LastActive: now}
	require.NoError(t, e.repo.CreateWorktree(e.ctx, wt))
	require.NoError(t, os.WriteFile(filepath.Join(ws, "api", "api-wip.txt"), []byte("api work\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(ws, "web", "web-wip.txt"), []byte("web work\n"), 0o644))
	require.NoError(t, e.repo.ArchiveWorktree(e.ctx, wt.ID))
	age(t, ws)
	for _, n := range []string{"api", "web"} {
		age(t, filepath.Join(ws, n))
	}
	require.NoError(t, e.sweeper.Sweep(e.ctx))
	results, _, err := e.sweeper.Cleanup(e.ctx, e.repo, e.userID, e.daemon, []string{wt.ID})
	require.NoError(t, err)
	require.Equal(t, worktreesweep.CleanupRemoved, results[0].Outcome, "%+v", results[0])
	require.False(t, exists(filepath.Join(ws, "api")))
	require.False(t, exists(filepath.Join(ws, "web")))

	_, err = e.svc.RecreateWorktree(e.ctx, connect.NewRequest(&reliantv1.RecreateWorktreeRequest{WorktreeId: wt.ID}))
	require.NoError(t, err)
	for name, file := range map[string]string{"api": "api-wip.txt", "web": "web-wip.txt"} {
		assert.True(t, exists(filepath.Join(ws, name, ".git")), "%s was rebuilt", name)
		b, err := os.ReadFile(filepath.Join(ws, name, file))
		require.NoError(t, err, "%s's saved work", name)
		assert.Contains(t, string(b), "work")
	}
	after, _ := e.repo.GetWorktree(e.ctx, wt.ID)
	assert.Nil(t, after.DeletedAt)
	assert.True(t, after.CleanupMetadata == nil || len(after.CleanupMetadata.SnapshotRefs) == 0, "all refs re-applied, so dropped")
}

// R-S4: a missing branch in ANY repo creates nothing.
func TestRecreate_AMissingBranchInOneRepoCreatesNothing(t *testing.T) {
	e, repoPaths := newMultiRepoEnv(t)
	ws := filepath.Join(e.root, "multi", "half")
	gitIn(t, repoPaths[0], "branch", "feat/half")
	now := time.Now().UTC()
	wt := &db.Worktree{ID: "half", Name: "half", Path: ws, Branch: "feat/half", BaseBranch: "main", ProjectID: e.project.ID,
		DaemonID: &e.daemon, Status: 1, CreatedAt: now, UpdatedAt: now, LastActive: now}
	require.NoError(t, e.repo.CreateWorktree(e.ctx, wt))
	require.NoError(t, e.repo.ArchiveWorktree(e.ctx, wt.ID))
	_, err := e.svc.RecreateWorktree(e.ctx, connect.NewRequest(&reliantv1.RecreateWorktreeRequest{WorktreeId: wt.ID}))
	require.Error(t, err)
	assert.False(t, exists(filepath.Join(ws, "api")), "nothing may be left behind")
	assert.False(t, exists(filepath.Join(ws, "web")))
}

// R-S5: a failed apply keeps the refs on the row.
func TestRecreate_AFailedApplyKeepsTheSnapshotRefs(t *testing.T) {
	e := newReclaimEnv(t)
	wt, path := e.worktree(t, "failapply")
	require.NoError(t, os.WriteFile(filepath.Join(path, "wip.txt"), []byte("precious\n"), 0o644))
	require.NoError(t, e.repo.ArchiveWorktree(e.ctx, wt.ID))
	age(t, path)
	require.NoError(t, e.sweeper.Sweep(e.ctx))
	results, _, err := e.sweeper.Cleanup(e.ctx, e.repo, e.userID, e.daemon, []string{wt.ID})
	require.NoError(t, err)
	require.Equal(t, worktreesweep.CleanupRemoved, results[0].Outcome)

	// Make the apply fail: the recreated checkout already has the file with
	// other content, but is not clean either way, so recreate must not claim success.
	gone, _ := e.repo.GetWorktree(e.ctx, wt.ID)
	require.Len(t, gone.CleanupMetadata.SnapshotRefs, 1)
	ref := gone.CleanupMetadata.SnapshotRefs[0]
	gitIn(t, e.project.Path, "update-ref", "-d", ref) // the saved work is gone: apply cannot succeed
	gitIn(t, e.project.Path, "update-ref", ref, gitIn(t, e.project.Path, "rev-parse", "main"))
	// ref now points at a commit whose tree has nothing: apply "succeeds" with no
	// files, so use a ref that cannot resolve instead.
	gitIn(t, e.project.Path, "update-ref", "-d", ref)

	_, err = e.svc.RecreateWorktree(e.ctx, connect.NewRequest(&reliantv1.RecreateWorktreeRequest{WorktreeId: wt.ID}))
	require.NoError(t, err)
	after, _ := e.repo.GetWorktree(e.ctx, wt.ID)
	require.NotNil(t, after.CleanupMetadata, "the refs are the only record of the work")
	assert.Equal(t, []string{ref}, after.CleanupMetadata.SnapshotRefs)
}

// R3-B1: when the branch moved since the work was saved, recreate says so and
// keeps the refs; it does not report a plain success.
func TestRecreate_BranchMovedIsReportedAndKeepsTheRefs(t *testing.T) {
	e := newReclaimEnv(t)
	wt, path := e.worktree(t, "movedbranch")
	require.NoError(t, os.WriteFile(filepath.Join(path, "wip.txt"), []byte("precious\n"), 0o644))
	require.NoError(t, e.repo.ArchiveWorktree(e.ctx, wt.ID))
	age(t, path)
	require.NoError(t, e.sweeper.Sweep(e.ctx))
	results, _, err := e.sweeper.Cleanup(e.ctx, e.repo, e.userID, e.daemon, []string{wt.ID})
	require.NoError(t, err)
	require.Equal(t, worktreesweep.CleanupRemoved, results[0].Outcome)

	tip := gitIn(t, e.project.Path, "rev-parse", wt.Branch)
	tree := gitIn(t, e.project.Path, "rev-parse", wt.Branch+"^{tree}")
	moved := gitIn(t, e.project.Path, "-c", "user.name=t", "-c", "user.email=t@t", "commit-tree", tree, "-p", tip, "-m", "moved on")
	gitIn(t, e.project.Path, "update-ref", "refs/heads/"+wt.Branch, moved)

	resp, err := e.svc.RecreateWorktree(e.ctx, connect.NewRequest(&reliantv1.RecreateWorktreeRequest{WorktreeId: wt.ID}))
	require.NoError(t, err)
	assert.Contains(t, resp.Msg.SnapshotWarning, "branch moved")
	require.Len(t, resp.Msg.SnapshotRefs, 1)
	after, _ := e.repo.GetWorktree(e.ctx, wt.ID)
	require.NotNil(t, after.CleanupMetadata)
	assert.Equal(t, resp.Msg.SnapshotRefs, after.CleanupMetadata.SnapshotRefs)
	assert.NoFileExists(t, filepath.Join(path, "wip.txt"))
}

// R-S2: unarchiving a workspace whose directory a removal already finished is
// refused, and the row records that the directory is gone.
func TestUnarchive_RefusedWhenTheMachineSaysGone(t *testing.T) {
	e := newReclaimEnv(t)
	wt, path := e.worktree(t, "gonerow")
	require.NoError(t, e.repo.ArchiveWorktree(e.ctx, wt.ID))
	age(t, path)
	require.NoError(t, e.sweeper.Sweep(e.ctx)) // removes it, but the server has not recorded it yet:
	require.NoError(t, e.repo.MergeWorktreeCleanupMetadata(e.ctx, wt.ID, func(m *core.CleanupMetadata, _ bool) bool {
		m.DirectoryDeleted = false
		return true
	}))
	require.False(t, exists(path))
	_, err := e.svc.UnarchiveWorktree(e.ctx, connect.NewRequest(&reliantv1.UnarchiveWorktreeRequest{WorktreeId: wt.ID}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	got, _ := e.repo.GetWorktree(e.ctx, wt.ID)
	assert.NotNil(t, got.DeletedAt, "the row must NOT flip to active over a missing directory")
	assert.True(t, got.CleanupMetadata.DirectoryDeleted, "and it now records that the files are gone")
}

// R-S2: an unreachable machine does not block the unarchive; it records the
// fence to retire so nothing is removed meanwhile.
func TestUnarchive_UnreachableMachineLeavesTheRowArchived(t *testing.T) {
	e := newReclaimEnv(t)
	wt, _ := e.worktree(t, "offlinerestore")
	require.NoError(t, e.repo.ArchiveWorktree(e.ctx, wt.ID))
	e.svc = NewWorktreeService(e.repo, nil, &failingRouter{}).WithSettler(e.sweeper)
	_, err := e.svc.UnarchiveWorktree(e.ctx, connect.NewRequest(&reliantv1.UnarchiveWorktreeRequest{WorktreeId: wt.ID}))
	require.Error(t, err)
	got, _ := e.repo.GetWorktree(e.ctx, wt.ID)
	assert.NotNil(t, got.DeletedAt, "an unverified re-lock must not make the row active")
}

type failingRouter struct{ worktreeTestDaemonRouter }

func (f *failingRouter) SendDaemonCommandToDaemon(context.Context, string, string, string, []byte, int32) ([]byte, error) {
	return nil, assert.AnError
}

// R4-B1: restoring a workspace whose checkout is parked brings it home (origin
// free), or keeps the row archived and says where it is parked (origin taken).
func TestUnarchive_OfAParkedRow(t *testing.T) {
	park := func(t *testing.T) (e *reclaimEnv, wt *db.Worktree, path, q string) {
		e = newReclaimEnv(t)
		wt, path = e.worktree(t, "parkedrestore")
		require.NoError(t, e.repo.ArchiveWorktree(e.ctx, wt.ID))
		row, err := e.repo.GetWorktree(e.ctx, wt.ID)
		require.NoError(t, err)
		claim := worktreereclaim.ArchivedLockReason(wt.ID, strconv.FormatInt(row.DeletedAt.UnixNano(), 10))
		// The daemon's reclaimer already exists (its start-up recovery has run).
		_, _ = e.router.SendDaemonCommand(context.Background(), e.userID, "worktree.reconcile",
			[]byte(fmt.Sprintf(`{"worktrees":[{"id":"warm","path":%q,"state":"active"}]}`, filepath.Join(e.root, "proj", "nothing"))), 0)
		gitIn(t, e.project.Path, "worktree", "unlock", path)
		q = filepath.Join(e.root, ".reclaim", wt.ID, fmt.Sprintf("root-%d-cafef00d", time.Now().UnixNano()))
		require.NoError(t, os.MkdirAll(filepath.Dir(q), 0o755))
		require.NoError(t, os.WriteFile(q+".origin", []byte(path), 0o644))
		gitIn(t, e.project.Path, "worktree", "move", path, q)
		gitIn(t, e.project.Path, "worktree", "lock", "--reason", claim, q)
		return
	}

	t.Run("origin free: brought home and active", func(t *testing.T) {
		e, wt, path, q := park(t)
		_, err := e.svc.UnarchiveWorktree(e.ctx, connect.NewRequest(&reliantv1.UnarchiveWorktreeRequest{WorktreeId: wt.ID}))
		require.NoError(t, err)
		after, _ := e.repo.GetWorktree(e.ctx, wt.ID)
		assert.Nil(t, after.DeletedAt)
		assert.True(t, exists(path), "the workspace must have its files back")
		assert.False(t, exists(q))
	})

	t.Run("origin taken: stays archived and names the parked path", func(t *testing.T) {
		e, wt, path, q := park(t)
		require.NoError(t, os.MkdirAll(path, 0o755))
		_, err := e.svc.UnarchiveWorktree(e.ctx, connect.NewRequest(&reliantv1.UnarchiveWorktreeRequest{WorktreeId: wt.ID}))
		require.Error(t, err)
		assert.Contains(t, err.Error(), q)
		after, _ := e.repo.GetWorktree(e.ctx, wt.ID)
		assert.NotNil(t, after.DeletedAt, "the row must stay archived")
		assert.True(t, exists(q), "the parked copy is untouched")
	})
}
