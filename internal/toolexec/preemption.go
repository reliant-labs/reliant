// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"time"

	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
)

// PreemptedCancelReason is the cancel reason that tells the daemon a call was
// PREEMPTED — cut short because a message arrived for the agent — rather than
// abandoned. A preemptable tool answers a preemption with a real result ("still
// running, call again"), and the daemon sends that answer back instead of
// replacing it with "Tool execution cancelled" (see daemonruntime.executeTool).
//
// The daemon matches on this exact string, so it is defined once, here.
const PreemptedCancelReason = "preempted: a message arrived for the agent"

// defaultPreemptReplyGrace is how long a preempted daemon call is given to
// answer before the wait is abandoned. A preemptable tool answers within one
// poll interval of being told; this only bounds a daemon that never got the
// message (offline, or too old to know) so the agent is not left waiting on a
// wait it was told to stop.
const defaultPreemptReplyGrace = 15 * time.Second

func (e *RemoteExecutor) preemptReplyGrace() time.Duration {
	if e.preemptGrace > 0 {
		return e.preemptGrace
	}
	return defaultPreemptReplyGrace
}

// cancelOnPreemption returns a context that is cancelled when the call is
// preempted (tools.WithPreemption). For a tool running in this process,
// cancellation IS how preemption is delivered: a preemptable tool answers its
// context ending with a real result. The returned cancel releases the watcher.
func cancelOnPreemption(ctx context.Context) (context.Context, context.CancelFunc) {
	preempt := tools.PreemptionFrom(ctx)
	if preempt == nil {
		return ctx, func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		select {
		case <-preempt:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

// relayPreemptionToDaemon delivers a preemption to a call running on the
// daemon, and returns the context to wait for its reply under.
//
// It must NOT cancel the wait, which is how an in-process call is preempted.
// Cancelling the wait means abandoning the call, and an abandoned call's reply
// is dropped — the reply being the tool's own answer, the thing preemption
// exists to return early. So the daemon is told to stop with a cancel push
// carrying PreemptedCancelReason, and the wait goes on: the tool notices within
// one poll, answers, and that answer is the result.
//
// The wait is abandoned only if no answer comes within the grace period, or the
// push cannot be delivered at all.
func (e *RemoteExecutor) relayPreemptionToDaemon(ctx context.Context, req *ToolRequest) (context.Context, func()) {
	preempt := tools.PreemptionFrom(ctx)
	if preempt == nil {
		return ctx, func() {}
	}
	waitCtx, abandon := context.WithCancel(ctx)
	replied := make(chan struct{})

	go func() {
		select {
		case <-preempt:
		case <-replied:
			return
		case <-waitCtx.Done():
			return
		}

		if req.ToolCallID == "" {
			// Nothing names the execution on the daemon, so it cannot be told.
			abandon()
			return
		}
		pushCtx, cancelPush := context.WithTimeout(WithDaemonSelector(context.WithoutCancel(ctx), req.DaemonSelector), daemonCancelPushTimeout)
		err := e.router.SendToolExecutionCancel(pushCtx, req.UserID, req.ToolCallID, PreemptedCancelReason)
		cancelPush()
		if err != nil {
			logging.WarnContext(ctx, "Could not tell the daemon to stop a preempted tool; abandoning the wait",
				"error", err, "toolCallID", req.ToolCallID, "toolName", req.ToolName)
			abandon()
			return
		}

		grace := time.NewTimer(e.preemptReplyGrace())
		defer grace.Stop()
		select {
		case <-grace.C:
			logging.WarnContext(ctx, "Preempted tool did not answer in time; abandoning the wait",
				"toolCallID", req.ToolCallID, "toolName", req.ToolName, "grace", e.preemptReplyGrace())
			abandon()
		case <-replied:
		case <-waitCtx.Done():
		}
	}()

	return waitCtx, func() {
		close(replied)
		abandon()
	}
}
