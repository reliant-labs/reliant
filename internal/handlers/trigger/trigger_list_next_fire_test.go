// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// createSchedules stores n schedule triggers and returns their ids.
func createSchedules(t *testing.T, env *triggerTestEnv, n int) []string {
	t.Helper()
	ids := make([]string, n)
	for i := range ids {
		ids[i] = env.create(t, env.definition(func(d *reliantv1.TriggerDefinition) {
			d.Name = fmt.Sprintf("schedule %d", i)
		})).GetId()
	}
	return ids
}

// shrinkNextFireBudget sets nextFireBudget for one test. The test must not be
// parallel: every parallel test of the package reads the var, and they are
// held at t.Parallel until the sequential tests are done.
func shrinkNextFireBudget(t *testing.T, d time.Duration) {
	t.Helper()
	prev := nextFireBudget
	nextFireBudget = d
	t.Cleanup(func() { nextFireBudget = prev })
}

// A slow Temporal used to cost a list its SDK deadline once per trigger, in
// turn (prod p99 246s). However many schedules there are, a list now waits
// for them at most one budget, and reports them without next_fire_at.
func TestListTriggersBoundsNextFireLookupsByOneBudget(t *testing.T) {
	env := setupTriggerTest(t)
	// More than nextFireWorkers, so a serial or per-batch wait would show.
	n := 3 * nextFireWorkers
	createSchedules(t, env, n)

	const budget = 100 * time.Millisecond
	shrinkNextFireBudget(t, budget)
	var asked atomic.Int32
	env.backend.mu.Lock()
	env.backend.nextFireHook = func(ctx context.Context, _ string) (*time.Time, error) {
		asked.Add(1)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	env.backend.mu.Unlock()

	start := time.Now()
	resp, err := env.svc.ListTriggers(env.ctx, connect.NewRequest(&reliantv1.ListTriggersRequest{}))
	elapsed := time.Since(start)

	require.NoError(t, err, "a schedule backend that never answers must not fail the list")
	require.Len(t, resp.Msg.GetTriggers(), n)
	for _, tr := range resp.Msg.GetTriggers() {
		assert.Nil(t, tr.NextFireAt, "trigger %s: a lookup that ran out of budget has no next fire time", tr.GetId())
	}
	// Serially, or a budget per batch of workers, this would be n/8 budgets
	// or more; one budget plus the list's own queries is well under this.
	assert.Less(t, elapsed, 2*time.Second, "the next-fire lookups must share one budget")
	assert.LessOrEqual(t, int(asked.Load()), nextFireWorkers,
		"once the budget is spent, the remaining triggers are not asked")
}

// Concurrent lookups land on the trigger they were made for, and one that
// fails leaves only its own trigger without a next fire time.
func TestListTriggersPutsEachNextFireTimeOnItsTrigger(t *testing.T) {
	t.Parallel()
	env := setupTriggerTest(t)
	ids := createSchedules(t, env, 2*nextFireWorkers+1)

	base := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	want := make(map[string]time.Time, len(ids))
	for i, id := range ids {
		want[id] = base.Add(time.Duration(i) * time.Hour)
	}
	failing := ids[3]
	env.backend.mu.Lock()
	env.backend.nextFireHook = func(_ context.Context, id string) (*time.Time, error) {
		if id == failing {
			return nil, errors.New("describe schedule: unavailable")
		}
		at, ok := want[id]
		if !ok {
			return nil, fmt.Errorf("unexpected trigger %s", id)
		}
		return &at, nil
	}
	env.backend.mu.Unlock()

	resp, err := env.svc.ListTriggers(env.ctx, connect.NewRequest(&reliantv1.ListTriggersRequest{}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetTriggers(), len(ids))
	for _, tr := range resp.Msg.GetTriggers() {
		if tr.GetId() == failing {
			assert.Nil(t, tr.NextFireAt, "the trigger whose lookup failed has no next fire time")
			continue
		}
		require.NotNil(t, tr.NextFireAt, "trigger %s", tr.GetId())
		assert.Equal(t, want[tr.GetId()].Format(time.RFC3339), tr.GetNextFireAt(), "trigger %s", tr.GetId())
	}
}
