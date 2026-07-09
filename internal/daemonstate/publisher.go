// Copyright (c) 2025 Reliant Labs

package daemonstate

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/reliant-labs/reliant/internal/logging"
)

const logPrefix = "[daemonstate]"

// ActivityRateLimit is the minimum interval between two activity publishes
// for the same daemon. The receive loop bumps lastActivity on every inbound
// message — without a rate limit, a chatty daemon would flood the stream at
// hundreds of events per second, which is pure overhead since the derivation
// consumer collapses anything finer than 1s into a single attachment write.
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
func (p *Publisher) Connected(daemonID, userID, daemonType string) {
	if p == nil || p.nc == nil || daemonID == "" {
		return
	}
	p.publish(Event{
		DaemonID:   daemonID,
		UserID:     userID,
		Type:       EventConnected,
		At:         time.Now().UTC(),
		DaemonType: daemonType,
	})
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
	p.publish(Event{
		DaemonID:   daemonID,
		UserID:     userID,
		Type:       EventDisconnected,
		At:         time.Now().UTC(),
		DaemonType: daemonType,
	})
}

// Activity publishes an activity event, rate-limited to at most one per
// daemon per ActivityRateLimit. Returns silently when throttled.
func (p *Publisher) Activity(daemonID, userID, daemonType string) {
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
	p.publish(Event{
		DaemonID:   daemonID,
		UserID:     userID,
		Type:       EventActivity,
		At:         now,
		DaemonType: daemonType,
	})
}

// NOTE: reachability-lease renewal on the daemon keepalive is intentionally NOT
// a daemonstate event. The gateway writes daemon_attachment.last_stream_activity
// directly (ToolsDaemonService heartbeat case → TouchDaemonAttachmentIfNewer)
// because the authoritative daemon.v1.state.* consumer lives in the control-plane
// repo and strictly rejects unknown event types. See tools_daemon.go for detail.

func (p *Publisher) publish(evt Event) {
	payload, err := json.Marshal(evt)
	if err != nil {
		// Impossible for the fixed Event shape, but log if it ever happens.
		logging.Debug(logPrefix+" marshal failed", "daemonID", evt.DaemonID, "type", evt.Type, "error", err)
		return
	}
	subj := Subject(evt.DaemonID, evt.Type)
	if err := p.nc.Publish(subj, payload); err != nil {
		logging.Debug(logPrefix+" publish failed", "subject", subj, "daemonID", evt.DaemonID, "type", evt.Type, "error", err)
	}
}
