// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/nomachine"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// WaitForMachineParams is wait_for_machine's input.
type WaitForMachineParams struct {
	// TimeoutSeconds bounds THIS call, not the machine: timing out leaves
	// everything as it was and is not an error.
	TimeoutSeconds int `json:"timeout_seconds,omitempty" jsonschema:"description=Maximum seconds to wait for the machine before returning a still-not-connected result (default: 1200, maximum: 1200). Timing out is not an error — call again to keep waiting."`
}

// WaitForMachineResponseMetadata is the structured half of a wait_for_machine
// result, for the UI and for tests.
type WaitForMachineResponseMetadata struct {
	// Connected: the machine is attached, so the failed call can be retried.
	Connected bool `json:"connected"`
	// State is the last state seen: connected, disconnected, starting,
	// suspended, failed, removed, or unknown when no read ever succeeded.
	State string `json:"state"`
	// WaitedMs is how long this call blocked.
	WaitedMs int64 `json:"waited_ms"`
	// TimedOut: the budget elapsed with the machine still not connected.
	TimedOut bool `json:"timed_out"`
	// StoppedEarly: cut short — preempted by a message, or interrupted.
	StoppedEarly bool `json:"stopped_early,omitempty"`
}

const (
	waitForMachineMaxTimeout = MaxBlockingToolWait

	// waitForMachinePollInterval matches preflight's cadence. The wait is
	// server-side, so this costs no model round-trips.
	waitForMachinePollInterval = 2 * time.Second

	// waitForMachineFailedSettle is how long a failed-to-start report must
	// hold, the machine never connecting meanwhile, before the wait believes
	// it. The control plane reports Failed for moments it recovers from by
	// itself: on 2026-10-30 a resumed machine read Failed for 17s while its
	// previous pod finished terminating, and a VM that crashed and was
	// re-created read Failed for ~40s before its daemon reconnected. Every
	// run waiting on those machines was told to stop, and the machine came
	// back on its own seconds later.
	waitForMachineFailedSettle = 2 * time.Minute

	waitForMachineDescription = `Wait for this run's machine to reconnect, then return so you can retry.

WHEN TO USE:
A machine tool (shell, view, edit, ...) just failed because the machine this run works on is not connected. Do NOT end your turn over it, and do NOT retry the failed call in a loop — call wait_for_machine once, then retry the call that failed.

WHEN NOT TO USE:
- The tool failed for some other reason (bad arguments, a command that exited non-zero). Only a machine that is not connected is worth waiting on.
- To start, wake or restart the machine. This never does — it only watches.

HOW IT WORKS:
It checks every couple of seconds and returns the moment the machine is connected. If the budget elapses first it returns normally, saying what state the machine is in; that is not an error, and calling again keeps waiting.

If it reports that the machine was removed, or that it failed to start and stayed failed, waiting will not help: tell the user what happened and stop.

RETURNS EARLY WHEN A MESSAGE ARRIVES:
If a message arrives for you while you wait — from the user, your parent, or a sub-agent — the wait ends early so you can read it. Nothing is lost; call wait_for_machine again if you still need the machine.

RETURNS:
- connected: true once the machine is attached — retry the call that failed
- otherwise its last known state (disconnected, starting, suspended, failed, removed) with what to do about it`
)

type waitForMachineTool struct {
	observer MachineObserver

	// pollInterval and timeoutUnit are the production constants unless a test
	// shrinks them; timeoutUnit is what one timeout_seconds is worth.
	pollInterval time.Duration
	timeoutUnit  time.Duration
	// failedSettle is waitForMachineFailedSettle unless a test shrinks it.
	failedSettle time.Duration
}

// NewWaitForMachineTool returns wait_for_machine. A nil observer makes it
// report that the machine cannot be observed here rather than fail.
func NewWaitForMachineTool(observer MachineObserver) Tool {
	return NewToolWrapper[WaitForMachineParams, ToolResponse](newWaitForMachineTool(observer))
}

func newWaitForMachineTool(observer MachineObserver) *waitForMachineTool {
	return &waitForMachineTool{
		observer:     observer,
		pollInterval: waitForMachinePollInterval,
		timeoutUnit:  time.Second,
		failedSettle: waitForMachineFailedSettle,
	}
}

func (t *waitForMachineTool) Name() string { return ToolWaitForMachine }

func (t *waitForMachineTool) Description() string { return waitForMachineDescription }

func (t *waitForMachineTool) RequiresPermission(WaitForMachineParams) (bool, error) {
	return false, nil
}

func (t *waitForMachineTool) IsReadOnly() bool { return true }

func (t *waitForMachineTool) Execute(rc *rctx.ToolContext, params WaitForMachineParams) (ToolResponse, error) {
	if nomachine.Is(rc.Context) {
		return NewTextErrorResponse(nomachine.Refusal(ToolWaitForMachine)), nil
	}
	if t.observer == nil {
		return NewTextErrorResponse("wait_for_machine is unavailable here: this run cannot observe its machine's connection."), nil
	}
	userID, ok := auth.GetUserIDFromContext(rc.Context)
	if !ok || userID == "" {
		return NewTextErrorResponse("no user in the tool context, so the machine cannot be looked up"), nil
	}

	budget := waitForMachineMaxTimeout
	if params.TimeoutSeconds > 0 {
		budget = time.Duration(params.TimeoutSeconds) * t.timeoutUnit
	}
	// Clamp rather than reject, as shell_wait does: wait as long as one call
	// allows and say "call again".
	if budget > waitForMachineMaxTimeout {
		budget = waitForMachineMaxTimeout
	}

	start := time.Now()
	deadline := start.Add(budget)
	last := "unknown"
	var lastMessage string
	// failedSince is when the current unbroken run of Failed looks began;
	// zero while the machine is not reporting Failed.
	var failedSince time.Time

	for {
		status, err := t.observer.MachineStatus(rc.Context, userID)
		// A failed read is transient: keep the last state and keep waiting.
		if err == nil {
			last = stateOf(status)
			lastMessage = status.StatusMessage
			failedSince = failedRunStart(failedSince, status)
			if resp, done := t.terminalResponse(status, time.Since(start), sinceOrZero(failedSince)); done {
				return resp, nil
			}
		}

		if !time.Now().Before(deadline) {
			return t.timedOutResponse(last, lastMessage, time.Since(start)), nil
		}

		select {
		case <-rc.Done():
			return t.stoppedEarly(rc, userID, last, lastMessage, failedSince, time.Since(start)), nil
		case <-time.After(t.pollInterval):
		}
	}
}

// failedRunStart carries the start of an unbroken run of Failed looks across
// one more look: kept while the machine still reports Failed, set on the first
// Failed look, cleared by anything else.
func failedRunStart(since time.Time, status MachineStatus) time.Time {
	switch {
	case !status.Failed:
		return time.Time{}
	case since.IsZero():
		return time.Now()
	}
	return since
}

func sinceOrZero(t time.Time) time.Duration {
	if t.IsZero() {
		return 0
	}
	return time.Since(t)
}

// terminalResponse answers when the look settles the wait: the machine is
// back, or it is gone for good. A Failed report settles it only once it has
// held for failedSettle (failedFor), since the control plane reports Failed
// for moments it recovers from by itself.
func (t *waitForMachineTool) terminalResponse(status MachineStatus, waited, failedFor time.Duration) (ToolResponse, bool) {
	switch {
	case status.Connected:
		return waitResponse(fmt.Sprintf(
			"The machine is connected again (waited %s). Retry the call that failed.",
			formatWaitDuration(waited)),
			WaitForMachineResponseMetadata{Connected: true, State: "connected", WaitedMs: waited.Milliseconds()}), true
	case status.Removed:
		return waitResponse(withReason(
			"This run's machine no longer exists, so waiting will not bring it back. "+
				"Tell the user the machine was removed and that they need to connect a machine to continue; then stop.",
			status.StatusMessage),
			WaitForMachineResponseMetadata{State: "removed", WaitedMs: waited.Milliseconds()}), true
	case status.Failed && failedFor >= t.failedSettle:
		return waitResponse(withReason(
			"This run's machine failed to start, so waiting will not bring it back. "+
				"Tell the user the machine failed to start and that they need to restart it or connect another; then stop.",
			status.StatusMessage),
			WaitForMachineResponseMetadata{State: "failed", WaitedMs: waited.Milliseconds()}), true
	}
	return ToolResponse{}, false
}

func (t *waitForMachineTool) timedOutResponse(state, message string, waited time.Duration) ToolResponse {
	text := fmt.Sprintf("The machine is still not connected after %s — not an error. ", formatWaitDuration(waited)) +
		stillWaitingGuidance(state)
	return waitResponse(withReason(text, message),
		WaitForMachineResponseMetadata{State: state, WaitedMs: waited.Milliseconds(), TimedOut: true})
}

// stoppedEarly answers a wait that was cut short. It takes one last look on a
// detached, bounded context — the call's own is the one that just died — since
// the machine may have come back in the instant before, and "it is back" is the
// answer the agent was waiting for.
func (t *waitForMachineTool) stoppedEarly(rc *rctx.ToolContext, userID, last, lastMessage string, failedSince time.Time, waited time.Duration) ToolResponse {
	checkCtx, cancel := context.WithTimeout(context.WithoutCancel(rc.Context), stoppedEarlyCheckTimeout)
	defer cancel()

	if status, err := t.observer.MachineStatus(checkCtx, userID); err == nil {
		last = stateOf(status)
		lastMessage = status.StatusMessage
		failedSince = failedRunStart(failedSince, status)
		if resp, done := t.terminalResponse(status, waited, sinceOrZero(failedSince)); done {
			return resp
		}
	}

	text := fmt.Sprintf("Stopped waiting for the machine after %s; it is still not connected. ", formatWaitDuration(waited)) +
		"Call wait_for_machine again if you still need it."
	return waitResponse(withReason(text, lastMessage),
		WaitForMachineResponseMetadata{State: last, WaitedMs: waited.Milliseconds(), StoppedEarly: true})
}

func stateOf(s MachineStatus) string {
	switch {
	case s.Connected:
		return "connected"
	case s.Removed:
		return "removed"
	case s.Failed:
		return "failed"
	case s.Suspended:
		return "suspended"
	case s.Starting:
		return "starting"
	}
	return "disconnected"
}

func stillWaitingGuidance(state string) string {
	switch state {
	case "suspended":
		return "The machine is asleep, and this run cannot wake it — the user can start it from the machines page or by sending a message. " +
			"Tell the user the machine is asleep and that you are waiting on it; call wait_for_machine again to keep waiting, or end your turn if the rest of your work does not need it."
	case "starting":
		return "The machine is on its way up. Call wait_for_machine again to keep waiting."
	case "disconnected":
		return "The machine may be restarting or reconnecting. Call wait_for_machine again to keep waiting."
	case "failed":
		return "The machine reported that it failed to start, which the control plane often recovers from by itself within a minute or two. " +
			"Call wait_for_machine again to keep waiting; it says so if the failure holds."
	}
	return "Its state could not be read just now. Call wait_for_machine again to keep waiting."
}

func withReason(text, message string) string {
	if message = strings.TrimSpace(message); message == "" {
		return text
	}
	return text + "\n\nStatus: " + message
}

func waitResponse(text string, metadata WaitForMachineResponseMetadata) ToolResponse {
	return WithResponseMetadata(NewTextResponse(text), metadata)
}
