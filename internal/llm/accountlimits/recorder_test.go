// Copyright (c) 2025 Reliant Labs

package accountlimits

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm/ratelimit"
)

func f64(f float64) *float64 { return &f }

// snap is a subscription snapshot whose windows reset at fixed times, as a real account's do.
func snap(at time.Time, status ratelimit.Status, used5h, used7d float64) ratelimit.Snapshot {
	return ratelimit.Snapshot{
		Status:     status,
		ObservedAt: at,
		Windows: []ratelimit.Window{
			{Name: "5h", UsedFraction: f64(used5h), ResetsAt: t0.Add(2 * time.Hour), Length: 5 * time.Hour},
			{Name: "7d", UsedFraction: f64(used7d), ResetsAt: t0.Add(48 * time.Hour), Length: 7 * 24 * time.Hour},
		},
		RepresentativeWindow: "5h",
	}
}

func newTestRecorder(t *testing.T) (*Recorder, *fakeStore, *fakeClock) {
	t.Helper()
	c := newClock()
	st := newFakeStore(c)
	st.addAccount("a1", "u1", "claude")
	st.ensureRow("a1")
	r := newRecorder(st, c.Now, time.Hour) // flushed by hand
	return r, st, c
}

func TestRecorderCoalescesAHundredSnapshotsIntoFewWrites(t *testing.T) {
	r, st, c := newTestRecorder(t)
	obs := r.For("u1", "a1")

	// First sighting is written (nothing known yet); then 100 snapshots in a minute whose
	// used fraction creeps by 0.0001 each: below the 2-point threshold, within the heartbeat.
	obs.ObserveRateLimits(context.Background(), snap(c.Now(), ratelimit.StatusAllowed, 0.10, 0.20))
	r.flush(context.Background(), false)
	require.Equal(t, 1, st.upserts)

	for i := 0; i < 100; i++ {
		c.Advance(500 * time.Millisecond)
		obs.ObserveRateLimits(context.Background(), snap(c.Now(), ratelimit.StatusAllowed, 0.10+float64(i)*0.0001, 0.20))
		r.flush(context.Background(), false)
	}
	assert.LessOrEqual(t, st.upserts, 2, "100 snapshots in under a minute coalesce into at most one more write")

	c.Advance(time.Minute)
	r.flush(context.Background(), false)
	assert.LessOrEqual(t, st.upserts, 2, "no new observation, nothing to write")
	obs.ObserveRateLimits(context.Background(), snap(c.Now(), ratelimit.StatusAllowed, 0.1100, 0.20))
	r.flush(context.Background(), false)
	assert.LessOrEqual(t, st.upserts, 3, "the heartbeat writes once a minute")
}

func TestRecorderWritesTheFlushTriggers(t *testing.T) {
	cases := []struct {
		name string
		next func(at time.Time) ratelimit.Snapshot
		want bool
	}{
		{"status change", func(at time.Time) ratelimit.Snapshot { return snap(at, ratelimit.StatusWarning, 0.10, 0.20) }, true},
		{"used moved by 2 points", func(at time.Time) ratelimit.Snapshot { return snap(at, ratelimit.StatusAllowed, 0.125, 0.20) }, true},
		{"used moved by under 2 points", func(at time.Time) ratelimit.Snapshot { return snap(at, ratelimit.StatusAllowed, 0.11, 0.20) }, false},
		{"a window started over", func(at time.Time) ratelimit.Snapshot {
			s := snap(at, ratelimit.StatusAllowed, 0.10, 0.20)
			s.Windows[0].ResetsAt = t0.Add(7 * time.Hour)
			return s
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, st, c := newTestRecorder(t)
			obs := r.For("u1", "a1")
			first := snap(c.Now(), ratelimit.StatusAllowed, 0.10, 0.20)
			obs.ObserveRateLimits(context.Background(), first)
			r.flush(context.Background(), false)
			before := st.upserts

			c.Advance(2 * time.Second)
			obs.ObserveRateLimits(context.Background(), tc.next(c.Now()))
			r.flush(context.Background(), false)
			assert.Equal(t, tc.want, st.upserts > before)
		})
	}
}

func TestRecorderWritesRejectedImmediatelyAndBenchesOnAKnownReset(t *testing.T) {
	r, st, c := newTestRecorder(t)
	obs := r.For("u1", "a1")
	obs.ObserveRateLimits(context.Background(), snap(c.Now(), ratelimit.StatusAllowed, 0.10, 0.20))
	r.flush(context.Background(), false)

	c.Advance(time.Second)
	rej := snap(c.Now(), ratelimit.StatusRejected, 1.0, 0.5)
	obs.ObserveRateLimits(context.Background(), rej)
	r.flush(context.Background(), false) // no waiting for the heartbeat

	l := st.limit("a1")
	require.NotNil(t, l)
	assert.Equal(t, "rejected", l.Status)
	require.NotNil(t, l.LimitedUntil, "a rejected snapshot with a known reset benches the account")
	assert.True(t, l.LimitedUntil.Equal(rej.Windows[0].ResetsAt))
	assert.Equal(t, "five_hour", l.LimitedWindow)
	require.Len(t, st.benches, 1)
	assert.False(t, st.benches[0].probe)
}

func TestRecorderDoesNotBenchOnAScopedWindow(t *testing.T) {
	r, st, c := newTestRecorder(t)
	s := snap(c.Now(), ratelimit.StatusAllowed, 0.10, 0.20)
	s.Windows = append(s.Windows, ratelimit.Window{Name: "7d.opus", UsedFraction: f64(1), ResetsAt: c.Now().Add(24 * time.Hour)})
	r.For("u1", "a1").ObserveRateLimits(context.Background(), s)
	r.flush(context.Background(), false)
	assert.Empty(t, st.benches, "Opus' weekly cap does not stop Sonnet")
}

func TestRecorderObserverNeverBlocksOnASlowStore(t *testing.T) {
	r, st, c := newTestRecorder(t)
	st.slow = make(chan struct{})
	obs := r.For("u1", "a1")

	// Park a flush inside the slow store.
	obs.ObserveRateLimits(context.Background(), snap(c.Now(), ratelimit.StatusAllowed, 0.1, 0.2))
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); r.flush(context.Background(), false) }()
	time.Sleep(50 * time.Millisecond)

	s := snap(c.Now(), ratelimit.StatusRejected, 1, 1)
	start := time.Now()
	for i := 0; i < 1000; i++ {
		obs.ObserveRateLimits(context.Background(), s)
	}
	perCall := time.Since(start) / 1000
	assert.Less(t, perCall, 50*time.Microsecond, "the observer must not wait for the store")

	close(st.slow)
	wg.Wait()
}

func TestRecorderKeepsAccountsApart(t *testing.T) {
	r, st, c := newTestRecorder(t)
	st.addAccount("a2", "u1", "claude")
	st.ensureRow("a2")
	r.For("u1", "a1").ObserveRateLimits(context.Background(), snap(c.Now(), ratelimit.StatusAllowed, 0.9, 0.2))
	r.For("u1", "a2").ObserveRateLimits(context.Background(), snap(c.Now(), ratelimit.StatusAllowed, 0.1, 0.2))
	r.flush(context.Background(), false)
	assert.InDelta(t, 0.9, *st.limit("a1").Windows[0].UsedFraction, 1e-9)
	assert.InDelta(t, 0.1, *st.limit("a2").Windows[0].UsedFraction, 1e-9)
}

func TestRecorderIgnoresAnObservationWithoutIdentity(t *testing.T) {
	r, st, c := newTestRecorder(t)
	r.For("", "a1").ObserveRateLimits(context.Background(), snap(c.Now(), ratelimit.StatusAllowed, 0.9, 0.2))
	r.For("u1", "").ObserveRateLimits(context.Background(), snap(c.Now(), ratelimit.StatusAllowed, 0.9, 0.2))
	r.flush(context.Background(), true)
	assert.Zero(t, st.upserts)
}

func TestRecorderRetriesAfterAFailedWrite(t *testing.T) {
	r, st, c := newTestRecorder(t)
	obs := r.For("u1", "a1")
	obs.ObserveRateLimits(context.Background(), snap(c.Now(), ratelimit.StatusAllowed, 0.1, 0.2))

	st.upsertErr = errors.New("db down")
	r.flush(context.Background(), false)
	assert.Zero(t, st.upserts, "nothing was written")

	st.upsertErr = nil
	r.flush(context.Background(), false)
	assert.Equal(t, 1, st.upserts, "the snapshot is kept and written on the next flush")

	// An observation that arrived during the failed write is not overwritten by the retry.
	st.upsertErr = errors.New("db down")
	c.Advance(time.Second)
	obs.ObserveRateLimits(context.Background(), snap(c.Now(), ratelimit.StatusWarning, 0.5, 0.2))
	r.flush(context.Background(), false)
	st.upsertErr = nil
	r.flush(context.Background(), false)
	assert.Equal(t, "warning", st.limit("a1").Status)
}

func TestBenchWithUnknownResetClimbsTheProbeLadder(t *testing.T) {
	r, st, c := newTestRecorder(t)
	ctx := context.Background()

	got, err := r.Bench(ctx, "u1", "a1", time.Time{}, "")
	require.NoError(t, err)
	assert.True(t, got.Equal(c.Now().Add(5*time.Minute)), "first unknown-reset refusal: 5 minutes")

	// A second refusal while the bench still runs does not climb: concurrent refusals cost one rung.
	again, err := r.Bench(ctx, "u1", "a1", time.Time{}, "")
	require.NoError(t, err)
	assert.True(t, again.Equal(got))
	assert.Equal(t, 1, st.limit("a1").LimitedProbes)

	want := []time.Duration{15 * time.Minute, 30 * time.Minute, time.Hour, time.Hour}
	for i, d := range want {
		c.Advance(time.Hour + time.Minute) // the previous bench has ended
		got, err = r.Bench(ctx, "u1", "a1", time.Time{}, "")
		require.NoError(t, err)
		assert.True(t, got.Equal(c.Now().Add(d)), "rung %d: %s", i+2, d)
	}
}

func TestBenchWithAKnownResetResetsTheLadderAndKeepsTheLaterBench(t *testing.T) {
	r, st, c := newTestRecorder(t)
	ctx := context.Background()
	_, err := r.Bench(ctx, "u1", "a1", time.Time{}, "")
	require.NoError(t, err)
	require.Equal(t, 1, st.limit("a1").LimitedProbes)

	reset := c.Now().Add(3 * time.Hour)
	got, err := r.Bench(ctx, "u1", "a1", reset, "five_hour")
	require.NoError(t, err)
	assert.True(t, got.Equal(reset))
	l := st.limit("a1")
	assert.Zero(t, l.LimitedProbes)
	assert.Equal(t, "five_hour", l.LimitedWindow)

	// An earlier reset does not shorten a bench already in force.
	got, err = r.Bench(ctx, "u1", "a1", c.Now().Add(time.Hour), "five_hour")
	require.NoError(t, err)
	assert.True(t, got.Equal(reset), "returns the bench in force, not the one asked for")
}

func TestBenchSchedulesAConfirmationPollAfterTheReset(t *testing.T) {
	r, st, c := newTestRecorder(t)
	reset := c.Now().Add(2 * time.Hour)
	_, err := r.Bench(context.Background(), "u1", "a1", reset, "five_hour")
	require.NoError(t, err)
	require.Len(t, st.ensures, 1)
	assert.True(t, st.ensures[0].Equal(reset.Add(30*time.Second)))
}

func TestBenchOfAnUnknownAccountErrors(t *testing.T) {
	r, _, _ := newTestRecorder(t)
	_, err := r.Bench(context.Background(), "u1", "ghost", time.Time{}, "")
	assert.Error(t, err)
}

func TestRecorderCloseFlushesWhatIsPending(t *testing.T) {
	c := newClock()
	st := newFakeStore(c)
	st.addAccount("a1", "u1", "claude")
	st.ensureRow("a1")
	r := NewRecorder(st, c.Now)
	r.For("u1", "a1").ObserveRateLimits(context.Background(), snap(c.Now(), ratelimit.StatusAllowed, 0.3, 0.2))
	r.Close()
	r.Close() // idempotent
	require.NotNil(t, st.limit("a1"))
	assert.InDelta(t, 0.3, *st.limit("a1").Windows[0].UsedFraction, 1e-9)
}

func TestSnapshotHeadersMarkTrafficAndASchedule(t *testing.T) {
	r, st, c := newTestRecorder(t)
	r.For("u1", "a1").ObserveRateLimits(context.Background(), snap(c.Now(), ratelimit.StatusAllowed, 0.3, 0.2))
	r.flush(context.Background(), false)
	l := st.limit("a1")
	assert.True(t, l.LastTrafficAt.Equal(c.Now()))
	require.NotNil(t, l.UsageAPINextAt, "an account in use is scheduled for after its headers go stale")
	assert.True(t, l.UsageAPINextAt.Equal(c.Now().Add(10*time.Minute)))
}

func TestRecorderTreatsDriftingRelativeResetsAsTheSameWindow(t *testing.T) {
	r, st, c := newTestRecorder(t)
	obs := r.For("u1", "a1")
	mk := func() ratelimit.Snapshot { // an API-key account: "reset in 6s" becomes a new absolute time every call
		return ratelimit.Snapshot{Status: ratelimit.StatusAllowed, ObservedAt: c.Now(), Windows: []ratelimit.Window{
			{Name: "requests", UsedFraction: f64(0.5), ResetsAt: c.Now().Add(6 * time.Second)}}}
	}
	obs.ObserveRateLimits(context.Background(), mk())
	r.flush(context.Background(), false)
	for i := 0; i < 20; i++ {
		c.Advance(3 * time.Second)
		obs.ObserveRateLimits(context.Background(), mk())
		r.flush(context.Background(), false)
	}
	assert.LessOrEqual(t, st.upserts, 2)
}
