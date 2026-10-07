// Copyright (c) 2025 Reliant Labs
package worktreesweep

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

// S1: the user restores the workspace between the daemon claiming it and the
// removal request. The server re-reads the row and must not send the removal.
func TestSweep_UnarchiveBetweenPhasesNeverRemoves(t *testing.T) {
	e := newEnv(t)
	wt, path := e.worktree(t, "race", true)
	var phases atomic.Int32
	e.sweeper = New(e.repo, senderFunc(func(ctx context.Context, u, d, cmd string, payload []byte, to int32) ([]byte, error) {
		out, err := e.sender.SendDaemonCommandToDaemon(ctx, u, d, cmd, payload, to)
		if phases.Add(1) == 1 {
			require.NoError(t, e.repo.UnarchiveWorktree(e.ctx, wt.ID)) // the user clicks Restore
		}
		return out, err
	}))
	require.NoError(t, e.sweeper.Sweep(e.ctx))
	assert.True(t, exists(path), "a restored workspace's directory must survive")
	assert.EqualValues(t, 1, phases.Load(), "phase two was never sent")
	assert.False(t, e.get(t, wt.ID).CleanupMetadata != nil && e.get(t, wt.ID).CleanupMetadata.DirectoryDeleted)
}

type senderFunc func(ctx context.Context, u, d, cmd string, payload []byte, to int32) ([]byte, error)

func (f senderFunc) SendDaemonCommandToDaemon(ctx context.Context, u, d, cmd string, payload []byte, to int32) ([]byte, error) {
	return f(ctx, u, d, cmd, payload, to)
}

// S1: the same race, with the unarchive landing AFTER phase two was built: the
// daemon refuses because the unarchive re-locked the checkout as active.
func TestSweep_RelockedAsActiveRefusesAStaleRemoval(t *testing.T) {
	e := newEnv(t)
	wt, path := e.worktree(t, "relock", true)
	cands, err := e.repo.ListWorktreesForReclaim(e.ctx)
	require.NoError(t, err)
	var cand *core.ReclaimCandidate
	for _, c := range cands {
		if c.Worktree.ID == wt.ID {
			cand = c
		}
	}
	require.NotNil(t, cand)
	live, _ := e.repo.ListLiveWorktreePathsForUser(e.ctx, e.userID)
	req := e.sweeper.describe(e.ctx, []*core.ReclaimCandidate{cand}, live, false)
	_, err = e.sweeper.send(e.ctx, target{e.userID, e.daemon}, cmdReconcile, 60000, req) // phase one: claims
	require.NoError(t, err)
	require.NoError(t, worktreereclaim.RestoreCheckout(e.ctx, path, wt.ID, Fence(e.get(t, wt.ID)))) // restore re-locks as active and retires the fence
	req.Worktrees[0].Remove = true
	resp, err := e.sweeper.send(e.ctx, target{e.userID, e.daemon}, cmdReconcile, 60000, req)
	require.NoError(t, err)
	assert.NotEqual(t, worktreereclaim.OutcomeRemoved, resp.Results[0].Outcome)
	assert.True(t, exists(path))
}

// S2: another live row at the same path means the archived row is described as
// active, so nothing is removed.
func TestSweep_SharedPathWithALiveRowIsNeverRemoved(t *testing.T) {
	e := newEnv(t)
	arch, path := e.worktree(t, "shared", true)
	now := time.Now().UTC()
	live := &db.Worktree{ID: "live-" + arch.ID, Name: "imported", Path: path, Branch: "b", BaseBranch: "main", ProjectID: e.project.ID,
		DaemonID: &e.daemon, Status: 1, CreatedAt: now, UpdatedAt: now, LastActive: now}
	require.NoError(t, e.repo.CreateWorktree(e.ctx, live))
	require.NoError(t, e.sweeper.Sweep(e.ctx))
	assert.True(t, exists(path))
	assert.True(t, sharesPathWithLive(arch, []core.WorktreePath{{ID: live.ID, Path: path}}))
	assert.True(t, sharesPathWithLive(arch, []core.WorktreePath{{ID: "x", Path: filepath.Dir(path)}}), "a live row containing it")
	assert.True(t, sharesPathWithLive(arch, []core.WorktreePath{{ID: "x", Path: filepath.Join(path, "sub")}}), "a live row inside it")
	assert.False(t, sharesPathWithLive(arch, []core.WorktreePath{{ID: "x", Path: path + "-other"}}), "a sibling with a common prefix is not an overlap")
}

// S7: Keep everything removes nothing automatically, yet still reports holds.
func TestSweep_KeepEverythingNeverRemovesButStillHolds(t *testing.T) {
	e := newEnv(t)
	e.sweeper.WithKeepFiles(keepAll{})
	clean, cleanPath := e.worktree(t, "keepclean", true)
	dirty, dirtyPath := e.worktree(t, "keepdirty", true)
	require.NoError(t, os.WriteFile(filepath.Join(dirtyPath, "wip.txt"), []byte("x"), 0o644))
	require.NoError(t, e.sweeper.Sweep(e.ctx))
	assert.True(t, exists(cleanPath), "keep everything: a clean worktree stays")
	assert.False(t, e.get(t, clean.ID).CleanupMetadata != nil && e.get(t, clean.ID).CleanupMetadata.DirectoryDeleted)
	assert.Equal(t, "dirty", e.get(t, dirty.ID).CleanupMetadata.HeldReason)
}

type keepAll struct{}

func (keepAll) KeepFiles(context.Context, string) bool { return true }

// S8: only the replica holding the advisory lock sweeps.
func TestSweepAsLeader_OnlyOneReplicaSweeps(t *testing.T) {
	e := newEnv(t)
	_, path := e.worktree(t, "leader", true)
	release, ok, err := e.repo.TryAdvisoryLock(e.ctx, sweepLockKey)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, e.sweeper.SweepAsLeader(e.ctx))
	assert.True(t, exists(path), "a replica that does not hold the lock must not sweep")
	assert.Empty(t, e.sender.calls)
	release()
	require.NoError(t, e.sweeper.SweepAsLeader(e.ctx))
	assert.False(t, exists(path), "once the lock is free a replica sweeps")
}

// S8: a held row is not re-checked every pass.
func TestSweep_HeldRowsBackOff(t *testing.T) {
	e := newEnv(t)
	_, path := e.worktree(t, "backoff", true)
	require.NoError(t, os.WriteFile(filepath.Join(path, "wip.txt"), []byte("x"), 0o644))
	require.NoError(t, e.sweeper.Sweep(e.ctx))
	first := len(e.sender.calls)
	require.Positive(t, first)
	require.NoError(t, e.sweeper.Sweep(e.ctx))
	assert.Equal(t, first, len(e.sender.calls), "a freshly held worktree is not asked about again")
	e.sweeper.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	require.NoError(t, e.sweeper.Sweep(e.ctx))
	assert.Greater(t, len(e.sender.calls), first, "it is re-checked once the backoff passes")
	d1, d2 := holdBackoff(&core.CleanupMetadata{}), holdBackoff(&core.CleanupMetadata{Rechecks: 3})
	assert.Less(t, d1, d2)
	assert.LessOrEqual(t, holdBackoff(&core.CleanupMetadata{Rechecks: 99}), 24*time.Hour)
}

// S8: a live row already locked is not re-sent every pass.
func TestSweep_LockedLiveRowsAreNotResentEveryPass(t *testing.T) {
	e := newEnv(t)
	e.worktree(t, "live1", false)
	require.NoError(t, e.sweeper.Sweep(e.ctx))
	first := len(e.sender.calls)
	require.NoError(t, e.sweeper.Sweep(e.ctx))
	assert.Equal(t, first, len(e.sender.calls))
}

// S4: permanent delete settles first; a held directory refuses the delete.
// (Exercised through SettleBlocking, which the service calls.)
func TestSettleBlocking_ReportsHeldAndRemovedForPermanentDelete(t *testing.T) {
	e := newEnv(t)
	dirty, dirtyPath := e.worktree(t, "pd-dirty", true)
	require.NoError(t, os.WriteFile(filepath.Join(dirtyPath, "wip.txt"), []byte("x"), 0o644))
	meta := e.sweeper.SettleBlocking(e.ctx, e.userID, &core.ReclaimCandidate{Worktree: e.get(t, dirty.ID), OwnerUserID: e.userID, ProjectPath: e.project.Path})
	require.NotNil(t, meta)
	assert.False(t, meta.DirectoryDeleted)
	assert.Equal(t, "dirty", meta.HeldReason)

	clean, cleanPath := e.worktree(t, "pd-clean", true)
	meta = e.sweeper.SettleBlocking(e.ctx, e.userID, &core.ReclaimCandidate{Worktree: e.get(t, clean.ID), OwnerUserID: e.userID, ProjectPath: e.project.Path})
	require.NotNil(t, meta)
	assert.True(t, meta.DirectoryDeleted)
	assert.False(t, exists(cleanPath))
}

// S10: a missing root is "foreign" and the row is NOT marked deleted.
func TestSweep_MissingRootDoesNotMarkRowsDeleted(t *testing.T) {
	e := newEnv(t)
	wt, path := e.worktree(t, "unmounted", true)
	require.NoError(t, os.Rename(e.root, e.root+".away"))
	t.Cleanup(func() { _ = os.Rename(e.root+".away", e.root) })
	require.NoError(t, e.sweeper.Sweep(e.ctx))
	meta := e.get(t, wt.ID).CleanupMetadata
	assert.False(t, meta != nil && meta.DirectoryDeleted, "an unmounted volume is not evidence that the directory is gone")
	_ = path
}

// S9
func TestDiskIsLow_Thresholds(t *testing.T) {
	const gib = 1 << 30
	assert.False(t, DiskIsLow(8*gib, 20*gib+gib*0), "a 20 GiB cloud workspace with 40% free is fine")
	assert.True(t, DiskIsLow(1*gib, 20*gib), "5% of a small disk is low")
	assert.False(t, DiskIsLow(30*gib, 256*gib), "30 GiB free of 256 GiB is not an alert under 500 GiB capacity")
	assert.True(t, DiskIsLow(40*gib, 1000*gib), "under 50 GiB on a big disk")
	assert.True(t, DiskIsLow(40*gib, 600*gib), "40 GiB is under 10% of 600")
	assert.False(t, DiskIsLow(120*gib, 1000*gib))
}

// S9: a disk-only alert needs the disk to be critical, since there is nothing
// to act on.
func TestStorageView_DiskOnlyAlertNeedsACriticalDisk(t *testing.T) {
	e := newEnv(t)
	put := func(free, total int64) {
		raw := `{"free_bytes":` + itoa(free) + `,"total_bytes":` + itoa(total) + `,"reported_at":"` + time.Now().UTC().Format(time.RFC3339) + `"}`
		require.NoError(t, e.repo.SetDaemonStorageState(e.ctx, e.daemon, raw))
	}
	const gib = 1 << 30
	put(8*gib, 100*gib) // low, but not critical
	view, err := StorageView(e.ctx, e.repo, e.userID)
	require.NoError(t, err)
	assert.Empty(t, view)
	put(3*gib, 100*gib) // under 5%
	view, err = StorageView(e.ctx, e.repo, e.userID)
	require.NoError(t, err)
	assert.Len(t, view, 1)
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// B2/B1 through the server: data and outside files are listed but not removable,
// and the confirmed clean-up skips them rather than failing.
func TestCleanup_DataAndNestedWorktreesAreListedButNeverRemoved(t *testing.T) {
	e := newEnv(t)
	require.NoError(t, os.WriteFile(filepath.Join(e.project.Path, ".gitignore"), []byte("data/\n"), 0o644))
	git(t, e.project.Path, "add", ".gitignore")
	git(t, e.project.Path, "commit", "-q", "-m", "ignore")
	wt, path := e.worktree(t, "data", true)
	require.NoError(t, os.MkdirAll(filepath.Join(path, "data"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(path, "data", "app.db"), []byte("rows"), 0o644))
	require.NoError(t, e.sweeper.Sweep(e.ctx))
	require.Equal(t, "data", e.get(t, wt.ID).CleanupMetadata.HeldReason)

	view, err := StorageView(e.ctx, e.repo, e.userID)
	require.NoError(t, err)
	require.Len(t, view, 1)
	require.Len(t, view[0].Held, 1)
	assert.False(t, view[0].Held[0].Removable)

	results, _, err := e.sweeper.Cleanup(e.ctx, e.repo, e.userID, e.daemon, []string{wt.ID})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, CleanupSkipped, results[0].Outcome)
	assert.True(t, exists(filepath.Join(path, "data", "app.db")), "data is never removed by Clean up")
}

// S6: StartCleanup returns immediately, runs detached, and records refs.
func TestStartCleanup_RunsDetachedAndRecordsSnapshotRefs(t *testing.T) {
	e := newEnv(t)
	wt, path := e.worktree(t, "async", true)
	require.NoError(t, os.WriteFile(filepath.Join(path, "wip.txt"), []byte("precious"), 0o644))
	require.NoError(t, e.sweeper.Sweep(e.ctx))

	ctx, cancel := context.WithCancel(e.ctx)
	accepted, skipped, err := e.sweeper.StartCleanup(ctx, e.repo, e.userID, e.daemon, []string{wt.ID, "nope"})
	require.NoError(t, err)
	cancel() // the request that started it goes away
	assert.Equal(t, []string{wt.ID}, accepted)
	require.Len(t, skipped, 1)
	assert.Eventually(t, func() bool {
		m := e.get(t, wt.ID).CleanupMetadata
		return m != nil && m.DirectoryDeleted && !m.Cleaning && len(m.SnapshotRefs) == 1
	}, 20*time.Second, 100*time.Millisecond, "the clean-up finishes and its refs are recorded after the request is gone")
	assert.False(t, exists(path))
}

// R-S6: kept worktrees are HELD: visible in the Inbox, removable by Clean up,
// and backed off like any other hold.
func TestSweep_KeptWorktreesAreHeldVisibleAndBackedOff(t *testing.T) {
	e := newEnv(t)
	e.sweeper.WithKeepFiles(keepAll{})
	wt, path := e.worktree(t, "keptheld", true)
	require.NoError(t, e.sweeper.Sweep(e.ctx))
	meta := e.get(t, wt.ID).CleanupMetadata
	require.NotNil(t, meta)
	assert.Equal(t, "kept", meta.HeldReason)
	assert.True(t, exists(path))

	view, err := StorageView(e.ctx, e.repo, e.userID)
	require.NoError(t, err)
	require.Len(t, view, 1, "a kept worktree shows in the inbox")
	require.Len(t, view[0].Held, 1)
	assert.True(t, view[0].Held[0].Removable, "Clean up can remove a kept worktree")

	first := len(e.sender.calls)
	require.NoError(t, e.sweeper.Sweep(e.ctx))
	assert.Equal(t, first, len(e.sender.calls), "kept rows back off like held rows, not every pass")
}

// R-S6: the confirmed Clean up removes a kept worktree (nothing to save).
func TestCleanup_RemovesAKeptWorktree(t *testing.T) {
	e := newEnv(t)
	e.sweeper.WithKeepFiles(keepAll{})
	wt, path := e.worktree(t, "keptclean", true)
	require.NoError(t, e.sweeper.Sweep(e.ctx))
	results, _, err := e.sweeper.Cleanup(e.ctx, e.repo, e.userID, e.daemon, []string{wt.ID})
	require.NoError(t, err)
	require.Equal(t, CleanupRemoved, results[0].Outcome, "%+v", results[0])
	assert.False(t, exists(path))
}

// R-S7: a clean-up flag left by a process that died expires on its own.
func TestCleaning_ExpiresWhenItsOwnerDies(t *testing.T) {
	e := newEnv(t)
	wt, path := e.worktree(t, "orphancleaning", true)
	require.NoError(t, os.WriteFile(filepath.Join(path, "wip.txt"), []byte("x"), 0o644))
	require.NoError(t, e.sweeper.Sweep(e.ctx))

	dead := time.Now().Add(-time.Minute).UTC()
	require.NoError(t, e.repo.MergeWorktreeCleanupMetadata(e.ctx, wt.ID, func(m *core.CleanupMetadata, _ bool) bool {
		m.Cleaning, m.CleaningBy, m.CleaningUntil = true, "a-process-that-died", &dead
		return true
	}))
	cur := e.get(t, wt.ID)
	assert.False(t, cur.CleanupMetadata.CleaningNow(time.Now()), "an expired lease is not cleaning")

	view, err := StorageView(e.ctx, e.repo, e.userID)
	require.NoError(t, err)
	require.Len(t, view[0].Held, 1)
	assert.False(t, view[0].Held[0].Cleaning, "the inbox must not show a dead clean-up as running")

	accepted, _, err := e.sweeper.StartCleanup(e.ctx, e.repo, e.userID, e.daemon, []string{wt.ID})
	require.NoError(t, err)
	assert.Equal(t, []string{wt.ID}, accepted, "Clean up is not blocked by a flag whose owner is gone")
	assert.Eventually(t, func() bool {
		m := e.get(t, wt.ID).CleanupMetadata
		return m != nil && m.DirectoryDeleted
	}, 20*time.Second, 100*time.Millisecond)
}

// R-S7: a live clean-up is recorded with its owner and a future deadline.
func TestCleaning_RecordsOwnerAndDeadline(t *testing.T) {
	e := newEnv(t)
	var m core.CleanupMetadata
	e.sweeper.markCleaning(&m)
	assert.True(t, m.Cleaning)
	assert.NotEmpty(t, m.CleaningBy)
	require.NotNil(t, m.CleaningUntil)
	assert.True(t, m.CleaningNow(time.Now()))
	assert.False(t, m.CleaningNow(time.Now().Add(time.Hour)))
}

// NIT: concurrent merges never lose each other's updates.
func TestMergeMeta_ConcurrentWritersDoNotLoseUpdates(t *testing.T) {
	e := newEnv(t)
	wt, _ := e.worktree(t, "race", true)
	const n = 20
	done := make(chan struct{}, n)
	for i := 0; i < n; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			e.sweeper.mergeMeta(e.ctx, wt, func(m *core.CleanupMetadata) { m.SnapshotRefs = append(m.SnapshotRefs, "ref-"+strconv.Itoa(i)) })
		}()
	}
	for i := 0; i < n; i++ {
		<-done
	}
	assert.Len(t, e.get(t, wt.ID).CleanupMetadata.SnapshotRefs, n, "every writer's ref must survive")
}

// NIT: the live-path check is scoped to the user whose directory it is.
func TestListLiveWorktreePathsForUser_IsScopedToTheUser(t *testing.T) {
	e := newEnv(t)
	_, path := e.worktree(t, "mine", false)
	other := newEnv(t)
	otherPaths, err := other.repo.ListLiveWorktreePathsForUser(other.ctx, other.userID)
	require.NoError(t, err)
	for _, p := range otherPaths {
		assert.NotEqual(t, path, p.Path, "another user's rows must not influence mine")
	}
	mine, err := e.repo.ListLiveWorktreePathsForUser(e.ctx, e.userID)
	require.NoError(t, err)
	var found bool
	for _, p := range mine {
		found = found || p.Path == path
	}
	assert.True(t, found)

	// A tenant with an identical path does not keep my archived row alive.
	arch, archPath := e.worktree(t, "arch", true)
	now := time.Now().UTC()
	require.NoError(t, other.repo.CreateWorktree(other.ctx, &db.Worktree{ID: "x-" + arch.ID, Name: "same", Path: archPath, Branch: "b", BaseBranch: "main",
		ProjectID: other.project.ID, DaemonID: &other.daemon, Status: 1, CreatedAt: now, UpdatedAt: now, LastActive: now}))
	require.NoError(t, e.sweeper.Sweep(e.ctx))
	assert.False(t, exists(archPath), "only MY live rows decide whether my archived directory may go")
}

// R-S2: a restore that cannot reach the machine records the fence to retire; the
// sweep then delivers it and clears it.
func TestSweep_DeliversAPendingFenceRetirement(t *testing.T) {
	e := newEnv(t)
	wt, path := e.worktree(t, "pending", true)
	archived := e.get(t, wt.ID)
	fence := Fence(archived)
	// The machine claims it under that fence.
	live, _ := e.repo.ListLiveWorktreePathsForUser(e.ctx, e.userID)
	req := e.sweeper.describe(e.ctx, []*core.ReclaimCandidate{{Worktree: archived, OwnerUserID: e.userID, ProjectPath: e.project.Path}}, live, false)
	_, err := e.sweeper.send(e.ctx, target{e.userID, e.daemon}, cmdReconcile, 60000, req)
	require.NoError(t, err)

	// The user restores while the machine is unreachable: the row flips with a
	// pending retirement.
	require.NoError(t, e.repo.UnarchiveWorktree(e.ctx, wt.ID))
	require.NoError(t, e.repo.MergeWorktreeCleanupMetadata(e.ctx, wt.ID, func(m *core.CleanupMetadata, _ bool) bool {
		m.RetireFence = fence
		return true
	}))
	require.NoError(t, e.sweeper.Sweep(e.ctx))
	info := lockOf(t, e, path)
	assert.Equal(t, worktreereclaim.RestoredLockReason(wt.ID, fence), info, "the sweep delivered the retirement")
	assert.Empty(t, e.get(t, wt.ID).CleanupMetadata.RetireFence, "and cleared it")
	assert.True(t, exists(path))
}

func lockOf(t *testing.T, e *env, path string) string {
	t.Helper()
	out := git(t, e.project.Path, "worktree", "list", "--porcelain")
	for _, block := range strings.Split(out, "\n\n") {
		if strings.Contains(block, path) {
			for _, line := range strings.Split(block, "\n") {
				if strings.HasPrefix(line, "locked ") {
					return strings.TrimPrefix(line, "locked ")
				}
			}
		}
	}
	return ""
}
