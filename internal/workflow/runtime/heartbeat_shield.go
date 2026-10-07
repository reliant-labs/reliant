// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"errors"
	"sync"

	"go.temporal.io/api/serviceerror"
)

// heartbeatRPCFailureSurvivor is implemented by an activity whose in-flight
// work must not be abandoned because one heartbeat RPC to the Temporal server
// was slow. Declared here, at its only consumer (registerActivityInternal).
//
// Opt-in rather than universal: an activity that can safely re-run (CallLLM,
// whose turn is keyed by callLLMIdempotencyKey) is better served by failing
// fast into a retry, which is what spuriousHeartbeatCancel already arranges.
// An activity that runs tools cannot re-run — see ExecuteToolsActivity.
type heartbeatRPCFailureSurvivor interface {
	OutlivesHeartbeatRPCFailure() bool
}

func outlivesHeartbeatRPCFailure(act any) bool {
	survivor, ok := act.(heartbeatRPCFailureSurvivor)
	return ok && survivor.OutlivesHeartbeatRPCFailure()
}

// shieldFromSpuriousHeartbeatCancel returns the context an activity's work
// should run under: one that ignores the SDK cancelling sdkCtx because a
// heartbeat RPC merely failed, and honours every other cancellation.
//
// Why this exists. The SDK cancels the activity context on ANY retryable
// heartbeat error (internal_task_handlers.go internalHeartBeat), and the
// heartbeat RPC's own deadline is the 2s throttle interval. On a loaded
// machine one slow round trip is enough. At 2026-10-06 09:16:09 five
// activities lost a heartbeat RPC to "context deadline exceeded" in the same
// second; for each ExecuteTools among them the in-flight tool was cancelled
// ("Tool execution cancelled: context canceled"), its row closed as
// Cancelled, and the wrapper — seeing a cancelled context — skipped the
// tool message save. The activity still reported success, so nothing retried,
// and the agent lost the command's result. Nothing had asked it to stop.
//
// The SDK's cancellation is co-operative and it says so: "the activity can
// ignore the cancellation and do its work and complete ... we will try to
// heartbeat" (temporalInvoker.Heartbeat). Ignoring it is safe ONLY for the
// spurious kind, so this forwards everything else unchanged and with its
// cause intact:
//   - a server cancel (CanceledError), pause or reset — a real instruction;
//   - worker shutdown — the worker is going away;
//   - the deadline — StartToCloseTimeout still bounds the work;
//   - NotFound / namespace errors — the server no longer knows this attempt
//     (it timed out and was re-dispatched), so this attempt's result can never
//     be delivered and the work should stop rather than act invisibly.
//
// Because the returned context stays live, the wrapper's heartbeat loop keeps
// heartbeating through the shielded window, so a single failed RPC does not
// snowball into a server-side HeartbeatTimeout, and the wrapper saves the
// tool message as it would for any completed activity.
//
// The trade-off, accepted deliberately: once sdkCtx has been cancelled, a
// LATER real cancel delivered on a heartbeat response cannot be observed —
// the SDK's cancel func has already fired, and RecordHeartbeat returns
// nothing. A tool already running at that point runs to completion. This does
// not strand a pause or an interrupt: both cancel the chat's in-flight tool
// calls at the daemon directly (threads.Service.CancelChatToolCalls and
// InterruptThread share cancelToolCalls), independent of Temporal, and
// a re-dispatched step returns the call's recorded result instead of running
// it again (checkPriorTerminalResult).
//
// The returned release func must be called when the activity returns; it
// stops the watcher, which runs on helpers so the caller can wait for it.
func shieldFromSpuriousHeartbeatCancel(
	sdkCtx context.Context,
	workerStopCh <-chan struct{},
	helpers *sync.WaitGroup,
	onShielded func(cause error),
) (context.Context, func()) {
	base := context.WithoutCancel(sdkCtx)
	stopDeadline := func() {}
	if deadline, ok := sdkCtx.Deadline(); ok {
		base, stopDeadline = context.WithDeadline(base, deadline)
	}
	work, cancelWork := context.WithCancelCause(base)

	released := make(chan struct{})
	helpers.Go(func() {
		select {
		case <-released:
			return
		case <-sdkCtx.Done():
		}
		cause := context.Cause(sdkCtx)
		if !spuriousHeartbeatCancel(sdkCtx, workerStopCh) || attemptUnknownToServer(cause) {
			cancelWork(cause)
			return
		}
		onShielded(cause)
	})

	return work, func() {
		close(released)
		cancelWork(context.Canceled)
		stopDeadline()
	}
}

// attemptUnknownToServer reports whether a heartbeat failed because the server
// no longer has this activity attempt — the SDK passes these through as a
// cancellation (internalHeartBeat), and unlike a slow RPC they are final.
func attemptUnknownToServer(cause error) bool {
	var notFound *serviceerror.NotFound
	var namespaceNotFound *serviceerror.NamespaceNotFound
	var namespaceNotActive *serviceerror.NamespaceNotActive
	return errors.As(cause, &notFound) ||
		errors.As(cause, &namespaceNotFound) ||
		errors.As(cause, &namespaceNotActive)
}
