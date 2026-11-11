package store_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
	"github.com/stretchr/testify/require"
)

// launchBoundSlot enrolls, admits and binds one capacity-capped run, as a
// trigger fire and its launcher do, and returns the slot it holds.
func launchBoundSlot(t *testing.T, ctx context.Context, env *storetest.Env, id string, scope store.TriggerCapacityScope, n uint32) store.TriggerCapacitySlot {
	t.Helper()
	require.NoError(t, store.EnqueueTriggerCapacity(ctx, env.ORM, capacityEvent(id), scope))
	claimed, err := claimCapacity(ctx, env, id, scope, n)
	require.NoError(t, err)
	require.True(t, claimed, "%s must be admitted", id)
	slot := store.TriggerCapacitySlot{EventID: id, WorkflowID: "root-" + id, FirstRunID: "run-" + id}
	require.NoError(t, env.ORM.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		return store.PrepareTriggerCapacityExecution(ctx, env.ORM, id, slot.WorkflowID)
	}))
	require.NoError(t, store.BindTriggerCapacityExecution(ctx, env.ORM, id, slot.WorkflowID, slot.FirstRunID))
	return slot
}

// A run parked on a provider holds no slot: the next queued firing launches,
// and the parked run cannot take its slot back above the cap.
func TestTriggerCapacityParkedRunReleasesItsSlot(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	scope := capacityScope()
	limited := launchBoundSlot(t, ctx, env, "limited", scope, 1)

	require.NoError(t, store.EnqueueTriggerCapacity(ctx, env.ORM, capacityEvent("model-free"), scope))
	claimed, err := claimCapacity(ctx, env, "model-free", scope, 1)
	require.NoError(t, err)
	require.False(t, claimed, "a running run holds the only slot")

	held, err := store.ParkTriggerCapacity(ctx, env.ORM, limited)
	require.NoError(t, err)
	require.True(t, held)
	claimed, err = claimCapacity(ctx, env, "model-free", scope, 1)
	require.NoError(t, err)
	require.True(t, claimed, "a parked run's slot goes to the next queued firing")

	held, granted, err := store.ResumeTriggerCapacity(ctx, env.ORM, limited)
	require.NoError(t, err)
	require.True(t, held)
	require.False(t, granted, "resuming must not push the scope above max_running")
}

// A parked run that asked for its slot back is ahead of every queued firing:
// a freed slot goes to it, never to the queue, and nothing is preempted.
func TestTriggerCapacityResumingRunHasPriorityOverQueue(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	scope := capacityScope()
	limited := launchBoundSlot(t, ctx, env, "limited", scope, 1)
	_, err := store.ParkTriggerCapacity(ctx, env.ORM, limited)
	require.NoError(t, err)
	other := launchBoundSlot(t, ctx, env, "other", scope, 1)

	_, granted, err := store.ResumeTriggerCapacity(ctx, env.ORM, limited)
	require.NoError(t, err)
	require.False(t, granted, "the scope is full")

	// The running run finishes; a queued firing arrives before the resumer asks again.
	require.NoError(t, store.ReleaseClosedTriggerCapacityExecution(ctx, env.ORM, store.TriggerCapacityExecution{
		EventID: other.EventID, WorkflowID: other.WorkflowID, FirstRunID: other.FirstRunID}))
	require.NoError(t, store.EnqueueTriggerCapacity(ctx, env.ORM, capacityEvent("queued"), scope))
	claimed, err := claimCapacity(ctx, env, "queued", scope, 1)
	require.NoError(t, err)
	require.False(t, claimed, "a resuming run is ahead of the queue")

	held, granted, err := store.ResumeTriggerCapacity(ctx, env.ORM, limited)
	require.NoError(t, err)
	require.True(t, held)
	require.True(t, granted, "the freed slot goes to the resuming run")
	claimed, err = claimCapacity(ctx, env, "queued", scope, 1)
	require.NoError(t, err)
	require.False(t, claimed, "the resumed run holds the slot again")
}

// Resume is against the cap the run was admitted under, and counts only
// running (unparked) runs.
func TestTriggerCapacityResumeHonoursAdmittedCap(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	scope := capacityScope()
	var slots []store.TriggerCapacitySlot
	for i := 0; i < 3; i++ {
		slots = append(slots, launchBoundSlot(t, ctx, env, fmt.Sprintf("run-%d", i), scope, 3))
	}
	for _, s := range slots {
		_, err := store.ParkTriggerCapacity(ctx, env.ORM, s)
		require.NoError(t, err)
	}
	for i, s := range slots {
		_, granted, err := store.ResumeTriggerCapacity(ctx, env.ORM, s)
		require.NoError(t, err)
		require.True(t, granted, "resume %d of 3 fits max_running 3", i)
	}
	// Resuming a run that is not parked is a no-op grant.
	held, granted, err := store.ResumeTriggerCapacity(ctx, env.ORM, slots[0])
	require.NoError(t, err)
	require.True(t, held)
	require.True(t, granted)
}

// A slot is matched by its exact chain: a later chain on the chat (a human
// turn), a released slot, or an uncapped event holds nothing to park.
func TestTriggerCapacityParkMatchesOnlyTheAdmittedChain(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	scope := capacityScope()
	slot := launchBoundSlot(t, ctx, env, "admitted", scope, 1)

	later := slot
	later.FirstRunID = "a-later-chain"
	held, err := store.ParkTriggerCapacity(ctx, env.ORM, later)
	require.NoError(t, err)
	require.False(t, held, "a later chain on the same chat holds no slot")
	held, granted, err := store.ResumeTriggerCapacity(ctx, env.ORM, later)
	require.NoError(t, err)
	require.False(t, held)
	require.True(t, granted)

	require.NoError(t, store.ReleaseClosedTriggerCapacityExecution(ctx, env.ORM, store.TriggerCapacityExecution{
		EventID: slot.EventID, WorkflowID: slot.WorkflowID, FirstRunID: slot.FirstRunID}))
	held, err = store.ParkTriggerCapacity(ctx, env.ORM, slot)
	require.NoError(t, err)
	require.False(t, held, "a released slot has nothing to park")
}
