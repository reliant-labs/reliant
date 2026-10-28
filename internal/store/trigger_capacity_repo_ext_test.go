package store_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
	"github.com/stretchr/testify/require"
)

func capacityEvent(id string) *core.TriggerEvent {
	return &core.TriggerEvent{ID: id, UserID: storetest.SeedUserID, Kind: core.TriggerEventKindIntegration,
		DedupeKey: id, Outcome: core.TriggerEventPending, OccurredAt: time.Now().UTC(), CreatedAt: time.Now().UTC()}
}

func capacityScope() store.TriggerCapacityScope {
	return store.TriggerCapacityScope{UserID: storetest.SeedUserID, ProjectID: storetest.SeedProjectID, WorkflowRef: "project://bounded"}
}

// claimCapacity is precisely the existing launch guard + pending-claim seam;
// both operations run under one database transaction, never a process mutex.
func claimCapacity(ctx context.Context, env *storetest.Env, id string, scope store.TriggerCapacityScope, n uint32) (bool, error) {
	claimed := false
	err := env.ORM.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		allowed, err := store.CanLaunchTriggerCapacity(ctx, env.ORM, id, scope, n)
		if err != nil || !allowed {
			return err
		}
		claimed, err = store.ClaimPendingTriggerEvent(ctx, env.ORM, id, map[string]any{})
		return err
	})
	return claimed, err
}

func TestTriggerCapacityRedriveFairnessPreservesOrdinaryDeliveryBudget(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	scope := capacityScope()
	for i := 0; i < 205; i++ {
		ev := capacityEvent(fmt.Sprintf("blocked-%03d", i))
		ev.CreatedAt = time.Now().Add(-48 * time.Hour)
		require.NoError(t, store.EnqueueTriggerCapacity(ctx, env.ORM, ev, scope))
	}
	ordinary := capacityEvent("ordinary-expiring")
	ordinary.CreatedAt = time.Now().Add(-48 * time.Hour)
	_, err := store.InsertTriggerEventIfAbsent(ctx, env.ORM, ordinary)
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		other := scope
		other.WorkflowRef = fmt.Sprintf("other-%d", i)
		ev := capacityEvent(fmt.Sprintf("other-%d", i))
		ev.CreatedAt = time.Now().Add(-time.Hour)
		require.NoError(t, store.EnqueueTriggerCapacity(ctx, env.ORM, ev, other))
	}
	seen := map[string]bool{}
	for pass := 0; pass < 4; pass++ {
		rows, err := store.ListStalePendingTriggerEvents(ctx, env.ORM, time.Now(), 1)
		require.NoError(t, err)
		require.LessOrEqual(t, len(rows), 2)
		ordinarySeen := false
		for _, ev := range rows {
			seen[ev.ID] = true
			if ev.ID == ordinary.ID {
				ordinarySeen = true
			}
		}
		require.True(t, ordinarySeen, "ordinary deliveries retain their own recovery/expiry budget")
	}
	require.True(t, seen["blocked-000"])
	for i := 0; i < 3; i++ {
		require.True(t, seen[fmt.Sprintf("other-%d", i)], "capacity scopes rotate fairly")
	}
	require.False(t, seen["blocked-001"], "only the oldest pending head of a blocked scope is redriven")
}

func TestTriggerCapacityFIFOAndCrashRetry(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	scope := capacityScope()
	first, second := capacityEvent("capacity-first"), capacityEvent("capacity-second")
	// Source time and delivery age do not define FIFO or expire capacity intents.
	first.CreatedAt = time.Now().Add(-48 * time.Hour)
	first.OccurredAt = time.Now().Add(time.Hour)
	require.NoError(t, store.EnqueueTriggerCapacity(ctx, env.ORM, first, scope))
	var before int64
	require.NoError(t, env.SQL.QueryRowContext(ctx, `SELECT capacity_order FROM trigger_events WHERE id=$1`, first.ID).Scan(&before))
	require.NoError(t, store.EnqueueTriggerCapacity(ctx, env.ORM, second, scope))
	// Ack loss/retry, including a new proposed row identity, preserves the old intent.
	retry := *first
	retry.ID = "discarded-retry-id"
	require.NoError(t, store.EnqueueTriggerCapacity(ctx, env.ORM, &retry, scope))
	var after int64
	require.NoError(t, env.SQL.QueryRowContext(ctx, `SELECT capacity_order FROM trigger_events WHERE id=$1`, first.ID).Scan(&after))
	require.Equal(t, before, after)
	claimed, err := claimCapacity(ctx, env, second.ID, scope, 2)
	require.NoError(t, err)
	require.False(t, claimed, "younger pending event must not overtake head even with free slots")
	// Crash before transaction commit cannot consume capacity.
	crashed := errors.New("simulated crash before commit")
	require.ErrorIs(t, env.ORM.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		allowed, err := store.CanLaunchTriggerCapacity(ctx, env.ORM, first.ID, scope, 1)
		require.NoError(t, err)
		require.True(t, allowed)
		claimed, err := store.ClaimPendingTriggerEvent(ctx, env.ORM, first.ID, map[string]any{})
		require.NoError(t, err)
		require.True(t, claimed)
		return crashed
	}), crashed)
	claimed, err = claimCapacity(ctx, env, first.ID, scope, 1)
	require.NoError(t, err)
	require.True(t, claimed)
	claimed, err = claimCapacity(ctx, env, first.ID, scope, 1)
	require.NoError(t, err)
	require.False(t, claimed, "committed claim replay cannot allocate another slot")
	claimed, err = claimCapacity(ctx, env, second.ID, scope, 1)
	require.NoError(t, err)
	require.False(t, claimed, "half-launch with no chat/run status must hold its slot")
	// Pausing changes the run, not the launch event's terminal evidence.
	seedRun(t, env, "capacity-paused-chat", core.Stopped(core.StopReasonPaused))
	_, err = env.SQL.ExecContext(ctx, `UPDATE trigger_events SET chat_id=$1 WHERE id=$2`, "capacity-paused-chat", first.ID)
	require.NoError(t, err)
	claimed, err = claimCapacity(ctx, env, second.ID, scope, 1)
	require.NoError(t, err)
	require.False(t, claimed, "paused run retains its slot")
	_, err = env.SQL.ExecContext(ctx, `DELETE FROM chats WHERE id=$1`, "capacity-paused-chat")
	require.NoError(t, err)
	claimed, err = claimCapacity(ctx, env, second.ID, scope, 1)
	require.NoError(t, err)
	require.False(t, claimed, "missing chat is uncertainty, not terminal evidence")
	for _, status := range []string{core.TriggerRunCompleted, core.TriggerRunFailed, core.TriggerRunCancelled} {
		_, err := env.SQL.ExecContext(ctx, `UPDATE trigger_events SET run_status=$1 WHERE id=$2`, status, first.ID)
		require.NoError(t, err)
		require.NoError(t, env.ORM.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
			allowed, err := store.CanLaunchTriggerCapacity(ctx, env.ORM, second.ID, scope, 1)
			require.NoError(t, err)
			require.False(t, allowed, "best-effort terminal status must not release capacity: %s", status)
			return nil
		}))
	}
	require.NoError(t, store.BindTriggerCapacityExecution(ctx, env.ORM, first.ID, "capacity-workflow", "capacity-first-run"))
	require.NoError(t, store.ReleaseTriggerCapacityExecution(ctx, env.ORM, store.TriggerCapacityExecution{EventID: first.ID, WorkflowID: "capacity-workflow", FirstRunID: "wrong-run"}))
	claimed, err = claimCapacity(ctx, env, second.ID, scope, 1)
	require.NoError(t, err)
	require.False(t, claimed, "wrong chain cannot release a slot")
	require.NoError(t, store.ReleaseTriggerCapacityExecution(ctx, env.ORM, store.TriggerCapacityExecution{EventID: first.ID, WorkflowID: "capacity-workflow", FirstRunID: "capacity-first-run"}))
	claimed, err = claimCapacity(ctx, env, second.ID, scope, 1)
	require.NoError(t, err)
	require.True(t, claimed, "a 48-hour queue head was not lost to delivery expiry")
}

func TestTriggerCapacityConcurrentWorkers(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	scope := capacityScope()
	const total = 12
	for i := 0; i < total; i++ {
		require.NoError(t, store.EnqueueTriggerCapacity(ctx, env.ORM, capacityEvent(fmt.Sprintf("parallel-%02d", i)), scope))
	}
	var wg sync.WaitGroup
	errs := make(chan error, total)
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := claimCapacity(ctx, env, fmt.Sprintf("parallel-%02d", i), scope, 2)
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	// Drain any heads whose workers checked before their predecessor committed.
	for i := 0; i < total; i++ {
		_, err := claimCapacity(ctx, env, fmt.Sprintf("parallel-%02d", i), scope, 2)
		require.NoError(t, err)
	}
	var count int
	require.NoError(t, env.SQL.QueryRowContext(ctx, `SELECT count(*) FROM trigger_events WHERE outcome='launched'`).Scan(&count))
	require.Equal(t, 2, count, "distributed launches must not exceed N")
	for i := 0; i < 2; i++ {
		var outcome string
		require.NoError(t, env.SQL.QueryRowContext(ctx, `SELECT outcome FROM trigger_events WHERE id=$1`, fmt.Sprintf("parallel-%02d", i)).Scan(&outcome))
		require.Equal(t, "launched", outcome, "only FIFO heads may claim")
	}
}

func TestTriggerCapacityScopeAndTransactionFences(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	scope := capacityScope()
	ev := capacityEvent("scope-first")
	require.NoError(t, store.EnqueueTriggerCapacity(ctx, env.ORM, ev, scope))
	_, err := store.CanLaunchTriggerCapacity(ctx, env.ORM, ev.ID, scope, 1)
	require.ErrorContains(t, err, "transaction")
	changed := scope
	changed.WorkflowRef = "project://other"
	require.ErrorContains(t, store.EnqueueTriggerCapacity(ctx, env.ORM, ev, changed), "immutable")
	other := capacityEvent("scope-other")
	require.NoError(t, store.EnqueueTriggerCapacity(ctx, env.ORM, other, changed))
	claimed, err := claimCapacity(ctx, env, ev.ID, scope, 1)
	require.NoError(t, err)
	require.True(t, claimed)
	claimed, err = claimCapacity(ctx, env, other.ID, changed, 1)
	require.NoError(t, err)
	require.True(t, claimed, "another workflow has independent capacity")
	for _, candidate := range []store.TriggerCapacityScope{
		{UserID: "another-user", ProjectID: scope.ProjectID, WorkflowRef: scope.WorkflowRef},
		{UserID: scope.UserID, ProjectID: "another-project", WorkflowRef: scope.WorkflowRef},
	} {
		candidateEvent := capacityEvent("scope-" + candidate.UserID + "-" + candidate.ProjectID)
		candidateEvent.UserID = candidate.UserID
		require.NoError(t, store.EnqueueTriggerCapacity(ctx, env.ORM, candidateEvent, candidate))
		claimed, err = claimCapacity(ctx, env, candidateEvent.ID, candidate, 1)
		require.NoError(t, err)
		require.True(t, claimed)
	}
}
