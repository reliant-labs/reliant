// Copyright (c) 2025 Reliant Labs
package worktreesweep

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/diskwatch"
	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

// shedFloors describes every row of store and returns each row's
// ShedAfterSeconds.
func shedFloors(t *testing.T, s *Sweeper, store *fakeStore, keep bool) map[string]int64 {
	t.Helper()
	cands, err := store.ListWorktreesForReclaim(context.Background())
	require.NoError(t, err)
	req, _ := s.describe(context.Background(), cands, nil, keep, s.runActivity(context.Background(), cands))
	out := map[string]int64{}
	for _, w := range req.Worktrees {
		out[w.ID] = w.ShedAfterSeconds
	}
	return out
}

// A kept row's regenerable output is shed: a live one no chat runs in, and
// an archived one (the daemon sheds only those held for their work). Never a
// running row, the main checkout, "Keep everything", or below "on".
func TestDescribe_AsksToShedKeptRowsOnlyWhenOn(t *testing.T) {
	t.Parallel()
	for _, mode := range []AutoArchiveMode{AutoArchiveDryRun, AutoArchiveOn} {
		for _, keep := range []bool{false, true} {
			store := newFakeStore("d")
			store.add("idle", false, nil)
			store.add("running", false, nil)
			store.add("archived", true, nil)
			store.add("main", false, nil).IsMain = true
			store.activity = map[string]core.WorktreeRunActivity{"running": {Running: true}}
			s := New(store, &scriptSender{answer: locked}).WithAutoArchive(mode, store, nil)
			want := map[string]int64{"idle": 0, "running": 0, "archived": 0, "main": 0}
			if mode == AutoArchiveOn && !keep {
				want["idle"], want["archived"] = int64(shedQuiet/time.Second), int64(shedQuiet/time.Second)
			}
			assert.Equal(t, want, shedFloors(t, s, store, keep), "mode=%s keep=%v", mode, keep)
		}
	}
}

// While the machine's disk guard acts, every row it holds — live ones too —
// is asked again at once, with the shorter floors. Before, pressure re-asked
// only archived held rows, never the live ones that held the space
// (2026-10-30: 591 live rows, 103 GiB).
func TestSweep_DiskGuardRechecksLiveRowsWithShorterFloors(t *testing.T) {
	t.Parallel()
	store := newFakeStore("d")
	store.add("live", false, nil)
	var mu sync.Mutex
	var seen []worktreereclaim.Worktree
	sender := &scriptSender{answer: func(w worktreereclaim.Worktree) worktreereclaim.Result {
		mu.Lock()
		seen = append(seen, w)
		mu.Unlock()
		return locked(w)
	}}
	s := New(store, sender).WithAutoArchive(AutoArchiveOn, store, nil).WithIdleRetirement(24*time.Hour, &runningStore{})
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	asked := func() []worktreereclaim.Worktree {
		mu.Lock()
		defer mu.Unlock()
		return append([]worktreereclaim.Worktree(nil), seen...)
	}

	require.NoError(t, s.Sweep(context.Background()))
	require.Len(t, asked(), 1)
	assert.Equal(t, int64(shedQuiet/time.Second), asked()[0].ShedAfterSeconds)
	assert.Equal(t, int64(24*time.Hour/time.Second), asked()[0].ProbeRecoverableAfterSeconds)
	now = now.Add(10 * time.Minute)
	require.NoError(t, s.Sweep(context.Background()))
	require.Len(t, asked(), 1, "a just-locked live row waits its recheck interval")

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
	require.Len(t, asked(), 2, "the guard's episode re-checks the live row now")
	assert.Equal(t, int64(guardShedQuiet/time.Second), asked()[1].ShedAfterSeconds)
	assert.Equal(t, int64(guardRecoverableIdle/time.Second), asked()[1].ProbeRecoverableAfterSeconds)
	require.NoError(t, s.Sweep(context.Background()))
	assert.Len(t, asked(), 2, "and not again in the same episode")
}
