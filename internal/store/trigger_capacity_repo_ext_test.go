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

func TestTriggerCapacityUnboundStartDoesNotHidePendingScopeHead(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	scope := capacityScope()
	first, second := capacityEvent("unbound-first"), capacityEvent("pending-second")
	first.CreatedAt = time.Now().Add(-time.Hour)
	second.CreatedAt = first.CreatedAt
	require.NoError(t, store.EnqueueTriggerCapacity(ctx, env.ORM, first, scope))
	require.NoError(t, store.EnqueueTriggerCapacity(ctx, env.ORM, second, scope))
	claimed, err := claimCapacity(ctx, env, first.ID, scope, 2)
	require.NoError(t, err)
	require.True(t, claimed)
	rows, err := store.ListStalePendingTriggerEvents(ctx, env.ORM, time.Now(), 10)
	require.NoError(t, err)
	ids := map[string]bool{}
	for _, row := range rows {
		ids[row.ID] = true
	}
	require.True(t, ids[first.ID], "unbound launch recovery remains reachable")
	require.True(t, ids[second.ID], "pending scope head must not disappear behind an unbound launch")
}

// A released unbound launch has nothing left to recover. Redriving it every
// rotation would share the batch with the real scope heads and starve them.
func TestTriggerCapacityReleasedUnboundLaunchIsNotRedriven(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	scope := capacityScope()
	released, head := capacityEvent("released-unbound"), capacityEvent("pending-head")
	released.CreatedAt = time.Now().Add(-time.Hour)
	head.CreatedAt = released.CreatedAt
	require.NoError(t, store.EnqueueTriggerCapacity(ctx, env.ORM, released, scope))
	require.NoError(t, store.EnqueueTriggerCapacity(ctx, env.ORM, head, scope))
	claimed, err := claimCapacity(ctx, env, released.ID, scope, 2)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, env.ORM.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		return store.PrepareTriggerCapacityExecution(ctx, env.ORM, released.ID, "released-root")
	}))
	_, err = env.SQL.ExecContext(ctx, `UPDATE trigger_events SET capacity_released=true,capacity_released_at=now(),capacity_release_reason='start_fenced_and_absent' WHERE id=$1`, released.ID)
	require.NoError(t, err)
	for pass := 0; pass < 3; pass++ {
		rows, err := store.ListStalePendingTriggerEvents(ctx, env.ORM, time.Now(), 1)
		require.NoError(t, err)
		ids := map[string]bool{}
		for _, row := range rows {
			ids[row.ID] = true
		}
		require.False(t, ids[released.ID], "a released unbound launch is not a redrive candidate")
		require.True(t, ids[head.ID], "the pending head is redriven every pass, not starved by released rows")
	}
}

func TestTriggerCapacityOperatorReleaseRequiresRootFence(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	scope := capacityScope()
	ev := capacityEvent("operator-event")
	require.NoError(t, store.EnqueueTriggerCapacity(ctx, env.ORM, ev, scope))
	claimed, err := claimCapacity(ctx, env, ev.ID, scope, 1)
	require.NoError(t, err)
	require.True(t, claimed)
	seedRun(t, env, "operator-root", core.Pending())
	require.NoError(t, env.ORM.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		return store.PrepareTriggerCapacityExecution(ctx, env.ORM, ev.ID, "operator-root")
	}))
	ok, err := store.ReleaseTriggerCapacityByOperator(ctx, env.ORM, ev.ID, "owner", "external execution fenced")
	require.NoError(t, err)
	require.False(t, ok, "pending root can still start")
	_, err = env.SQL.ExecContext(ctx, `UPDATE workflows SET state=3,stop_reason=2 WHERE id=$1`, "operator-root")
	require.NoError(t, err)
	ok, err = store.ReleaseTriggerCapacityByOperator(ctx, env.ORM, ev.ID, "owner", "external execution fenced")
	require.NoError(t, err)
	require.False(t, ok, "a start attempted inside the bound may still be accepted")
	ageStartAttempt(t, env, ev.ID)
	ok, err = store.ReleaseTriggerCapacityByOperator(ctx, env.ORM, ev.ID, "owner", "confirmed external execution terminated")
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = store.ReleaseTriggerCapacityByOperator(ctx, env.ORM, ev.ID, "owner", "second attempt")
	require.NoError(t, err)
	require.False(t, ok, "release is monotonic and idempotent")
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
	// Full-replace generic updates must not erase the admission identity.
	preserved, err := store.GetTriggerEventByID(ctx, env.ORM, first.ID)
	require.NoError(t, err)
	preserved.CapacityProjectID = nil
	preserved.CapacityWorkflowRef = nil
	preserved.CapacityOrder = nil
	preserved.CapacityWorkflowID = nil
	preserved.CapacityFirstRunID = nil
	require.NoError(t, store.UpdateTriggerEvent(ctx, env.ORM, preserved))
	require.NotNil(t, preserved.CapacityOrder)
	require.Equal(t, "capacity-workflow", *preserved.CapacityWorkflowID)
	require.Equal(t, "capacity-first-run", *preserved.CapacityFirstRunID)
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

// ageStartAttempt moves an event's last start attempt past the start-attempt bound.
func ageStartAttempt(t *testing.T, env *storetest.Env, id string) {
	t.Helper()
	_, err := env.SQL.ExecContext(context.Background(),
		`UPDATE trigger_events SET capacity_start_attempt_at = now() - make_interval(secs => $2) - interval '1 second' WHERE id=$1`,
		id, core.StartAttemptBound.Seconds())
	require.NoError(t, err)
}

func launchedUnbound(t *testing.T, env *storetest.Env, id, root string, status core.WorkflowStatus) store.TriggerCapacityExecution {
	t.Helper()
	ctx := context.Background()
	scope := capacityScope()
	require.NoError(t, store.EnqueueTriggerCapacity(ctx, env.ORM, capacityEvent(id), scope))
	claimed, err := claimCapacity(ctx, env, id, scope, 10)
	require.NoError(t, err)
	require.True(t, claimed)
	seedRun(t, env, root, status)
	require.NoError(t, env.ORM.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		return store.PrepareTriggerCapacityExecution(ctx, env.ORM, id, root)
	}))
	return store.TriggerCapacityExecution{EventID: id, WorkflowID: root}
}

func releasedFlag(t *testing.T, env *storetest.Env, id string) bool {
	t.Helper()
	var released bool
	require.NoError(t, env.SQL.QueryRowContext(context.Background(), `SELECT capacity_released FROM trigger_events WHERE id=$1`, id).Scan(&released))
	return released
}

// B4-a: a root that left PENDING (Terminate's CANCELLED, or a missing row) is
// not the fence. The release UPDATE itself requires the last start attempt to
// be older than core.StartAttemptBound.
func TestTriggerCapacityUnboundReleaseRequiresAgedStartAttempt(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	e := launchedUnbound(t, env, "inflight-event", "inflight-root", core.Pending())
	_, err := env.SQL.ExecContext(ctx, `UPDATE workflows SET state=3,stop_reason=3 WHERE id=$1`, e.WorkflowID)
	require.NoError(t, err)
	ok, err := store.ReleaseUnboundTriggerCapacity(ctx, env.ORM, e, "start_fenced_and_absent")
	require.NoError(t, err)
	require.False(t, ok, "a start stamped inside the bound may still be accepted")
	_, err = env.SQL.ExecContext(ctx, `DELETE FROM workflows WHERE id=$1`, e.WorkflowID)
	require.NoError(t, err)
	ok, err = store.ReleaseUnboundTriggerCapacity(ctx, env.ORM, e, "start_fenced_and_absent")
	require.NoError(t, err)
	require.False(t, ok, "a missing root does not stand in for the aged stamp")
	ageStartAttempt(t, env, e.EventID)
	ok, err = store.ReleaseUnboundTriggerCapacity(ctx, env.ORM, e, "start_fenced_and_absent")
	require.NoError(t, err)
	require.True(t, ok)
}

// A retry's claim refreshes the stamp under the event row lock, so an aged
// slot it claims is not releasable once its root then leaves PENDING; and a
// slot already released refuses the claim.
func TestTriggerCapacityRetryClaimRacesAgedRelease(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	e := launchedUnbound(t, env, "retry-event", "retry-root", core.Pending())
	ageStartAttempt(t, env, e.EventID)
	// The claim holds the event row while the release waits on it.
	claimedTx := make(chan struct{})
	commit := make(chan struct{})
	txDone := make(chan error, 1)
	go func() {
		txDone <- env.ORM.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
			ok, err := store.ClaimWorkflowStart(ctx, env.ORM, e.WorkflowID)
			if err != nil {
				return err
			}
			if !ok {
				return errors.New("claim refused")
			}
			close(claimedTx)
			<-commit
			return nil
		})
	}()
	<-claimedTx
	// Terminate's write lands on the root as soon as the claim commits.
	terminated := make(chan error, 1)
	go func() {
		_, err := env.SQL.ExecContext(ctx, `UPDATE workflows SET state=3,stop_reason=3 WHERE id=$1`, e.WorkflowID)
		terminated <- err
	}()
	released := make(chan bool, 1)
	go func() {
		<-terminated
		ok, err := store.ReleaseUnboundTriggerCapacity(ctx, env.ORM, e, "start_fenced_and_absent")
		require.NoError(t, err)
		released <- ok
	}()
	close(commit)
	require.NoError(t, <-txDone)
	require.False(t, <-released, "the claim's fresh stamp keeps the in-flight retry's slot")
	require.False(t, releasedFlag(t, env, e.EventID))

	// Released first: the claim must not start a run beside the freed slot.
	r := launchedUnbound(t, env, "released-event", "released-root", core.Pending())
	_, err := env.SQL.ExecContext(ctx, `UPDATE trigger_events SET capacity_released=true,capacity_released_at=now(),capacity_release_reason='test' WHERE id=$1`, r.EventID)
	require.NoError(t, err)
	ok, err := store.ClaimWorkflowStart(ctx, env.ORM, r.WorkflowID)
	require.NoError(t, err)
	require.False(t, ok, "a released slot is not this start's to use")

	// A root with no capacity slot claims exactly as before.
	seedRun(t, env, "plain-root", core.Pending())
	ok, err = store.ClaimWorkflowStart(ctx, env.ORM, "plain-root")
	require.NoError(t, err)
	require.True(t, ok)
}

// The failed-start fence moves the root through the shared status writer
// (completed_at stamped), and only once both the failure and the attempt aged.
func TestTriggerCapacityFailedStartFenceRequiresAgedAttempt(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	e := launchedUnbound(t, env, "fence-event", "fence-root", core.Pending())
	_, err := env.SQL.ExecContext(ctx, `UPDATE workflows SET start_failed_at = now() - interval '1 hour' WHERE id=$1`, e.WorkflowID)
	require.NoError(t, err)
	ok, err := store.FenceUnboundTriggerCapacityStart(ctx, env.ORM, e)
	require.NoError(t, err)
	require.False(t, ok, "the launch's own attempt is still inside the bound")
	ageStartAttempt(t, env, e.EventID)
	ok, err = store.FenceUnboundTriggerCapacityStart(ctx, env.ORM, e)
	require.NoError(t, err)
	require.True(t, ok)
	var state, reason int
	var completed bool
	require.NoError(t, env.SQL.QueryRowContext(ctx, `SELECT state, stop_reason, completed_at IS NOT NULL FROM workflows WHERE id=$1`, e.WorkflowID).Scan(&state, &reason, &completed))
	require.Equal(t, core.Failed().State, core.WorkflowState(state))
	require.True(t, completed)
}
