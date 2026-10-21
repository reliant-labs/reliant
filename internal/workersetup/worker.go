// Copyright (c) 2025 Reliant Labs
package workersetup

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// temporalWorker is the part of the SDK's worker.Worker the component drives.
//
// Not the SDK's Run: it calls Start again, and Start panics once the worker
// has stopped — even though the first Start succeeded — so a Drain or a fatal
// error landing between this component's Start and that one crashed the
// process. The component calls Start exactly once and learns of a fatal error
// from worker.Options.OnFatalError instead.
type temporalWorker interface {
	Start() error
	Stop()
}

// Worker is the Temporal worker as a supervised component (a
// lifecyclekit.Runner, Drainer and Readier):
//
//   - Run starts polling the task queue and returns once the worker is
//     stopping: when Drain begins (Drain waits for the stop), when ctx
//     ended (it stops the worker itself), or on a fatal worker error, which
//     it returns.
//   - Drain stops polling and gives in-flight activities
//     activityReleaseGrace to finish; then activityRelease cancels the ones
//     still running, and they report a retryable failure, which releases
//     them for Temporal to re-dispatch at once. Drain waits for that — the
//     SDK's Stop returns once every task has reported, or at
//     WorkerStopTimeout — bounded by ctx: the shutdown budget.
//   - Ready reports nil once the worker has started polling.
//
// It never watches signals itself. The SDK's worker.InterruptCh would stop
// polling on SIGTERM in parallel with the process's own drain, before the
// readiness flip; the process's supervisor decides when Drain runs.
type Worker struct {
	worker temporalWorker
	fatal  <-chan error // the SDK's fatal error (OnFatalError); it stops itself

	mu       sync.Mutex
	running  bool          // Run has begun starting the worker
	draining chan struct{} // closed when Drain begins: a Run that has not begun must not start

	startDone chan struct{} // closed once Start has returned
	started   bool          // Start returned nil; written before startDone closes
}

// newWorker wraps w, whose fatal errors arrive on fatal.
func newWorker(w temporalWorker, fatal <-chan error) *Worker {
	return &Worker{worker: w, fatal: fatal, draining: make(chan struct{}), startDone: make(chan struct{})}
}

// fatalErrorSink is the worker.Options.OnFatalError for a Worker reading
// the returned channel. The SDK reports at most one fatal error.
func fatalErrorSink() (onFatalError func(error), fatal <-chan error) {
	c := make(chan error, 1)
	return func(err error) {
		select {
		case c <- err:
		default:
		}
	}, c
}

// Run starts the worker and blocks until it is stopping (see Worker).
func (w *Worker) Run(ctx context.Context) error {
	w.mu.Lock()
	if w.isDraining() {
		w.mu.Unlock()
		return nil
	}
	w.running = true
	w.mu.Unlock()

	err := w.worker.Start()
	w.started = err == nil
	close(w.startDone)
	if err != nil {
		return fmt.Errorf("start temporal worker: %w", err)
	}

	select {
	case <-w.draining:
		return nil // Drain stops it
	case err := <-w.fatal:
		return fmt.Errorf("temporal worker: %w", err) // the SDK stops it
	case <-ctx.Done():
		if !w.isDraining() {
			w.worker.Stop()
		}
		return nil
	}
}

// Drain stops the worker, bounded by ctx (see Worker).
func (w *Worker) Drain(ctx context.Context) error {
	w.mu.Lock()
	if !w.isDraining() {
		close(w.draining)
	}
	running := w.running
	w.mu.Unlock()
	if !running {
		return nil // never started; Run will not start it now
	}

	// Stop before Start returned would make Start panic; wait for it.
	select {
	case <-w.startDone:
	case <-ctx.Done():
		return fmt.Errorf("temporal worker still starting at the shutdown deadline: %w", ctx.Err())
	}
	if !w.started {
		return nil
	}

	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		w.worker.Stop()
	}()
	select {
	case <-stopped:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("temporal worker still stopping at the shutdown deadline: %w", ctx.Err())
	}
}

// Ready reports nil once the worker has started polling.
func (w *Worker) Ready(context.Context) error {
	select {
	case <-w.startDone:
		if w.started {
			return nil
		}
		return errors.New("temporal worker failed to start")
	default:
		return errors.New("temporal worker starting")
	}
}

func (w *Worker) isDraining() bool {
	select {
	case <-w.draining:
		return true
	default:
		return false
	}
}
