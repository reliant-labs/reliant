// Copyright (c) 2025 Reliant Labs
package catalogcache

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errVerdict = errors.New("credential rejected")

// fakeClock is a settable clock for the cache.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (f *fakeClock) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *fakeClock) advance(d time.Duration) {
	f.mu.Lock()
	f.t = f.t.Add(d)
	f.mu.Unlock()
}

// fakeSource is a catalog endpoint: it answers with val or err, counts its
// calls and, while gate is set, blocks every call until gate is closed.
type fakeSource struct {
	mu    sync.Mutex
	val   string
	err   error
	gate  chan struct{}
	calls atomic.Int32
}

func (s *fakeSource) set(val string, err error) {
	s.mu.Lock()
	s.val, s.err = val, err
	s.mu.Unlock()
}

func (s *fakeSource) fetch(ctx context.Context) (string, error) {
	s.calls.Add(1)
	s.mu.Lock()
	gate := s.gate
	s.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.val, s.err
}

func newTestCache(t *testing.T) (*Cache[string], *fakeClock) {
	t.Helper()
	clock := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	c := New[string](Policy{
		TTL:        5 * time.Minute,
		MaxStale:   time.Hour,
		FailureTTL: 30 * time.Second,
		Timeout:    time.Second,
		Verdict:    func(err error) bool { return errors.Is(err, errVerdict) },
	})
	c.now = clock.now
	return c, clock
}

func wait(t *testing.T, c *Cache[string], key string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, c.Wait(ctx, key))
}

// Past its TTL a report is served at once, and replaced in the background:
// the request that finds it due never waits on the provider.
func TestGet_StaleReportIsServedWhileOneFetchReplacesIt(t *testing.T) {
	t.Parallel()
	c, clock := newTestCache(t)
	src := &fakeSource{val: "v1"}
	ctx := context.Background()

	got, err := c.Get(ctx, "k", src.fetch)
	require.NoError(t, err)
	assert.Equal(t, "v1", got)

	clock.advance(time.Minute)
	got, err = c.Get(ctx, "k", src.fetch)
	require.NoError(t, err)
	assert.Equal(t, "v1", got)
	assert.Equal(t, int32(1), src.calls.Load(), "a fresh report is served with no fetch")

	// The TTL elapses and the catalog changes. The provider is slow: a caller
	// that waited would block on the gate.
	clock.advance(5 * time.Minute)
	src.set("v2", nil)
	gate := make(chan struct{})
	src.mu.Lock()
	src.gate = gate
	src.mu.Unlock()

	for range 5 {
		got, err = c.Get(ctx, "k", src.fetch)
		require.NoError(t, err)
		assert.Equal(t, "v1", got, "the stale report is served while the provider answers")
	}
	close(gate)
	wait(t, c, "k")
	assert.Equal(t, int32(2), src.calls.Load(), "callers of a due key share one background fetch")

	got, err = c.Get(ctx, "k", src.fetch)
	require.NoError(t, err)
	assert.Equal(t, "v2", got, "the refreshed catalog serves the next request")
}

// A report older than MaxStale is not served: the caller waits for the fetch.
func TestGet_ReportPastMaxStaleIsFetchedSynchronously(t *testing.T) {
	t.Parallel()
	c, clock := newTestCache(t)
	src := &fakeSource{val: "v1"}
	ctx := context.Background()

	_, err := c.Get(ctx, "k", src.fetch)
	require.NoError(t, err)

	clock.advance(time.Hour)
	src.set("v2", nil)
	got, err := c.Get(ctx, "k", src.fetch)
	require.NoError(t, err)
	assert.Equal(t, "v2", got)
}

// Concurrent cold callers share one fetch.
func TestGet_ColdCallersShareOneFetch(t *testing.T) {
	t.Parallel()
	c, _ := newTestCache(t)
	gate := make(chan struct{})
	src := &fakeSource{val: "v1", gate: gate}

	var wg sync.WaitGroup
	results := make([]string, 8)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := c.Get(context.Background(), "k", src.fetch)
			assert.NoError(t, err)
			results[i] = v
		}()
	}
	// Every caller is waiting on the one fetch before it answers.
	require.Eventually(t, func() bool { return src.calls.Load() == 1 }, 5*time.Second, time.Millisecond)
	close(gate)
	wg.Wait()
	assert.Equal(t, int32(1), src.calls.Load())
	for _, v := range results {
		assert.Equal(t, "v1", v)
	}
}

// An outage never replaces a good report: it keeps serving, and the fetch is
// retried after FailureTTL.
func TestGet_OutageServesTheLastGoodReport(t *testing.T) {
	t.Parallel()
	c, clock := newTestCache(t)
	src := &fakeSource{val: "v1"}
	ctx := context.Background()

	_, err := c.Get(ctx, "k", src.fetch)
	require.NoError(t, err)

	clock.advance(6 * time.Minute)
	src.set("", errors.New("503"))
	got, err := c.Get(ctx, "k", src.fetch)
	require.NoError(t, err)
	assert.Equal(t, "v1", got)
	wait(t, c, "k")

	got, err = c.Get(ctx, "k", src.fetch)
	require.NoError(t, err, "the outage is hidden behind the last good report")
	assert.Equal(t, "v1", got)
	assert.Equal(t, int32(2), src.calls.Load(), "inside FailureTTL the provider is not asked again")

	clock.advance(31 * time.Second)
	src.set("v2", nil)
	got, err = c.Get(ctx, "k", src.fetch)
	require.NoError(t, err)
	assert.Equal(t, "v1", got)
	wait(t, c, "k")
	got, _ = c.Get(ctx, "k", src.fetch)
	assert.Equal(t, "v2", got, "after FailureTTL the fetch is retried in the background")
}

// An outage with no good report to hide behind is returned, as before.
func TestGet_ColdOutageIsAnError(t *testing.T) {
	t.Parallel()
	c, _ := newTestCache(t)
	src := &fakeSource{err: errors.New("503")}

	_, err := c.Get(context.Background(), "k", src.fetch)
	require.Error(t, err)
	_, err = c.Get(context.Background(), "k", src.fetch)
	require.Error(t, err)
	assert.Equal(t, int32(1), src.calls.Load(), "a failure is remembered for FailureTTL")
}

// A verdict replaces a good report, is served for FailureTTL, and is then asked
// again synchronously: a credential the provider accepts again serves the very
// next resolution.
func TestGet_VerdictReplacesTheReportAndIsAskedAgainSynchronously(t *testing.T) {
	t.Parallel()
	c, clock := newTestCache(t)
	src := &fakeSource{val: "v1"}
	ctx := context.Background()

	_, err := c.Get(ctx, "k", src.fetch)
	require.NoError(t, err)

	clock.advance(6 * time.Minute)
	src.set("", errVerdict)
	_, _ = c.Get(ctx, "k", src.fetch) // stale v1, refresh lands the verdict
	wait(t, c, "k")

	_, err = c.Get(ctx, "k", src.fetch)
	require.ErrorIs(t, err, errVerdict, "a verdict is never hidden behind the stale report")
	_, ok := c.Peek("k")
	assert.False(t, ok, "Peek offers nothing a verdict refuses")

	clock.advance(31 * time.Second)
	src.set("v2", nil)
	got, err := c.Get(ctx, "k", src.fetch)
	require.NoError(t, err)
	assert.Equal(t, "v2", got)
}

// Evict makes the next Get wait for a fresh fetch, not serve the stale report.
func TestEvict_NextGetFetches(t *testing.T) {
	t.Parallel()
	c, _ := newTestCache(t)
	src := &fakeSource{val: "v1"}
	ctx := context.Background()

	_, err := c.Get(ctx, "k", src.fetch)
	require.NoError(t, err)
	src.set("v2", nil)
	c.Evict("k")
	got, err := c.Get(ctx, "k", src.fetch)
	require.NoError(t, err)
	assert.Equal(t, "v2", got)
}

// An evicted refresh may finish before or after its replacement, but must
// neither restore the old catalog nor be shared by post-eviction callers.
func TestEvict_DetachesInflightRefresh(t *testing.T) {
	t.Parallel()
	for _, oldFirst := range []bool{true, false} {
		name := "old_finishes_last"
		if oldFirst {
			name = "old_finishes_first"
		}
		t.Run(name, func(t *testing.T) {
			c, clock := newTestCache(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			c.Store("k", "seed", nil)
			clock.advance(6 * time.Minute)
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			got, err := c.Get(ctx, "k", func(ctx context.Context) (string, error) {
				select {
				case <-release:
					return "refused", nil
				case <-ctx.Done():
					return "", ctx.Err()
				}
			})
			require.NoError(t, err)
			require.Equal(t, "seed", got)
			c.mu.Lock()
			old := c.entries["k"].inflight
			c.mu.Unlock()
			require.NotNil(t, old)
			finishOld := func() {
				unblock()
				select {
				case <-old.done:
				case <-ctx.Done():
					t.Fatal("evicted fetch did not finish")
				}
			}

			c.Evict("k")
			if oldFirst {
				finishOld()
			}
			_, ok := c.Peek("k")
			require.False(t, ok, "eviction must forget the old report")
			got, err = c.Get(ctx, "k", func(context.Context) (string, error) {
				return "fresh", nil
			})
			require.NoError(t, err)
			require.Equal(t, "fresh", got, "a post-eviction Get must not join the old fetch")
			if !oldFirst {
				finishOld()
			}
			got, ok = c.Peek("k")
			require.True(t, ok)
			assert.Equal(t, "fresh", got, "the evicted fetch must not overwrite its replacement")
		})
	}
}

// A caller that gives up returns its own context's error, and the fetch it
// started still lands for the callers after it.
func TestGet_CallerCancellationDoesNotFailTheSharedFetch(t *testing.T) {
	t.Parallel()
	c, _ := newTestCache(t)
	gate := make(chan struct{})
	src := &fakeSource{val: "v1", gate: gate}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Get(ctx, "k", src.fetch)
	require.ErrorIs(t, err, context.Canceled)

	close(gate)
	wait(t, c, "k")
	got, err := c.Get(context.Background(), "k", src.fetch)
	require.NoError(t, err)
	assert.Equal(t, "v1", got)
	assert.Equal(t, int32(1), src.calls.Load())
}

// Entries nothing can serve any more are dropped.
func TestPrune_DropsUnservableEntries(t *testing.T) {
	t.Parallel()
	c, clock := newTestCache(t)
	src := &fakeSource{val: "v1"}
	ctx := context.Background()

	_, err := c.Get(ctx, "old", src.fetch)
	require.NoError(t, err)
	clock.advance(2 * time.Hour)
	_, err = c.Get(ctx, "new", src.fetch)
	require.NoError(t, err)

	c.mu.Lock()
	defer c.mu.Unlock()
	assert.NotContains(t, c.entries, "old")
	assert.Contains(t, c.entries, "new")
}
