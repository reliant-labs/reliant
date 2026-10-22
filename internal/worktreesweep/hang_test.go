// Copyright (c) 2025 Reliant Labs
package worktreesweep

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

// timeoutSender fails every request with the router's daemon-timeout error.
type timeoutSender struct {
	mu       sync.Mutex
	requests [][]string
}

func (s *timeoutSender) SendDaemonCommandToDaemon(_ context.Context, _, _, _ string, payload []byte, _ int32) ([]byte, error) {
	var req worktreereclaim.Request
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	for _, w := range req.Worktrees {
		ids = append(ids, w.ID)
	}
	s.requests = append(s.requests, ids)
	return nil, svcerr.DeadlineExceeded("the machine did not answer in time")
}

func TestSweep_ATimedOutBatchIsNotSplitAndResent(t *testing.T) {
	store := newFakeStore("d")
	for _, id := range []string{"a", "b", "c", "d"} {
		store.add(id, true, nil)
	}
	sender := &timeoutSender{}
	s := New(store, sender)
	require.NoError(t, s.Sweep(context.Background()))

	require.Len(t, sender.requests, 1, "the daemon may still be working on these rows: no bisected re-send")
	assert.ElementsMatch(t, []string{"a", "b", "c", "d"}, sender.requests[0])
	for _, id := range []string{"a", "b", "c", "d"} {
		meta := store.rows[id].Worktree.CleanupMetadata
		require.NotNil(t, meta, id)
		assert.Equal(t, "error", meta.Transient, "%s backs off like any failed row", id)
	}
}

func TestSweep_RowsStillInFlightAreNotSentAgain(t *testing.T) {
	store := newFakeStore("d")
	store.add("a", true, nil)
	store.add("b", true, nil)
	sender := &scriptSender{answer: locked}
	s := New(store, sender)
	now := time.Now().UTC()
	s.now = func() time.Time { return now }

	// A request for "a" is out to daemon d (a settle in progress, or a timed-out one).
	require.Len(t, s.inflight.acquire(now, []leaseKey{{"d", "a"}}), 1)

	require.NoError(t, s.Sweep(context.Background()))
	require.Len(t, sender.requests, 1)
	assert.Equal(t, []string{"b"}, sender.requests[0], "the leased row is left out of the request")

	s.inflight.release([]leaseKey{{"d", "a"}})
	require.NoError(t, s.Sweep(context.Background()))
	flat := map[string]int{}
	for _, r := range sender.requests {
		for _, id := range r {
			flat[id]++
		}
	}
	assert.Equal(t, 1, flat["a"], "once the lease is free the row is asked about")
}

func TestSettle_ATimedOutRowStaysLeasedUntilTheHoldPasses(t *testing.T) {
	store := newFakeStore("d")
	store.add("w", true, nil)
	timeouts := &timeoutSender{}
	s := New(store, timeouts)
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	require.NoError(t, s.Sweep(context.Background()))
	require.Len(t, timeouts.requests, 1)

	// A different path (an archive's immediate settle) reaches the same row
	// while the daemon may still be working on it: it is not sent again.
	s.settleNow(context.Background(), "u", store.rows["w"])
	assert.Len(t, timeouts.requests, 1, "the daemon was never heard from: do not stack a second request")

	now = now.Add(inFlightHold + time.Second)
	s.settleNow(context.Background(), "u", store.rows["w"])
	assert.Len(t, timeouts.requests, 2, "after the hold it is asked again")
}

func TestSweep_AFailedRequestReleasesItsLease(t *testing.T) {
	store := newFakeStore("d")
	store.add("w", true, nil)
	sender := &scriptSender{failWith: map[string]bool{"w": true}, answer: locked}
	s := New(store, sender)
	require.NoError(t, s.Sweep(context.Background()))
	assert.Empty(t, s.inflight.until, "a request that failed outright left nothing running, so nothing stays leased")
}

func TestApply_InconclusiveProbeBacksOffBrieflyAndIsNotLocked(t *testing.T) {
	store := newFakeStore("d")
	wt := store.add("w", false, nil)
	sender := &scriptSender{answer: func(w worktreereclaim.Worktree) worktreereclaim.Result {
		return worktreereclaim.Result{ID: w.ID, Outcome: worktreereclaim.OutcomeProbeInconclusive}
	}}
	s := New(store, sender)
	now := time.Now().UTC()
	s.now = func() time.Time { return now }

	require.NoError(t, s.Sweep(context.Background()))
	meta := store.rows["w"].Worktree.CleanupMetadata
	require.NotNil(t, meta)
	assert.Nil(t, meta.LockedAt, "an unanswered question is not a lock confirmation: no 24h silence")
	assert.Equal(t, "probe_inconclusive", meta.Transient)
	assert.False(t, s.due(wt, time.Time{}), "it waits out the short transient back-off")

	now = now.Add(transientBackoffMin + time.Minute)
	assert.True(t, s.due(store.rows["w"].Worktree, time.Time{}), "and is asked again within minutes, not a day")
	assert.Less(t, transientBackoffMin, lockRecheck)
}
