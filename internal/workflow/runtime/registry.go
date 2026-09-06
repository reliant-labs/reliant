// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/drivererrors"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/telemetry"
	"github.com/reliant-labs/reliant/internal/workflow/lifecycle"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
)

// NOTE: Type conversion code has been removed!
// Temporal's serialization layer handles all JSON marshaling/unmarshaling.
// Activities receive typed inputs directly - no manual map-to-struct conversion needed.
// This simplification eliminates ~400 lines of reflection-based conversion code.

// ============================================================================
// TYPED ACTIVITY INTERFACE (with generics for type safety)
// ============================================================================

// TypedActivity is a generic activity interface with strongly-typed inputs/outputs
// Similar to our tool registry pattern
type TypedActivity[TInput any, TOutput any] interface {
	// Name returns the activity name for registration
	Name() string

	// Execute runs the activity with typed inputs and returns typed output
	Execute(ctx context.Context, input TInput) (TOutput, error)
}

// ============================================================================
// ERROR CLASSIFICATION (copied from activities package to avoid import cycle)
// ============================================================================

// TerminalError indicates an error that should not be retried
type TerminalError struct {
	Message string
	Cause   error
}

func (e *TerminalError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Cause)
	}
	return e.Message
}

func (e *TerminalError) Unwrap() error {
	return e.Cause
}

// classifyError determines if an error is terminal or transient
// Returns a Temporal-compatible error that controls retry behavior
func classifyError(err error) error {
	if err == nil {
		return nil
	}

	// Check if already classified as TerminalError
	var terminalErr *TerminalError
	if errors.As(err, &terminalErr) {
		return temporal.NewNonRetryableApplicationError(terminalErr.Error(), "TerminalError", terminalErr.Cause)
	}

	// Auto-classify based on error content
	errStr := strings.ToLower(err.Error())

	// Check for specific transient error types first (before pattern matching)
	// MalformedJSONError from streaming indicates network/API issues - always retry
	if strings.Contains(errStr, "malformed json in tool input (transient)") {
		return err // Transient - Temporal will retry
	}

	// Deterministic empty-input preflight failure (e.g., Codex no input items)
	if errors.Is(err, drivererrors.ErrEmptyInput) {
		return temporal.NewNonRetryableApplicationError(err.Error(), "TerminalError", err)
	}

	// SDK-level JSON parsing errors during streaming accumulation
	// These occur when the API sends malformed partial JSON chunks
	//
	// Matched on the "error calling MarshalJSON" prefix rather than the failing
	// type's name, because that name is not stable across Go releases: 1.26
	// reports "json.RawMessage", 1.27 reports "*jsontext.Value" (RawMessage
	// became an alias for jsontext.Value). Keying on the type name meant the
	// "invalid character" variant fell through to the "invalid" terminal
	// pattern below and wedged the workflow permanently instead of retrying.
	if strings.Contains(errStr, "error calling marshaljson") &&
		(strings.Contains(errStr, "unexpected end of json") ||
			strings.Contains(errStr, "invalid character")) {
		return err // Transient - Temporal will retry
	}

	// Upstream LLM HTTP failures during streaming are frequently TRANSIENT and
	// must retry rather than wedge the workflow. The motivating case: a brand-new
	// user's first message can race ahead of account provisioning — before the
	// managed Reliant key + per-user LiteLLM virtual key exist, the LLM proxy
	// surfaces a 404 Not Found. That 404 would otherwise match the generic
	// "not found" terminal pattern below and pause the workflow PERMANENTLY, so
	// the chat only "recovers" when the user manually starts a fresh one — the
	// multi-minute stall observed right after signup. Classifying these upstream
	// statuses as transient lets Temporal back off and retry until provisioning
	// lands (typically seconds). 400/401/403 are deliberately excluded: a bad
	// request / auth failure is genuinely terminal and falls through below.
	if strings.Contains(errStr, "stream llm response") || strings.Contains(errStr, "failed to stream") {
		for _, status := range []string{"404", "408", "409", "425", "429", "500", "502", "503", "504"} {
			if strings.Contains(errStr, status) {
				return err // Transient upstream LLM failure - Temporal will retry
			}
		}
	}

	// Terminal errors - validation, business logic, not found, etc.
	terminalPatterns := []string{
		"not found",
		"does not exist",
		"invalid",
		"malformed",
		"forbidden",
		"unauthorized",
		"permission denied",
		"quota exceeded",
		"bad request",
		"cannot be empty",
		"cannot be nil",
		"unknown model",
		"unsupported",
		"prompt is too long", // Token limit exceeded - won't be fixed by retry
		"too many tokens",    // Alternative token limit error message
		"maximum context",    // Context length exceeded
		"context length",     // Context length error
		"reauthentication is needed",
		"application-default login",
		"refresherror",
		// User hasn't configured an API key for any matching provider
		"no available provider",
	}

	for _, pattern := range terminalPatterns {
		if strings.Contains(errStr, pattern) {
			return temporal.NewNonRetryableApplicationError(err.Error(), "TerminalError", err)
		}
	}

	// Transient errors - network, timeout, rate limit, service issues
	// These will be retried by Temporal (returned as normal errors)
	transientPatterns := []string{
		"timeout",
		"connection refused",
		"connection reset",
		"network",
		"rate limit",
		"too many requests",
		"service unavailable",
		"internal server error",
		"bad gateway",
		"gateway timeout",
		"deadline exceeded",
		"context deadline exceeded",
		"context canceled",
		"temporary failure",
		"try again",
		"no such host",
		"dial tcp",
	}

	for _, pattern := range transientPatterns {
		if strings.Contains(errStr, pattern) {
			return err // Return as-is for Temporal retry
		}
	}

	// Default: treat as transient (retryable)
	// Better to retry too much than fail permanently on recoverable errors
	return err
}

// categorizeError returns the category of an error for logging/metrics
func categorizeError(err error) string {
	if err == nil {
		return "unknown"
	}

	if errors.Is(err, drivererrors.ErrEmptyInput) {
		return "validation"
	}

	errStr := strings.ToLower(err.Error())

	if strings.Contains(errStr, "not found") || strings.Contains(errStr, "does not exist") {
		return "not_found"
	}

	if strings.Contains(errStr, "invalid") || strings.Contains(errStr, "malformed") ||
		strings.Contains(errStr, "cannot be empty") || strings.Contains(errStr, "cannot be nil") {
		return "validation"
	}

	if strings.Contains(errStr, "rate limit") || strings.Contains(errStr, "too many requests") {
		return "rate_limit"
	}

	if strings.Contains(errStr, "reauthentication is needed") ||
		strings.Contains(errStr, "application-default login") ||
		strings.Contains(errStr, "refresherror") ||
		strings.Contains(errStr, "unauthorized") ||
		strings.Contains(errStr, "forbidden") ||
		strings.Contains(errStr, "permission denied") {
		return "auth"
	}

	if strings.Contains(errStr, "timeout") || strings.Contains(errStr, "network") ||
		strings.Contains(errStr, "connection") || strings.Contains(errStr, "deadline exceeded") ||
		strings.Contains(errStr, "no such host") || strings.Contains(errStr, "dial tcp") {
		return "network"
	}

	var terminalErr *TerminalError
	if errors.As(err, &terminalErr) {
		return "terminal"
	}

	return "unknown"
}

const (
	// activityHeartbeatInterval is how often ActivityWrapper heartbeats while an
	// activity runs.
	//
	// Temporal delivers a pending cancellation to an activity ONLY in a
	// heartbeat RPC's response. The SDK's temporalInvoker.Heartbeat swallows
	// any RecordHeartbeat call that lands inside an open batching window
	// entirely locally — it just overwrites the pending details and returns
	// nil, with no RPC and no cancellation check. So calling RecordHeartbeat
	// more often than the window is open cannot speed up cancellation: cancel
	// latency == MaxHeartbeatThrottleInterval (internal/workersetup), full
	// stop. Verified against SDK v1.37.0 and v1.47.0 source, both identical;
	// see specs/fast-cancel-briefing.md.
	//
	// The interval here is set to match the throttle (500ms) rather than
	// below it: it costs nothing extra (the SDK drops any surplus ticks), and
	// it means a heartbeat is always ready to go the instant the throttle
	// window opens, rather than adding its own latency on top of the window.
	//
	// The throttle is ALSO the heartbeat RPC's own deadline, but floored at
	// minRPCTimeout=1s (internal_task_handlers.go internalHeartBeat) — so a
	// throttle at or below 1s never tightens that budget below 1s. There is
	// no floor-related reason to keep the throttle above 500ms.
	activityHeartbeatInterval = 500 * time.Millisecond

	// activityHeartbeatTimeout is how long Temporal waits for a heartbeat before
	// declaring the activity dead and re-dispatching it. It is the recovery time
	// for a worker that died WITHOUT releasing its activities — SIGKILL, OOM,
	// crash, or an air rebuild that orphaned the process. A worker that exits
	// cleanly reports the failure itself and never reaches this deadline. It is
	// NOT on the cancellation path — cancel latency is governed entirely by
	// MaxHeartbeatThrottleInterval above.
	//
	// It deliberately does NOT need to cover a rebuild: a restarted worker is a
	// new worker and never reclaims the old activity task, so stretching this
	// only lengthens the dead air before the retry can start. Sizing is against
	// the EFFECTIVE heartbeat cadence, which is the worker's throttle interval,
	// not activityHeartbeatInterval directly — the SDK coalesces surplus ticks
	// down to one delivery per throttle window.
	//
	// The throttle interval is derived as 0.8*HeartbeatTimeout capped by
	// MaxHeartbeatThrottleInterval (getHeartbeatThrottleInterval); ours is
	// 0.8*30s=24s, capped down to the 500ms MaxHeartbeatThrottleInterval, so
	// this value does not itself determine the effective cadence — the cap
	// does. Left at 30s (unchanged) rather than shortened to match the new
	// 500ms throttle: at 500ms, "ten missed heartbeats" would be only 5s,
	// tight enough that a worker briefly pinned by GC, a `go build`, or a slow
	// Temporal server round trip could trip a false "worker died" and have a
	// healthy, still-running activity re-dispatched underneath it — a far
	// worse failure mode (duplicate work, non-idempotent tool calls) than a
	// few extra seconds of dead-worker recovery time. No evidence motivates
	// shortening it, so it stays.
	activityHeartbeatTimeout = 30 * time.Second

	// stepActivityMaxAttempts is the retry ladder every GRAPH STEP is
	// dispatched with (StepExecutor.activityOptions). It lives here, next to
	// the wrapper that reports attempt progress to the UI, because the wrapper
	// has to know the ladder's length and cannot always learn it from Temporal.
	//
	// ActivityInfo.RetryPolicy is documented nil-able ("If the value is nil, it
	// means the server didn't send information about retry policy … but it may
	// still be defined server-side", sdk activity.go), and in practice it IS
	// nil for these activities — measured DB-wide, 582 error rows carried
	// is_retrying:false against 13 true, because a nil policy meant
	// max_attempts was never written and the is_retrying guard short-circuited
	// to false. Every mid-ladder attempt then rendered as a terminal red error
	// instead of the calm "Retrying (Attempt 4/5)" the UI was built for.
	//
	// One constant, referenced by both the dispatch site and the reporter, is
	// what keeps the reported denominator honest: change the ladder and the
	// badge follows, with no second literal to forget.
	stepActivityMaxAttempts int32 = 5

	// routerActivityStartToClose is the router's own CallLLM dispatch timeout
	// (router_executor.go). The router shares activityHeartbeatTimeout with
	// graph steps but runs a 3-attempt ladder, so this is what tells the two
	// dispatch signatures apart in resolveMaxAttempts.
	routerActivityStartToClose = 5 * time.Minute
)

// spuriousHeartbeatCancel reports whether ctx was cancelled by a heartbeat RPC
// that merely failed locally, rather than by a real instruction to stop.
//
// The SDK cancels the activity context on ANY heartbeat error it classifies as
// retryable (internal_task_handlers.go internalHeartBeat), on the theory that a
// heartbeat which keeps failing means the activity is about to breach its
// HeartbeatTimeout anyway. That theory does not hold here. The heartbeat RPC is
// given a deadline equal to the throttle interval — floored to minRPCTimeout,
// 1s — so ONE slow round trip to the Temporal server is enough to trip it, and
// context.DeadlineExceeded converts to codes.Unknown, which is in the SDK's
// retryable set. The result is an activity killed seconds into a 30-day
// StartToCloseTimeout because a single heartbeat was slow, surfacing as
// "context canceled" from whatever DB call happened to be in flight.
//
// Genuine cancellations are distinguishable: the SDK cancels with a cause, and
// only a real server-side cancel carries a CanceledError (its own completion
// path keys off exactly this, see activityTaskHandlerImpl.Execute). Worker
// shutdown and activity pause/reset set their own distinct causes and are
// handled elsewhere. So a cause that is neither a CanceledError nor one of our
// own cancels is the SDK giving up on a healthy activity, and the work deserves
// a clean retry instead of a cancellation the workflow would treat as a pause.
//
// This deliberately does not slow cancellation down: real cancels still arrive
// on the very next heartbeat tick and are passed through untouched.
func spuriousHeartbeatCancel(ctx context.Context, workerStopCh <-chan struct{}) bool {
	if !errors.Is(ctx.Err(), context.Canceled) {
		return false
	}
	// Worker shutdown has its own rewrite with a more specific message.
	if workerStopped(workerStopCh) {
		return false
	}
	cause := context.Cause(ctx)
	if cause == nil || errors.Is(cause, context.Canceled) {
		// No distinguishing cause recorded — treat as a real cancellation so a
		// genuine pause is never mistaken for infrastructure noise.
		return false
	}
	// A real server-side cancel arrives as a CanceledError. Pause/reset are
	// legitimate stop instructions too and must not be retried.
	if temporal.IsCanceledError(cause) ||
		errors.Is(cause, activity.ErrActivityPaused) ||
		errors.Is(cause, activity.ErrActivityReset) {
		return false
	}
	return true
}

// workerStopped reports whether the activity worker has begun shutting down.
// The SDK closes this channel at the top of Worker.Stop(), so a closed channel
// means any error the activity produced after that point is shutdown fallout
// rather than a real failure of the work.
func workerStopped(ch <-chan struct{}) bool {
	if ch == nil {
		return false
	}
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// isTerminal checks if an error is terminal (non-retryable)
func isTerminal(err error) bool {
	if err == nil {
		return false
	}

	// Check for TerminalError type
	var terminalErr *TerminalError
	if errors.As(err, &terminalErr) {
		return true
	}

	// Check for Temporal ApplicationError
	var appErr *temporal.ApplicationError
	if errors.As(err, &appErr) {
		return appErr.Type() == "TerminalError"
	}

	return false
}

// ============================================================================
// ACTIVITY WRAPPER (Middleware Infrastructure)
// ============================================================================

// ActivityWrapper provides type-safe activity wrapping with middleware functionality.
//
// The wrapper preserves all middleware functionality:
// - Heartbeating for fast cancellation detection
// - Panic recovery with proper error propagation
// - Chat updates dual-write for UI tracking
// - Structured logging and observability
//
// Type conversion is handled by Temporal's serialization layer - no manual conversion needed!
type ActivityWrapper[I any, O any] struct {
	outputType reflect.Type
	name       string
	activity   func(ctx context.Context, input I) (O, error)
	repo       db.Repository
	workKind   lifecycle.WorkKind
}

// NewActivityWrapper creates a new type-safe activity wrapper.
//
// Parameters:
//   - name: The activity name for registration with Temporal
//   - activity: The typed activity function to wrap
//   - repo: Database repository for chat updates
func NewActivityWrapper[I any, O any](
	name string,
	activity func(ctx context.Context, input I) (O, error),
	repo db.Repository,
) *ActivityWrapper[I, O] {
	var o O
	return &ActivityWrapper[I, O]{
		outputType: reflect.TypeOf(o),
		name:       name,
		activity:   activity,
		repo:       repo,
	}
}

// Execute wraps the typed activity with all middleware functionality.
// This is the function that gets registered with Temporal.
//
// Temporal's serialization layer handles all type conversion automatically.
// Input comes as typed I, output goes as typed O - no manual conversion needed!
func (w *ActivityWrapper[I, O]) Execute(ctx context.Context, input I) (O, error) {
	var zeroOutput O

	// Get activity info from Temporal context
	info := activity.GetInfo(ctx)
	activityType := info.ActivityType.Name
	activityID := info.ActivityID
	attemptNumber := int(info.Attempt)
	workflowID := info.WorkflowExecution.ID
	maxAttempts := resolveMaxAttempts(info)

	// Worker shutdown (air hot-reload, k8s rollout) must unwind the activity
	// NOW, not when the activity context is cancelled.
	//
	// The SDK's ordering is the trap (sdk/internal/internal_worker.go
	// activityWorker.Stop -> baseWorker.Stop): Stop() closes the worker-stop
	// channel FIRST, then waits WorkerStopTimeout for in-flight activities, and
	// only cancels the background context that parents every activity ctx AFTER
	// that wait has already expired. So an activity that watches only ctx.Done()
	// learns about the shutdown at the exact moment the worker has given up on
	// it and is exiting — too late to report anything. Nothing reaches the
	// server, the activity task is abandoned mid-flight, and Temporal has to sit
	// out the whole HeartbeatTimeout before it can re-dispatch. That stall is
	// the "activity Heartbeat timeout" seen after every hot reload.
	//
	// Cancelling our own derived context off the stop channel inverts that: the
	// activity unwinds within its normal cancellation latency (~100ms), and the
	// worker is still alive to report the failure, so Temporal re-dispatches on
	// the next backoff instead of after the heartbeat deadline.
	workerStopCh := activity.GetWorkerStopChannel(ctx)
	ctx, cancelForWorkerStop := context.WithCancel(ctx)
	defer cancelForWorkerStop()
	if workerStopCh != nil {
		go func() {
			select {
			case <-workerStopCh:
				logging.Info("[ActivityWrapper] Worker stopping, cancelling activity for fast re-dispatch",
					"activityType", activityType,
					"activityID", activityID)
				cancelForWorkerStop()
			case <-ctx.Done():
			}
		}()
	}

	// Extract step_id - try from input first, fall back to parsing activityID
	// The workflow engine uses activityID format "stepID-timestamp" (e.g., "tally-1234567890")
	inputInfo := extractActivityInputInfo(input)
	logging.Info("[ActivityWrapper] Extracted input info",
		"activityType", activityType,
		"loopNodeID", inputInfo.LoopNodeID,
		"loopIteration", inputInfo.LoopIteration,
		"stepID", inputInfo.StepID,
	)
	stepID := inputInfo.StepID
	if stepID == "" {
		// Parse step ID from activity ID (format: "stepID-timestamp")
		if idx := strings.LastIndex(activityID, "-"); idx > 0 {
			stepID = activityID[:idx]
		}
	}

	// Extract chat_id for node execution events
	chatID := extractChatID(input)

	logging.Info("[ActivityWrapper] Activity execution started",
		"activityType", activityType,
		"activityID", activityID,
		"attemptNumber", attemptNumber,
		"workflowID", workflowID,
		"stepID", stepID)

	// Belt-and-suspenders: if Temporal is executing AGENT work for this
	// workflow, the workflow IS running. Ensure the DB agrees. This is a fast
	// no-op when already marked Running, and self-heals stale status otherwise.
	//
	// Restricted to agent work for the same reason the stopped-run guard is.
	// Lifecycle work runs precisely WHILE a run is stopped (that is how it
	// resumes), and observability work reports on a run that may already have
	// stopped — letting either one assert "this run is running" would undo a
	// pause the user just asked for. Only agent work executing is real proof
	// the run is live.
	//
	// Skipped when the context is already cancelled (pause/shutdown in
	// progress), for the same reason.
	if w.repo != nil && chatID != "" && ctx.Err() == nil && w.workKind == lifecycle.AgentWork {
		w.repo.EnsureWorkflowRunning(ctx, workflowID, chatID)
	}

	// Track execution start time for duration calculation
	startTime := time.Now()

	// Emit node execution "started" event for UI streaming
	w.emitNodeExecutionEvent(ctx, "started", false, stepID, activityType, chatID, workflowID, activityID, &startTime, nil, nil, nil, nil)

	// Start heartbeat goroutine for fast cancellation detection.
	// See activityHeartbeatInterval / activityHeartbeatTimeout for the cadence
	// and the deadline it is sized against.
	heartbeatCtx, cancelHeartbeat := context.WithCancel(ctx)
	defer cancelHeartbeat() // Stop heartbeat when activity completes

	go func() {
		ticker := time.NewTicker(activityHeartbeatInterval)
		defer ticker.Stop()

		for {
			select {
			case <-heartbeatCtx.Done():
				// Activity completed or context cancelled (worker stopped)
				if ctx.Err() == context.Canceled {
					logging.Debug("[ActivityWrapper] Heartbeat stopped due to worker shutdown",
						"activityType", activityType,
						"activityID", activityID)
				}
				return
			case <-ticker.C:
				// Check for cancellation before sending heartbeat
				// This helps detect worker shutdown faster
				if ctx.Err() != nil {
					logging.Debug("[ActivityWrapper] Context cancelled, stopping heartbeat",
						"activityType", activityType,
						"activityID", activityID,
						"error", ctx.Err())
					return
				}
				// Send heartbeat with current progress
				activity.RecordHeartbeat(ctx, map[string]interface{}{
					"activity_type": activityType,
					"attempt":       attemptNumber,
					"status":        "running",
				})
			}
		}
	}()

	// Setup defer for panic recovery
	var execErr error
	defer func() {
		// Recover from panics
		if r := recover(); r != nil {
			logging.Error("[ActivityWrapper] Activity panicked",
				"activityType", activityType,
				"activityID", activityID,
				"panic", r)

			// Best-effort: Write error event to chat_updates on panic
			panicErr := fmt.Errorf("panic: %v", r)
			w.writeErrorEvent(ctx, input, activityType, activityID, attemptNumber, workflowID, panicErr, maxAttempts)

			// Report panic to telemetry with context (non-blocking)
			go func() {
				telemetry.CaptureExceptionWithContext(panicErr, map[string]string{
					"activity_type": activityType,
					"activity_id":   activityID,
					"workflow_id":   workflowID,
					"chat_id":       chatID,
					"step_id":       stepID,
					"error_type":    "panic",
				}, map[string]interface{}{
					"attempt_number": attemptNumber,
				})
			}()

			// Emit node execution "failed" event
			endTime := time.Now()
			duration := endTime.Sub(startTime).Milliseconds()
			errMsg := panicErr.Error()
			w.emitNodeExecutionEvent(ctx, "failed", false, stepID, activityType, chatID, workflowID, activityID, &startTime, &endTime, &duration, nil, &errMsg)

			// Re-panic to propagate to Temporal
			panic(r)
		}
	}()

	// Nothing runs for a stopped run. The lifecycle package owns what that
	// means and which work is exempt; this boundary only asks.
	//
	// NON-RETRYABLE on purpose: being stopped is not a fault, and retrying
	// would recreate the very loop the rule exists to break. Temporal records
	// the activity as failed, the loop unwinds, and the run settles at its
	// stopped status instead of burning turns.
	if decision := lifecycle.MayExecuteWork(ctx, w.repo, workflowID, w.workKind); !decision.Allowed {
		logging.Warn("[ActivityWrapper] Refusing to execute activity for a stopped workflow",
			"activityType", activityType,
			"workflowID", workflowID,
			"chatID", chatID,
			"stopReason", decision.Reason,
			"retryable", decision.Retryable)
		msg := fmt.Sprintf("workflow is stopped (%s); not executing %s", decision.Reason, activityType)
		if decision.Retryable {
			// The status row may be a stale read mid-resume; a plain error
			// lets Temporal retry, and the next attempt sees the updated row.
			return zeroOutput, errors.New(msg)
		}
		return zeroOutput, temporal.NewNonRetryableApplicationError(msg, "WorkflowStopped", nil)
	}

	// Execute the typed activity directly - no conversion needed!
	// Temporal already deserialized the input to type I
	result, execErr := w.activity(ctx, input)

	// Calculate execution duration
	endTime := time.Now()
	durationMs := endTime.Sub(startTime).Milliseconds()

	// The worker going away is infrastructure churn, not a failure of this step,
	// and whatever error the activity unwound with describes the cancellation
	// rather than the cause. Rewrite it to an explicitly retryable
	// WorkerShutdown error so it can never be mistaken for a user pause: a
	// context.Canceled surfacing as a Temporal CanceledError would park the
	// chat until the user manually resumes. Skip the chat-visible error event
	// and Sentry too — the user should see the step retry, not a red error for a
	// rebuild they triggered.
	// Same reasoning for a heartbeat that failed locally: the activity was
	// healthy and the cancellation is an artifact of the heartbeat RPC's own
	// deadline, so retry the step rather than letting a bogus context.Canceled
	// reach the workflow and park the chat. See spuriousHeartbeatCancel.
	if execErr != nil && spuriousHeartbeatCancel(ctx, workerStopCh) {
		logging.Warn("[ActivityWrapper] Activity cancelled by a failed heartbeat RPC, returning retryable error",
			"activityType", activityType,
			"activityID", activityID,
			"attemptNumber", attemptNumber,
			"durationMs", durationMs,
			"cause", context.Cause(ctx),
			"error", execErr)
		w.writeStepExecution(ctx, workflowID, stepID, activityType, nil, execErr, durationMs, inputInfo.LoopNodeID, inputInfo.LoopIteration)
		return zeroOutput, temporal.NewApplicationErrorWithCause(
			fmt.Sprintf("heartbeat RPC failed while running %s; retrying", activityType),
			"HeartbeatCancel",
			execErr,
		)
	}

	if execErr != nil && workerStopped(workerStopCh) {
		logging.Info("[ActivityWrapper] Activity aborted by worker shutdown, returning retryable error",
			"activityType", activityType,
			"activityID", activityID,
			"attemptNumber", attemptNumber,
			"durationMs", durationMs,
			"error", execErr)
		w.writeStepExecution(ctx, workflowID, stepID, activityType, nil, execErr, durationMs, inputInfo.LoopNodeID, inputInfo.LoopIteration)
		return zeroOutput, temporal.NewApplicationErrorWithCause(
			fmt.Sprintf("worker shut down while running %s; retrying on the next worker", activityType),
			"WorkerShutdown",
			execErr,
		)
	}

	if execErr != nil {
		// Use Warn for non-terminal errors (e.g. YAML validation failures,
		// missing API keys) so they don't trigger Sentry via the sentry log
		// handler. Terminal errors stay at Error level.
		logFn := logging.Warn
		if isTerminal(execErr) {
			logFn = logging.Error
		}
		logFn("[ActivityWrapper] Activity execution failed",
			"activityType", activityType,
			"activityID", activityID,
			"error", execErr,
			"attemptNumber", attemptNumber,
			"durationMs", durationMs)

		// Best-effort: Write error event to chat_updates
		w.writeErrorEvent(ctx, input, activityType, activityID, attemptNumber, workflowID, execErr, maxAttempts)

		// Report error to telemetry with context (non-blocking)
		// Only report terminal errors to avoid noise from transient failures
		if isTerminal(execErr) {
			go func() {
				telemetry.CaptureExceptionWithContext(execErr, map[string]string{
					"activity_type":  activityType,
					"activity_id":    activityID,
					"workflow_id":    workflowID,
					"chat_id":        chatID,
					"step_id":        stepID,
					"error_type":     "terminal",
					"error_category": categorizeError(execErr),
				}, map[string]interface{}{
					"attempt_number": attemptNumber,
					"duration_ms":    durationMs,
				})
			}()
		}

		// Best-effort: Write step execution record for history tracking
		w.writeStepExecution(ctx, workflowID, stepID, activityType, nil, execErr, durationMs, inputInfo.LoopNodeID, inputInfo.LoopIteration)

		// Emit node execution "failed" event for UI streaming
		errMsg := execErr.Error()
		w.emitNodeExecutionEvent(ctx, "failed", false, stepID, activityType, chatID, workflowID, activityID, &startTime, &endTime, &durationMs, nil, &errMsg)

		return zeroOutput, execErr
	}

	logging.Info("[ActivityWrapper] Activity execution completed",
		"activityType", activityType,
		"activityID", activityID,
		"success", true,
		"attemptNumber", attemptNumber,
		"durationMs", durationMs)

	// Best-effort: Write step execution record for history tracking
	w.writeStepExecution(ctx, workflowID, stepID, activityType, result, nil, durationMs, inputInfo.LoopNodeID, inputInfo.LoopIteration)

	// Emit node execution "completed" event for UI streaming
	// Try to extract exit_code from result for run steps
	var exitCode *int
	resultMap := toMapInterface(result)
	if resultMap != nil {
		if ec, ok := resultMap["exit_code"]; ok {
			switch v := ec.(type) {
			case float64:
				ecInt := int(v)
				exitCode = &ecInt
			case int:
				exitCode = &v
			case int64:
				ecInt := int(v)
				exitCode = &ecInt
			}
		}
	}
	// `skipped` is stamped on the output by the activity that records the skip,
	// so this reads the output rather than matching on an activity name.
	w.emitNodeExecutionEvent(ctx, "completed", model.IsSkippedOutput(resultMap), stepID, activityType, chatID, workflowID, activityID, &startTime, &endTime, &durationMs, exitCode, nil)

	return result, nil
}

// activityErrorEventID is the DEDUP KEY for one activity's whole retry series.
//
// chat_updates are collapsed per entity_id by
// GetLatestNonMessageUpdatesPerEntity ("last write per entity wins"), and the
// frontend's error log dedups by id too (applyErrorUpdates replaces in place).
// A freshly generated uuid per call — which is what this used to be — opted out
// of both: every attempt landed under its own id, so ONE failing activity
// rendered as three stacked rows in the transcript ("Retrying (Attempt 1/3)",
// "Retrying (Attempt 2/3)", "Attempt 3/3") instead of a single error whose
// badge advances.
//
// Keying on the activity gives retries a stable id, so they update one row.
// activity_id is stable across attempts of the same activity (Temporal reuses
// it — attempt_number is the part that varies) and unique per activity within a
// run, so genuinely distinct failures still get distinct rows.
//
// workflowID is included because activity ids restart at 1 for each new run,
// including a continue-as-new successor: without it, a later run's first
// activity would overwrite the earlier run's recorded error.
func activityErrorEventID(workflowID, activityID string) string {
	if activityID == "" {
		// Nothing stable to key on (Temporal always sets an activity id, but a
		// blank one here would silently merge unrelated failures into one row).
		return "activity-error-" + uuid.New().String()
	}
	return fmt.Sprintf("activity-error-%s-%s", workflowID, activityID)
}

// exhaustionErrorEventID is the id a retry-exhaustion error must be written
// under so it REPLACES the failing activity's error row instead of adding a
// second one beside it.
//
// When the ladder is exhausted the workflow emits its own error through the
// WorkflowError activity, describing the same failure the wrapper already
// recorded under activityErrorEventID. Minting a fresh uuid there opted out of
// every dedup path — the frontend's dedup-by-id and the server's
// GetLatestNonMessageUpdatesPerEntity both collapse per entity id — so the
// final attempt rendered TWICE, 19ms apart, as two stacked banners for one
// failure. Reusing the series id makes the exhaustion row the last write to
// that entity, which is exactly what "last write per entity wins" should
// resolve to: the terminal state of that activity.
//
// The activity id comes from Temporal's own *ActivityError, which carries the
// id the SERVER assigned ("1", "2", …). It is deliberately NOT taken from
// RunningStep.ActivityID: that field is set to node.GetId() for backwards
// compat (step_executor.go), so keying on it would produce a step-named id that
// no wrapper row ever used, and the duplicate would survive.
//
// Returns "" when the error is not an activity failure (so no series row
// exists to replace) — the caller then leaves the id unset and WriteWorkflowError
// mints one, which is right for an error that is its own event.
func exhaustionErrorEventID(workflowID string, err error) string {
	var activityErr *temporal.ActivityError
	if !errors.As(err, &activityErr) {
		return ""
	}
	activityID := activityErr.ActivityID()
	if activityID == "" {
		return ""
	}
	return activityErrorEventID(workflowID, activityID)
}

// resolveMaxAttempts is the length of the retry ladder this activity is running
// on, or 0 when it genuinely cannot be determined.
//
// The server's answer wins whenever there is one: it is authoritative, and it
// may differ from what the workflow asked for. When it is absent — the nil case
// the SDK documents, and the case that actually occurs here — fall back to the
// ladder the dispatch site configured.
//
// The fallback is deliberately narrow, and it is a SIGNATURE MATCH on the
// dispatch options rather than a blanket default. Only StepExecutor.activityOptions
// dispatches the 5-attempt ladder; every infrastructure dispatch (WorkflowError,
// SaveMessage, ValidateThreadOwnership, …) configures MaximumAttempts: 3. A
// blanket default would report a confident WRONG denominator for those — worse
// than reporting none, because "Attempt 2/5" on a 3-attempt ladder tells the
// user there is headroom that does not exist, and "Attempt 3/5" on an exhausted
// one renders a dead failure as still-retrying.
//
// Two options make up the step signature. The heartbeat alone is not enough:
// the router dispatches CallLLM with the same HeartbeatTimeout but its own
// 3-attempt ladder and a fixed 5-minute StartToCloseTimeout, so that pair is
// excluded explicitly. A graph node that happens to declare `timeout: 5m` falls
// out of the match too — it reports unknown rather than wrong, which is the
// direction this is built to fail in.
//
// 0 means unknown, and unknown must stay unknown: the caller omits max_attempts
// and leaves is_retrying false rather than guessing a failure is recoverable.
// activityIsRetrying reports whether this failure still has an attempt coming,
// which is what decides between the calm "Retrying (Attempt 4/5)" affordance
// and a terminal red error in the transcript.
//
// An unknown ladder length (0, see resolveMaxAttempts) means NOT retrying. That
// is the conservative direction on purpose: claiming a dead failure is still
// retrying leaves the user waiting for a turn that will never come, whereas the
// reverse merely under-sells a recovery that speaks for itself when it lands.
func activityIsRetrying(attemptNumber int, maxAttempts int32, err error) bool {
	return maxAttempts > 0 && int32(attemptNumber) < maxAttempts && !isTerminal(err)
}

func resolveMaxAttempts(info activity.Info) int32 {
	if info.RetryPolicy != nil && info.RetryPolicy.MaximumAttempts > 0 {
		return info.RetryPolicy.MaximumAttempts
	}
	if info.HeartbeatTimeout == activityHeartbeatTimeout && info.StartToCloseTimeout != routerActivityStartToClose {
		return stepActivityMaxAttempts
	}
	return 0
}

// writeErrorEvent writes an error event to chat_updates when an activity fails.
// This is a best-effort write - it will not fail the activity if the write fails.
// Only writes errors if chat_id can be extracted from the input.
//
// IMPORTANT: This function uses a background context with a timeout because the
// activity context may already be cancelled or about to be cancelled when an error
// occurs. Using the activity context could cause the error write to fail silently.
func (w *ActivityWrapper[I, O]) writeErrorEvent(
	ctx context.Context,
	input I,
	activityType string,
	activityID string,
	attemptNumber int,
	workflowID string,
	err error,
	maxAttempts int32,
) {
	logging.Info("[ActivityWrapper] writeErrorEvent called",
		"activityType", activityType,
		"activityID", activityID,
		"attemptNumber", attemptNumber,
		"error", err.Error())

	// Extract chat_id from input if available
	chatID := extractChatID(input)
	if chatID == "" {
		logging.Info("[ActivityWrapper] Skipping error event write - no chat_id in input",
			"activityType", activityType,
			"activityID", activityID)
		return
	}

	logging.Info("[ActivityWrapper] Extracted chat_id for error event",
		"chatID", chatID,
		"activityType", activityType,
		"activityID", activityID)

	errorID := activityErrorEventID(workflowID, activityID)

	// Build error data matching the ErrorUpdate interface expected by frontend
	// See web/src/types/streaming.ts ErrorUpdate interface
	isRetrying := activityIsRetrying(attemptNumber, maxAttempts, err)
	errorData := map[string]interface{}{
		"update_type":    "error",
		"id":             errorID,
		"chat_id":        chatID,
		"activity_type":  activityType,
		"activity_id":    activityID,
		"error_message":  err.Error(),
		"timestamp":      time.Now().UTC().Format(time.RFC3339Nano),
		"attempt_number": attemptNumber,
		"workflow_id":    workflowID,
		"is_retrying":    isRetrying,
	}
	// Scope the error to the thread that produced it. Omitted entirely when
	// the activity has no thread, so the timeline's "no thread means
	// chat-scoped" branch still applies rather than matching on "".
	if thread := extractThread(input); thread != "" {
		errorData["thread"] = thread
	}
	if maxAttempts > 0 {
		errorData["max_attempts"] = int(maxAttempts)
	}

	// Marshal to JSON
	errorDataJSON, marshalErr := json.Marshal(errorData)
	if marshalErr != nil {
		logging.Error("[ActivityWrapper] Failed to marshal error event data",
			"activityType", activityType,
			"activityID", activityID,
			"marshalError", marshalErr)
		return
	}

	// Use a background context with timeout for the DB write.
	// The activity context may be cancelled or about to be cancelled when an error occurs,
	// which could cause the error write to fail. Using a fresh context ensures the error
	// event is written even if the activity is being terminated.
	writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Check if the original context was already cancelled (for logging purposes)
	if ctx.Err() != nil {
		logging.Warn("[ActivityWrapper] Original context was cancelled, using background context for error write",
			"activityType", activityType,
			"activityID", activityID,
			"chatID", chatID,
			"ctxErr", ctx.Err().Error())
	}

	// Best-effort write to chat_updates
	if writeErr := w.repo.CreateChatUpdate(writeCtx, chatID, db.UpdateTypeError, errorID, string(errorDataJSON)); writeErr != nil {
		logging.Error("[ActivityWrapper] Failed to write error event to chat_updates",
			"activityType", activityType,
			"activityID", activityID,
			"chatID", chatID,
			"errorID", errorID,
			"writeError", writeErr)
		return
	}

	logging.Info("[ActivityWrapper] Successfully wrote error event to chat_updates",
		"activityType", activityType,
		"activityID", activityID,
		"chatID", chatID,
		"errorID", errorID,
		"attemptNumber", attemptNumber)
}

// nodeStatusFor is the node's state, given the lifecycle event that produced it
// and whether the node was skipped.
//
// The two are different questions and only the second can say a node did not
// run. A skipped node's activity completes normally, so eventType alone reports
// it as COMPLETED — which is why NodeStatusSkipped sat in the proto and in
// db.models with nothing ever assigning it, and why `workflow watch` printed
// "✓ node review completed" for a reviewer that was configured off.
func nodeStatusFor(eventType string, skipped bool) db.NodeExecutionStatus {
	if skipped {
		return db.NodeStatusSkipped
	}
	switch eventType {
	case "started":
		return db.NodeStatusRunning
	case "completed":
		return db.NodeStatusCompleted
	case "failed":
		return db.NodeStatusFailed
	case "cancelled":
		return db.NodeStatusCancelled
	default:
		return db.NodeStatusRunning
	}
}

// emitNodeExecutionEvent emits a node execution event for UI streaming.
// This is a best-effort write - it will not fail the activity if the write fails.
func (w *ActivityWrapper[I, O]) emitNodeExecutionEvent(
	ctx context.Context,
	eventType string,
	// skipped marks a node whose condition evaluated false. Its activity ran to
	// completion — so the LIFECYCLE event really is "completed" — but the node's
	// work never happened. Those are two different facts and the event carries
	// both: event_type is the lifecycle, status is the verdict. Reporting only
	// the lifecycle is how `workflow watch` printed "✓ node review completed"
	// for a reviewer that never ran.
	skipped bool,
	nodeID string,
	nodeType string,
	chatID string,
	workflowID string,
	activityID string,
	startedAt *time.Time,
	completedAt *time.Time,
	durationMs *int64,
	exitCode *int,
	errorMessage *string,
) {
	// Skip if we don't have the required IDs
	if chatID == "" || nodeID == "" {
		logging.Debug("[ActivityWrapper] Skipping node execution event - missing chat_id or node_id",
			"chatID", chatID,
			"nodeID", nodeID,
			"eventType", eventType)
		return
	}

	status := nodeStatusFor(eventType, skipped)

	// Determine node type from activity type (strip V2_ prefix if present)
	cleanNodeType := strings.TrimPrefix(nodeType, "")
	// Map activity types to workflow node types
	switch {
	case strings.HasPrefix(cleanNodeType, "ExecuteAgent"):
		cleanNodeType = "agent"
	case strings.HasPrefix(cleanNodeType, "ExecuteRunStep"):
		cleanNodeType = model.NodeTypeRun
	case strings.HasPrefix(cleanNodeType, "ExecuteApproval"):
		cleanNodeType = model.NodeTypeApproval
	case strings.HasPrefix(cleanNodeType, "CallLLM"):
		cleanNodeType = "llm_call"
	case strings.HasPrefix(cleanNodeType, "ExecuteTools"):
		cleanNodeType = "tools"
	case strings.HasPrefix(cleanNodeType, "SaveMessage"):
		cleanNodeType = model.NodeTypeSaveMessage
	case strings.Contains(cleanNodeType, "."):
		// Action steps with dot notation - extract the prefix as category
		cleanNodeType = "action"
	}

	// Build node execution state
	nodeState := &db.NodeExecutionState{
		NodeID:     nodeID,
		NodeType:   cleanNodeType,
		Status:     status,
		WorkflowID: workflowID,
		ChatID:     chatID,
		ActivityID: &activityID,
	}

	if startedAt != nil {
		nodeState.StartedAt = startedAt
	}
	if completedAt != nil {
		nodeState.CompletedAt = completedAt
	}
	if durationMs != nil {
		nodeState.DurationMs = durationMs
	}
	if exitCode != nil {
		nodeState.ExitCode = exitCode
	}
	if errorMessage != nil {
		nodeState.ErrorMessage = errorMessage
	}

	// Use a background context with timeout for the DB write
	writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Best-effort write to chat_updates
	if err := w.repo.EmitNodeExecutionEvent(writeCtx, eventType, nodeState); err != nil {
		logging.Warn("[ActivityWrapper] Failed to emit node execution event",
			"eventType", eventType,
			"nodeID", nodeID,
			"chatID", chatID,
			"error", err)
		return
	}

	logging.Debug("[ActivityWrapper] Emitted node execution event",
		"eventType", eventType,
		"nodeID", nodeID,
		"nodeType", cleanNodeType,
		"status", status,
		"workflowID", workflowID)
}

// extractChatID attempts to extract chat_id from various input formats.
// Returns empty string if chat_id is not found.
func extractChatID(input interface{}) string {
	return extractInputString(input, "chat_id", "ChatID")
}

// extractInputString pulls a named string out of an activity input, whether
// that input is a bare map, a flat struct, or the v3 types.ActivityInput shape
// that carries the identifiers one level down inside its RuntimeContext.
//
// The one-level descent is what makes activity failures visible at all. Every
// v3 activity is dispatched as ActivityInput{Runtime, Node}, and chat_id and
// thread live on Runtime — so a top-level-only scan answered "" for all of
// them, writeErrorEvent bailed out with "no chat_id in input", and NOT ONE
// activity error ever reached the chat. Measured over a day of worker logs: 26
// error events attempted, 26 skipped, 0 written — while
// web/src/components/Chat/WorkflowErrorMessage.tsx sat fully built, retry
// states and all, and never rendered once.
//
// That is how a chat can stop dead with nothing on screen: the failure is real,
// the transport is real, the renderer is real, and the id that connects them is
// one struct deeper than the lookup went.
//
// Depth is capped at one level. The identifiers are exactly one hop away by
// contract; an unbounded walk would start guessing at unrelated nested structs
// and answer with whatever it found first.
func extractInputString(input interface{}, jsonName, goName string) string {
	return extractInputStringAtDepth(input, jsonName, goName, 1)
}

func extractInputStringAtDepth(input interface{}, jsonName, goName string, depth int) string {
	if input == nil {
		return ""
	}

	if inputMap, ok := input.(map[string]interface{}); ok {
		if v, ok := inputMap[jsonName].(string); ok && v != "" {
			return v
		}
	}

	val := reflect.ValueOf(input)
	if val.Kind() == reflect.Ptr {
		if val.IsNil() {
			return ""
		}
		val = val.Elem()
	}
	if val.Kind() != reflect.Struct {
		return ""
	}

	// Own fields first: a nested struct must never shadow the input's own
	// answer, whichever order the fields happen to be declared in.
	for i := 0; i < val.NumField(); i++ {
		field := val.Type().Field(i)
		fieldVal := val.Field(i)
		if fieldVal.Kind() != reflect.String {
			continue
		}
		if jsonTag := field.Tag.Get("json"); jsonTag != "" {
			if strings.Split(jsonTag, ",")[0] == jsonName {
				if v := fieldVal.String(); v != "" {
					return v
				}
			}
		}
		if field.Name == goName || field.Name == jsonName {
			if v := fieldVal.String(); v != "" {
				return v
			}
		}
	}

	if depth <= 0 {
		return ""
	}

	for i := 0; i < val.NumField(); i++ {
		fieldVal := val.Field(i)
		if fieldVal.Kind() == reflect.Ptr {
			if fieldVal.IsNil() {
				continue
			}
			fieldVal = fieldVal.Elem()
		}
		if fieldVal.Kind() != reflect.Struct || !fieldVal.CanInterface() {
			continue
		}
		if v := extractInputStringAtDepth(fieldVal.Interface(), jsonName, goName, depth-1); v != "" {
			return v
		}
	}

	return ""
}

// extractThread pulls the thread an activity was working on out of its input,
// so the error event it produces can be scoped to that thread.
//
// Without it every activity error is chat-global, and the timeline shows it in
// EVERY thread of the chat — including spawns that started long after the
// error and never saw it. Observed: a run of DrainAgentMessages failures
// rendered at the top of a spawn thread that did not exist when they happened.
// InterleavedTimeline already scopes an error that carries a thread; nothing
// was filling the field in.
//
// Returns "" when the input has no thread, which is the honest answer for a
// genuinely chat-scoped activity. The timeline keeps showing those everywhere
// rather than guessing a thread — guessing is what produced the wrong-thread
// render to begin with.
func extractThread(input interface{}) string {
	return extractInputString(input, "thread", "Thread")
}

// activityInputInfo holds common fields extracted from activity inputs for tracking.
type activityInputInfo struct {
	ChatID        string
	StepID        string
	WorkflowID    string
	LoopNodeID    string // The loop node that spawned this activity (if any)
	LoopIteration int    // The iteration index within the loop (0-indexed, -1 if not in loop)
}

// extractActivityInputInfo extracts common fields from a typed input using JSON.
// This is needed because we don't know the exact type at compile time.
func extractActivityInputInfo(input interface{}) activityInputInfo {
	info := activityInputInfo{
		LoopIteration: -1, // Default to -1 to indicate "not in a loop"
	}

	// Use JSON to convert to map and extract fields
	jsonBytes, err := json.Marshal(input)
	if err != nil {
		return info
	}

	var m map[string]interface{}
	if err := json.Unmarshal(jsonBytes, &m); err != nil {
		return info
	}

	// A v3 activity input marshals as {"runtime": {...}, "node": {...}}, so the
	// identifiers this reads are one object down. Without the descent every v3
	// activity reported an empty step_id and workflow_id, which is why the
	// per-step audit trail is empty: writeStepExecution skips a row it cannot
	// key, so step_executions has ZERO rows for chats the new runtime ran. A
	// stopped chat then has no durable record of which step it stopped on.
	m = withNestedInputKeys(m)

	if chatID, exists := m["chat_id"]; exists {
		if chatIDStr, ok := chatID.(string); ok {
			info.ChatID = chatIDStr
		}
	}
	if stepID, exists := m["step_id"]; exists {
		if stepIDStr, ok := stepID.(string); ok {
			info.StepID = stepIDStr
		}
	}
	if workflowID, exists := m["workflow_id"]; exists {
		if workflowIDStr, ok := workflowID.(string); ok {
			info.WorkflowID = workflowIDStr
		}
	}
	if loopNodeID, exists := m["loop_node_id"]; exists {
		if loopNodeIDStr, ok := loopNodeID.(string); ok {
			info.LoopNodeID = loopNodeIDStr
		}
	}
	if loopIter, exists := m["loop_iteration"]; exists {
		switch v := loopIter.(type) {
		case float64:
			info.LoopIteration = int(v)
		case int:
			info.LoopIteration = v
		case int64:
			info.LoopIteration = int(v)
		}
	}
	return info
}

// withNestedInputKeys returns m with the scalar keys of its one-level-nested
// objects folded in, so a caller reading well-known identifiers finds them
// whether the input is flat or wrapped.
//
// A key already present at the top level always wins, and among nested objects
// the first one carrying a key wins — the input's own answer is never
// overwritten by something found deeper. Only strings and numbers are lifted;
// nested objects and arrays stay where they are rather than colliding with a
// same-named field one level up.
func withNestedInputKeys(m map[string]interface{}) map[string]interface{} {
	// Sorted so that two nested objects offering the same key resolve the same
	// way on every call — map iteration order would make the winner a coin flip
	// and the resulting logs unreproducible.
	outerKeys := make([]string, 0, len(m))
	for k := range m {
		outerKeys = append(outerKeys, k)
	}
	sort.Strings(outerKeys)

	var merged map[string]interface{}
	for _, ok := range outerKeys {
		nested, isObj := m[ok].(map[string]interface{})
		if !isObj {
			continue
		}
		for k, nv := range nested {
			switch nv.(type) {
			case string, float64, bool:
			default:
				continue
			}
			if _, exists := m[k]; exists {
				continue
			}
			if merged == nil {
				merged = make(map[string]interface{}, len(m)+len(nested))
				for mk, mv := range m {
					merged[mk] = mv
				}
			}
			if _, exists := merged[k]; exists {
				continue
			}
			merged[k] = nv
		}
	}
	if merged == nil {
		return m
	}
	return merged
}

// toMapInterface converts an interface to map[string]interface{} if possible.
func toMapInterface(v interface{}) map[string]interface{} {
	if m, ok := v.(map[string]interface{}); ok {
		return m
	}

	// Try JSON round-trip for structs
	jsonBytes, err := json.Marshal(v)
	if err != nil {
		return nil
	}

	var m map[string]interface{}
	if err := json.Unmarshal(jsonBytes, &m); err != nil {
		return nil
	}
	return m
}

// writeStepExecution writes a step execution record for workflow history tracking.
// This enables CEL expressions to query step history (e.g., size(history.filter(exit_code != 0)) >= 3).
func (w *ActivityWrapper[I, O]) writeStepExecution(ctx context.Context, workflowID, stepID, activityType string, output interface{}, execErr error, durationMs int64, loopNodeID string, loopIteration int) {
	// Skip if we don't have the required IDs
	if workflowID == "" || stepID == "" {
		logging.Info("[ActivityWrapper] Skipping step execution write - missing workflow_id or step_id",
			"workflowID", workflowID,
			"stepID", stepID,
			"activityType", activityType)
		return
	}

	exec := buildStepExecution(workflowID, stepID, activityType, output, execErr, durationMs, loopNodeID, loopIteration)
	verdict := recordedVerdict{ExitCode: exec.ExitCode, Success: exec.Success}

	// Use a background context with timeout for the DB write.
	// The activity context may be cancelled or about to be cancelled,
	// which could cause the write to fail. Using a fresh context ensures the
	// step execution is written even if the activity is being terminated.
	writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Best-effort write to step_executions (don't fail activity if this fails)
	if err := w.repo.CreateStepExecution(writeCtx, exec); err != nil {
		logging.Error("[ActivityWrapper] Failed to create step execution",
			"error", err,
			"workflowID", workflowID,
			"stepID", stepID,
			"activityType", activityType)
	} else {
		logging.Info("[ActivityWrapper] Step execution recorded",
			"workflowID", workflowID,
			"stepID", stepID,
			"activityType", activityType,
			"success", verdict.Success.Bool,
			"durationMs", durationMs)
	}
}

// buildStepExecution assembles the step_executions row for one activity
// outcome. Split out from writeStepExecution so what the row CLAIMS is
// testable without a database — the claim is the part that was wrong.
func buildStepExecution(workflowID, stepID, activityType string, output interface{}, execErr error, durationMs int64, loopNodeID string, loopIteration int) *db.StepExecution {
	// Marshal the output once and reuse it for both the stored JSON and the
	// verdict, instead of round-tripping through encoding/json twice.
	var outputJSON sql.NullString
	var outputMap map[string]interface{}
	if output != nil {
		if jsonBytes, err := json.Marshal(output); err == nil {
			outputJSON = sql.NullString{String: string(jsonBytes), Valid: true}
			_ = json.Unmarshal(jsonBytes, &outputMap)
		}
	}

	verdict := deriveRecordedVerdict(outputMap, execErr)

	var loopNodeIDSQL sql.NullString
	var loopIterationSQL sql.NullInt64
	if loopNodeID != "" {
		loopNodeIDSQL = sql.NullString{String: loopNodeID, Valid: true}
		loopIterationSQL = sql.NullInt64{Int64: int64(loopIteration), Valid: true}
	}

	return &db.StepExecution{
		ID:            uuid.New().String(),
		WorkflowID:    workflowID,
		StepID:        stepID,
		ActivityName:  activityType,
		OutputJSON:    outputJSON,
		ExitCode:      verdict.ExitCode,
		Success:       verdict.Success,
		DurationMs:    sql.NullInt64{Int64: durationMs, Valid: true},
		LoopNodeID:    loopNodeIDSQL,
		LoopIteration: loopIterationSQL,
		// No .UTC(): created_at is timestamptz, so the offset is preserved and
		// normalized on the way in and a local time stores the same instant.
		// This site is where the two-basis bug was found — it wrote local time
		// into a `timestamp without time zone` column while workflows wrote UTC
		// into another — and adding one more .UTC() would have left the next
		// call site free to make the same mistake.
		CreatedAt: time.Now(),
	}
}

// recordedVerdict is what a step_executions row records about whether the step
// passed: the exit code when there is one, and success.
//
// A skipped node is NOT distinguished here. It does not need to be: the row
// already carries an exact discriminator — activity_name = SkippedStep and
// output_json = {"skipped": true} — and adding a third state to `success` would
// mean a reader had to learn a new encoding beside a discriminator that already
// exists. The fix for "a skip reads as a pass" belongs where readers actually
// look, which is the node execution event (see emitNodeExecutionEvent) and the
// `workflow status` renderer.
type recordedVerdict struct {
	ExitCode sql.NullInt64
	Success  sql.NullBool
}

// deriveRecordedVerdict decides what a step execution row claims about its step,
// from the activity's already-marshalled output map and its error.
//
// outputMap is the activity output as JSON sees it; nil when the activity
// produced no output (the error path) or produced something that is not a JSON
// object.
func deriveRecordedVerdict(outputMap map[string]interface{}, execErr error) recordedVerdict {
	var v recordedVerdict
	if ec, ok := outputMap["exit_code"]; ok {
		switch n := ec.(type) {
		case int:
			v.ExitCode = sql.NullInt64{Int64: int64(n), Valid: true}
		case int64:
			v.ExitCode = sql.NullInt64{Int64: n, Valid: true}
		case float64:
			v.ExitCode = sql.NullInt64{Int64: int64(n), Valid: true}
		}
	}

	switch {
	case execErr != nil:
		v.Success = sql.NullBool{Bool: false, Valid: true}
	case v.ExitCode.Valid:
		v.Success = sql.NullBool{Bool: v.ExitCode.Int64 == 0, Valid: true}
	default:
		v.Success = sql.NullBool{Bool: true, Valid: true}
	}
	return v
}

// NOTE: writeActivityUpdate has been removed.
// Activity status updates were written to chat_updates with update_type='activity_status',
// but this is not in the allowed list of update_types (message, approval, thread, tool_call,
// streaming_delta, workflow_status, error) and caused constraint violations.
// These updates were also never consumed by the UI, as confirmed in BACKEND_CLEANUP_AUDIT.md.
// Temporal already tracks all activity execution state, so these dual-writes were redundant.

// ============================================================================
// ACTIVITY REGISTRY
// ============================================================================

// ActivityRegistry manages activity registration and wrapping with middleware
// Similar to tool registry but for Temporal activities
type ActivityRegistry struct {
	repo        db.Repository
	activities  map[string]interface{}  // activity name -> wrapped activity function
	outputTypes map[string]reflect.Type // activity name -> output type for schema introspection
}

// NewActivityRegistry creates a new activity registry
func NewActivityRegistry(repo db.Repository) *ActivityRegistry {
	return &ActivityRegistry{
		repo:        repo,
		activities:  make(map[string]interface{}),
		outputTypes: make(map[string]reflect.Type),
	}
}

// RegisterActivity registers ordinary AGENT work — the activities that advance
// a conversation. This is the guarded kind: a stopped run refuses it.
//
// If what you are registering only reports state (see RegisterObservability-
// Activity) or changes the run's own status (see RegisterLifecycleActivity),
// use those instead. Registering reporting work here is what previously got
// EmitToolCallStatus blocked on paused runs, blinding the UI at exactly the
// moment a user was trying to understand why their chat had stopped.
func RegisterActivity[TInput any, TOutput any](
	registry *ActivityRegistry,
	act TypedActivity[TInput, TOutput],
) {
	registerActivityInternal(registry, act, lifecycle.AgentWork)
}

// RegisterLifecycleActivity registers work that changes or repairs the run's
// OWN state: status writes, checkpoints, cleanup, the error explaining why it
// stopped.
//
// Exempt from the stopped-run rule, and it must be — this is how a stopped run
// reports, repairs and un-stops itself. The activity that writes "started" on
// resume is itself issued by a run still marked stopped.
func RegisterLifecycleActivity[TInput any, TOutput any](
	registry *ActivityRegistry,
	act TypedActivity[TInput, TOutput],
) {
	registerActivityInternal(registry, act, lifecycle.LifecycleWork)
}

// RegisterObservabilityActivity registers work that only REPORTS what already
// happened — tool call status, stream markers, execution events.
//
// Exempt from the stopped-run rule. Suppressing it stops no work (the work is
// already done or already stopped); it only hides the outcome from the user.
func RegisterObservabilityActivity[TInput any, TOutput any](
	registry *ActivityRegistry,
	act TypedActivity[TInput, TOutput],
) {
	registerActivityInternal(registry, act, lifecycle.ObservabilityWork)
}

func registerActivityInternal[TInput any, TOutput any](
	registry *ActivityRegistry,
	act TypedActivity[TInput, TOutput],
	kind lifecycle.WorkKind,
) {
	name := act.Name()

	// Create an ActivityWrapper that handles:
	// - Heartbeating for fast cancellation detection
	// - Panic recovery with proper error propagation
	// - Chat updates dual-write for UI tracking
	// - Structured logging and observability
	wrapper := NewActivityWrapper(name, act.Execute, registry.repo)
	wrapper.workKind = kind

	// Wrap with error classification middleware
	// The function signature matches what Temporal expects for typed activities
	wrappedFn := func(ctx context.Context, input TInput) (TOutput, error) {
		// Pre-execution middleware (logging)
		logger := getActivityLogger(ctx)
		activityInfo := activity.GetInfo(ctx)
		logger.Info("[Workflow Runtime Registry] Activity starting",
			"activity", name,
			"activity_id", activityInfo.ActivityID,
			"attempt", activityInfo.Attempt,
		)

		// Execute the activity wrapper (handles middleware)
		output, err := wrapper.Execute(ctx, input)

		// Post-execution middleware - Smart error handling
		if err != nil {
			// Classify and handle the error
			classified := classifyError(err)
			category := categorizeError(err)
			terminal := isTerminal(classified)

			// Use Warn for non-terminal errors so they don't trigger Sentry.
			if terminal {
				logger.Error("[Workflow Runtime Registry] Activity failed",
					"activity", name, "error", err, "category", category, "is_terminal", terminal)
			} else {
				logger.Warn("[Workflow Runtime Registry] Activity failed",
					"activity", name, "error", err, "category", category, "is_terminal", terminal)
			}

			// Return the classified error (terminal or transient)
			// Temporal will handle retry logic based on error type
			return output, classified
		}

		logger.Info("[Workflow Runtime Registry] Activity completed", "activity", name)

		return output, nil
	}

	// Store the wrapped function and output type
	registry.activities[name] = wrappedFn

	// Store output type for schema introspection (enables GetOutputDefaults)
	var o TOutput
	registry.outputTypes[name] = reflect.TypeOf(o)

	// Register input/output types with schema for static validation.
	// This enables validation of activity input field names and output field references.
	//
	// NOTE: Some activities (e.g., V2_CallLLM) pre-register in init() with flat input types
	// to allow workflows to use args like `model: "claude-4-sonnet"` instead of nested
	// `node: { model: "..." }`. Don't overwrite those registrations.
	if !schema.IsActivityTypeRegistered(name) {
		var i TInput
		schema.RegisterActivityType(name, reflect.TypeOf(i), reflect.TypeOf(o))
	}
}

// RegisterWithWorker registers all activities with a Temporal worker
func (r *ActivityRegistry) RegisterWithWorker(w worker.Worker) {
	for name, activityFn := range r.activities {
		w.RegisterActivityWithOptions(activityFn, activity.RegisterOptions{
			Name: name,
		})
	}
}

// Get retrieves a registered activity by name
func (r *ActivityRegistry) Get(name string) (interface{}, error) {
	activity, ok := r.activities[name]
	if !ok {
		return nil, fmt.Errorf("activity not found: %s", name)
	}
	return activity, nil
}

// List returns all registered activity names
func (r *ActivityRegistry) List() []string {
	names := make([]string, 0, len(r.activities))
	for name := range r.activities {
		names = append(names, name)
	}
	return names
}

// GetOutputDefaults returns the zero-value fields for an activity's output type.
// This is used to ensure source data has all expected fields before CEL evaluation,
// preventing "no such key" errors when workflows reference optional fields.
//
// The returned map contains all fields from the output struct with their zero values
// (nil for pointers/slices, 0 for numbers, "" for strings, etc.)
func (r *ActivityRegistry) GetOutputDefaults(activityName string) (map[string]interface{}, error) {
	outputType, ok := r.outputTypes[activityName]
	if !ok {
		return nil, fmt.Errorf("activity not found: %s", activityName)
	}

	// Handle nil type (shouldn't happen but be safe)
	if outputType == nil {
		return make(map[string]interface{}), nil
	}

	// Create zero value of the output type
	var zeroValue reflect.Value
	if outputType.Kind() == reflect.Ptr {
		zeroValue = reflect.New(outputType.Elem())
	} else {
		zeroValue = reflect.New(outputType).Elem()
	}

	// Marshal to JSON and back to get map with all fields
	jsonBytes, err := json.Marshal(zeroValue.Interface())
	if err != nil {
		return nil, fmt.Errorf("failed to marshal zero value: %w", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(jsonBytes, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal to map: %w", err)
	}

	return result, nil
}

// ============================================================================
// HELPER FUNCTIONS
// ============================================================================

// getActivityLogger gets the Temporal activity logger
func getActivityLogger(ctx context.Context) log.Logger {
	return activity.GetLogger(ctx)
}

// ============================================================================
// EXAMPLE USAGE
// ============================================================================

/*
// Define a typed activity
type ProcessMessageActivity struct {
	repo db.Repository
	llm  llm.Client
}

type ProcessMessageInput struct {
	MessageID string
	ChatID    string
}

type ProcessMessageOutput struct {
	ResponseMessageID string
	HasToolCalls      bool
	ToolCalls         []string
}

func (a *ProcessMessageActivity) Name() string {
	return "ProcessMessage"
}

func (a *ProcessMessageActivity) Execute(ctx context.Context, input ProcessMessageInput) (ProcessMessageOutput, error) {
	// Pure business logic - no state management!
	// Middleware handles logging, state tracking, etc.

	// ... implementation ...

	return ProcessMessageOutput{
		ResponseMessageID: "msg-123",
		HasToolCalls:      true,
		ToolCalls:         []string{"tool-1", "tool-2"},
	}, nil
}

// Register it
registry := NewActivityRegistry(repo)
RegisterActivity(registry, &ProcessMessageActivity{repo: repo, llm: llmClient})

// Register with Temporal worker
registry.RegisterWithWorker(worker)
*/

// The stopped-workflow guard formerly lived here as workflowIsStopped. It now
// lives in internal/workflow/lifecycle, which owns what "stopped" means and
// which work is exempt; see the call in Execute.
