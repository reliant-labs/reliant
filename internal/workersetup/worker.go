// Copyright (c) 2025 Reliant Labs
package workersetup

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// temporalWorker is the part of the SDK's worker.Worker the component drives.
type temporalWorker interface {
	Start() error
	Run(interruptCh <-chan interface{}) error
	Stop()
}

// Worker is the Temporal worker as a supervised component (a
// lifecyclekit.Runner, Drainer and Readier):
//
//   - Run starts polling the task queue and returns once the worker has
//     stopped: when Drain stopped it, when ctx ended (it stops the worker
//     itself), or on a fatal worker error, which it returns.
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

	mu       sync.Mutex
	running  bool // Run has begun starting the worker
	draining bool // Drain has begun: a Run that has not begun must not start

	startDone chan struct{} // closed once Start has returned
	started   bool          // Start returned nil; written before startDone closes
}

func newWorker(w temporalWorker) *Worker {
	return &Worker{worker: w, startDone: make(chan struct{})}
}

// Run starts the worker and blocks until it stops (see Worker).
func (w *Worker) Run(ctx context.Context) error {
	w.mu.Lock()
	if w.draining {
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

	interrupt := make(chan interface{})
	stop := context.AfterFunc(ctx, func() { close(interrupt) })
	defer stop()
	// Start is memoized: Run reuses the start above and blocks until the
	// interrupt or until the worker stops (Drain's Stop, or a fatal error).
	if err := w.worker.Run(interrupt); err != nil {
		return fmt.Errorf("temporal worker: %w", err)
	}
	if ctx.Err() == nil && !w.isDraining() {
		return errors.New("temporal worker stopped without being asked to")
	}
	return nil
}

// Drain stops the worker, bounded by ctx (see Worker).
func (w *Worker) Drain(ctx context.Context) error {
	w.mu.Lock()
	w.draining = true
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
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.draining
}
