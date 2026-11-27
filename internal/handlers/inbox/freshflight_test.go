package inbox

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

// heldRun is a run fn that closes started, then holds until release.
type heldRun struct {
	started, gate chan struct{}
	once          sync.Once
}

func newHeldRun(t *testing.T) *heldRun {
	h := &heldRun{started: make(chan struct{}), gate: make(chan struct{})}
	t.Cleanup(h.release)
	return h
}

func (h *heldRun) release() { h.once.Do(func() { close(h.gate) }) }

func (h *heldRun) fn(val int) func() (int, error) {
	return func() (int, error) {
		close(h.started)
		<-h.gate
		return val, nil
	}
}

type flightResult struct {
	val int
	err error
}

// doAsync runs g.Do in a goroutine; its result arrives on the returned channel.
func doAsync(ctx context.Context, g *freshFlight[int], key string, fn func() (int, error)) <-chan flightResult {
	out := make(chan flightResult, 1)
	go func() {
		v, err := g.Do(ctx, key, fn)
		out <- flightResult{v, err}
	}()
	return out
}

func waitFlightQueued(t *testing.T, g *freshFlight[int], key string, n int) {
	t.Helper()
	require.Eventually(t, func() bool { return g.queued(key) == n }, 10*time.Second, time.Millisecond,
		"%d calls queued on %q", n, key)
}

func waitFlightIdle(t *testing.T, g *freshFlight[int]) {
	t.Helper()
	require.Eventually(t, func() bool {
		g.mu.Lock()
		defer g.mu.Unlock()
		return len(g.keys) == 0
	}, 10*time.Second, time.Millisecond, "every key forgotten once idle")
}

func unexpected(t *testing.T) func() (int, error) {
	return func() (int, error) {
		t.Error("this run should never execute")
		return 0, nil
	}
}

func TestFreshFlight_ACallAloneRunsItsFn(t *testing.T) {
	var g freshFlight[int]
	v, err := g.Do(context.Background(), "k", func() (int, error) { return 7, nil })
	require.NoError(t, err)
	assert.Equal(t, 7, v)
	waitFlightIdle(t, &g)
}

func TestFreshFlight_AlreadyCanceledCallDoesNotRegister(t *testing.T) {
	var g freshFlight[int]
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	v, err := g.Do(ctx, "k", unexpected(t))
	assert.Zero(t, v)
	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, g.keys, "a canceled call must not create a run")

	h := newHeldRun(t)
	first := doAsync(context.Background(), &g, "k", h.fn(1))
	<-h.started
	v, err = g.Do(ctx, "k", unexpected(t))
	assert.Zero(t, v)
	require.ErrorIs(t, err, context.Canceled)
	g.mu.Lock()
	next := g.keys["k"].next
	g.mu.Unlock()
	assert.Nil(t, next, "a canceled call must not queue a run")
	h.release()
	assert.Equal(t, flightResult{1, nil}, <-first)
	waitFlightIdle(t, &g)
}

func TestFreshFlight_CancellationBeforeResultReturnsContextError(t *testing.T) {
	var g freshFlight[int]
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	v, err := g.Do(ctx, "k", func() (int, error) {
		cancel()
		return 7, nil
	})
	assert.Zero(t, v)
	require.ErrorIs(t, err, context.Canceled)
	waitFlightIdle(t, &g)
}

// A call arriving during a run gets a run that starts after it: the next one,
// shared with every call queued beside it and started from the first of them.
func TestFreshFlight_ACallDuringARunGetsTheNextRun(t *testing.T) {
	var g freshFlight[int]
	h := newHeldRun(t)
	first := doAsync(context.Background(), &g, "k", h.fn(1))
	<-h.started

	second := doAsync(context.Background(), &g, "k", func() (int, error) { return 2, nil })
	waitFlightQueued(t, &g, "k", 1)
	third := doAsync(context.Background(), &g, "k", unexpected(t))
	waitFlightQueued(t, &g, "k", 2)
	h.release()

	assert.Equal(t, flightResult{1, nil}, <-first)
	assert.Equal(t, flightResult{2, nil}, <-second, "not the run already in flight")
	assert.Equal(t, flightResult{2, nil}, <-third, "shares the queued run")
	waitFlightIdle(t, &g)
}

// A burst during one run costs exactly one more run.
func TestFreshFlight_ABurstDuringARunCostsOneMoreRun(t *testing.T) {
	var g freshFlight[int]
	h := newHeldRun(t)
	first := doAsync(context.Background(), &g, "k", h.fn(0))
	<-h.started

	var runs atomic.Int32
	const burst = 8
	results := make([]<-chan flightResult, burst)
	for i := range results {
		results[i] = doAsync(context.Background(), &g, "k", func() (int, error) { return int(runs.Add(1)), nil })
	}
	waitFlightQueued(t, &g, "k", burst)
	h.release()

	require.NoError(t, (<-first).err)
	for _, r := range results {
		assert.Equal(t, flightResult{1, nil}, <-r)
	}
	assert.Equal(t, int32(1), runs.Load(), "one run for the whole burst")
	waitFlightIdle(t, &g)
}

// A caller that gives up returns at once with its context's error; the run it
// queued (and started from its fn) still answers the callers who stayed.
func TestFreshFlight_ACancelledCallerReturnsAtOnceAndTheRunGoesOn(t *testing.T) {
	var g freshFlight[int]
	h := newHeldRun(t)
	first := doAsync(context.Background(), &g, "k", h.fn(1))
	<-h.started

	ctx, cancel := context.WithCancel(context.Background())
	quitter := doAsync(ctx, &g, "k", func() (int, error) { return 2, nil })
	waitFlightQueued(t, &g, "k", 1)
	stayer := doAsync(context.Background(), &g, "k", unexpected(t))
	waitFlightQueued(t, &g, "k", 2)

	cancel()
	assert.ErrorIs(t, (<-quitter).err, context.Canceled, "returns while the run in flight still holds")
	waitFlightQueued(t, &g, "k", 1)
	h.release()

	assert.Equal(t, flightResult{1, nil}, <-first)
	assert.Equal(t, flightResult{2, nil}, <-stayer)
	waitFlightIdle(t, &g)
}

// A caller giving up on the run in flight does not stop it for the others.
func TestFreshFlight_TheRunInFlightOutlivesItsCaller(t *testing.T) {
	var g freshFlight[int]
	h := newHeldRun(t)
	ctx, cancel := context.WithCancel(context.Background())
	first := doAsync(ctx, &g, "k", h.fn(1))
	<-h.started
	cancel()
	assert.ErrorIs(t, (<-first).err, context.Canceled)

	// The run is still in flight: a new call queues behind it rather than
	// starting a second concurrent run.
	second := doAsync(context.Background(), &g, "k", func() (int, error) { return 2, nil })
	waitFlightQueued(t, &g, "k", 1)
	h.release()
	assert.Equal(t, flightResult{2, nil}, <-second)
	waitFlightIdle(t, &g)
}

// A queued run whose every caller gave up before it started never runs.
func TestFreshFlight_AQueuedRunNobodyWaitsForIsSkipped(t *testing.T) {
	var g freshFlight[int]
	h := newHeldRun(t)
	first := doAsync(context.Background(), &g, "k", h.fn(1))
	<-h.started

	ctx, cancel := context.WithCancel(context.Background())
	quitter := doAsync(ctx, &g, "k", unexpected(t))
	waitFlightQueued(t, &g, "k", 1)
	cancel()
	assert.ErrorIs(t, (<-quitter).err, context.Canceled)
	waitFlightQueued(t, &g, "k", 0)
	h.release()

	assert.Equal(t, flightResult{1, nil}, <-first)
	waitFlightIdle(t, &g)
	v, err := g.Do(context.Background(), "k", func() (int, error) { return 3, nil })
	require.NoError(t, err)
	assert.Equal(t, 3, v, "the key starts afresh")
}

// Keys never share: a run of one key does not hold a call of another.
func TestFreshFlight_KeysAreIndependent(t *testing.T) {
	var g freshFlight[int]
	h := newHeldRun(t)
	first := doAsync(context.Background(), &g, "a", h.fn(1))
	<-h.started

	v, err := g.Do(context.Background(), "b", func() (int, error) { return 2, nil })
	require.NoError(t, err)
	assert.Equal(t, 2, v, "b ran while a's run held")
	h.release()
	assert.Equal(t, flightResult{1, nil}, <-first)
	waitFlightIdle(t, &g)
}

func TestFreshFlight_AnErrorReachesEveryCallerOfTheRun(t *testing.T) {
	var g freshFlight[int]
	h := newHeldRun(t)
	first := doAsync(context.Background(), &g, "k", h.fn(1))
	<-h.started

	boom := errors.New("boom")
	failing := func() (int, error) { return 0, boom }
	a := doAsync(context.Background(), &g, "k", failing)
	waitFlightQueued(t, &g, "k", 1)
	b := doAsync(context.Background(), &g, "k", unexpected(t))
	waitFlightQueued(t, &g, "k", 2)
	h.release()

	require.NoError(t, (<-first).err)
	assert.ErrorIs(t, (<-a).err, boom)
	assert.ErrorIs(t, (<-b).err, boom)
	waitFlightIdle(t, &g)
}

// A panicking run fails its callers instead of the process, and the key keeps working.
func TestFreshFlight_APanicIsReturnedAsAnError(t *testing.T) {
	var g freshFlight[int]
	_, err := g.Do(context.Background(), "k", func() (int, error) { panic("kaboom") })
	require.ErrorContains(t, err, "kaboom")
	waitFlightIdle(t, &g)

	v, err := g.Do(context.Background(), "k", func() (int, error) { return 4, nil })
	require.NoError(t, err)
	assert.Equal(t, 4, v)
}
