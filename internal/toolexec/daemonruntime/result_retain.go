// Copyright (c) 2026 Reliant Labs
package daemonruntime

import (
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
)

// Retained results: a run step's result, kept a while after a gateway took it.
//
// The outbox (gateway_session.go) forgets a result once a gateway acknowledges
// it, because the gateway has handed it to the worker waiting on its result
// subject. A worker that went away mid-command — every
// reliant-temporal-worker rollout releases its in-flight activities — was not
// waiting any more: the gateway publishes the result to nobody, acknowledges
// it, and the retry of the run step that comes up on another worker finds the
// command "delivered" and its result gone. It cannot reattach, so it refuses
// to run the command again and the run pauses for the user.
//
// So a run step's result (the request's run_step context flag) is retained
// for resultRetainTTL after its acknowledgement, and tool.status resends it
// for a "delivered" request, saying so (ToolStatusResponse.Resent). Only run
// steps: they are the calls a retry reattaches to (handlers.ExecuteRunStep),
// and a model's tool calls are far more numerous. Memory only: a daemon that
// restarts loses them, and the retry then refuses as it always did.

// The retained-result bounds. The window covers a retry that comes up after
// the previous worker's heartbeat timeout (60s) and the step's retry backoff
// (at most a minute), with room for a machine that is reconnecting meanwhile;
// a retry later than that refuses. The budget is a handful of run steps at
// tools.MaxRunStepOutputSize.
const (
	resultRetainTTL    = 10 * time.Minute
	resultRetainBudget = 16 << 20
)

// markRetain makes requestID's result retained after its acknowledgement.
// Called before the call can finish, so hold sees the mark.
func (o *resultOutbox) markRetain(requestID string) {
	if requestID == "" {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.retainOnAck == nil {
		o.retainOnAck = make(map[string]struct{})
	}
	o.retainOnAck[requestID] = struct{}{}
}

// retainLocked keeps h, just acknowledged at now. Must hold o.mu.
func (o *resultOutbox) retainLocked(h *heldResult, now time.Time) {
	if o.retained == nil {
		o.retained = make(map[string]*heldResult)
	}
	if prev, ok := o.retained[h.requestID]; ok {
		o.retainedBytes -= prev.size
	}
	kept := *h
	kept.heldAt = now
	o.retained[h.requestID] = &kept
	o.retainedBytes += kept.size
}

// expireRetainedLocked drops retained results past resultRetainTTL, then the
// oldest while over resultRetainBudget. Must hold o.mu.
func (o *resultOutbox) expireRetainedLocked(now time.Time) {
	for id, h := range o.retained {
		if now.Sub(h.heldAt) > resultRetainTTL {
			delete(o.retained, id)
			o.retainedBytes -= h.size
		}
	}
	for o.retainedBytes > resultRetainBudget && len(o.retained) > 0 {
		var oldest *heldResult
		for _, h := range o.retained {
			if oldest == nil || h.heldAt.Before(oldest.heldAt) {
				oldest = h
			}
		}
		delete(o.retained, oldest.requestID)
		o.retainedBytes -= oldest.size
	}
}

// retainedResult returns requestID's retained result, if it still has one.
func (o *resultOutbox) retainedResult(requestID string) *heldResult {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.expireLocked(time.Now())
	return o.retained[requestID]
}

// resendRetainedResult sends requestID's retained result on s, reporting
// whether it did. The gateway that takes it has no waiter for it and
// publishes it to the request's result subject, where a reattaching retry
// listens; its acknowledgement finds nothing held and is a no-op.
func (d *daemonClient) resendRetainedResult(s *gatewaySession, requestID string) bool {
	h := d.results.retainedResult(requestID)
	if h == nil || s.send(h.msg) != nil {
		return false
	}
	logging.Info(logPrefix+" Resent a retained run-step result", "request_id", requestID,
		"retained_for", time.Since(h.heldAt).Round(time.Millisecond))
	return true
}
