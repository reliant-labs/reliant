// Copyright (c) 2025 Reliant Labs
package worktreesweep

import (
	"context"
	"encoding/json"
	"errors"
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

// Reclaim in two phases: a quiet landed worktree is marked (its daemon places
// a tombstone) and reclaimed a day later only if nothing contradicted the
// mark (mark.go).

// clock is the test's one clock: the server's, the database's and the
// daemon's.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// tombDaemon plays a daemon that places tombstones: it remembers each
// worktree's tombstone and use stamp, and answers landed for every row asked.
type tombDaemon struct {
	mu        sync.Mutex
	clock     *clock
	marks     map[string]string    // worktree -> mark id on disk
	use       map[string]time.Time // worktree -> daemon's last-use stamp
	noTombs   bool                 // a daemon that predates tombstones
	notLanded bool
	down      bool          // the machine cannot be reached
	skew      time.Duration // how far the daemon's clock runs behind
	cmds      []string
	archived  []worktreereclaim.Worktree // archived requests, as sent
	reclaimAt []time.Time                // each placed mark's notice time
}

func newTombDaemon(c *clock) *tombDaemon {
	return &tombDaemon{clock: c, marks: map[string]string{}, use: map[string]time.Time{}}
}

func (d *tombDaemon) SendDaemonCommandToDaemon(_ context.Context, _, _, cmd string, payload []byte, _ int32) ([]byte, error) {
	var req worktreereclaim.Request
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.down {
		return nil, errors.New("daemon unreachable")
	}
	d.cmds = append(d.cmds, cmd)
	resp := worktreereclaim.Response{}
	for _, w := range req.Worktrees {
		if w.State == worktreereclaim.StateArchived {
			d.archived = append(d.archived, w)
			r := worktreereclaim.Result{ID: w.ID, Outcome: worktreereclaim.OutcomeClaimed}
			switch {
			case w.ReclaimMark != "" && d.marks[w.ID] != w.ReclaimMark:
				r = worktreereclaim.Result{ID: w.ID, Outcome: worktreereclaim.OutcomeHeld, Reason: worktreereclaim.ReasonMarked}
			case w.Remove:
				r.Outcome = worktreereclaim.OutcomeRemoved
			}
			resp.Results = append(resp.Results, r)
			continue
		}
		switch cmd {
		case worktreereclaim.MarkCommand:
			if w.Mark != nil && !d.noTombs {
				d.marks[w.ID] = w.Mark.ID
				d.reclaimAt = append(d.reclaimAt, w.Mark.ReclaimAt)
				d.use[w.ID] = d.clock.now().Add(-d.skew) // placing it writes files
			}
		case worktreereclaim.UnmarkCommand:
			if w.Unmark == "*" || d.marks[w.ID] == w.Unmark {
				delete(d.marks, w.ID)
			}
		}
		r := worktreereclaim.Result{ID: w.ID, Outcome: worktreereclaim.OutcomeLocked, Landed: w.ProbeLanded && !d.notLanded}
		if u, ok := d.use[w.ID]; ok {
			r.LastUse = &u
		}
		if !d.noTombs {
			r.Tombstone = &worktreereclaim.TombstoneReport{MarkID: d.marks[w.ID]}
		}
		resp.Results = append(resp.Results, r)
	}
	return json.Marshal(resp)
}

// markStore is fakeStore's database half of marks, with the real store's
// row-lock checks (store.MarkWorktreeForReclaim and friends).
type markStore struct {
	*fakeStore
	clock *clock
}

func (m markStore) quiet(id string, since time.Time) bool {
	a := m.activity[id]
	return !a.Running && !a.LastActive.After(since)
}

func (m markStore) MarkWorktreeForReclaim(_ context.Context, id, markID string, quietSince time.Time, use *time.Time, reason string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	wt := m.rows[id].Worktree
	if wt.DeletedAt != nil || wt.CleanupMetadata.Marked() || !m.quiet(id, quietSince) {
		return false, nil
	}
	meta := core.CleanupMetadata{}
	if wt.CleanupMetadata != nil {
		meta = *wt.CleanupMetadata
	}
	at := m.clock.now()
	meta.ReclaimMarkID, meta.ReclaimMarkedAt, meta.ReclaimMarkUse, meta.ReclaimMarkReason = markID, &at, use, reason
	wt.CleanupMetadata = &meta
	return true, nil
}

func (m markStore) ClearWorktreeMark(_ context.Context, id, markID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	wt := m.rows[id].Worktree
	if !wt.CleanupMetadata.Marked() || (markID != "" && wt.CleanupMetadata.ReclaimMarkID != markID) {
		return false, nil
	}
	meta := *wt.CleanupMetadata
	meta.ReclaimMarkID, meta.ReclaimMarkedAt, meta.ReclaimMarkUse, meta.ReclaimMarkReason = "", nil, nil, ""
	wt.CleanupMetadata = &meta
	return true, nil
}

func (m markStore) ArchiveMarkedWorktree(_ context.Context, id, markID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	wt := m.rows[id].Worktree
	meta := wt.CleanupMetadata
	if wt.DeletedAt != nil || !meta.Marked() || meta.ReclaimMarkID != markID || !m.quiet(id, *meta.ReclaimMarkedAt) {
		return false, nil
	}
	at := m.clock.now()
	wt.DeletedAt = &at
	cp := *meta
	cp.ArchivedBecause = cp.ReclaimMarkReason
	wt.CleanupMetadata = &cp
	m.archived = append(m.archived, id)
	return true, nil
}

type markEnv struct {
	store  *fakeStore
	marks  markStore
	daemon *tombDaemon
	clock  *clock
	s      *Sweeper
}

// newMarkEnv is one live row "wt" whose daemon answers landed, its rows
// quiet for quietFor, under mode.
func newMarkEnv(t *testing.T, mode AutoArchiveMode, quietFor time.Duration) *markEnv {
	t.Helper()
	c := &clock{t: time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)}
	store := newFakeStore("d")
	store.add("wt", false, nil)
	store.activity = map[string]core.WorktreeRunActivity{"wt": {LastActive: c.now().Add(-quietFor)}}
	d := newTombDaemon(c)
	ms := markStore{fakeStore: store, clock: c}
	s := New(store, d).WithAutoArchive(mode, store, nil).WithReclaimMarks(ms, 0, 0)
	s.now = c.now
	return &markEnv{store: store, marks: ms, daemon: d, clock: c, s: s}
}

// pass runs one sweep after advancing the clock past the live recheck.
func (e *markEnv) pass(t *testing.T, after time.Duration) {
	t.Helper()
	e.clock.advance(after)
	require.NoError(t, e.s.Sweep(context.Background()))
}

func (e *markEnv) meta() *core.CleanupMetadata {
	e.store.mu.Lock()
	defer e.store.mu.Unlock()
	return e.store.rows["wt"].Worktree.CleanupMetadata
}

func TestMark_AQuietLandedWorktreeIsMarkedThenReclaimedADayLater(t *testing.T) {
	t.Parallel()
	e := newMarkEnv(t, AutoArchiveOn, 25*time.Hour)

	e.pass(t, 0)
	require.True(t, e.meta().Marked(), "landed and quiet for a day: marked")
	markID := e.meta().ReclaimMarkID
	assert.Equal(t, markID, e.daemon.marks["wt"], "its daemon placed the tombstone")
	assert.Empty(t, e.store.archived, "a mark archives nothing")
	assert.Equal(t, core.ArchivedBecauseLanded, e.meta().ReclaimMarkReason)

	e.pass(t, 23*time.Hour)
	assert.Empty(t, e.store.archived, "the mark stands a day first")
	assert.Equal(t, markID, e.meta().ReclaimMarkID, "rechecks that find nothing new leave it")

	e.pass(t, 2*time.Hour)
	require.Equal(t, []string{"wt"}, e.store.archived, "a day after the mark, nothing changed: reclaimed")
	assert.Equal(t, core.ArchivedBecauseLanded, e.meta().ArchivedBecause)

	e.pass(t, landedRecheck+time.Minute)
	require.NotEmpty(t, e.daemon.archived)
	assert.Equal(t, markID, e.daemon.archived[0].ReclaimMark, "its removal is sent under the mark")
}

func TestMark_LessThanADayQuietIsNotMarked(t *testing.T) {
	t.Parallel()
	// The 3h rule of 2026-11-10 is gone: 23 hours of quiet marks nothing.
	e := newMarkEnv(t, AutoArchiveOn, 23*time.Hour)
	e.pass(t, 0)
	assert.False(t, e.meta().Marked())
	assert.Empty(t, e.daemon.marks)
	assert.Empty(t, e.store.archived)
}

func TestMark_FloorsAreADay(t *testing.T) {
	t.Parallel()
	e := newMarkEnv(t, AutoArchiveOn, 2*time.Hour)
	e.s.WithReclaimMarks(e.marks, time.Hour, time.Hour)
	e.pass(t, 0)
	assert.False(t, e.meta().Marked(), "a configured hour is raised to the day floor")
}

func TestMark_CancelledByAnythingThatHappensInIt(t *testing.T) {
	t.Parallel()
	for name, touch := range map[string]func(e *markEnv){
		"the tombstone is deleted": func(e *markEnv) { delete(e.daemon.marks, "wt") },
		"it is used by path":       func(e *markEnv) { e.daemon.use["wt"] = e.clock.now() },
		"a chat works in it": func(e *markEnv) {
			e.store.mu.Lock()
			e.store.activity["wt"] = core.WorktreeRunActivity{LastActive: e.clock.now()}
			e.store.mu.Unlock()
		},
		"a run starts in it": func(e *markEnv) {
			e.store.mu.Lock()
			e.store.activity["wt"] = core.WorktreeRunActivity{Running: true, LastActive: e.clock.now().Add(-48 * time.Hour)}
			e.store.mu.Unlock()
		},
		"new work does not land": func(e *markEnv) { e.daemon.notLanded = true },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e := newMarkEnv(t, AutoArchiveOn, 25*time.Hour)
			e.pass(t, 0)
			require.True(t, e.meta().Marked())

			e.clock.advance(time.Hour)
			touch(e)
			e.pass(t, landedRecheck+time.Minute)
			assert.False(t, e.meta().Marked(), "cancelled")
			assert.Empty(t, e.daemon.marks, "its tombstone removed")
			assert.Contains(t, e.daemon.cmds, worktreereclaim.UnmarkCommand)

			e.pass(t, 30*time.Hour)
			assert.Empty(t, e.store.archived, "never reclaimed on a cancelled mark")
		})
	}
}

func TestMark_ADaemonWithoutTombstonesHasNothingReclaimed(t *testing.T) {
	t.Parallel()
	e := newMarkEnv(t, AutoArchiveOn, 10*24*time.Hour)
	e.daemon.noTombs = true
	e.pass(t, 0)
	e.pass(t, 48*time.Hour)
	assert.False(t, e.meta().Marked())
	assert.Empty(t, e.store.archived, "it waits, as Keep everything does")
	assert.NotContains(t, e.daemon.cmds, worktreereclaim.MarkCommand)
}

func TestMark_DryRunMarksButNeverReclaims(t *testing.T) {
	t.Parallel()
	e := newMarkEnv(t, AutoArchiveDryRun, 25*time.Hour)
	e.pass(t, 0)
	assert.True(t, e.meta().Marked(), "dry-run places the mark, so the rollout can be watched")
	e.pass(t, 48*time.Hour)
	assert.Empty(t, e.store.archived)
	assert.True(t, e.meta().Marked())
}

func TestMark_TheTombstoneItselfIsNotUse(t *testing.T) {
	t.Parallel()
	e := newMarkEnv(t, AutoArchiveOn, 25*time.Hour)
	e.pass(t, 0)
	require.True(t, e.meta().Marked())
	// Every later answer repeats the stamp of the tombstone's own writes.
	for range 3 {
		e.pass(t, landedRecheck+time.Minute)
		require.True(t, e.meta().Marked(), "the mark's own writes cancelled it")
	}
	assert.True(t, e.store.activity["wt"].LastActive.Before(*e.meta().ReclaimMarkedAt), "nor recorded as use")
}

func TestMark_UseIsJudgedOnTheDaemonsOwnClock(t *testing.T) {
	t.Parallel()
	// The daemon's clock runs two hours behind the database's: its stamp of
	// a use an hour after the mark reads as an hour BEFORE the mark on the
	// row, and only the comparison with its own baseline sees it.
	e := newMarkEnv(t, AutoArchiveOn, 25*time.Hour)
	e.daemon.skew = 2 * time.Hour
	e.pass(t, 0)
	require.True(t, e.meta().Marked())

	e.clock.advance(time.Hour)
	e.daemon.mu.Lock()
	e.daemon.use["wt"] = e.clock.now().Add(-e.daemon.skew)
	e.daemon.mu.Unlock()
	e.pass(t, landedRecheck+time.Minute)
	assert.False(t, e.meta().Marked(), "used after the mark, by the daemon's own clock")
	e.pass(t, 30*time.Hour)
	assert.Empty(t, e.store.archived)
}

// Inbox decisions (KeepMarked, ReclaimMarkedNow): markStore answers them
// as the real store does.

func (m markStore) ListMarkedWorktreesForUser(_ context.Context, userID string) ([]*core.HeldWorktree, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*core.HeldWorktree
	for _, id := range m.order {
		c := m.rows[id]
		if c.OwnerUserID == userID && c.Worktree.DeletedAt == nil && c.Worktree.CleanupMetadata.Marked() {
			cp := *c.Worktree
			out = append(out, &core.HeldWorktree{Worktree: &cp, ProjectPath: c.ProjectPath})
		}
	}
	return out, nil
}

func (m markStore) ApproveWorktreeMark(_ context.Context, id, markID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	wt := m.rows[id].Worktree
	if wt.DeletedAt != nil || !wt.CleanupMetadata.Marked() || wt.CleanupMetadata.ReclaimMarkID != markID {
		return false, nil
	}
	meta := *wt.CleanupMetadata
	at := m.clock.now()
	meta.ReclaimApprovedAt = &at
	wt.CleanupMetadata = &meta
	return true, nil
}

func TestMark_ReclaimNowSkipsOnlyTheWait(t *testing.T) {
	t.Parallel()
	e := newMarkEnv(t, AutoArchiveOn, 25*time.Hour)
	e.pass(t, 0)
	markID := e.meta().ReclaimMarkID
	require.NotEmpty(t, markID)

	require.ErrorIs(t, e.s.ReclaimMarkedNow(context.Background(), e.marks, "u", "wt", "stale"), ErrMarkChanged)
	require.ErrorIs(t, e.s.ReclaimMarkedNow(context.Background(), e.marks, "someone-else", "wt", markID), ErrNotMarked)
	assert.Nil(t, e.meta().ReclaimApprovedAt, "neither recorded an approval")

	e.clock.advance(time.Hour)
	require.NoError(t, e.s.ReclaimMarkedNow(context.Background(), e.marks, "u", "wt", markID))
	e.s.WaitCleanups()
	assert.Equal(t, []string{"wt"}, e.store.archived, "an hour after the mark, approved: reclaimed without the day's wait")
}

// "Reclaim now" is durable: when its immediate settles cannot reach the
// machine, the approval stands, the next ordinary pass reclaims it without
// the day's wait, and the person may ask again meanwhile.
func TestMark_ReclaimNowThatCannotReachTheMachineIsRetried(t *testing.T) {
	t.Parallel()
	e := newMarkEnv(t, AutoArchiveOn, 25*time.Hour)
	e.pass(t, 0)
	markID := e.meta().ReclaimMarkID
	e.clock.advance(time.Hour)

	e.daemon.mu.Lock()
	e.daemon.down = true
	e.daemon.mu.Unlock()
	require.NoError(t, e.s.ReclaimMarkedNow(context.Background(), e.marks, "u", "wt", markID))
	e.s.WaitCleanups()
	assert.Empty(t, e.store.archived, "the machine was unreachable")
	require.NotNil(t, e.meta().ReclaimApprovedAt, "the approval stands")
	assert.Equal(t, markID, e.meta().ReclaimMarkID)

	// Asking again is allowed: the approval is re-recorded, nothing is lost.
	require.NoError(t, e.s.ReclaimMarkedNow(context.Background(), e.marks, "u", "wt", markID))
	e.s.WaitCleanups()
	assert.Empty(t, e.store.archived, "still unreachable")

	e.daemon.mu.Lock()
	e.daemon.down = false
	e.daemon.mu.Unlock()
	e.pass(t, landedRecheck+time.Minute)
	require.Equal(t, []string{"wt"}, e.store.archived, "the next ordinary pass reclaimed the approved mark, well inside the day's grace")
}

func TestMark_ReclaimNowStillRefusesAWorktreeInUse(t *testing.T) {
	t.Parallel()
	e := newMarkEnv(t, AutoArchiveOn, 25*time.Hour)
	e.pass(t, 0)
	markID := e.meta().ReclaimMarkID
	e.clock.advance(time.Hour)
	e.daemon.mu.Lock()
	e.daemon.use["wt"] = e.clock.now() // used by path since the mark
	e.daemon.mu.Unlock()

	require.NoError(t, e.s.ReclaimMarkedNow(context.Background(), e.marks, "u", "wt", markID))
	e.s.WaitCleanups()
	assert.Empty(t, e.store.archived, "approval skips the wait, never a check")
	assert.False(t, e.meta().Marked(), "the use cancelled the mark instead")
}

func TestMark_KeepCancelsTheMarkShown(t *testing.T) {
	t.Parallel()
	e := newMarkEnv(t, AutoArchiveOn, 25*time.Hour)
	e.pass(t, 0)
	markID := e.meta().ReclaimMarkID

	require.ErrorIs(t, e.s.KeepMarked(context.Background(), e.marks, "u", "wt", "stale"), ErrMarkChanged)
	require.True(t, e.meta().Marked())
	require.NoError(t, e.s.KeepMarked(context.Background(), e.marks, "u", "wt", markID))
	assert.False(t, e.meta().Marked())
	assert.Empty(t, e.daemon.marks, "its tombstone removed")
	e.pass(t, 30*time.Hour)
	assert.Empty(t, e.store.archived)
	assert.NotEqual(t, markID, e.meta().ReclaimMarkID, "quiet a day again: a new mark, never the kept one")
}

// guard turns the machine's disk guard on (a report with a reclaiming
// volume) or off (a report with none), as of the env's clock.
func (e *markEnv) guard(t *testing.T, on bool) {
	t.Helper()
	vol := &reliantv1.DiskVolume{Label: "home", TotalBytes: 400 * diskwatch.GiB, FreeBytes: 300 * diskwatch.GiB}
	if on {
		vol.FreeBytes, vol.Guard = 30*diskwatch.GiB, reliantv1.DiskGuardState_DISK_GUARD_STATE_RECLAIMING
	}
	raw, err := diskwatch.Encode(diskwatch.Stamp(&reliantv1.DiskReport{Volumes: []*reliantv1.DiskVolume{vol}}, nil, e.clock.now()))
	require.NoError(t, err)
	e.store.mu.Lock()
	e.store.disk = map[string]string{"d": raw}
	e.store.mu.Unlock()
}

func TestMark_UnderDiskPressureAnHourOfQuietAndAnHourOfGrace(t *testing.T) {
	t.Parallel()
	e := newMarkEnv(t, AutoArchiveOn, 2*time.Hour)
	e.pass(t, 0)
	require.False(t, e.meta().Marked(), "two hours of quiet, no pressure: not marked")

	// The guard's episode begins after that check: it re-asks the row now.
	e.clock.advance(time.Minute)
	e.guard(t, true)
	e.pass(t, 0)
	require.True(t, e.meta().Marked(), "under pressure an hour of quiet is enough")
	require.Len(t, e.daemon.reclaimAt, 1)
	assert.Equal(t, e.meta().ReclaimMarkedAt.Add(guardReclaimGrace), e.daemon.reclaimAt[0],
		"the tombstone says it goes in an hour")

	e.pass(t, 30*time.Minute)
	assert.Empty(t, e.store.archived, "the hour is not up")
	e.pass(t, 31*time.Minute)
	assert.Equal(t, []string{"wt"}, e.store.archived, "an hour after the mark, under pressure: reclaimed")
}

func TestMark_PressureThatClearsMidGraceWaitsTheFullGrace(t *testing.T) {
	t.Parallel()
	e := newMarkEnv(t, AutoArchiveOn, 2*time.Hour)
	e.guard(t, true)
	e.pass(t, 0)
	require.True(t, e.meta().Marked())
	markedAt := *e.meta().ReclaimMarkedAt

	e.guard(t, false)
	e.pass(t, 2*time.Hour)
	assert.Empty(t, e.store.archived, "pressure cleared: the mark waits the full grace again")
	assert.True(t, e.meta().Marked(), "and still stands")

	e.clock.mu.Lock()
	e.clock.t = markedAt.Add(DefaultReclaimGrace + time.Minute)
	e.clock.mu.Unlock()
	e.pass(t, 0)
	assert.Equal(t, []string{"wt"}, e.store.archived, "a day after the mark")
}

func TestMark_UnderDiskPressureEveryOtherCheckStands(t *testing.T) {
	t.Parallel()
	for name, touch := range map[string]func(e *markEnv){
		"the tombstone is deleted": func(e *markEnv) { delete(e.daemon.marks, "wt") },
		"it is used by path":       func(e *markEnv) { e.daemon.use["wt"] = e.clock.now() },
		"a chat works in it": func(e *markEnv) {
			e.store.mu.Lock()
			e.store.activity["wt"] = core.WorktreeRunActivity{LastActive: e.clock.now()}
			e.store.mu.Unlock()
		},
		"new work does not land":                             func(e *markEnv) { e.daemon.notLanded = true },
		"its daemon is downgraded to one without tombstones": func(e *markEnv) { e.daemon.noTombs = true },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e := newMarkEnv(t, AutoArchiveOn, 2*time.Hour)
			e.guard(t, true)
			e.pass(t, 0)
			require.True(t, e.meta().Marked())
			e.clock.advance(10 * time.Minute)
			e.daemon.mu.Lock()
			touch(e)
			e.daemon.mu.Unlock()
			e.pass(t, 2*time.Hour)
			assert.Empty(t, e.store.archived, "pressure shortens the waits, never a check")
			assert.False(t, e.meta().Marked(), "the mark is cancelled")
		})
	}

	e := newMarkEnv(t, AutoArchiveOn, 10*24*time.Hour)
	e.daemon.noTombs = true
	e.guard(t, true)
	e.pass(t, 0)
	e.pass(t, 3*time.Hour)
	assert.Empty(t, e.store.archived, "a daemon without tombstones is never reclaimed, under pressure too")
}
