// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
)

// heartbeatRPCFailureSurvivor is implemented by an activity whose in-flight
// work must not be abandoned because one heartbeat RPC to the Temporal server
// was slow. Declared here, at its only consumer (registerActivityInternal).
//
// Opt-in rather than universal: short activities lose little to a retry and
// are left on the SDK's own path (spuriousHeartbeatCancel turns the cancel
// into a retry). ExecuteTools opts in because a tool cannot re-run; CallLLM
// because a retry throws away a stream the user is watching and pays for it
// again — and under load the retries are what keep the server loaded.
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
// Once engaged, a LATER real cancel still reaches the work. The SDK's cancel
// func has already fired by then, so the SDK heartbeat path
// (activity.RecordHeartbeat) can no longer report one: a CanceledError in a
// heartbeat response calls a cancel func that is already spent, and
// RecordHeartbeat returns nothing. So the wrapper heartbeats an engaged
// shield DIRECTLY instead (heartbeatShield.heartbeat), through the worker's
// client, which returns the server's verdict — and a cancel, pause, reset or
// NotFound in it cancels the work with that cause, exactly as it would have
// before the shield engaged. That direct RPC also gets its own deadline
// (shieldedHeartbeatRPCTimeout) instead of the SDK's 2s one, which is the
// budget a loaded server keeps blowing.
//
// The returned shield's release func must be called when the activity
// returns; it stops the watcher, which runs on helpers so the caller can wait
// for it.
func shieldFromSpuriousHeartbeatCancel(
	sdkCtx context.Context,
	workerStopCh <-chan struct{},
	helpers *sync.WaitGroup,
	onShielded func(cause error),
) *heartbeatShield {
	base := context.WithoutCancel(sdkCtx)
	stopDeadline := func() {}
	if deadline, ok := sdkCtx.Deadline(); ok {
		base, stopDeadline = context.WithDeadline(base, deadline)
	}
	work, cancelWork := context.WithCancelCause(base)
	s := &heartbeatShield{work: work, cancelWork: cancelWork}

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
		s.engagedFlag.Store(true)
		onShielded(cause)
	})

	s.release = func() {
		close(released)
		cancelWork(context.Canceled)
		stopDeadline()
	}
	return s
}

const (
	// shieldedHeartbeatInterval is how often an engaged shield heartbeats. It
	// matches the SDK's batching window (maxHeartbeatThrottleInterval in
	// workersetup), so an engaged activity costs the server no more heartbeat
	// RPCs than a healthy one does.
	shieldedHeartbeatInterval = 2 * time.Second

	// shieldedHeartbeatRPCTimeout bounds one direct heartbeat RPC. It is the
	// budget the SDK path cannot widen (its deadline IS the 2s throttle
	// interval), and the reason a slow server stops killing work: 10s rides
	// out the persistence stalls measured on 2026-10-30 while staying well
	// inside activityHeartbeatTimeout (30s), so a run of slow RPCs still
	// lands a heartbeat before the server would declare the worker dead.
	shieldedHeartbeatRPCTimeout = 10 * time.Second
)

// directHeartbeater records one heartbeat and returns the server's verdict:
// nil, a temporal CanceledError, activity.ErrActivityPaused /
// ErrActivityReset, a serviceerror (NotFound when the attempt is gone), or a
// transport error. The production one is clientHeartbeat; tests inject a fake
// through ActivityRegistry.directHeartbeat.
type directHeartbeater func(ctx context.Context, details any) error

// clientHeartbeat heartbeats through the worker's own client rather than the
// activity's SDK invoker, so the result — including a cancel request — comes
// back to the caller instead of into an already-spent cancel func.
func clientHeartbeat(ctx context.Context, details any) error {
	info := activity.GetInfo(ctx)
	return activity.GetClient(ctx).RecordActivityHeartbeat(ctx, info.TaskToken, details)
}

// heartbeatShield is the context an activity's work runs under when it opts
// into surviving heartbeat RPC failures, plus the state the wrapper's
// heartbeat loop needs to keep cancellation working once it has engaged.
type heartbeatShield struct {
	work       context.Context
	cancelWork context.CancelCauseFunc
	release    func()

	// engagedFlag is set once the SDK has spuriously cancelled sdkCtx and the
	// shield absorbed it. From then on the SDK heartbeat path is blind to
	// cancellation, so heartbeats must go through heartbeat.
	engagedFlag atomic.Bool

	// lastDirect is when heartbeat last sent an RPC; only the heartbeat loop
	// goroutine touches it.
	lastDirect time.Time
	// failingSince is when the current run of failed direct heartbeats began
	// (zero while they succeed); heartbeat-loop goroutine only.
	failingSince time.Time
}

// engaged reports whether the shield has absorbed a spurious cancel.
func (s *heartbeatShield) engaged() bool {
	return s.engagedFlag.Load()
}

// heartbeat sends one direct heartbeat for an engaged shield, at most once
// per shieldedHeartbeatInterval, and acts on the server's verdict: a real
// instruction to stop (cancel, pause, reset, or an attempt the server no
// longer knows) cancels the work with that cause. Any other error is a slow or
// unreachable server — the very condition the shield exists to ride out — so
// the work continues, and the server's own HeartbeatTimeout stays the backstop
// that ends an attempt nobody can heartbeat for (its next heartbeat then gets
// NotFound, which stops the work here).
//
// It returns the RPC error, if one was sent and failed, for logging.
func (s *heartbeatShield) heartbeat(now time.Time, send directHeartbeater, details any) error {
	if now.Sub(s.lastDirect) < shieldedHeartbeatInterval {
		return nil
	}
	s.lastDirect = now
	rpcCtx, cancel := context.WithTimeout(s.work, shieldedHeartbeatRPCTimeout)
	defer cancel()
	err := send(rpcCtx, details)
	if err == nil {
		s.failingSince = time.Time{}
		return nil
	}
	if realStopInstruction(err) {
		s.cancelWork(err)
		return err
	}
	if s.failingSince.IsZero() {
		s.failingSince = now
	}
	return err
}

// realStopInstruction reports whether a heartbeat verdict tells the attempt to
// stop, as opposed to the RPC merely failing.
func realStopInstruction(err error) bool {
	return temporal.IsCanceledError(err) ||
		errors.Is(err, activity.ErrActivityPaused) ||
		errors.Is(err, activity.ErrActivityReset) ||
		attemptUnknownToServer(err)
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
