// Copyright (c) 2025 Reliant Labs

package mcpserver

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLimiterAllowsNormalUse(t *testing.T) {
	l := NewLimiter(Limits{RequestsPerMinute: 120, Burst: 10, MaxConcurrent: 4})

	for i := 0; i < 10; i++ {
		release, err := l.Acquire("grant-1")
		require.NoError(t, err, "a burst within the configured allowance must pass")
		release()
	}
}

func TestLimiterStopsARunawayLoop(t *testing.T) {
	// The failure being defended against: a model that reads a refusal as
	// transient and retries as fast as it can.
	l := NewLimiter(Limits{RequestsPerMinute: 60, Burst: 5, MaxConcurrent: 10})

	var lastErr error
	for i := 0; i < 100; i++ {
		release, err := l.Acquire("grant-1")
		if err != nil {
			lastErr = err
			break
		}
		release()
	}

	require.Error(t, lastErr, "an unbounded loop must eventually be refused")
	require.ErrorIs(t, lastErr, ErrRateLimited)
	require.Contains(t, lastErr.Error(), "requests per minute",
		"the refusal should say what the limit is")
}

// TestLimiterIsolatesGrants is the property that makes per-grant the right
// unit: one connector in a loop must not deny service to another, or to the
// user's own session sharing the workspace.
func TestLimiterIsolatesGrants(t *testing.T) {
	l := NewLimiter(Limits{RequestsPerMinute: 60, Burst: 3, MaxConcurrent: 10})

	// Exhaust one grant.
	for i := 0; i < 20; i++ {
		release, err := l.Acquire("noisy")
		if err != nil {
			break
		}
		release()
	}
	_, err := l.Acquire("noisy")
	require.Error(t, err, "the noisy grant should be limited by now")

	// A different grant is unaffected.
	release, err := l.Acquire("quiet")
	require.NoError(t, err, "one connector's loop must not starve another")
	release()
}

func TestLimiterBoundsConcurrency(t *testing.T) {
	l := NewLimiter(Limits{RequestsPerMinute: 6000, Burst: 1000, MaxConcurrent: 3})

	var releases []func()
	for i := 0; i < 3; i++ {
		release, err := l.Acquire("grant-1")
		require.NoError(t, err)
		releases = append(releases, release)
	}

	_, err := l.Acquire("grant-1")
	require.ErrorIs(t, err, ErrTooManyConcurrent,
		"a fourth simultaneous command must be refused")
	require.Contains(t, err.Error(), "at once")

	// Releasing one frees a slot.
	releases[0]()
	release, err := l.Acquire("grant-1")
	require.NoError(t, err, "releasing an in-flight call must free capacity")
	release()

	for _, r := range releases[1:] {
		r()
	}
}

// TestReleaseIsIdempotent guards the deferred-release path: a double release
// would decrement someone else's count and slowly leak capacity.
func TestReleaseIsIdempotent(t *testing.T) {
	l := NewLimiter(Limits{RequestsPerMinute: 6000, Burst: 1000, MaxConcurrent: 2})

	release, err := l.Acquire("grant-1")
	require.NoError(t, err)
	release()
	release()
	release()

	// Capacity is still exactly 2, not inflated by the extra releases.
	r1, err := l.Acquire("grant-1")
	require.NoError(t, err)
	r2, err := l.Acquire("grant-1")
	require.NoError(t, err)
	_, err = l.Acquire("grant-1")
	require.ErrorIs(t, err, ErrTooManyConcurrent, "extra releases must not inflate capacity")

	r1()
	r2()
}

func TestLimiterIsConcurrencySafe(t *testing.T) {
	l := NewLimiter(Limits{RequestsPerMinute: 60000, Burst: 10000, MaxConcurrent: 50})

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := l.Acquire("grant-1")
			if err == nil {
				time.Sleep(time.Millisecond)
				release()
			}
		}()
	}
	wg.Wait()

	// Every acquisition was released, so full capacity is available again.
	release, err := l.Acquire("grant-1")
	require.NoError(t, err, "in-flight counts must balance under concurrency")
	release()
}

func TestCleanupKeepsInFlightEntries(t *testing.T) {
	l := NewLimiter(Limits{})

	release, err := l.Acquire("busy")
	require.NoError(t, err)

	// Force both entries to look idle.
	l.mu.Lock()
	for _, entry := range l.perKey {
		entry.lastSeen = time.Now().Add(-2 * idleLimiterTTL)
	}
	l.mu.Unlock()

	l.Cleanup()

	// Evicting an entry with work in flight would make its release decrement
	// a fresh entry and corrupt the count.
	l.mu.Lock()
	_, stillTracked := l.perKey["busy"]
	l.mu.Unlock()
	require.True(t, stillTracked, "an entry with a call in flight must not be evicted")

	release()

	l.Cleanup()
	l.mu.Lock()
	_, present := l.perKey["busy"]
	l.mu.Unlock()
	require.False(t, present, "an idle entry should be reaped once its work finishes")
}

func TestNilLimiterIsPermissive(t *testing.T) {
	var l *Limiter
	release, err := l.Acquire("grant-1")
	require.NoError(t, err)
	require.NotNil(t, release)
	release()
}

func TestZeroLimitsUseDefaults(t *testing.T) {
	l := NewLimiter(Limits{})
	require.Equal(t, defaultRequestsPerMinute, l.limits.RequestsPerMinute)
	require.Equal(t, defaultBurst, l.limits.Burst)
	require.Equal(t, defaultMaxConcurrent, l.limits.MaxConcurrent)

	// A zero value must limit rather than disable limiting: this endpoint is
	// public, so an unconfigured deployment must still be bounded.
	release, err := l.Acquire("grant-1")
	require.NoError(t, err)
	release()
	require.NotZero(t, l.limits.MaxConcurrent)
}

func TestLimitedCallsAreRefusedWithGuidance(t *testing.T) {
	sender := &fakeSender{}
	sess := testSession()
	limiter := NewLimiter(Limits{RequestsPerMinute: 60, Burst: 1, MaxConcurrent: 10})
	deps := Deps{Sender: sender, Limiter: limiter}

	// First call consumes the burst.
	res := callTool(t, sess, deps, "read_file", map[string]any{"path": "/workspace/a.txt"})
	require.False(t, res.IsError)

	res = callTool(t, sess, deps, "read_file", map[string]any{"path": "/workspace/a.txt"})
	require.True(t, res.IsError, "the second call should exceed the burst")
	require.Contains(t, resultText(t, res), "Wait before trying again",
		"the model needs to be told that retrying immediately will not help")
	require.Len(t, sender.calls, 1, "a limited call must not reach the daemon")
}

// TestLimitedCallsAreNotAudited: a model looping at ten requests a second
// would otherwise fill the audit log with the noise that makes it unreadable.
func TestLimitedCallsAreNotAudited(t *testing.T) {
	audit := &fakeAudit{}
	sess := testSession()
	limiter := NewLimiter(Limits{RequestsPerMinute: 60, Burst: 1, MaxConcurrent: 10})
	deps := Deps{Sender: &fakeSender{}, Audit: audit, Limiter: limiter}

	callTool(t, sess, deps, "read_file", map[string]any{"path": "/workspace/a.txt"})
	before := len(audit.entries)

	for i := 0; i < 5; i++ {
		callTool(t, sess, deps, "read_file", map[string]any{"path": "/workspace/a.txt"})
	}

	require.Equal(t, before, len(audit.entries),
		"rate-limited calls must not be written to the audit log")
}

func TestErrorsAreDistinguishable(t *testing.T) {
	require.False(t, errors.Is(ErrRateLimited, ErrTooManyConcurrent),
		"rate and concurrency limits are different conditions and must be told apart")
}
