// Copyright (c) 2025 Reliant Labs

// Package daemonevents publishes daemon connect/disconnect lifecycle events
// to NATS JetStream so that downstream consumers (e.g. the control-plane
// admin-server) can mirror the connection state into their own
// `controlplane.daemons` table.
//
// The events are intentionally low-volume (one per connect/disconnect) and
// use a long-retention JetStream stream so a temporarily-down consumer can
// catch up on missed events.
package daemonevents

import (
	"context"
	"encoding/json"
	"fmt"
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

	// CurrentEventVersion is the current schema version for daemon events.
	CurrentEventVersion = 1

	logPrefix = "[daemonevents]"
)

// EventType identifies the kind of daemon event.
type EventType string

const (
	EventTypeConnected    EventType = "connected"
	EventTypeDisconnected EventType = "disconnected"
)

// Event is the wire format for a daemon lifecycle event.
//
// Versioned so consumers can detect and reject unsupported newer payloads
// without crashing.
type Event struct {
	Version   int       `json:"version"`
	Type      EventType `json:"type"`
	UserID    string    `json:"userId"`
	DaemonID  string    `json:"daemonId"`
	Timestamp time.Time `json:"timestamp"`
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

// OnDaemonConnected satisfies toolexec.DaemonConnectionListener.
func (p *Publisher) OnDaemonConnected(userID, daemonID string) {
	p.publish(EventTypeConnected, SubjectConnected, userID, daemonID)
}

// OnDaemonDisconnected satisfies toolexec.DaemonConnectionListener.
func (p *Publisher) OnDaemonDisconnected(userID, daemonID string) {
	p.publish(EventTypeDisconnected, SubjectDisconnected, userID, daemonID)
}

func (p *Publisher) publish(t EventType, subject, userID, daemonID string) {
	if p == nil || p.js == nil {
		return
	}
	evt := Event{
		Version:   CurrentEventVersion,
		Type:      t,
		UserID:    userID,
		DaemonID:  daemonID,
		Timestamp: time.Now().UTC(),
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
