// Copyright (c) 2025 Reliant Labs

// Package daemonevents publishes daemon connect/disconnect lifecycle events
// to NATS JetStream so that downstream consumers (e.g. the control-plane
// admin-server) can mirror the connection state into their own
// `controlplane.daemons` table.
//
// The events are intentionally low-volume (one per connect/disconnect) and
// use a long-retention JetStream stream so a temporarily-down consumer can
// catch up on missed events.
//
// forge:exclude-contract
//
// Leaf utility package: the exported surface is concrete helpers over the
// stdlib or the OS, with no collaborator to fake and no second implementation.
// An interface here would have exactly one implementor and one caller shape,
// which is indirection without a seam.
package daemonevents

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/reliant-labs/reliant/internal/logging"
)

const (
	// StreamDaemonEvents is the JetStream stream that carries
	// daemon-gateway → admin-server lifecycle events.
	StreamDaemonEvents = "DAEMON_EVENTS"

	// SubjectDaemonEventsAll matches every daemon event subject.
	SubjectDaemonEventsAll = "daemon.v1.events.*"

	// SubjectConnected is published when a daemon establishes a stream.
	SubjectConnected = "daemon.v1.events.connected"

	// SubjectDisconnected is published when a daemon's stream tears down.
	SubjectDisconnected = "daemon.v1.events.disconnected"

	// SubjectConnectFailed is published when an outbound connect attempt
	// from the gateway to a daemon pod fails before reaching the registered
	// state (NetworkPolicy drop, dial timeout, registration timeout, etc).
	SubjectConnectFailed = "daemon.v1.events.connect_failed"

	// CurrentEventVersion is the current schema version for daemon events.
	CurrentEventVersion = 1

	logPrefix = "[daemonevents]"
)

// EventType identifies the kind of daemon event.
type EventType string

const (
	EventTypeConnected     EventType = "connected"
	EventTypeDisconnected  EventType = "disconnected"
	EventTypeConnectFailed EventType = "connect_failed"
)

// Event is the wire format for a daemon lifecycle event.
//
// Versioned so consumers can detect and reject unsupported newer payloads
// without crashing. Schema must match the consumer's DaemonEvent struct in
// `control-plane/internal/natsio/messages.go`.
//
// Reason is set on disconnected / connect_failed events and surfaced to the
// frontend on the daemon record so users see e.g. "Couldn't connect — dial
// tcp: i/o timeout" instead of a generic "Disconnected" badge.
//
// Name/Hostname/Platform are set on connected events only, carrying the
// identity the daemon asserted at registration (see
// ToolsDaemonService.notifyConnected) so a control-plane consumer upserting
// an external daemon has something better than the daemon id to show as its
// name. All three are additive optional fields (added without bumping
// Version): an older consumer's encoding/json.Unmarshal silently drops
// unknown fields, so it keeps working unchanged, and a consumer built
// against this version treats missing fields as "" — the same as an old
// publisher — so the two schemas can evolve independently as long as new
// fields stay optional. A bump would only be warranted for a change a
// pre-upgrade consumer could misinterpret (e.g. a field whose meaning
// changes, or a field going from optional to load-bearing).
type Event struct {
	Version   int       `json:"version"`
	Type      EventType `json:"type"`
	UserID    string    `json:"userId"`
	DaemonID  string    `json:"daemonId"`
	Timestamp time.Time `json:"timestamp"`
	Reason    string    `json:"reason,omitempty"`
	Name      string    `json:"name,omitempty"`
	Hostname  string    `json:"hostname,omitempty"`
	Platform  string    `json:"platform,omitempty"`
}

// Publisher implements toolexec.DaemonConnectionListener and writes events
// to JetStream. Publishing is best-effort: failures are logged but never
// propagated to the gRPC stream handler so a NATS outage never blocks
// daemon registration.
type Publisher struct {
	js jetstream.JetStream
}

// EnsureStream creates or updates the DAEMON_EVENTS JetStream stream.
// Uses file storage with 7-day retention so a downed consumer can catch up.
func EnsureStream(ctx context.Context, js jetstream.JetStream) error {
	_, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      StreamDaemonEvents,
		Subjects:  []string{SubjectDaemonEventsAll},
		Retention: jetstream.LimitsPolicy,
		Storage:   jetstream.FileStorage,
		MaxAge:    7 * 24 * time.Hour,
		MaxMsgs:   1000000,
	})
	if err != nil {
		return fmt.Errorf("create/update %s: %w", StreamDaemonEvents, err)
	}
	return nil
}

// NewPublisher constructs a Publisher from an existing NATS connection.
// The stream is NOT auto-created here — call EnsureStream during startup.
func NewPublisher(nc *nats.Conn) (*Publisher, error) {
	js, err := jetstream.New(nc)
	if err != nil {
		return nil, fmt.Errorf("creating jetstream context: %w", err)
	}
	return &Publisher{js: js}, nil
}

// OnDaemonConnected satisfies toolexec.DaemonConnectionListener. Callers that
// have the daemon's registered identity available should call
// OnDaemonConnectedWithInfo instead so the control plane gets a usable name.
func (p *Publisher) OnDaemonConnected(userID, daemonID string) {
	p.publishConnected(userID, daemonID, "", "", "")
}

// OnDaemonConnectedWithInfo satisfies toolexec.DaemonConnectionInfoListener.
// name/hostname/platform are whatever the daemon asserted in its
// DaemonRegister message (internal/grpc/services/tools_daemon.go); any of
// them may be empty. ToolsDaemonService.notifyConnected calls this instead
// of OnDaemonConnected when the listener implements the interface, so this
// is the path actually used in production; OnDaemonConnected above only
// exists to satisfy the base interface and covers callers/tests with no
// identity to give.
func (p *Publisher) OnDaemonConnectedWithInfo(userID, daemonID, name, hostname, platform string) {
	p.publishConnected(userID, daemonID, name, hostname, platform)
}

// OnDaemonDisconnected satisfies toolexec.DaemonConnectionListener.
func (p *Publisher) OnDaemonDisconnected(userID, daemonID string) {
	p.publish(EventTypeDisconnected, SubjectDisconnected, userID, daemonID, "", "", "", "")
}

// OnDaemonDisconnectedWithReason is like OnDaemonDisconnected but carries a
// human-readable reason that the control plane persists on the daemon row.
func (p *Publisher) OnDaemonDisconnectedWithReason(userID, daemonID, reason string) {
	p.publish(EventTypeDisconnected, SubjectDisconnected, userID, daemonID, reason, "", "", "")
}

// OnDaemonConnectFailed is called by the connector loop whenever an outbound
// dial / registration attempt fails. The reason is the wrapped error returned
// by connectOnce.
func (p *Publisher) OnDaemonConnectFailed(userID, daemonID, reason string) {
	p.publish(EventTypeConnectFailed, SubjectConnectFailed, userID, daemonID, reason, "", "", "")
}

// publishConnected resolves the display name and publishes a connected
// event. This is the ONE place that decides name precedence — name, then
// hostname, then the daemon id itself — so Event.Name is never empty on the
// wire and every consumer, present or future, can use it as-is without
// re-deriving the rule. Hostname/Platform still travel unresolved alongside
// it for consumers (like the control plane) that want the raw value too.
func (p *Publisher) publishConnected(userID, daemonID, name, hostname, platform string) {
	resolvedName := strings.TrimSpace(name)
	if resolvedName == "" {
		resolvedName = strings.TrimSpace(hostname)
	}
	if resolvedName == "" {
		resolvedName = daemonID
	}
	p.publish(EventTypeConnected, SubjectConnected, userID, daemonID, "", resolvedName, hostname, platform)
}

func (p *Publisher) publish(t EventType, subject, userID, daemonID, reason, name, hostname, platform string) {
	if p == nil || p.js == nil {
		return
	}
	evt := Event{
		Version:   CurrentEventVersion,
		Type:      t,
		UserID:    userID,
		DaemonID:  daemonID,
		Timestamp: time.Now().UTC(),
		Reason:    reason,
		Name:      name,
		Hostname:  hostname,
		Platform:  platform,
	}
	data, err := json.Marshal(evt)
	if err != nil {
		logging.Warn(logPrefix+" failed to marshal event",
			"type", t, "daemonID", daemonID, "error", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := p.js.Publish(ctx, subject, data); err != nil {
		logging.Warn(logPrefix+" failed to publish event",
			"subject", subject, "daemonID", daemonID, "error", err)
		return
	}
	logging.Debug(logPrefix+" published event",
		"subject", subject, "daemonID", daemonID, "userID", userID)
}
