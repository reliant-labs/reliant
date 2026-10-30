// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"errors"
	"fmt"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// PauseController bundles the pause-related callbacks that every executor
// needs: checking/handling a pause signal at step boundaries and obtaining
// the current cancellable activity context.
//
// All methods are nil-receiver safe — callers never need a nil guard.
type PauseController struct {
	// CheckPause is called at step boundaries to cooperatively block when a
	// pause signal has been received. The workflow.Context parameter MUST be
	// the calling goroutine's own context — using a parent/root context from
	// a different coroutine causes Temporal's "trying to block on coroutine
	// which is already blocked" panic.
	CheckPause func(workflow.Context)

	// ActivityCtxFn returns the current cancellable context for dispatching
	// activities. This is a function (not a stored context) so that after
	// pause/resume the caller can swap in a fresh context and all executors
	// (including nested ones) automatically pick it up.
	ActivityCtxFn func() workflow.Context

	// RequestPause triggers a self-pause from within the workflow. Used when
	// a retryable error (like a rate limit) exhausts Temporal's retry budget.
	// After calling this, callers should call DoCheckPause to block until resume.
	RequestPause func()

	// DaemonOffline is the per-run daemon-offline circuit breaker. It rides
	// on the PauseController because that's the one object DynamicWorkflow
	// already threads through every executor (main loop, inline workflows,
	// loop iterations, spawned threads), so consecutive "no daemon connected"
	// failures are counted across the whole run. nil when not wired
	// (tests).
	DaemonOffline *DaemonOfflineCircuitBreaker

	// RepeatedToolErrors ends an unattended agent whose tool calls keep
	// failing with the same error (repeated_tool_error_breaker.go). One per
	// run, shared like DaemonOffline, keyed by thread inside. nil when not
	// wired (tests).
	RepeatedToolErrors *RepeatedToolErrorBreaker

	// Cancelled reports whether the user asked to cancel the thread this
	// controller belongs to. Spawned threads run INLINE inside the parent's
	// Temporal execution (executeSpawnInline, via workflow.Go), so there is no
	// child workflow to terminate — cancelling one means the spawn's own loop
	// noticing at a step boundary and stopping.
	//
	// That distinction cost a real user-visible bug: cancelling a spawn called
	// TerminateWorkflow(child_workflow_id), which failed with "workflow not
	// found for ID" because that id names a THREAD, not a Temporal execution.
	// The DB was still marked cancelled, so the UI said "cancelled" while the
	// spawn wrote another twelve messages over the next seventeen minutes.
	//
	// nil when not wired (tests, non-spawn executors).
	Cancelled func() bool

	// ResetCancelled clears the thread-keyed cancellation flag. A resumption
	// of an agent reuses the agent's thread id, so a stop aimed at an earlier
	// run of that thread must not cancel the new one. The tool-call-keyed
	// flag is left alone: it names exactly one run and is never reused.
	// nil when not wired.
	ResetCancelled func()

	// SubAgent marks the controller of a spawned sub-agent's thread. Its
	// CheckPause and RequestPause are still the run's shared ones — a user
	// pause stops a sub-agent like any other thread — but a step that exhausts
	// its retries on this thread ends the agent and reports to its parent
	// instead of pausing the run (see failsAlone). Set by the spawn path for
	// the spawn's own controller, which every executor inside the spawn
	// inherits.
	SubAgent bool

	// FailFast marks the controller of an on_failure handler. It has no pause
	// hooks, and a step that exhausts its retries on it ends the handler with
	// that error instead of waiting for a resume nothing will send: the run is
	// already failed, and a handler that parks would hold it open.
	FailFast bool

	// UsageLimit holds a step that failed on a provider's usage limit until the
	// limit resets (usage_limit_wait.go). One per run, shared by every thread.
	// nil when not wired (tests).
	UsageLimit *usageLimitWaiter

	// MachineWake wakes the run's machine before the first machine step after
	// a hold (machine_resume_wake.go). One per run, shared by every thread so
	// threads resuming together share one wake. nil when the run needs no
	// machine, and in tests.
	MachineWake *machineResumeWaker

	// GaveUp is the error the run must end with because an unattended
	// self-pause outlived its self-resume ladder (pauseCoordinator.GaveUp),
	// or nil. nil func when not wired (tests, on_failure handlers).
	GaveUp func() error
}

// gaveUp is GaveUp, nil-safe.
func (pc *PauseController) gaveUp() error {
	if pc == nil || pc.GaveUp == nil {
		return nil
	}
	return pc.GaveUp()
}

// endsRunInsteadOfPausing is the error a step that exhausted its retries ends
// the run with instead of pausing it, or nil to pause as usual:
//
//   - the run already gave up (an unattended self-pause outlived its ladder):
//     nothing may park any more;
//   - the step's machine never took its command within the run step's own
//     wait (types.RunStepMachineUnavailableErrType). That wait was the retry:
//     a pause would only hold the run, and an automation's capacity slot, for
//     a resume nobody may send.
//
// Every executor's pause site asks this first.
func (pc *PauseController) endsRunInsteadOfPausing(stepID string, err error) error {
	if gaveUp := pc.gaveUp(); gaveUp != nil {
		return gaveUp
	}
	var appErr *temporal.ApplicationError
	if errors.As(err, &appErr) && appErr.Type() == types.RunStepMachineUnavailableErrType {
		return temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("step %s failed: %s", stepID, appErr.Message()),
			types.RunStepMachineUnavailableErrType, nil)
	}
	return nil
}

// failsFast reports whether a step that exhausted its retries on this
// controller ends the executor with its error (an on_failure handler).
func (pc *PauseController) failsFast() bool { return pc != nil && pc.FailFast }

// IsCancelled reports whether this thread has been cancelled by the user.
// Nil-safe on both the receiver and the function.
func (pc *PauseController) IsCancelled() bool {
	return pc != nil && pc.Cancelled != nil && pc.Cancelled()
}

// DoCheckPause calls CheckPause if the receiver and the function are non-nil.
// The ctx MUST be the calling goroutine's own workflow.Context.
func (pc *PauseController) DoCheckPause(ctx workflow.Context) {
	if pc != nil && pc.CheckPause != nil {
		pc.CheckPause(ctx)
	}
}

// GetActivityCtx returns the current cancellable activity context.
// Falls back to fallback if the receiver or ActivityCtxFn is nil.
func (pc *PauseController) GetActivityCtx(fallback workflow.Context) workflow.Context {
	if pc != nil && pc.ActivityCtxFn != nil {
		return pc.ActivityCtxFn()
	}
	return fallback
}

// DoRequestPause calls RequestPause if the receiver and the function are non-nil.
func (pc *PauseController) DoRequestPause() {
	if pc != nil && pc.RequestPause != nil {
		pc.RequestPause()
	}
}

// DoRequestPauseFor is DoRequestPause for a step that exhausted its retries on
// err, recording the cause so a capacity wake can tell a pause it may release
// (a provider-capacity failure) from one it must not (capacity_wake.go).
func (pc *PauseController) DoRequestPauseFor(err error) {
	pc.DoRequestPause()
	if pc != nil && pc.UsageLimit != nil && pc.UsageLimit.pauser != nil {
		pc.UsageLimit.pauser.noteSelfPauseCause(err)
	}
}

// ObserveRepeatedToolErrors feeds a completed step to the repeated-tool-error
// breaker, if one is wired, and returns the error that ends the agent when the
// same failure has repeated too often. Nil-safe on both the receiver and the
// breaker.
func (pc *PauseController) ObserveRepeatedToolErrors(thread, activityName string, stepEvent *StepEvent) error {
	if pc == nil {
		return nil
	}
	return pc.RepeatedToolErrors.Observe(thread, activityName, stepEvent)
}

// ObserveDaemonOfflineStep feeds a completed step to the daemon-offline
// circuit breaker, if one is wired. May block until resume when the breaker
// decides to pause. The ctx MUST be the calling goroutine's own
// workflow.Context. Nil-safe on both the receiver and the breaker.
func (pc *PauseController) ObserveDaemonOfflineStep(callerCtx workflow.Context, activityName string, stepEvent *StepEvent) {
	if pc != nil && pc.DaemonOffline != nil {
		pc.DaemonOffline.ObserveStep(callerCtx, activityName, stepEvent)
	}
}

// WakeMachineBeforeStep wakes the run's machine, if a hold may have let it
// sleep, before a machine step dispatches. callerCtx MUST be the calling
// goroutine's own workflow.Context. Nil-safe on both the receiver and the
// waker.
func (pc *PauseController) WakeMachineBeforeStep(callerCtx, actCtx workflow.Context, toolNames []string, toolsKnown bool) {
	if pc != nil {
		pc.MachineWake.beforeMachineStep(callerCtx, actCtx, toolNames, toolsKnown)
	}
}

// MachineStepDone records that a machine step just completed. Nil-safe.
func (pc *PauseController) MachineStepDone(ctx workflow.Context) {
	if pc != nil {
		pc.MachineWake.machineStepDone(ctx)
	}
}
