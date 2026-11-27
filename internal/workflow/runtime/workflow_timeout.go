// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"errors"
	"fmt"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// A run timeout (Workflow.timeout) bounds a run from its start.
//
// It is enforced here, in the workflow, by a Temporal timer: deterministic,
// replayed like any other command, and needing nothing outside the run. When
// the timer fires the run is cancelled the way a stop cancels it: the run's
// context, which every node, wait and pause derives from, is cancelled, the
// graph unwinds, and the completion defer runs on_failure and finally on a
// disconnected context. Then, instead of ending CANCELLED, the run ends
// FAILED with errRunTimeout: an exceeded timeout is a failure. Ending FAILED
// in Temporal is what settles the rest through the normal run-end path: the
// workflow row and its launch event record failed with the error, and the
// trigger-capacity slot frees when the capacity reconciler sees the chain
// closed (triggers.reconcileCapacityExecution).
//
// The timer counts from the run's start (runStartedAtMs: a continue-as-new
// successor inherits its predecessor's), and nothing pauses it: a paused run,
// or one waiting on an approval, a question, a machine or capacity, is still
// running, and a run stuck in one of those is what the timeout is for.
//
// on_failure and finally run outside it, after it fires: each is already
// bounded by onFailureTimeout, so a timed-out run ends at most that long
// (twice, when it declares both) after its timeout. Re-using that bound keeps
// one rule for how long a run's handlers may take, whatever ended it.
//
// Only a root run is bounded. A workflow run by ref, or a spawned child, runs
// inside its root's run and its root's timeout.

// workflowTimeoutChangeID gates the timer. A history recorded before it has no
// marker and replays without one, even when its definition declares a timeout
// (an older worker that ran it did not enforce one).
const workflowTimeoutChangeID = "workflow-run-timeout"

// runTimeoutErrType is the ApplicationError type a timed-out run fails with.
const runTimeoutErrType = "RunTimeout"

// runTimeout is one run's armed timeout. Its methods are nil-safe: nil is a
// run with no timeout.
type runTimeout struct {
	literal string
	// fired: the timer cancelled the run.
	fired bool
	// disarmed: the run is ending on its own; a timer that fires from here
	// on does nothing, so the completion bookkeeping is never cancelled
	// half-way through.
	disarmed bool
}

// armRunTimeout starts the run's timeout timer when wf declares one and the
// run is a root run, and returns the context the run should continue on:
// cancelled when the timeout passes. ctx itself is returned, with a nil
// runTimeout, when there is nothing to arm.
func armRunTimeout(ctx workflow.Context, wf *reliantv1.Workflow, resume *ResumeInput, root bool) (workflow.Context, *runTimeout) {
	if !root {
		return ctx, nil
	}
	timeout, literal, set, err := model.WorkflowTimeout(wf)
	if !set {
		return ctx, nil
	}
	if err != nil {
		// Validation refuses this at load; a definition that got past an
		// older validator runs unbounded rather than failing on it.
		workflow.GetLogger(ctx).Warn("[Workflow Runtime] Ignoring an invalid run timeout", "timeout", literal, "error", err)
		return ctx, nil
	}
	if workflow.GetVersion(ctx, workflowTimeoutChangeID, workflow.DefaultVersion, 1) == workflow.DefaultVersion {
		return ctx, nil
	}
	rt := &runTimeout{literal: literal}
	runCtx, cancel := workflow.WithCancel(ctx)
	deadline := time.UnixMilli(runStartedAtMs(ctx, resume)).Add(timeout)
	remaining := deadline.Sub(workflow.Now(ctx))
	if remaining < 0 {
		remaining = 0
	}
	workflow.Go(ctx, func(gctx workflow.Context) {
		// A cancel of the whole run cancels the timer too, and its error
		// ends this coroutine without firing.
		if err := workflow.NewTimer(gctx, remaining).Get(gctx, nil); err != nil {
			return
		}
		if rt.disarmed || runCtx.Err() != nil {
			return
		}
		rt.fired = true
		workflow.GetLogger(gctx).Info("[Workflow Runtime] Run timeout exceeded, cancelling the run",
			"workflowID", workflow.GetInfo(gctx).WorkflowExecution.ID, "timeout", literal)
		cancel()
	})
	return runCtx, rt
}

// disarm is called as the run starts ending: from here a firing timer leaves
// the run alone.
func (rt *runTimeout) disarm() {
	if rt != nil {
		rt.disarmed = true
	}
}

// expired reports whether the timeout ended the run.
func (rt *runTimeout) expired() bool { return rt != nil && rt.fired }

// RunTimeoutError is what a run its timeout ended fails with: the run's
// error, its failure.error, and the error its row and launch event record.
type RunTimeoutError struct {
	// Timeout is the declared timeout, as written ("3h").
	Timeout string
}

func (e *RunTimeoutError) Error() string { return fmt.Sprintf("run timeout %s exceeded", e.Timeout) }

// err is the run's failure, once the timeout ended it.
func (rt *runTimeout) err() error { return &RunTimeoutError{Timeout: rt.literal} }

// temporalErr is the error the execution returns to Temporal: non-retryable,
// so Temporal does not start the run again, with the same message.
func (rt *runTimeout) temporalErr() error {
	return temporal.NewNonRetryableApplicationError(rt.err().Error(), runTimeoutErrType, nil)
}

// isRunTimeout reports whether err is a timed-out run's failure.
func isRunTimeout(err error) bool {
	var timeoutErr *RunTimeoutError
	if errors.As(err, &timeoutErr) {
		return true
	}
	var appErr *temporal.ApplicationError
	return errors.As(err, &appErr) && appErr.Type() == runTimeoutErrType
}
