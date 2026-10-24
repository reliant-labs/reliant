// Copyright (c) 2025 Reliant Labs

package daemonstate

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/pkg/daemonwire"
)

const logPrefix = "[daemonstate]"

// ActivityRateLimit is the minimum interval between two activity publishes
// for the same daemon. An agent issues tool calls and a person types far
// faster than anything downstream needs to hear about — the control plane
// collapses activity into one Workspace CR write per 30s — so a burst
// becomes one event, carrying the first reason in its window.
const ActivityRateLimit = 1 * time.Second

// Publisher publishes daemon lifecycle events on the daemon.v1.state.>
// subject family using core NATS publish (fire-and-forget). Failures are
// logged at debug level and never block the caller — the derivation
// consumer's hand-rolled belt-and-suspenders writers still cover us until
// Step 4 of the proposal lands.
type Publisher struct {
	nc *nats.Conn

	mu             sync.Mutex
	lastActivityAt map[string]time.Time
}

// NewPublisher returns a publisher that writes to nc. A nil nc yields a
// no-op publisher so wiring code doesn't have to nil-check every call site.
func NewPublisher(nc *nats.Conn) *Publisher {
	return &Publisher{
		nc:             nc,
		lastActivityAt: make(map[string]time.Time),
	}
}

// Connected publishes a one-shot connected event. Not rate-limited — every
// connect must be visible to the consumer so derived stores can transition.
// build is the build the connecting daemon reported; the zero value (an
// unknown build) is left off the wire.
func (p *Publisher) Connected(daemonID, userID, daemonType string, build daemonwire.Build) {
	if p == nil || p.nc == nil || daemonID == "" {
		return
	}
	evt := daemonwire.Event{
		DaemonID:   daemonID,
		UserID:     userID,
		Type:       daemonwire.EventConnected,
		At:         time.Now().UTC(),
		DaemonType: daemonType,
	}
	if build != (daemonwire.Build{}) {
		evt.Build = &build
	}
	p.publish(evt)
}

// Disconnected publishes a one-shot disconnected event. Not rate-limited.
// The userID/daemonType are best-effort: the gateway knows them at teardown
// time but the consumer treats this as a pure transition signal — the
// daemon-row write is keyed on DaemonID alone.
func (p *Publisher) Disconnected(daemonID, userID, daemonType string) {
	if p == nil || p.nc == nil || daemonID == "" {
		return
	}
	// Drop any pending activity throttle bookkeeping so a future reconnect
	// of the same ID isn't subject to a stale rate-limit window.
	p.mu.Lock()
	delete(p.lastActivityAt, daemonID)
	p.mu.Unlock()
	p.publish(daemonwire.Event{
		DaemonID:   daemonID,
		UserID:     userID,
		Type:       daemonwire.EventDisconnected,
		At:         time.Now().UTC(),
		DaemonType: daemonType,
	})
}

// Activity publishes an activity event — a person or an agent did work on the
// machine, for the reason given (see daemonwire.Event.Reason) — rate-limited
// to at most one per daemon per ActivityRateLimit. Returns silently when
// throttled.
//
// What counts is decided by the caller, against internal/daemonactivity's
// policy: every event published here keeps a cloud machine awake.
func (p *Publisher) Activity(daemonID, userID, daemonType, reason string) {
	if p == nil || p.nc == nil || daemonID == "" {
		return
	}
	now := time.Now().UTC()
	p.mu.Lock()
	last := p.lastActivityAt[daemonID]
	if !last.IsZero() && now.Sub(last) < ActivityRateLimit {
		p.mu.Unlock()
		return
	}
	p.lastActivityAt[daemonID] = now
	p.mu.Unlock()
	p.publish(daemonwire.Event{
		DaemonID:   daemonID,
		UserID:     userID,
		Type:       daemonwire.EventActivity,
		At:         now,
		DaemonType: daemonType,
		Reason:     reason,
	})
}

// NOTE: the reachability lease (daemon_attachment) is never written from these
// events. The gateway holding the stream writes it directly, in stream order
// (ToolsDaemonService: upsert on connect, renewLease → RenewDaemonAttachment on
// every heartbeat its receive loop reads, releaseLease →
// ReleaseDaemonAttachment on teardown); renewal is not an event at all because
// the control plane's daemon.v1.state.* consumer strictly rejects unknown event
// types. See tools_daemon.go and Derivation.dispatch.

func (p *Publisher) publish(evt daemonwire.Event) {
	payload, err := json.Marshal(evt)
	if err != nil {
		// Impossible for the fixed Event shape, but log if it ever happens.
		logging.Debug(logPrefix+" marshal failed", "daemonID", evt.DaemonID, "type", evt.Type, "error", err)
		return
	}
	subj := daemonwire.Subject(evt.DaemonID, evt.Type)
	if err := p.nc.Publish(subj, payload); err != nil {
		logging.Debug(logPrefix+" publish failed", "subject", subj, "daemonID", evt.DaemonID, "type", evt.Type, "error", err)
	}
}
