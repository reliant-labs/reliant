// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"sync/atomic"
	"time"

	"github.com/reliant-labs/reliant/internal/daemonoffline"
	"go.temporal.io/sdk/workflow"
)

// DaemonOfflinePauseThreshold is the number of CONSECUTIVE daemon-targeted
// step completions that failed with the "no daemon connected" condition before
// the workflow may pause itself.
const DaemonOfflinePauseThreshold = 3

// DaemonOfflinePauseGrace is the minimum uninterrupted daemon-unreachable
// interval before pausing a workflow. The gateway can roll independently of a
// workspace pod, briefly leaving no NATS responder while every daemon
// reconnects. Counting three quick tool attempts during that window must not
// make the user manually resume a chat that would have recovered by itself.
const DaemonOfflinePauseGrace = 2 * time.Minute

// daemonOfflinePauseGraceOverride, in nanoseconds, replaces
// DaemonOfflinePauseGrace for every breaker this process creates when it is
// positive. Only SetDaemonOfflinePauseGraceForTest sets it.
var daemonOfflinePauseGraceOverride atomic.Int64

func daemonOfflinePauseGrace() time.Duration {
	if d := daemonOfflinePauseGraceOverride.Load(); d > 0 {
		return time.Duration(d)
	}
	return DaemonOfflinePauseGrace
}

// SetDaemonOfflinePauseGraceForTest shortens the grace every breaker created
// in this process waits before pausing, and returns a restore func. The e2e
// stories run DynamicWorkflow on a real Temporal dev server, which cannot skip
// workflow time, so a story that drives the pause would otherwise wait out
// the full two minutes. A breaker reads the grace when its workflow starts.
func SetDaemonOfflinePauseGraceForTest(d time.Duration) (restore func()) {
	previous := daemonOfflinePauseGraceOverride.Swap(int64(d))
	return func() { daemonOfflinePauseGraceOverride.Store(previous) }
}

// DaemonOfflinePauseMessage is the user-facing chat message emitted (via the
// WorkflowError activity) when the circuit breaker pauses the workflow.
// Paused chats resume when the user sends a message (SendMessage routes
// paused chats through PauseService.ResumeWorkflow). A failed request only
// proves this workflow could not reach its daemon; it does not prove the
// machine is stopped, so it must not instruct the user to start it.
const DaemonOfflinePauseMessage = "Paused: this chat has been unable to reach its machine for two minutes. The machine may still be reconnecting; send a message to retry."

// DaemonOfflineCircuitBreaker counts consecutive daemon-offline step
// completions and pauses the workflow when the streak reaches the threshold.
//
// Lifetime: one breaker per workflow execution, created by DynamicWorkflow
// and shared with EVERY StepExecutor (main loop, inline workflow executors,
// loop iterations, parallel branches, spawned threads) by riding on the
// PauseController. That sharing is what makes the count meaningful for agent
// chats: the agent loop runs inside InlineLoopExecutor, whose per-iteration
// ExecuteTools completions never surface as main-loop step events — the only
// chokepoint every completion passes through is StepExecutor.HandleCompletion.
//
// Lives on the workflow's (deterministic) stack and is never persisted —
// replay reconstructs it from the recorded activity outcomes, and the pause
// callback replays deterministically through the same signal-based pause
// machinery used for user pause and retry-exhaustion.
//
// Counting policy (see classifyStepEvent):
//   - A completed step whose daemon-targeted tool results ALL failed with
//     "no daemon connected" (and none succeeded) → consecutive++.
//   - A completed step where the daemon answered at least one call (any
//     non-error tool result, or a successful run step) → consecutive = 0.
//   - Anything else (CallLLM, save_message, non-daemon tool errors,
//     cancellations, Go-level step errors) → consecutive UNCHANGED. Neutral
//     steps must not reset the streak, otherwise the
//     [ExecuteTools-fail, CallLLM, ExecuteTools-fail, ...] cadence of an
//     agent loop would never trip the breaker.
//
// Go-level step ERRORS are deliberately neutral even when they carry the
// daemon-offline marker: activities that FAIL (rather than succeed with
// error-shaped tool results) exhaust Temporal's retry budget and are already
// routed through the retry-exhaustion self-pause in workflow.go /
// loop_executor.go. Counting them here would double-pause.
type DaemonOfflineCircuitBreaker struct {
	threshold          int
	grace              time.Duration
	consecutiveOffline int
	firstOfflineAt     time.Time
	now                func(workflow.Context) time.Time

	// pause blocks until the user resumes the workflow. Wired by
	// DynamicWorkflow to: emit the user-facing chat message, mark the
	// workflow paused in the DB, flip the cooperative pause flag, and block
	// on the pause epoch. callerCtx MUST be the calling goroutine's own
	// workflow.Context.
	pause func(callerCtx workflow.Context, streak int)
}

// NewDaemonOfflineCircuitBreaker creates a breaker that invokes pause only
// after both the consecutive-offline threshold and DaemonOfflinePauseGrace.
// pause may be nil (the breaker then only counts — useful in tests).
func NewDaemonOfflineCircuitBreaker(threshold int, pause func(callerCtx workflow.Context, streak int)) *DaemonOfflineCircuitBreaker {
	return newDaemonOfflineCircuitBreaker(threshold, daemonOfflinePauseGrace(), pause)
}

func newDaemonOfflineCircuitBreaker(threshold int, grace time.Duration, pause func(callerCtx workflow.Context, streak int)) *DaemonOfflineCircuitBreaker {
	return &DaemonOfflineCircuitBreaker{
		threshold: threshold,
		grace:     grace,
		now: func(ctx workflow.Context) time.Time {
			if ctx != nil {
				return workflow.Now(ctx)
			}
			return time.Now()
		},
		pause: pause,
	}
}

// stepVerdict classifies a completed step's daemon-offline evidence.
type stepVerdict int

const (
	// verdictNeutral: no daemon evidence either way (CallLLM, save_message,
	// non-daemon errors, cancellations).
	verdictNeutral stepVerdict = iota
	// verdictOffline: daemon-targeted work failed with "no daemon connected"
	// and nothing succeeded.
	verdictOffline
	// verdictAlive: the daemon answered at least one call.
	verdictAlive
)

// classifyStepEvent inspects a completed step and classifies its
// daemon-offline evidence.
//
//   - Step errors are neutral: failed activities go through the
//     retry-exhaustion pause machinery, not the circuit breaker (see the
//     DaemonOfflineCircuitBreaker doc comment).
//   - A successful ExecuteRunStep proves the daemon executed a command.
//   - ExecuteTools outputs are scanned per tool result: a result with
//     is_error=true carrying the daemon-offline substring counts as offline;
//     any non-error result counts as the daemon (or an inline tool)
//     answering, which resets the streak — a successful tool call is the
//     canonical "we're back" signal.
func classifyStepEvent(activityName string, stepEvent *StepEvent) stepVerdict {
	if stepEvent == nil || stepEvent.Error != nil {
		return verdictNeutral
	}

	// Run steps return non-tool_results output shapes; reaching here without
	// an error means the daemon executed the command.
	if activityName == "ExecuteRunStep" {
		return verdictAlive
	}

	toolResults, ok := stepEvent.Data["tool_results"].([]interface{})
	if !ok || len(toolResults) == 0 {
		return verdictNeutral
	}

	sawOffline := false
	sawSuccess := false
	for _, item := range toolResults {
		tr, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		content, _ := tr["content"].(string)
		isError, _ := tr["is_error"].(bool)

		if isError {
			if daemonoffline.IsToolResultContent(content) {
				sawOffline = true
			}
			// Non-daemon tool errors (validation failures, command failures,
			// ...) are neutral: they neither prove nor disprove daemon
			// liveness.
			continue
		}
		sawSuccess = true
	}

	switch {
	case sawSuccess:
		return verdictAlive
	case sawOffline:
		return verdictOffline
	default:
		return verdictNeutral
	}
}

// ObserveStep records a completed step outcome: bumps the streak on
// daemon-offline steps, resets it when a tool call succeeded, and leaves it
// unchanged otherwise. It pauses only when the streak has persisted for the
// configured grace period, so a gateway rollout cannot force a manual resume.
//
// The streak is deliberately NOT reset when pausing: if the daemon is still
// offline after resume, the very next offline step re-pauses after a single
// round-trip instead of burning another <threshold> round-trips. Any daemon
// success resets the streak as usual.
//
// callerCtx MUST be the calling goroutine's own workflow.Context (the pause
// callback blocks on it via the epoch-based pause machinery). Nil-receiver
// safe.
func (b *DaemonOfflineCircuitBreaker) ObserveStep(callerCtx workflow.Context, activityName string, stepEvent *StepEvent) {
	if b == nil {
		return
	}

	now := b.now(callerCtx)

	switch classifyStepEvent(activityName, stepEvent) {
	case verdictAlive:
		b.consecutiveOffline = 0
		b.firstOfflineAt = time.Time{}
	case verdictOffline:
		if b.firstOfflineAt.IsZero() {
			b.firstOfflineAt = now
		}
		b.consecutiveOffline++
		if b.consecutiveOffline >= b.threshold && now.Sub(b.firstOfflineAt) >= b.grace && b.pause != nil {
			b.pause(callerCtx, b.consecutiveOffline)
		}
	case verdictNeutral:
		// No daemon evidence either way — leave the streak unchanged.
	}
}

// ConsecutiveOffline exposes the streak length without mutating state.
// Intended for tests / observability. Nil-receiver safe.
func (b *DaemonOfflineCircuitBreaker) ConsecutiveOffline() int {
	if b == nil {
		return 0
	}
	return b.consecutiveOffline
}
