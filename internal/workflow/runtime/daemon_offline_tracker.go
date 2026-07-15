// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"github.com/reliant-labs/reliant/internal/daemonoffline"
	"go.temporal.io/sdk/workflow"
)

// DaemonOfflinePauseThreshold is the number of CONSECUTIVE daemon-targeted
// step completions that failed with the "no daemon connected" condition after
// which the workflow pauses itself (instead of doing another LLM round-trip
// against a dead daemon).
//
// Why 3:
//   - 1 is too aggressive: a brief disconnect during a single ExecuteTools
//     fan-out (e.g. the user's laptop briefly lost wifi) would pause the
//     workflow even though the daemon comes back milliseconds later.
//   - 2 still risks pausing flows during expected daemon restarts (workspace
//     pod restart, daemon binary upgrade).
//   - 3 gives ~3 LLM-call boundaries of headroom (~30s+ depending on model
//     latency) while still bounding the token burn and chat spam to something
//     the user will tolerate. The threshold is a const, not an env var: it's
//     a quality-of-life knob, not an operator-tunable knob.
//
// If we later observe this is too aggressive or too lax, change the const —
// not introduce a flag.
const DaemonOfflinePauseThreshold = 3

// DaemonOfflinePauseMessage is the user-facing chat message emitted (via the
// WorkflowError activity) when the circuit breaker pauses the workflow.
// Paused chats resume when the user sends a message (SendMessage routes
// paused chats through PauseService.ResumeWorkflow).
const DaemonOfflinePauseMessage = "Paused: no machine is connected. Start your machine and send a message to continue."

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
	consecutiveOffline int

	// pause blocks until the user resumes the workflow. Wired by
	// DynamicWorkflow to: emit the user-facing chat message, mark the
	// workflow paused in the DB, flip the cooperative pause flag, and block
	// on the pause epoch. callerCtx MUST be the calling goroutine's own
	// workflow.Context.
	pause func(callerCtx workflow.Context, streak int)
}

// NewDaemonOfflineCircuitBreaker creates a breaker that invokes pause once
// the consecutive-offline streak reaches threshold. pause may be nil (the
// breaker then only counts — useful in tests).
func NewDaemonOfflineCircuitBreaker(threshold int, pause func(callerCtx workflow.Context, streak int)) *DaemonOfflineCircuitBreaker {
	return &DaemonOfflineCircuitBreaker{
		threshold: threshold,
		pause:     pause,
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
// unchanged otherwise. When the streak reaches the threshold the pause
// callback fires, blocking until the user resumes the workflow.
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
	switch classifyStepEvent(activityName, stepEvent) {
	case verdictAlive:
		b.consecutiveOffline = 0
	case verdictOffline:
		b.consecutiveOffline++
		if b.consecutiveOffline >= b.threshold && b.pause != nil {
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
