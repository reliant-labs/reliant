// Copyright (c) 2025 Reliant Labs
package worktreesweep

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

// Reclaim on merge, the server half: which live rows the daemon is asked
// about and allowed to shed, and when a landed one is archived.

// landedEnv is one live row, "wt", with a live chat bound to it, whose daemon
// answers landed (with whatever answer returns).
func landedEnv(t *testing.T, mode AutoArchiveMode, answer func(w worktreereclaim.Worktree) worktreereclaim.Result) (*fakeStore, *scriptSender, *Sweeper) {
	t.Helper()
	store := newFakeStore("d")
	id := "wt"
	store.chats = []*core.Chat{{ID: "agent-chat", WorktreeID: &id}}
	store.activity = map[string]core.WorktreeRunActivity{}
	store.add(id, false, nil)
	if answer == nil {
		answer = func(w worktreereclaim.Worktree) worktreereclaim.Result {
			return worktreereclaim.Result{ID: w.ID, Outcome: worktreereclaim.OutcomeLocked, Landed: w.ProbeLanded}
		}
	}
	sender := &scriptSender{seen: map[string]worktreereclaim.Worktree{}, answer: answer}
	s := New(store, sender).WithAutoArchive(mode, store, nil)
	return store, sender, s
}

func quietFor(d time.Duration) core.WorktreeRunActivity {
	return core.WorktreeRunActivity{LastActive: time.Now().Add(-d)}
}

func TestLanded_AQuietLandedWorktreeIsArchivedAndItsChatStaysLive(t *testing.T) {
	t.Parallel()
	store, sender, s := landedEnv(t, AutoArchiveOn, nil)
	store.activity["wt"] = quietFor(4 * time.Hour)

	require.NoError(t, s.Sweep(context.Background()))
	w := sender.seen["wt"]
	assert.True(t, w.ProbeLanded, "a live row with no chat run is asked whether it landed")
	assert.False(t, w.ShedLanded, "live probes never delete; whole retirement follows archival")
	assert.Equal(t, []string{"wt"}, store.archived, "a live chat does not keep a landed worktree: it gets it back when it runs")
	assert.EqualValues(t, 1, s.landedArchived.Load())
	meta := store.rows["wt"].Worktree.CleanupMetadata
	require.NotNil(t, meta)
	assert.Equal(t, core.ArchivedBecauseLanded, meta.ArchivedBecause)
	assert.NotNil(t, meta.LandedAt)
}

func TestLanded_NotQuietLongEnoughWaits(t *testing.T) {
	t.Parallel()
	store, _, s := landedEnv(t, AutoArchiveOn, nil)
	store.activity["wt"] = quietFor(time.Hour)

	require.NoError(t, s.Sweep(context.Background()))
	assert.Empty(t, store.archived)
	assert.EqualValues(t, 1, s.landed.Load())
	meta := store.rows["wt"].Worktree.CleanupMetadata
	require.NotNil(t, meta)
	assert.NotNil(t, meta.LandedAt, "the landing is remembered while it waits")

	s.WithLandedIdle(30 * time.Minute)
	s.now = func() time.Time { return time.Now().Add(landedRecheck + time.Minute) }
	require.NoError(t, s.Sweep(context.Background()))
	assert.Equal(t, []string{"wt"}, store.archived, "the idle period is configurable")
}

func TestLanded_ARunningChatIsOnlyLocked(t *testing.T) {
	t.Parallel()
	store, sender, s := landedEnv(t, AutoArchiveOn, nil)
	store.activity["wt"] = core.WorktreeRunActivity{Running: true, LastActive: time.Now().Add(-24 * time.Hour)}

	require.NoError(t, s.Sweep(context.Background()))
	w := sender.seen["wt"]
	assert.False(t, w.ProbeLanded, "a worktree a run is going in is not asked")
	assert.False(t, w.ShedLanded, "and never shed")
	assert.Empty(t, store.archived)
}

func TestLanded_UnreadableRunsFailClosed(t *testing.T) {
	t.Parallel()
	store, sender, s := landedEnv(t, AutoArchiveOn, nil)
	store.activityErr = errors.New("db down")

	require.NoError(t, s.Sweep(context.Background()))
	assert.False(t, sender.seen["wt"].ProbeLanded)
	assert.False(t, sender.seen["wt"].ShedLanded)
	assert.Empty(t, store.archived)
}

func TestLanded_DryRunAsksButNeitherShedsNorArchives(t *testing.T) {
	t.Parallel()
	store, sender, s := landedEnv(t, AutoArchiveDryRun, nil)
	store.activity["wt"] = quietFor(24 * time.Hour)

	require.NoError(t, s.Sweep(context.Background()))
	assert.True(t, sender.seen["wt"].ProbeLanded)
	assert.False(t, sender.seen["wt"].ShedLanded)
	assert.Empty(t, store.archived)
	assert.EqualValues(t, 1, s.landed.Load(), "the dry run counts what it would reclaim")
}

func TestLanded_OffWhenTheIdlePeriodIsZero(t *testing.T) {
	t.Parallel()
	store, sender, s := landedEnv(t, AutoArchiveOn, nil)
	s.WithLandedIdle(0)
	store.activity["wt"] = quietFor(24 * time.Hour)

	require.NoError(t, s.Sweep(context.Background()))
	assert.False(t, sender.seen["wt"].ProbeLanded)
	assert.Empty(t, store.archived)
}

func TestLanded_KeepEverythingIsNeverAsked(t *testing.T) {
	t.Parallel()
	store, sender, s := landedEnv(t, AutoArchiveOn, nil)
	s.WithKeepFiles(keepAll{})
	store.activity["wt"] = quietFor(24 * time.Hour)

	require.NoError(t, s.Sweep(context.Background()))
	assert.False(t, sender.seen["wt"].ProbeLanded)
	assert.Empty(t, store.archived)
}

func TestLanded_MainCheckoutIsNeverAsked(t *testing.T) {
	t.Parallel()
	store, sender, s := landedEnv(t, AutoArchiveOn, nil)
	store.rows["wt"].Worktree.IsMain = true
	store.activity["wt"] = quietFor(24 * time.Hour)

	require.NoError(t, s.Sweep(context.Background()))
	assert.False(t, sender.seen["wt"].ProbeLanded)
	assert.Empty(t, store.archived)
}

func TestLanded_ShedBytesAreRecordedOnTheLiveRow(t *testing.T) {
	t.Parallel()
	store, _, s := landedEnv(t, AutoArchiveOn, func(w worktreereclaim.Worktree) worktreereclaim.Result {
		return worktreereclaim.Result{ID: w.ID, Outcome: worktreereclaim.OutcomeLocked, Landed: true, Stripped: []string{"node_modules"}, StrippedBytes: 1 << 30}
	})
	store.activity["wt"] = quietFor(time.Hour) // landed, not quiet enough to archive

	require.NoError(t, s.Sweep(context.Background()))
	meta := store.rows["wt"].Worktree.CleanupMetadata
	require.NotNil(t, meta)
	assert.EqualValues(t, 1<<30, meta.StrippedBytes)
	assert.Equal(t, []string{"node_modules"}, meta.StrippedPaths)
	assert.Empty(t, store.archived)
}

func TestLanded_NewWorkClearsTheLanding(t *testing.T) {
	t.Parallel()
	landed := true
	store, _, s := landedEnv(t, AutoArchiveOn, func(w worktreereclaim.Worktree) worktreereclaim.Result {
		return worktreereclaim.Result{ID: w.ID, Outcome: worktreereclaim.OutcomeLocked, Landed: landed}
	})
	store.activity["wt"] = quietFor(time.Hour)
	now := time.Now()
	s.now = func() time.Time { return now }

	require.NoError(t, s.Sweep(context.Background()))
	require.NotNil(t, store.rows["wt"].Worktree.CleanupMetadata.LandedAt)

	landed = false
	now = now.Add(landedRecheck + time.Minute)
	require.NoError(t, s.Sweep(context.Background()))
	assert.Nil(t, store.rows["wt"].Worktree.CleanupMetadata.LandedAt, "a commit after the merge means it has not landed")
	assert.Empty(t, store.archived)
}

func TestLanded_LiveRowsAreAskedEveryHalfHour(t *testing.T) {
	t.Parallel()
	store, sender, s := landedEnv(t, AutoArchiveOn, nil)
	store.activity["wt"] = quietFor(time.Hour)
	now := time.Now()
	s.now = func() time.Time { return now }

	require.NoError(t, s.Sweep(context.Background()))
	asked := len(sender.requests)
	require.Positive(t, asked)

	now = now.Add(landedRecheck - time.Minute)
	require.NoError(t, s.Sweep(context.Background()))
	assert.Len(t, sender.requests, asked, "not again inside the recheck interval")

	now = now.Add(2 * time.Minute)
	require.NoError(t, s.Sweep(context.Background()))
	assert.Greater(t, len(sender.requests), asked, "a merge frees its disk within the hour, not the next day")

	// With reclaim-on-merge off, a locked live row waits a day as before.
	s.WithLandedIdle(0)
	asked = len(sender.requests)
	now = now.Add(landedRecheck + time.Minute)
	require.NoError(t, s.Sweep(context.Background()))
	assert.Len(t, sender.requests, asked)
}
