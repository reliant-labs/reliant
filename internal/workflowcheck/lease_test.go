// Copyright (c) 2025 Reliant Labs
package workflowcheck

import (
	"context"
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
	h.store.renewed = make(chan []string, 1)
	a, b := h.svc, h.syncer("syncer-b")
	row := h.open(t, &reliantv1.WorkflowCheck{Name: "reliant/ci"})
	h.observe(&core.WorkflowCheckObservation{EventOutcome: core.TriggerEventPending})

	h.gh.hook = func(context.Context, string) {
		h.gh.hook = nil
		h.clock.advance(renewEvery)
		h.beat <- h.clock.now
		require.Equal(t, []string{row.ID}, <-h.store.renewed)
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
	h.store.renewed = make(chan []string, 1)
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
		require.Empty(t, <-h.store.renewed, "A's renewal finds the lease gone")
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
