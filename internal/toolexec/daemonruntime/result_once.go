// Copyright (c) 2026 Reliant Labs
package daemonruntime

import (
	"fmt"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/toolexec"
	activitytypes "github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
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
// mints), so this changes nothing for them.
//
// What the daemon knows of an id is the outbox's — a running call, a held
// result, a delivery inside resultHoldTTL, a retained result — and every one
// of those ends: by age, or by a byte budget that drops results to make room.
// None of them can be what keeps a deploy from running twice. So a run step's
// id also leaves a tombstone, independent of its result and of any budget: a
// <request id>.seen file, synced before the command starts, whose
// modification time is when it may be forgotten, reloaded by a restart.
//
// How long that is follows from who can send the id again. The worker sends a
// run step's command only from the activity attempt that first sent it, and
// only while it can prove the earlier sends never reached a machine
// (handlers.executeUntilDelivered); a retry never sends it, it reattaches.
// That attempt starts its last send within RunStepMachineWaitBudget of its
// first, and a send is over within the command's timeout plus
// RunStepSendOverhead. So no copy of the id can arrive later than command
// timeout + RunStepMachineWaitBudget + RunStepSendOverhead after the first;
// the tombstone lasts that long, and never less than runStepSeenFloor, a
// margin for clock steps and for whatever the contract becomes.

// runStepSeenFloor is the shortest a run step's tombstone lasts.
const runStepSeenFloor = 24 * time.Hour

// runStepSeenSweep is how often expired tombstones are swept; between sweeps
// an expired one still answers "already ran", which is only ever safer.
const runStepSeenSweep = time.Minute

// runStepSeenFor is how long a run step's tombstone lasts, for a command
// whose timeout is commandTimeout.
func runStepSeenFor(commandTimeout time.Duration) time.Duration {
	return max(runStepSeenFloor, commandTimeout+activitytypes.RunStepMachineWaitBudget+activitytypes.RunStepSendOverhead)
}

// markRunStep records that requestID is a run step's command, before it runs:
// its result is retained after delivery (markRetain), and its id is
// tombstoned for runStepSeenFor(commandTimeout), on disk first.
//
// A non-nil error means the tombstone could not be made durable: the command
// must not run (failClosedRunStep), since a restart would forget it ran. The
// tombstone is still held in memory, so this process never runs it either.
// An outbox with no store (an embedded runtime, no data directory) has no
// durability to lose and returns nil.
func (o *resultOutbox) markRunStep(requestID string, commandTimeout time.Duration) error {
	if requestID == "" {
		return nil
	}
	o.markRetain(requestID)
	expires := o.now().Add(runStepSeenFor(commandTimeout))
	err := o.store.markSeen(requestID, expires)
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.seen == nil {
		o.seen = make(map[string]time.Time)
	}
	o.seen[requestID] = expires
	return err
}

// failClosedRunStep is the result of a run step whose tombstone could not be
// made durable (markRunStep): the command was not run. It is the daemon's own
// outcome-unknown answer (ErrorCodeDaemonRestarted), so the worker pauses the
// step instead of recording a verdict or sending it again, and a resume
// dispatches it afresh under a new id.
func failClosedRunStep(requestID string, cause error) *toolexec.ExecutionResult {
	msg := fmt.Sprintf("Tool request %q was not run: this machine could not durably record that it started it "+
		"(%v), and without that record a daemon restart could run it a second time. Its outcome is unknown to "+
		"the run; nothing was executed. Check the machine's disk, then resume the run.", requestID, cause)
	return &toolexec.ExecutionResult{
		Success:      false,
		IsError:      true,
		Content:      msg,
		ErrorMessage: msg,
		ErrorCode:    toolexec.ErrorCodeDaemonRestarted,
	}
}

// expireSeenLocked forgets the tombstones past their expiry, at most once per
// runStepSeenSweep. Must hold o.mu.
func (o *resultOutbox) expireSeenLocked(now time.Time) {
	if len(o.seen) == 0 || now.Sub(o.seenSweptAt) < runStepSeenSweep {
		return
	}
	o.seenSweptAt = now
	for id, expires := range o.seen {
		if !now.Before(expires) {
			delete(o.seen, id)
			o.store.removeSeen(id)
		}
	}
}

// begin records a tool call that arrived to run, unless the outbox already
// knows requestID: then fresh is false, known is what became of it, and the
// call must not run again. An empty id is always fresh, and not recorded.
func (o *resultOutbox) begin(requestID string) (known toolexec.ToolRequestState, fresh bool) {
	if requestID == "" {
		return toolexec.ToolRequestUnknown, true
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.expireLocked(o.now())
	if known = o.stateLocked(requestID); known != toolexec.ToolRequestUnknown {
		return known, false
	}
	if o.running == nil {
		o.running = make(map[string]struct{})
	}
	o.running[requestID] = struct{}{}
	return toolexec.ToolRequestUnknown, true
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
