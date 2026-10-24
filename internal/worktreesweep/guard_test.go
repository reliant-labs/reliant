// Copyright (c) 2025 Reliant Labs
package worktreesweep

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/diskwatch"
	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

// A machine whose disk guard starts acting has its kept workspaces re-checked
// at the next pass, whatever their back-off — once per episode, not every pass.
func TestSweep_DiskGuardEpisodeRechecksHeldRowsOnce(t *testing.T) {
	testkit.Slow(t, "builds real git worktrees and runs git against them")
	t.Parallel()

	e := newEnv(t)
	_, path := e.worktree(t, "held", true)
	require.NoError(t, os.WriteFile(filepath.Join(path, "wip.txt"), []byte("x"), 0o644))
	require.NoError(t, e.sweeper.Sweep(e.ctx))
	first := len(e.sender.calls)
	require.Positive(t, first)
	require.NoError(t, e.sweeper.Sweep(e.ctx))
	require.Equal(t, first, len(e.sender.calls), "a freshly held worktree backs off")

	store := func(g reliantv1.DiskGuardState, prev *reliantv1.DiskReport) *reliantv1.DiskReport {
		rep := diskwatch.Stamp(&reliantv1.DiskReport{Volumes: []*reliantv1.DiskVolume{
			{Label: "home", Paths: []string{"/home/workspace"}, TotalBytes: 400 * diskwatch.GiB, FreeBytes: 30 * diskwatch.GiB, Guard: g},
		}}, prev, time.Now())
		raw, err := diskwatch.Encode(rep)
		require.NoError(t, err)
		require.NoError(t, e.repo.SetDaemonStorageState(e.ctx, e.daemon, raw))
		return rep
	}

	episode := store(reliantv1.DiskGuardState_DISK_GUARD_STATE_RECLAIMING, nil)
	require.NoError(t, e.sweeper.Sweep(e.ctx))
	second := len(e.sender.calls)
	assert.Greater(t, second, first, "the guard's episode re-checks the held worktree now")

	store(reliantv1.DiskGuardState_DISK_GUARD_STATE_REFUSING_HEAVY, episode)
	require.NoError(t, e.sweeper.Sweep(e.ctx))
	assert.Equal(t, second, len(e.sender.calls), "the same episode does not re-check it again")
}

// The same property without a database: a held row, backed off, is asked
// about again once its machine's guard episode begins, and only once.
func TestSweep_DiskGuardEpisodeRechecksHeldRowsOnce_Fake(t *testing.T) {
	t.Parallel()

	store := newFakeStore("d")
	store.add("w", true, nil)
	sender := &scriptSender{answer: func(w worktreereclaim.Worktree) worktreereclaim.Result {
		return worktreereclaim.Result{ID: w.ID, Outcome: worktreereclaim.OutcomeHeld, Reason: worktreereclaim.ReasonDirty}
	}}
	s := New(store, sender)
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	asked := func() int { sender.mu.Lock(); defer sender.mu.Unlock(); return len(sender.requests) }

	require.NoError(t, s.Sweep(context.Background()))
	require.Equal(t, 1, asked())
	require.NoError(t, s.Sweep(context.Background()))
	require.Equal(t, 1, asked(), "held: backed off")

	rep := diskwatch.Stamp(&reliantv1.DiskReport{Volumes: []*reliantv1.DiskVolume{
		{Label: "home", TotalBytes: 400 * diskwatch.GiB, FreeBytes: 30 * diskwatch.GiB, Guard: reliantv1.DiskGuardState_DISK_GUARD_STATE_RECLAIMING},
	}}, nil, now.Add(time.Minute))
	raw, err := diskwatch.Encode(rep)
	require.NoError(t, err)
	store.mu.Lock()
	store.disk = map[string]string{"d": raw}
	store.mu.Unlock()
	now = now.Add(2 * time.Minute)

	require.NoError(t, s.Sweep(context.Background()))
	assert.Equal(t, 2, asked(), "the guard's episode re-checks it now")
	require.NoError(t, s.Sweep(context.Background()))
	assert.Equal(t, 2, asked(), "and not again in the same episode")
}
