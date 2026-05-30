// Copyright (c) 2025 Reliant Labs

// Package daemonquery implements the gateway side of the pull-based daemon
// status RPC. When a daemon connects to a gateway, the gateway subscribes to
// `daemon.v1.query.<daemonID>.status`. The control-plane (or anyone) can
// publish a NATS request on that subject to ask "is daemon X connected, and
// when did we last see traffic from it?". NATS' subject-based routing means
// the request reaches whichever gateway currently holds the bidi stream — no
// service registry needed.
//
// Subscription = liveness. When the bidi stream tears down, the gateway
// unsubscribes; subsequent requests time out, which the caller correctly
// interprets as "no gateway has this daemon → disconnected." If the gateway
// process dies entirely, the subscriptions die with it, and NATS quickly
// stops routing to it — same correct behaviour.
//
// This is the "pull" path that replaces the push-based heartbeat-into-DB
// pipeline. The DB column for daemon runtime status becomes obsolete; status
// is answered from in-memory gateway state at read time.
package daemonquery

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/reliant-labs/reliant/internal/logging"
)

const logPrefix = "[daemonquery]"

// Subject constants, the Status payload, and ParseStatus live in subject.go —
// that file is the single source of truth for the wire contract.

// StatusSource is implemented by the gateway's connection registry
// (ToolsDaemonService). The responder calls it during request handling to
// read current in-memory state — the source of truth.
type StatusSource interface {
	// DaemonStatus returns the current connection state for a daemon. ok=false
	// means no connection is held by this gateway (e.g. teardown race between
	// the request arriving and the subscription tearing down).
	DaemonStatus(daemonID string) (lastActive time.Time, daemonType string, ok bool)
}

// Responder satisfies toolexec.DaemonConnectionListener and manages a NATS
// subscription per connected daemon. Safe for concurrent calls.
type Responder struct {
	nc     *nats.Conn
	source StatusSource

	mu   sync.Mutex
	subs map[string]*nats.Subscription
}

// NewResponder builds a Responder. The source must be set; the responder
// holds a reference to it for the lifetime of each subscription.
func NewResponder(nc *nats.Conn, source StatusSource) *Responder {
	return &Responder{
		nc:     nc,
		source: source,
		subs:   make(map[string]*nats.Subscription),
	}
}

// OnDaemonConnected subscribes to the daemon's status query subject. The
// handler reads from the StatusSource and replies with a JSON-encoded Status.
func (r *Responder) OnDaemonConnected(_, daemonID string) {
	if r == nil || r.nc == nil {
		return
	}
	subject := SubjectStatus(daemonID)
	sub, err := r.nc.Subscribe(subject, r.handle(daemonID))
	if err != nil {
		logging.Warn(logPrefix+" failed to subscribe to status query subject",
			"daemonID", daemonID, "subject", subject, "error", err)
		return
	}
	r.mu.Lock()
	// Defensive: if a prior sub exists (re-register race), tear it down.
	if old, ok := r.subs[daemonID]; ok && old != nil {
		_ = old.Unsubscribe()
	}
	r.subs[daemonID] = sub
	r.mu.Unlock()
}

// OnDaemonDisconnected unsubscribes from the daemon's status query subject.
// After this returns, status requests for the daemon will time out at the
// caller, which is the correct "no gateway has this daemon" signal.
func (r *Responder) OnDaemonDisconnected(_, daemonID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	sub := r.subs[daemonID]
	delete(r.subs, daemonID)
	r.mu.Unlock()
	if sub != nil {
		_ = sub.Unsubscribe()
	}
}

// CloseAll unsubscribes every active subscription. Call on shutdown.
func (r *Responder) CloseAll() {
	if r == nil {
		return
	}
	r.mu.Lock()
	subs := r.subs
	r.subs = make(map[string]*nats.Subscription)
	r.mu.Unlock()
	for _, s := range subs {
		_ = s.Unsubscribe()
	}
}

func (r *Responder) handle(daemonID string) nats.MsgHandler {
	return func(msg *nats.Msg) {
		lastActive, daemonType, ok := r.source.DaemonStatus(daemonID)
		// Race: the source may report ok=false if the connection tore down
		// between the message arriving and us handling it. Just don't reply
		// — the caller times out and treats as disconnected, which is true.
		if !ok {
			return
		}
		payload, err := json.Marshal(Status{
			Connected:    true,
			LastActiveMs: lastActive.UnixMilli(),
			DaemonType:   daemonType,
		})
		if err != nil {
			// Should be impossible for this fixed shape, but log if it happens.
			logging.Warn(logPrefix+" failed to marshal status",
				"daemonID", daemonID, "error", err)
			return
		}
		if msg.Reply == "" {
			return // not a request — nothing to reply to
		}
		if err := msg.Respond(payload); err != nil {
			logging.Warn(logPrefix+" failed to respond to status query",
				"daemonID", daemonID, "error", err)
		}
	}
}
