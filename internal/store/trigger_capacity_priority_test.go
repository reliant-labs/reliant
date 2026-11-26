package store_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
	"github.com/stretchr/testify/require"
)

// capacityFiring is one enrolled event of a priority/per-key admission case.
type capacityFiring struct {
	id       string
	priority *int64
	key      string // its max_running_per_key group; "" for none
	latest   string // its waiting: latest supersede group; "" for none
}

// event is the firing's event; its id (and dedupe key) is prefix+id, so the
// cases of one test share a database without sharing events.
func (f capacityFiring) event(prefix string) *core.TriggerEvent {
	ev := capacityEvent(prefix + f.id)
	ev.CapacityPriority = f.priority
	if f.key != "" {
		ev.CapacityRunningKey = &f.key
	}
	if f.latest != "" {
		ev.CapacityKey = &f.latest
	}
	return ev
}

func prio(n int64) *int64 { return &n }

// capacityTestBase is the fixed "now" of these tests: they never read the
// wall clock.
var capacityTestBase = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)

// claimWithin is claimCapacity under a full admission policy.
func claimWithin(ctx context.Context, env *storetest.Env, id string, scope store.TriggerCapacityScope, limits store.TriggerCapacityLimits) (bool, error) {
	claimed := false
	err := env.ORM.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		allowed, err := store.CanLaunchTriggerCapacity(ctx, env.ORM, id, scope, limits)
		if err != nil || !allowed {
			return err
		}
		claimed, err = store.ClaimPendingTriggerEvent(ctx, env.ORM, id, map[string]any{}, nil)
		return err
	})
	return claimed, err
}

// drainCapacity admits events until the scope admits nothing more, and
// returns their ids, less prefix, in admission order. At each step it proves
// the launch guard agrees with the hint: the named event is admitted, and
// every other waiting event is refused.
func drainCapacity(t *testing.T, ctx context.Context, env *storetest.Env, scope store.TriggerCapacityScope, limits store.TriggerCapacityLimits, prefix string, local []string) []string {
	t.Helper()
	ids := make([]string, len(local))
	for i, id := range local {
		ids[i] = prefix + id
	}
	var admitted []string
	for range ids {
		next, err := store.NextAdmissibleTriggerCapacityEvent(ctx, env.ORM, scope, limits)
		require.NoError(t, err)
		if next == nil {
			break
		}
		for _, id := range ids {
			if outcome, _ := outcomeOf(t, env, id); id == next.ID || outcome != core.TriggerEventPending {
				continue
			}
			claimed, err := claimWithin(ctx, env, id, scope, limits)
			require.NoError(t, err)
			require.False(t, claimed, "%s must wait while %s is admitted next", id, next.ID)
		}
		claimed, err := claimWithin(ctx, env, next.ID, scope, limits)
		require.NoError(t, err)
		require.True(t, claimed, "the event named next (%s) must be admitted", next.ID)
		admitted = append(admitted, strings.TrimPrefix(next.ID, prefix))
	}
	return admitted
}

func releaseCapacity(t *testing.T, ctx context.Context, env *storetest.Env, id string) {
	t.Helper()
	_, err := env.SQL.ExecContext(ctx, `UPDATE trigger_events SET capacity_released=true,capacity_released_at=$2,capacity_release_reason='closed' WHERE id=$1`, id, capacityTestBase)
	require.NoError(t, err)
}

// Waiting firings are admitted by priority, then arrival; a key at its
// max_running_per_key cap holds back only its own firings; waiting: latest
// still supersedes within its key; and with neither field the queue is the
// plain max_running FIFO it always was.
func TestTriggerCapacityAdmissionOrder(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()
	i := 0
	for _, tc := range []struct {
		name    string
		limits  store.TriggerCapacityLimits
		running []capacityFiring // launched before the waiting firings arrive
		waiting []capacityFiring // enrolled in this order
		want    []string         // admission order until nothing more is admitted
		// superseded settle skipped at enrollment (waiting: latest).
		superseded []string
		// release frees these running slots after the first drain; then
		// wantAfter is admitted.
		release   []string
		wantAfter []string
	}{
		{
			name:    "plain max_running is FIFO up to the cap",
			limits:  store.TriggerCapacityLimits{MaxRunning: 3},
			waiting: []capacityFiring{{id: "a"}, {id: "b"}, {id: "c"}, {id: "d"}},
			want:    []string{"a", "b", "c"},
		},
		{
			name:      "plain max_running admits the FIFO head once a slot frees",
			limits:    store.TriggerCapacityLimits{MaxRunning: 1},
			running:   []capacityFiring{{id: "r"}},
			waiting:   []capacityFiring{{id: "a"}, {id: "b"}},
			want:      nil,
			release:   []string{"r"},
			wantAfter: []string{"a"},
		},
		{
			name:   "higher priority first",
			limits: store.TriggerCapacityLimits{MaxRunning: 10},
			waiting: []capacityFiring{
				{id: "low", priority: prio(0)}, {id: "high", priority: prio(10)},
				{id: "mid", priority: prio(5)}, {id: "negative", priority: prio(-1)},
			},
			want: []string{"high", "mid", "low", "negative"},
		},
		{
			name:   "ties by arrival, and no priority is priority 0",
			limits: store.TriggerCapacityLimits{MaxRunning: 10},
			waiting: []capacityFiring{
				{id: "a", priority: prio(5)}, {id: "unset"}, {id: "b", priority: prio(5)},
				{id: "zero", priority: prio(0)}, {id: "c", priority: prio(5)},
			},
			want: []string{"a", "b", "c", "unset", "zero"},
		},
		{
			name:    "a full scope admits nothing, whatever the priority",
			limits:  store.TriggerCapacityLimits{MaxRunning: 1},
			running: []capacityFiring{{id: "r"}},
			waiting: []capacityFiring{{id: "old"}, {id: "urgent", priority: prio(100)}},
			want:    nil, release: []string{"r"}, wantAfter: []string{"urgent"},
		},
		{
			name:    "a key at its cap holds only its own firings",
			limits:  store.TriggerCapacityLimits{MaxRunning: 10, MaxRunningPerKey: 1},
			running: []capacityFiring{{id: "r1", key: "pr-1"}},
			waiting: []capacityFiring{
				{id: "a", key: "pr-1"}, {id: "b", key: "pr-2"}, {id: "c", key: "pr-1"},
				{id: "d", key: "pr-3"}, {id: "unkeyed"},
			},
			want:      []string{"b", "d", "unkeyed"},
			release:   []string{"r1"},
			wantAfter: []string{"a"}, // c still waits: a now runs pr-1
		},
		{
			name:    "a per-key cap of two",
			limits:  store.TriggerCapacityLimits{MaxRunning: 10, MaxRunningPerKey: 2},
			running: []capacityFiring{{id: "r1", key: "pr-1"}},
			waiting: []capacityFiring{{id: "a", key: "pr-1"}, {id: "b", key: "pr-1"}, {id: "c", key: "pr-2"}},
			want:    []string{"a", "c"},
		},
		{
			name:    "a key at its cap holds even a higher priority firing",
			limits:  store.TriggerCapacityLimits{MaxRunning: 10, MaxRunningPerKey: 1},
			running: []capacityFiring{{id: "r1", key: "pr-1"}},
			waiting: []capacityFiring{{id: "urgent", key: "pr-1", priority: prio(10)}, {id: "b", key: "pr-2"}},
			want:    []string{"b"}, release: []string{"r1"}, wantAfter: []string{"urgent"},
		},
		{
			name:    "the workflow-wide cap still bounds every key",
			limits:  store.TriggerCapacityLimits{MaxRunning: 2, MaxRunningPerKey: 1},
			waiting: []capacityFiring{{id: "a", key: "pr-1"}, {id: "b", key: "pr-2"}, {id: "c", key: "pr-3"}},
			want:    []string{"a", "b"},
		},
		{
			name:    "only a per-key cap: every key runs at once",
			limits:  store.TriggerCapacityLimits{MaxRunning: store.TriggerCapacityUnbounded, MaxRunningPerKey: 1},
			waiting: []capacityFiring{{id: "a", key: "pr-1"}, {id: "b", key: "pr-1"}, {id: "c", key: "pr-2"}, {id: "d", key: "pr-3"}},
			want:    []string{"a", "c", "d"},
		},
		{
			name:    "waiting: latest supersedes within the key it caps",
			limits:  store.TriggerCapacityLimits{MaxRunning: 4, MaxRunningPerKey: 1},
			running: []capacityFiring{{id: "r1", key: "pr-1", latest: "pr-1"}},
			waiting: []capacityFiring{
				{id: "old-1", key: "pr-1", latest: "pr-1"}, {id: "b", key: "pr-2", latest: "pr-2"},
				{id: "new-1", key: "pr-1", latest: "pr-1"},
			},
			superseded: []string{"old-1"},
			want:       []string{"b"},
			release:    []string{"r1"},
			wantAfter:  []string{"new-1"},
		},
		{
			name:   "waiting: latest keeps the newest firing whatever its priority",
			limits: store.TriggerCapacityLimits{MaxRunning: 4},
			waiting: []capacityFiring{
				{id: "urgent-old", latest: "k", priority: prio(9)}, {id: "other", latest: "j"},
				{id: "plain-new", latest: "k"},
			},
			superseded: []string{"urgent-old"},
			want:       []string{"other", "plain-new"},
		},
	} {
		// One database for every case, each in a scope (and with event ids)
		// of its own: a database per case cost a second each.
		i++
		prefix := fmt.Sprintf("case-%02d-", i)
		scope := capacityScope()
		scope.WorkflowRef = "project://" + prefix
		t.Run(tc.name, func(t *testing.T) {
			for _, f := range tc.running {
				require.NoError(t, store.EnqueueTriggerCapacity(ctx, env.ORM, f.event(prefix), scope))
				claimed, err := claimWithin(ctx, env, prefix+f.id, scope, store.TriggerCapacityLimits{MaxRunning: 100})
				require.NoError(t, err)
				require.True(t, claimed, "running %s", f.id)
			}
			var ids []string
			for _, f := range tc.waiting {
				require.NoError(t, store.EnqueueTriggerCapacity(ctx, env.ORM, f.event(prefix), scope))
				ids = append(ids, f.id)
			}
			for _, id := range tc.superseded {
				outcome, detail := outcomeOf(t, env, prefix+id)
				require.Equal(t, core.TriggerEventSkipped, outcome, id)
				require.Equal(t, store.TriggerCapacitySuperseded, detail, id)
			}
			require.Equal(t, tc.want, drainCapacity(t, ctx, env, scope, tc.limits, prefix, ids))
			for _, id := range tc.release {
				releaseCapacity(t, ctx, env, prefix+id)
			}
			if tc.release != nil {
				require.Equal(t, tc.wantAfter, drainCapacity(t, ctx, env, scope, tc.limits, prefix, ids))
			}
		})
	}
}

// A run parked on a provider gives back its workflow-wide slot but keeps its
// key's: the per-key cap is about the subject (one run per pull request), so a
// new firing of the parked run's key still waits.
func TestTriggerCapacityParkedRunKeepsItsKey(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()
	scope := capacityScope()
	limits := store.TriggerCapacityLimits{MaxRunning: 1, MaxRunningPerKey: 1}
	parked := capacityFiring{id: "parked", key: "pr-1"}
	require.NoError(t, store.EnqueueTriggerCapacity(ctx, env.ORM, parked.event(""), scope))
	claimed, err := claimWithin(ctx, env, parked.id, scope, limits)
	require.NoError(t, err)
	require.True(t, claimed)
	slot := store.TriggerCapacitySlot{EventID: parked.id, WorkflowID: "root-parked", FirstRunID: "run-parked"}
	require.NoError(t, env.ORM.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		return store.PrepareTriggerCapacityExecution(ctx, env.ORM, parked.id, slot.WorkflowID)
	}))
	require.NoError(t, store.BindTriggerCapacityExecution(ctx, env.ORM, parked.id, slot.WorkflowID, slot.FirstRunID))
	held, err := store.ParkTriggerCapacity(ctx, env.ORM, slot)
	require.NoError(t, err)
	require.True(t, held)

	for _, f := range []capacityFiring{{id: "same-key", key: "pr-1"}, {id: "other-key", key: "pr-2"}} {
		require.NoError(t, store.EnqueueTriggerCapacity(ctx, env.ORM, f.event(""), scope))
	}
	require.Equal(t, []string{"other-key"}, drainCapacity(t, ctx, env, scope, limits, "", []string{"same-key", "other-key"}))
}

// The redriver's scope head is the scope's first waiting event in admission
// order, so a waiting high-priority firing is the one it fires again.
func TestTriggerCapacityRedriveHeadFollowsPriority(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()
	scope := capacityScope()
	for _, f := range []capacityFiring{{id: "old"}, {id: "urgent", priority: prio(3)}, {id: "newer"}} {
		ev := f.event("")
		ev.CreatedAt = capacityTestBase.Add(-time.Hour)
		require.NoError(t, store.EnqueueTriggerCapacity(ctx, env.ORM, ev, scope))
	}
	rows, err := store.ListStalePendingTriggerEvents(ctx, env.ORM, capacityTestBase, 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "urgent", rows[0].ID)
	require.Equal(t, int64(3), *rows[0].CapacityPriority, "the enrolled priority reads back")
}

// An expired waiting event is never admitted, and the expiry sweep settles it
// although a higher-priority head hides it from the redriver's listing: a low
// priority event behind a stream of higher ones still expires after a day
// rather than launching late.
func TestTriggerCapacityExpiredEventBehindHigherPriorityHead(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()
	scope := capacityScope()
	cutoff := capacityTestBase.Add(-24 * time.Hour)
	for _, f := range []struct {
		firing  capacityFiring
		created time.Time
	}{
		{capacityFiring{id: "expired-low", priority: prio(0)}, cutoff.Add(-time.Hour)},
		{capacityFiring{id: "fresh-high", priority: prio(10)}, capacityTestBase.Add(-time.Hour)},
	} {
		ev := f.firing.event("")
		ev.CreatedAt = f.created
		require.NoError(t, store.EnqueueTriggerCapacity(ctx, env.ORM, ev, scope))
	}
	ordinary := capacityEvent("ordinary-old")
	ordinary.CreatedAt = cutoff.Add(-time.Hour)
	_, err := store.InsertTriggerEventIfAbsent(ctx, env.ORM, ordinary)
	require.NoError(t, err)

	rows, err := store.ListStalePendingTriggerEvents(ctx, env.ORM, capacityTestBase, 10)
	require.NoError(t, err)
	var listed []string
	for _, row := range rows {
		listed = append(listed, row.ID)
	}
	require.NotContains(t, listed, "expired-low", "the higher-priority head hides it from the listing")

	// Without an expiry cutoff the expired event would be admitted after the
	// head; with one it never is.
	next, err := store.NextAdmissibleTriggerCapacityEvent(ctx, env.ORM, scope, store.TriggerCapacityLimits{MaxRunning: 10})
	require.NoError(t, err)
	require.Equal(t, "fresh-high", next.ID)
	limits := store.TriggerCapacityLimits{MaxRunning: 10, ExpiredBefore: cutoff}
	require.Equal(t, []string{"fresh-high"}, drainCapacity(t, ctx, env, scope, limits, "", []string{"expired-low", "fresh-high"}))
	claimed, err := claimWithin(ctx, env, "expired-low", scope, limits)
	require.NoError(t, err)
	require.False(t, claimed, "an expired event never launches")

	settled, err := store.ExpirePendingTriggerCapacityEvents(ctx, env.ORM, cutoff, "too old", 10)
	require.NoError(t, err)
	require.Len(t, settled, 1)
	require.Equal(t, "expired-low", settled[0].ID)
	require.Equal(t, core.TriggerEventFailed, settled[0].Outcome, "the settled event as it is now")
	outcome, detail := outcomeOf(t, env, "expired-low")
	require.Equal(t, core.TriggerEventFailed, outcome)
	require.Equal(t, "too old", detail)
	outcome, _ = outcomeOf(t, env, "fresh-high")
	require.Equal(t, core.TriggerEventLaunched, outcome, "a launched event is never expired")
	outcome, _ = outcomeOf(t, env, "ordinary-old")
	require.Equal(t, core.TriggerEventPending, outcome, "an ordinary delivery is the listing's to expire")

	settled, err = store.ExpirePendingTriggerCapacityEvents(ctx, env.ORM, cutoff, "too old", 10)
	require.NoError(t, err)
	require.Empty(t, settled, "the sweep is idempotent")
}
