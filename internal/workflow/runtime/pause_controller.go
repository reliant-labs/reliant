// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"go.temporal.io/sdk/workflow"
)

// PauseController bundles the pause-related and yield-related callbacks that
// every executor needs: checking/handling a pause signal at step boundaries,
// obtaining the current cancellable activity context, and checking for
// force-yield signals targeted at a specific thread.
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

	// CheckYield is called at step boundaries to check if a force-yield has been
	// requested for this executor's thread. Returns true if yield was requested.
	CheckYield func() bool

	// ClearYield resets the force-yield flag for this thread after it has been
	// consumed. Without this, stale yield flags persist for the lifetime of the
	// Temporal execution and cause subsequent transient errors to be
	// misinterpreted as force-yields.
	ClearYield func()

	// RequestPause triggers a self-pause from within the workflow. Used when
	// a retryable error (like a rate limit) exhausts Temporal's retry budget.
	// After calling this, callers should call DoCheckPause to block until resume.
	RequestPause func()
}

// DoCheckPause calls CheckPause if the receiver and the function are non-nil.
// The ctx MUST be the calling goroutine's own workflow.Context.
func (pc *PauseController) DoCheckPause(ctx workflow.Context) {
	if pc != nil && pc.CheckPause != nil {
		pc.CheckPause(ctx)
	}
}

// DoCheckYield calls CheckYield if the receiver and the function are non-nil.
// Returns true if a yield was requested.
func (pc *PauseController) DoCheckYield() bool {
	if pc != nil && pc.CheckYield != nil {
		return pc.CheckYield()
	}
	return false
}

// DoClearYield calls ClearYield if the receiver and the function are non-nil.
func (pc *PauseController) DoClearYield() {
	if pc != nil && pc.ClearYield != nil {
		pc.ClearYield()
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
