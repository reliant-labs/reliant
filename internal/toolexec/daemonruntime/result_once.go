// Copyright (c) 2026 Reliant Labs
package daemonruntime

import (
	"fmt"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// A request id runs once.
//
// A run step's command travels under a request id derived from its Temporal
// activity, the same on every attempt (handlers.runStepRequestID), so that a
// retry whose worker went away can reattach to the command instead of running
// it again. The worker never sends a command twice unless the first send
// provably never reached a machine. That is the worker's discipline; this is
// the machine's: a tool request whose id this daemon already knows — running,
// finished and held, or delivered — is never executed again. The caller gets
// what the first execution produced, or, once that is gone, an answer saying
// the request already ran and its outcome is unknown.
//
// Request ids that are not a run step's are fresh per call (a UUID the router
// mints), so this changes nothing for them. The memory is the outbox's: a
// running call, a held result, or a delivery inside resultHoldTTL, which a
// restart keeps for held and retained results and turns into "interrupted by
// daemon restart" for running ones.
//
// That memory is bounded by result retention — resultHoldTTL, and the hold
// and retain byte budgets — and a run step's id lives as long as its activity
// may be retried, which is far longer. So a run step's id also leaves a
// tombstone (markRetain): its name and the time it began, nothing else, in
// memory and as <request id>.ran on disk, written durably before the command
// runs. It is independent of the result and of every byte budget, and is
// forgotten only after runStepRanTTL. A repeat that finds only the tombstone
// gets alreadyRanResponse.

// runStepRanTTL is how long a run step's tombstone keeps its id from running
// again. A run step's request id names one activity of one workflow run
// (handlers.runStepRequestID), and that activity is retried for at most its
// schedule-to-close timeout: the node's own timeout plus the machine-wait
// allowance, or 30 days when the node sets none
// (StepExecutor.activityOptionsWithSlack). A day more than the longest
// default covers clock skew between the worker and this machine. A tombstone
// is a few dozen bytes, so it carries no budget: one that a budget could evict
// would make its id fresh again.
const runStepRanTTL = 31 * 24 * time.Hour

// ranSweepEvery throttles the tombstone sweep: expireLocked runs on every
// status question, and tombstones live for weeks, so walking them each time
// buys nothing.
const ranSweepEvery = time.Minute

// begin records a tool call that arrived to run, unless the outbox already
// knows requestID: then fresh is false, known is what became of it, and the
// call must not run again. An empty id is always fresh, and not recorded.
func (o *resultOutbox) begin(requestID string) (known toolexec.ToolRequestState, fresh bool) {
	if requestID == "" {
		return toolexec.ToolRequestUnknown, true
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.expireLocked(time.Now())
	if known = o.stateLocked(requestID); known != toolexec.ToolRequestUnknown {
		return known, false
	}
	if _, ran := o.ran[requestID]; ran {
		// It ran here, and its result is gone (expired, or dropped over
		// budget): answered as delivered and gone.
		return toolexec.ToolRequestDelivered, false
	}
	if o.running == nil {
		o.running = make(map[string]struct{})
	}
	o.running[requestID] = struct{}{}
	return toolexec.ToolRequestUnknown, true
}

// recordRanLocked keeps requestID's tombstone, begun at at. Must hold o.mu.
func (o *resultOutbox) recordRanLocked(requestID string, at time.Time) {
	if o.ran == nil {
		o.ran = make(map[string]time.Time)
	}
	o.ran[requestID] = at
}

// expireRanLocked forgets tombstones past runStepRanTTL, at most once per
// ranSweepEvery. Must hold o.mu.
func (o *resultOutbox) expireRanLocked(now time.Time) {
	if len(o.ran) == 0 || now.Sub(o.ranSweptAt) < ranSweepEvery {
		return
	}
	o.ranSweptAt = now
	for id, at := range o.ran {
		if now.Sub(at) > runStepRanTTL {
			delete(o.ran, id)
			o.store.removeRan(id)
		}
	}
}

// answerRepeatedRequest answers a tool request whose id already ran or is
// running (begin), on s, the stream it arrived on, without running it:
//
//   - running: nothing now; its result goes out when it ends, and whichever
//     gateway takes it delivers it to the request's result subject;
//   - finished and held: the held result, again;
//   - delivered and retained: the retained result, again;
//   - delivered and gone: alreadyRanResponse.
func (d *daemonClient) answerRepeatedRequest(s *gatewaySession, requestID string, known toolexec.ToolRequestState) {
	if s == nil {
		s = d.session.Load()
	}
	answer := "waiting for the running call"
	switch known {
	case toolexec.ToolRequestCompleted:
		answer = "resent its held result"
		if d.sendHeldResults(s, func(h *heldResult) bool { return h.requestID == requestID }) == 0 {
			answer = "could not resend its held result"
		}
	case toolexec.ToolRequestDelivered:
		answer = "resent its retained result"
		if !d.resendRetainedResult(s, requestID) {
			answer = "its result is gone; answered that it already ran"
			if err := s.send(alreadyRanResponse(requestID)); err != nil {
				answer = "its result is gone, and the answer could not be sent"
			}
		}
	}
	logging.Info(logPrefix+" A tool request arrived again under an id this daemon already ran; not running it again",
		"request_id", requestID, "state", known, "answer", answer)
}

// alreadyRanResponse answers a request that ran on this daemon before and
// whose result is no longer held. It is the daemon's own outcome-unknown
// answer (ErrorCodeDaemonRestarted, which every worker reads as "it may have
// run; verify its effects"), never a verdict on the command.
func alreadyRanResponse(requestID string) *reliantv1.DaemonMessage {
	msg := fmt.Sprintf("Tool request %q already ran on this machine and its result is no longer held, "+
		"so it was not run again and its outcome is unknown. Verify its effects before running it again.", requestID)
	return &reliantv1.DaemonMessage{
		Message: &reliantv1.DaemonMessage_ToolResponse{ToolResponse: &reliantv1.ToolResponse{
			RequestId:    requestID,
			Success:      false,
			IsError:      true,
			Content:      msg,
			ErrorMessage: msg,
			ErrorCode:    toolexec.ErrorCodeDaemonRestarted,
		}},
	}
}
