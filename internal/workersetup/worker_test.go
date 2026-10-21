// Copyright (c) 2025 Reliant Labs
package workersetup

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeTemporalWorker mimics the SDK's AggregatedWorker: Start is memoized,
// Stop is idempotent and closes the stop channel first, Start after Stop
// panics, and a fatal error calls OnFatalError and then Stop.
type fakeTemporalWorker struct {
	startGate chan struct{} // nil: Start returns at once
	startErr  error
	stopGate  chan struct{} // nil: Stop returns at once

	starts, stopsBegun atomic.Int32
	stopOnce           sync.Once
	stopC              chan struct{}
	onFatalError       func(error)
	fatal              <-chan error
}

func newFakeTemporalWorker() *fakeTemporalWorker {
	onFatalError, fatal := fatalErrorSink()
	return &fakeTemporalWorker{stopC: make(chan struct{}), onFatalError: onFatalError, fatal: fatal}
}

func newFakeWorker(f *fakeTemporalWorker) *Worker { return newWorker(f, f.fatal) }

func (f *fakeTemporalWorker) Start() error {
	select {
	case <-f.stopC:
		panic("Start after Stop")
	default:
	}
	if f.starts.Add(1) == 1 && f.startGate != nil {
		<-f.startGate
	}
	return f.startErr
}

func (f *fakeTemporalWorker) Stop() {
	first := false
	f.stopOnce.Do(func() {
		first = true
		f.stopsBegun.Add(1)
		close(f.stopC)
	})
	if first && f.stopGate != nil {
		<-f.stopGate
	}
}

func (f *fakeTemporalWorker) failFatally(err error) {
	f.onFatalError(err)
	f.Stop()
}

func runInBackground(ctx context.Context, w *Worker) <-chan error {
	ran := make(chan error, 1)
	go func() { ran <- w.Run(ctx) }()
	return ran
}

func requireReturns(t *testing.T, ran <-chan error) error {
	t.Helper()
	select {
	case err := <-ran:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
		return nil
	}
}

func awaitReady(t *testing.T, w *Worker) {
	t.Helper()
	require.Eventually(t, func() bool { return w.Ready(context.Background()) == nil },
		5*time.Second, time.Millisecond)
}

func TestWorkerDrainStopsARunningWorker(t *testing.T) {
	fake := newFakeTemporalWorker()
	w := newFakeWorker(fake)
	require.Error(t, w.Ready(context.Background()), "not ready before it runs")

	ran := runInBackground(context.Background(), w)
	awaitReady(t, w)

	require.NoError(t, w.Drain(context.Background()))
	require.NoError(t, requireReturns(t, ran), "a drained worker's Run is a clean stop")
	require.EqualValues(t, 1, fake.stopsBegun.Load())
	// A second Start (the SDK's Run makes one) panics if a stop beat it.
	require.EqualValues(t, 1, fake.starts.Load(), "Start runs once")
}

func TestWorkerStopsWhenItsRunContextEnds(t *testing.T) {
	fake := newFakeTemporalWorker()
	w := newFakeWorker(fake)
	ctx, cancel := context.WithCancel(context.Background())
	ran := runInBackground(ctx, w)
	awaitReady(t, w)

	cancel()
	require.NoError(t, requireReturns(t, ran))
	require.EqualValues(t, 1, fake.stopsBegun.Load())
}

func TestWorkerDrainBeforeRunNeverStartsIt(t *testing.T) {
	fake := newFakeTemporalWorker()
	w := newFakeWorker(fake)

	require.NoError(t, w.Drain(context.Background()))
	require.NoError(t, w.Run(context.Background()))
	require.Zero(t, fake.starts.Load(), "Start after Stop panics in the SDK: it must not be reached")
	require.Zero(t, fake.stopsBegun.Load())
}

// Drain arriving while Start is still in flight waits for it: stopping
// first would make the SDK's Start panic.
func TestWorkerDrainWaitsForAStartInFlight(t *testing.T) {
	fake := newFakeTemporalWorker()
	fake.startGate = make(chan struct{})
	w := newFakeWorker(fake)
	ran := runInBackground(context.Background(), w)
	require.Eventually(t, func() bool { return fake.starts.Load() == 1 }, 5*time.Second, time.Millisecond)

	drained := make(chan error, 1)
	go func() { drained <- w.Drain(context.Background()) }()
	select {
	case <-drained:
		t.Fatal("Drain returned while Start was still running")
	case <-time.After(20 * time.Millisecond):
	}

	close(fake.startGate)
	require.NoError(t, <-drained)
	require.NoError(t, requireReturns(t, ran))
	require.EqualValues(t, 1, fake.stopsBegun.Load())
	require.EqualValues(t, 1, fake.starts.Load(), "Start runs once")
}

func TestWorkerDrainIsBoundedByTheShutdownBudget(t *testing.T) {
	fake := newFakeTemporalWorker()
	fake.stopGate = make(chan struct{})
	defer close(fake.stopGate)
	w := newFakeWorker(fake)
	ran := runInBackground(context.Background(), w)
	awaitReady(t, w)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := w.Drain(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorContains(t, err, "still stopping at the shutdown deadline")
	// Run returns as soon as the stop begins, as the SDK's does.
	require.NoError(t, requireReturns(t, ran))
}

func TestWorkerRunReturnsAFatalWorkerError(t *testing.T) {
	fake := newFakeTemporalWorker()
	w := newFakeWorker(fake)
	ran := runInBackground(context.Background(), w)
	awaitReady(t, w)

	fatal := errors.New("namespace not found")
	fake.failFatally(fatal)
	require.ErrorIs(t, requireReturns(t, ran), fatal)
	require.EqualValues(t, 1, fake.starts.Load(), "Start runs once")
	require.NoError(t, w.Drain(context.Background()), "draining a worker that already stopped is a no-op")
}

func TestWorkerRunReturnsAStartFailure(t *testing.T) {
	fake := newFakeTemporalWorker()
	fake.startErr = errors.New("temporal unreachable")
	w := newFakeWorker(fake)

	require.ErrorIs(t, w.Run(context.Background()), fake.startErr)
	require.ErrorContains(t, w.Ready(context.Background()), "failed to start")
	require.NoError(t, w.Drain(context.Background()))
	require.Zero(t, fake.stopsBegun.Load(), "a worker that never started is not stopped")
}
