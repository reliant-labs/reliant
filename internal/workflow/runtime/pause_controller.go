// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"go.temporal.io/sdk/workflow"
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
	// (simulator, tests).
	DaemonOffline *DaemonOfflineCircuitBreaker
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

// ObserveDaemonOfflineStep feeds a completed step to the daemon-offline
// circuit breaker, if one is wired. May block until resume when the breaker
// decides to pause. The ctx MUST be the calling goroutine's own
// workflow.Context. Nil-safe on both the receiver and the breaker.
func (pc *PauseController) ObserveDaemonOfflineStep(callerCtx workflow.Context, activityName string, stepEvent *StepEvent) {
	if pc != nil && pc.DaemonOffline != nil {
		pc.DaemonOffline.ObserveStep(callerCtx, activityName, stepEvent)
	}
}
