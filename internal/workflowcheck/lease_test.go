// Copyright (c) 2025 Reliant Labs
package workflowcheck

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// The lease (claimed_by, claimed_until, lease_generation): every test here
// runs two syncers, A and B, over one store, one GitHub and one clock, and
// acts mid-sync from inside A's GitHub write, so each interleaving is fixed
// rather than raced. The store's own fence is pinned against Postgres in
// internal/store (TestWorkflowCheckRunLease*).

// Two syncers race for one due check: the one that claims it syncs it, the
// other finds nothing to claim while the lease is held, and GitHub sees one
// check run.
func TestSyncersRaceForOneCheck(t *testing.T) {
	h := newHarness()
	a, b := h.svc, h.syncer("syncer-b")
	row := h.open(t, &reliantv1.WorkflowCheck{Name: "reliant/ci"})
	h.observe(&core.WorkflowCheckObservation{EventOutcome: core.TriggerEventPending})

	var bClaimed int
	h.gh.hook = func(context.Context, string) {
		h.gh.hook = nil
		n, err := b.SyncOnce(context.Background())
		require.NoError(t, err)
		bClaimed = n
	}
	n, err := a.SyncOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, n, "A claimed and synced it")
	require.Zero(t, bClaimed, "B finds the check leased to A")
	require.Equal(t, []string{"find", "create:queued/"}, h.gh.writes, "one check run on GitHub")
	require.Equal(t, int64(1), row.LeaseGeneration)
	require.Empty(t, row.ClaimedBy, "A's save released the lease")
	require.Nil(t, row.ClaimedUntil)

	// Due again: whichever claims first syncs; the other gets nothing.
	h.clock.advance(pollLive)
	n, err = b.SyncOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, n)
	n, err = a.SyncOnce(context.Background())
	require.NoError(t, err)
	require.Zero(t, n)
	require.Equal(t, int64(2), row.LeaseGeneration)
}

// A's lease expires while its sync is stuck in a GitHub write; B takes the
// check, concludes it and finishes it. A's save is then refused by the fence:
// the check stays as B left it, and A's failure is not recorded over it.
func TestLeaseExpiresMidSyncAndTheStaleSaveIsRefused(t *testing.T) {
	h := newHarness()
	a, b := h.svc, h.syncer("syncer-b")
	ctx := context.Background()
	row := h.open(t, &reliantv1.WorkflowCheck{Name: "reliant/ci"})
	h.observe(launched(status(core.Active())))
	h.sync(t) // A: queued
	require.NoError(t, a.Started(ctx, "chat-1"))

	h.gh.hook = func(_ context.Context, what string) {
		h.gh.hook = nil
		require.Equal(t, "update", what, "A is moving the check to in_progress")
		// A stalls past its lease; meanwhile the run ends and B syncs.
		h.clock.advance(lease + time.Second)
		require.NoError(t, b.Ended(ctx, "chat-1", core.WorkflowCheckEnding{Status: core.TriggerRunCompleted, Verdict: "success"}))
		h.observe(launched(status(core.Completed())))
		n, err := b.SyncOnce(ctx)
		require.NoError(t, err)
		require.Equal(t, 1, n, "B took the expired lease")
	}
	n, err := a.SyncOnce(ctx)
	require.NoError(t, err, "a lost lease is not a failed sync")
	require.Zero(t, n, "A's result was dropped")

	require.Equal(t, core.CheckStatusCompleted, row.GitHubStatus, "B's state stands")
	require.Equal(t, "success", row.GitHubConclusion)
	require.NotNil(t, row.FinishedAt)
	require.Zero(t, row.Attempts, "A's failed write is not recorded over B's success")
	require.Empty(t, row.LastError)
	require.Equal(t, int64(3), row.LeaseGeneration, "A's first claim, A's second, B's")
	require.Empty(t, row.ClaimedBy)
}

// The heartbeat renews the lease of a sync that outlives it, so no other
// syncer takes the check, and the sync's save lands.
func TestHeartbeatKeepsTheLeaseThroughALongSync(t *testing.T) {
	h := newHarness()
	h.renewals = make(chan renewal, 1)
	a, b := h.svc, h.syncer("syncer-b")
	row := h.open(t, &reliantv1.WorkflowCheck{Name: "reliant/ci"})
	h.observe(&core.WorkflowCheckObservation{EventOutcome: core.TriggerEventPending})

	h.gh.hook = func(context.Context, string) {
		h.gh.hook = nil
		h.clock.advance(renewEvery)
		h.beat <- h.clock.now
		require.Equal(t, []string{row.ID}, (<-h.renewals).ids)
		// Past the claim's lease, within the renewed one.
		h.clock.advance(lease - renewEvery + time.Second)
		n, err := b.SyncOnce(context.Background())
		require.NoError(t, err)
		require.Zero(t, n, "the renewed lease is still A's")
	}
	n, err := a.SyncOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Equal(t, core.CheckStatusQueued, row.GitHubStatus)
	require.Equal(t, int64(1), row.LeaseGeneration, "nobody else claimed it")
	require.Empty(t, row.ClaimedBy)
}

// The heartbeat finds A's lease taken: A's in-flight GitHub write is
// cancelled, nothing is written, and A's save is refused.
func TestHeartbeatCancelsASyncWhoseLeaseWasLost(t *testing.T) {
	h := newHarness()
	h.renewals = make(chan renewal, 1)
	row := h.open(t, &reliantv1.WorkflowCheck{Name: "reliant/ci"})
	h.observe(&core.WorkflowCheckObservation{EventOutcome: core.TriggerEventPending})

	var writeCtx context.Context
	h.gh.hook = func(ctx context.Context, _ string) {
		h.gh.hook = nil
		writeCtx = ctx
		h.clock.advance(lease + time.Second)
		taken, err := h.store.ClaimWorkflowCheckRuns(context.Background(), "syncer-b", h.clock.now, h.clock.now.Add(lease), batch)
		require.NoError(t, err)
		require.Len(t, taken, 1, "B takes the expired lease")
		h.beat <- h.clock.now
		require.Empty(t, (<-h.renewals).ids, "A's renewal finds the lease gone")
		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Second):
			t.Fatal("A's write was not cancelled after its lease was lost")
		}
	}
	n, err := h.svc.SyncOnce(context.Background())
	require.NoError(t, err)
	require.Zero(t, n)
	require.Error(t, writeCtx.Err())
	require.Equal(t, []string{"find"}, h.gh.writes, "A created nothing")
	require.Nil(t, row.GitHubCheckRunID)
	require.Equal(t, "syncer-b", row.ClaimedBy, "B's lease stands")
	require.Equal(t, int64(2), row.LeaseGeneration)
	require.Zero(t, row.Attempts)
}

// The renewals fail (the database is unreachable from A) until A's lease
// expires: A's in-flight GitHub write is cancelled at the expiry, before
// another syncer takes the check, and A writes nothing after it.
func TestRenewalErrorsPastExpiryCancelTheWrite(t *testing.T) {
	h := newHarness()
	h.renewals = make(chan renewal, 1)
	a, b := h.svc, h.syncer("syncer-b")
	row := h.open(t, &reliantv1.WorkflowCheck{Name: "reliant/ci"})
	h.observe(&core.WorkflowCheckObservation{EventOutcome: core.TriggerEventPending})
	h.store.renewErr = errors.New("database unreachable")

	h.gh.hook = func(ctx context.Context, what string) {
		h.gh.hook = nil
		require.Equal(t, "create", what, "A's create is in flight")
		for range lease / renewEvery {
			h.clock.advance(renewEvery)
			if ctx.Err() == nil {
				h.beat <- h.clock.Now()
				require.Error(t, (<-h.renewals).err)
			}
		}
		require.ErrorIs(t, context.Cause(ctx), core.ErrWorkflowCheckLeaseLost, "cancelled at the lease's expiry, renewals failing")
		require.True(t, !h.clock.Now().Before(*h.store.rows[row.ID].ClaimedUntil), "no earlier than the expiry the database holds")

		// Only now may B take the check; it creates it.
		h.clock.advance(time.Second)
		n, err := b.SyncOnce(context.Background())
		require.NoError(t, err)
		require.Equal(t, 1, n, "B took the expired lease")
	}
	n, err := a.SyncOnce(context.Background())
	require.NoError(t, err, "a check stopped for its lease is not a failed sync")
	require.Zero(t, n)

	require.Equal(t, []string{"find", "find", "create:queued/"}, h.gh.writes, "A's create never landed; B's is the only one")
	require.Equal(t, core.CheckStatusQueued, row.GitHubStatus, "B's state stands")
	require.Equal(t, int64(2), row.LeaseGeneration)
	require.Empty(t, row.ClaimedBy)
	require.Zero(t, row.Attempts, "A's stop is not recorded over B's success")
	require.Empty(t, row.LastError)
}

// The renewals fail and A's lease nears its expiry while it is sending a
// concluded check's annotations: the call in flight finishes, but no call
// starts within writeMargin of the expiry. What landed is saved (nobody
// took the row), the row is due again with no attempt counted, and the next
// claim sends the rest.
func TestRenewalErrorsStopWritesNearExpiry(t *testing.T) {
	h := newHarness()
	h.renewals = make(chan renewal, 1)
	ctx := context.Background()
	row := h.open(t, &reliantv1.WorkflowCheck{Name: "reliant/ci"})
	annotations := make([]any, 120)
	for i := range annotations {
		annotations[i] = map[string]any{"path": fmt.Sprintf("f%03d.go", i), "start_line": 1, "message": "m"}
	}
	require.NoError(t, h.svc.Ended(ctx, "chat-1", core.WorkflowCheckEnding{Status: core.TriggerRunCompleted, Verdict: "success",
		Output: map[string]any{"title": "Passed", "summary": "s", "annotations": annotations}}))
	h.observe(launched(status(core.Completed())))
	h.store.renewErr = errors.New("database unreachable")

	h.gh.hook = func(ctx context.Context, what string) {
		h.gh.hook = nil
		require.Equal(t, "create", what)
		h.clock.advance(renewEvery)
		h.beat <- h.clock.Now()
		require.Error(t, (<-h.renewals).err)
		// Inside the margin, not yet expired: this call goes on.
		h.clock.advance(lease - renewEvery - writeMargin + time.Second)
		require.NoError(t, ctx.Err())
	}
	n, err := h.svc.SyncOnce(ctx)
	require.NoError(t, err)
	require.Zero(t, n)
	require.Equal(t, []string{"find", "create:completed/success"}, h.gh.writes, "no annotation batch started inside the margin")
	require.NotNil(t, row.GitHubCheckRunID, "the create that landed is saved")
	require.Zero(t, row.AnnotationsSent)
	require.Nil(t, row.FinishedAt)
	require.Zero(t, row.Attempts)
	require.Empty(t, row.LastError)
	require.Empty(t, row.ClaimedBy, "released")
	require.False(t, row.NextSyncAt.After(h.clock.Now()), "due again at once")

	// The database answers again: the next claim sends the annotations.
	h.store.renewErr = nil
	h.sync(t)
	require.Equal(t, []string{"find", "create:completed/success", "update:/+50", "update:/+50", "update:/+20"}, h.gh.writes)
	require.Equal(t, 120, row.AnnotationsSent)
	require.NotNil(t, row.FinishedAt)
}

// A renewal fails once, then succeeds: the lease's expiry moves out, the
// write outlives the claim's original expiry, nobody else takes the check,
// and A's save lands.
func TestTransientRenewalErrorThenSuccessKeepsTheLease(t *testing.T) {
	h := newHarness()
	h.renewals = make(chan renewal, 1)
	a, b := h.svc, h.syncer("syncer-b")
	row := h.open(t, &reliantv1.WorkflowCheck{Name: "reliant/ci"})
	h.observe(&core.WorkflowCheckObservation{EventOutcome: core.TriggerEventPending})
	h.store.renewErr = errors.New("connection reset")

	h.gh.hook = func(ctx context.Context, _ string) {
		h.gh.hook = nil
		h.clock.advance(renewEvery)
		h.beat <- h.clock.Now()
		require.Error(t, (<-h.renewals).err)

		h.store.renewErr = nil
		h.clock.advance(renewEvery)
		h.beat <- h.clock.Now()
		require.Equal(t, []string{row.ID}, (<-h.renewals).ids)

		// Past the claim's expiry, well inside the renewed one.
		h.clock.advance(lease - 2*renewEvery + 10*time.Second)
		require.NoError(t, ctx.Err(), "the renewal moved the expiry")
		n, err := b.SyncOnce(context.Background())
		require.NoError(t, err)
		require.Zero(t, n, "the lease is still A's")
	}
	n, err := a.SyncOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Equal(t, []string{"find", "create:queued/"}, h.gh.writes)
	require.Equal(t, core.CheckStatusQueued, row.GitHubStatus)
	require.Equal(t, int64(1), row.LeaseGeneration, "nobody else claimed it")
	require.Empty(t, row.ClaimedBy)
}
