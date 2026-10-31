// Copyright (c) 2025 Reliant Labs
package worktreesweep

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

// daemonSender is a Sender whose "daemon" is the real worktreereclaim code
// running against real git repositories on this machine, reached through the
// same JSON the wire carries.
type daemonSender struct {
	reclaimer *worktreereclaim.Reclaimer
	mu        sync.Mutex
	calls     []string
}

func (d *daemonSender) SendDaemonCommandToDaemon(ctx context.Context, _, _, cmd string, payload []byte, _ int32) ([]byte, error) {
	d.mu.Lock()
	d.calls = append(d.calls, cmd)
	d.mu.Unlock()
	var req worktreereclaim.Request
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, err
	}
	switch cmd {
	case cmdReconcile:
		return json.Marshal(d.reclaimer.Reconcile(ctx, req))
	case cmdSnapshotRemove:
		return json.Marshal(d.reclaimer.SnapshotRemove(ctx, req))
	}
	panic("unexpected command " + cmd)
}

// idleLease is the runtime admission of a machine running nothing.
type idleLease struct{}

func (idleLease) Check(context.Context) error { return nil }
func (idleLease) Release()                    {}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e.com")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

type env struct {
	repo    *db.Repo
	sweeper *Sweeper
	sender  *daemonSender
	userID  string
	daemon  string
	project *db.Project
	root    string
	ctx     context.Context
}

func newEnv(t *testing.T) *env {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	repo := db.NewTestRepo(t)
	base := t.TempDir()
	e := &env{repo: repo, userID: uuid.NewString(), daemon: "d-" + uuid.NewString(), root: filepath.Join(base, "worktrees"), ctx: context.Background()}

	projectPath := filepath.Join(base, "project")
	git(t, base, "init", "-q", "-b", "main", projectPath)
	require.NoError(t, os.WriteFile(filepath.Join(projectPath, "a.txt"), []byte("one\n"), 0o644))
	git(t, projectPath, "add", ".")
	git(t, projectPath, "commit", "-q", "-m", "init")

	now := time.Now().UTC()
	e.project = &db.Project{ID: "p-" + e.userID, UserID: e.userID, Name: "proj", Path: projectPath, CreatedAt: now, UpdatedAt: now, LastActive: now}
	require.NoError(t, repo.CreateProject(e.ctx, e.project))
	require.NoError(t, repo.UpsertDaemon(e.ctx, &db.Daemon{ID: e.daemon, UserID: e.userID}))
	require.NoError(t, repo.UpsertDaemonAttachment(e.ctx, &db.DaemonAttachment{DaemonID: e.daemon, UserID: e.userID, Source: db.DaemonAttachmentSourceInbound}))

	if err := os.MkdirAll(e.root, 0o755); err != nil {
		t.Fatal(err)
	}
	// The daemon's runtime admission and process-use check (cmd_worktree.go),
	// as an idle machine answers them: nothing runs in any worktree.
	e.sender = &daemonSender{reclaimer: &worktreereclaim.Reclaimer{
		Root:       e.root,
		Cwds:       func(context.Context) ([]string, error) { return nil, nil },
		Acquire:    func(context.Context, []string) (worktreereclaim.RecoveryLease, error) { return idleLease{}, nil },
		ProcessUse: func(context.Context, []string) error { return nil },
	}}
	e.sweeper = New(repo, e.sender)
	return e
}

// worktree creates a real, locked git worktree and its row.
func (e *env) worktree(t *testing.T, name string, archived bool) (*db.Worktree, string) {
	t.Helper()
	id := uuid.NewString()
	path := filepath.Join(e.root, "proj", name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	git(t, e.project.Path, "worktree", "add", "-q", "-b", "feat/"+name, path, "main")
	require.NoError(t, worktreereclaim.LockCheckout(e.ctx, path, id))
	now := time.Now().UTC()
	wt := &db.Worktree{ID: id, Name: name, Path: path, Branch: "feat/" + name, BaseBranch: "main", ProjectID: e.project.ID,
		DaemonID: &e.daemon, Status: 1, CreatedAt: now, UpdatedAt: now, LastActive: now}
	require.NoError(t, e.repo.CreateWorktree(e.ctx, wt))
	if archived {
		require.NoError(t, e.repo.ArchiveWorktree(e.ctx, id))
	}
	return wt, path
}

func (e *env) get(t *testing.T, id string) *db.Worktree {
	t.Helper()
	wt, err := e.repo.GetWorktree(e.ctx, id)
	require.NoError(t, err)
	return wt
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func TestSweep_RemovesCleanArchivedAndRecordsIt(t *testing.T) {
	testkit.Slow(t, "builds real git worktrees and runs git against them")
	t.Parallel()

	e := newEnv(t)
	wt, path := e.worktree(t, "clean", true)
	require.NoError(t, e.sweeper.Sweep(e.ctx))
	assert.False(t, exists(path), "clean merged archived worktree must be removed")
	got := e.get(t, wt.ID)
	require.NotNil(t, got.CleanupMetadata)
	assert.True(t, got.CleanupMetadata.DirectoryDeleted)
	assert.Empty(t, got.CleanupMetadata.HeldReason)
	git(t, e.project.Path, "rev-parse", "--verify", "refs/heads/feat/clean") // branch survives
}

func TestSweep_HoldsDirtyAndRecordsReasonAndSize(t *testing.T) {
	testkit.Slow(t, "builds real git worktrees and runs git against them")
	t.Parallel()

	e := newEnv(t)
	wt, path := e.worktree(t, "dirty", true)
	require.NoError(t, os.WriteFile(filepath.Join(path, "wip.txt"), []byte("not committed\n"), 0o644))
	require.NoError(t, e.sweeper.Sweep(e.ctx))
	assert.True(t, exists(path), "dirty worktree must be kept")
	got := e.get(t, wt.ID)
	require.NotNil(t, got.CleanupMetadata)
	assert.False(t, got.CleanupMetadata.DirectoryDeleted)
	assert.Equal(t, "dirty", got.CleanupMetadata.HeldReason)
	assert.Positive(t, got.CleanupMetadata.SizeBytes)
}

func TestSweep_LocksActiveWorktreesAndNeverRemovesThem(t *testing.T) {
	testkit.Slow(t, "builds real git worktrees and runs git against them")
	t.Parallel()

	e := newEnv(t)
	wt, path := e.worktree(t, "live", false)
	git(t, e.project.Path, "worktree", "unlock", path)
	require.NoError(t, e.sweeper.Sweep(e.ctx))
	assert.True(t, exists(path))
	assert.Contains(t, git(t, e.project.Path, "worktree", "list", "--porcelain"), "locked reliant: worktree "+wt.ID)
	meta := e.get(t, wt.ID).CleanupMetadata
	require.NotNil(t, meta)
	assert.NotNil(t, meta.LockedAt, "the lock is recorded so the sweep need not ask again for a day")
	assert.False(t, meta.DirectoryDeleted, "an active row never records a removal")
}

func TestSweep_AdoptsUnattributedRows(t *testing.T) {
	testkit.Slow(t, "builds real git worktrees and runs git against them")
	t.Parallel()

	e := newEnv(t)
	wt, path := e.worktree(t, "legacy", true)
	_, err := e.repo.DB.ExecContext(e.ctx, `UPDATE worktrees SET daemon_id = NULL WHERE id = $1`, wt.ID)
	require.NoError(t, err)
	require.NoError(t, e.sweeper.Sweep(e.ctx))
	assert.False(t, exists(path))
	got := e.get(t, wt.ID)
	require.NotNil(t, got.DaemonID, "the daemon that found the directory is recorded as its owner")
	assert.Equal(t, e.daemon, *got.DaemonID)
}

func TestSweep_SkipsOfflineDaemon(t *testing.T) {
	testkit.Slow(t, "builds real git worktrees and runs git against them")
	t.Parallel()

	e := newEnv(t)
	_, path := e.worktree(t, "offline", true)
	_, err := e.repo.DB.ExecContext(e.ctx, `DELETE FROM daemon_attachment WHERE daemon_id = $1`, e.daemon)
	require.NoError(t, err)
	require.NoError(t, e.sweeper.Sweep(e.ctx))
	assert.True(t, exists(path))
	assert.Empty(t, e.sender.calls, "an offline daemon is not asked")
}

func TestSettle_RemovesImmediately(t *testing.T) {
	testkit.Slow(t, "builds real git worktrees and runs git against them")
	t.Parallel()

	e := newEnv(t)
	wt, path := e.worktree(t, "now", true)
	e.sweeper.Settle(e.ctx, e.userID, &core.ReclaimCandidate{Worktree: e.get(t, wt.ID), OwnerUserID: e.userID, ProjectPath: e.project.Path})
	assert.False(t, exists(path))
}

func TestStorageView_ShowsHeldAndLowDiskPerMachine(t *testing.T) {
	testkit.Slow(t, "builds real git worktrees and runs git against them")
	t.Parallel()

	e := newEnv(t)
	_, path := e.worktree(t, "held", true)
	require.NoError(t, os.WriteFile(filepath.Join(path, "wip.txt"), []byte("x"), 0o644))
	require.NoError(t, e.sweeper.Sweep(e.ctx))

	view, err := StorageView(e.ctx, e.repo, e.userID)
	require.NoError(t, err)
	require.Len(t, view, 1, "one entry per machine")
	assert.Equal(t, e.daemon, view[0].DaemonID)
	require.Len(t, view[0].Held, 1)
	assert.Equal(t, "dirty", view[0].Held[0].Reason)
	assert.True(t, view[0].Online)
	assert.False(t, view[0].DiskLow(), "kept workspaces alone are not low disk")

	// A second held worktree joins the same machine's entry.
	_, path2 := e.worktree(t, "held2", true)
	require.NoError(t, os.WriteFile(filepath.Join(path2, "wip.txt"), []byte("x"), 0o644))
	require.NoError(t, e.sweeper.Sweep(e.ctx))
	view, err = StorageView(e.ctx, e.repo, e.userID)
	require.NoError(t, err)
	require.Len(t, view, 1, "still one entry per machine")
	assert.Len(t, view[0].Held, 2)
}

func TestStorageView_HealthyDiskWithNothingHeldHasNoItem(t *testing.T) {
	testkit.Slow(t, "builds real git worktrees and runs git against them")
	t.Parallel()

	e := newEnv(t)
	e.worktree(t, "fine", true)
	require.NoError(t, e.sweeper.Sweep(e.ctx))
	view, err := StorageView(e.ctx, e.repo, e.userID)
	require.NoError(t, err)
	assert.Empty(t, view)
}

func TestCleanup_SnapshotsThenRemovesOnlyWhatWasConfirmed(t *testing.T) {
	testkit.Slow(t, "builds real git worktrees and runs git against them")
	t.Parallel()

	e := newEnv(t)
	wtA, pathA := e.worktree(t, "a", true)
	wtB, pathB := e.worktree(t, "b", true)
	for _, p := range []string{pathA, pathB} {
		require.NoError(t, os.WriteFile(filepath.Join(p, "wip.txt"), []byte("precious\n"), 0o644))
	}
	require.NoError(t, e.sweeper.Sweep(e.ctx))
	require.True(t, exists(pathA) && exists(pathB))

	results, freed, err := e.sweeper.Cleanup(e.ctx, e.repo, e.userID, e.daemon, []string{wtA.ID, "not-a-worktree"})
	require.NoError(t, err)
	byID := map[string]CleanupResult{}
	for _, r := range results {
		byID[r.WorktreeID] = r
	}
	require.Equal(t, CleanupRemoved, byID[wtA.ID].Outcome, "%+v", byID[wtA.ID])
	require.Len(t, byID[wtA.ID].SnapshotRefs, 1)
	refA := byID[wtA.ID].SnapshotRefs[0]
	assert.Contains(t, refA, "refs/reliant/wip/"+wtA.ID+"/root/")
	assert.Equal(t, CleanupSkipped, byID["not-a-worktree"].Outcome)
	assert.Positive(t, freed)
	assert.False(t, exists(pathA))
	assert.True(t, exists(pathB), "a worktree the user did not confirm must be left alone")

	assert.Equal(t, "precious", git(t, e.project.Path, "show", refA+":wip.txt"))
	got := e.get(t, wtA.ID)
	assert.True(t, got.CleanupMetadata.DirectoryDeleted)
	assert.Equal(t, []string{refA}, got.CleanupMetadata.SnapshotRefs)
	assert.False(t, e.get(t, wtB.ID).CleanupMetadata.DirectoryDeleted)
}

func TestCleanup_OfflineMachineIsRefused(t *testing.T) {
	testkit.Slow(t, "builds real git worktrees and runs git against them")
	t.Parallel()

	e := newEnv(t)
	wt, path := e.worktree(t, "off", true)
	require.NoError(t, os.WriteFile(filepath.Join(path, "wip.txt"), []byte("x"), 0o644))
	require.NoError(t, e.sweeper.Sweep(e.ctx))
	_, err := e.repo.DB.ExecContext(e.ctx, `DELETE FROM daemon_attachment WHERE daemon_id = $1`, e.daemon)
	require.NoError(t, err)
	_, _, err = e.sweeper.Cleanup(e.ctx, e.repo, e.userID, e.daemon, []string{wt.ID})
	assert.ErrorIs(t, err, ErrOffline)
	assert.True(t, exists(path))
}

func TestCleanup_NeverActsOnAnotherUsersWorktree(t *testing.T) {
	testkit.Slow(t, "builds real git worktrees and runs git against them")
	t.Parallel()

	e := newEnv(t)
	wt, path := e.worktree(t, "mine", true)
	require.NoError(t, os.WriteFile(filepath.Join(path, "wip.txt"), []byte("x"), 0o644))
	require.NoError(t, e.sweeper.Sweep(e.ctx))
	results, _, err := e.sweeper.Cleanup(e.ctx, e.repo, uuid.NewString(), e.daemon, []string{wt.ID})
	if err == nil {
		for _, r := range results {
			assert.NotEqual(t, CleanupRemoved, r.Outcome)
		}
	}
	assert.True(t, exists(path))
}

func TestSweep_DoesNotShedFilesFromDirtyRecoveryCandidate(t *testing.T) {
	testkit.Slow(t, "builds real git worktrees and runs git against them")
	t.Parallel()

	e := newEnv(t)
	wt, path := e.worktree(t, "shed", true)
	// Ignored directory naming alone is not durable eligibility proof. Dirty
	// recovery candidates must not be stripped while preservation is held.
	require.NoError(t, os.WriteFile(filepath.Join(path, ".gitignore"), []byte("node_modules/\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(path, "node_modules", "pkg"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(path, "node_modules", "pkg", "index.js"), []byte("module.exports = 1\n"), 0o644))
	require.NoError(t, e.sweeper.Sweep(e.ctx))

	assert.True(t, exists(filepath.Join(path, ".gitignore")), "the work is kept")
	assert.True(t, exists(filepath.Join(path, "node_modules")), "held recovery candidates retain ignored content")
	data, err := os.ReadFile(filepath.Join(path, "node_modules", "pkg", "index.js"))
	require.NoError(t, err)
	assert.Equal(t, "module.exports = 1\n", string(data))
	meta := e.get(t, wt.ID).CleanupMetadata
	require.NotNil(t, meta)
	assert.Equal(t, "dirty", meta.HeldReason)
	assert.Empty(t, meta.StrippedPaths)
	assert.Zero(t, meta.StrippedBytes)
	assert.False(t, meta.DirectoryDeleted)
}

func TestSweep_UnpushedCommitsRequireRemoteRecoveryProof(t *testing.T) {
	testkit.Slow(t, "builds real git worktrees and runs git against them")
	t.Parallel()

	e := newEnv(t)
	wt, path := e.worktree(t, "unpushed", false)
	git(t, path, "checkout", "-q", "--detach")
	require.NoError(t, os.WriteFile(filepath.Join(path, "only-here.txt"), []byte("x\n"), 0o644))
	git(t, path, "add", ".")
	git(t, path, "commit", "-q", "-m", "local only")
	sha := git(t, path, "rev-parse", "HEAD")
	require.NoError(t, e.repo.ArchiveWorktree(e.ctx, wt.ID))

	require.NoError(t, e.sweeper.Sweep(e.ctx))
	assert.True(t, exists(path), "local-only history must remain without an independent remote receipt")
	meta := e.get(t, wt.ID).CleanupMetadata
	require.NotNil(t, meta)
	assert.False(t, meta.DirectoryDeleted)
	assert.NotEmpty(t, meta.HeldReason)
	assert.Empty(t, meta.SnapshotRefs, "local refs cannot claim durable preservation")
	assert.Equal(t, sha, git(t, path, "rev-parse", "HEAD"))
	data, err := os.ReadFile(filepath.Join(path, "only-here.txt"))
	require.NoError(t, err)
	assert.Equal(t, "x\n", string(data))
}
