// Copyright (c) 2025 Reliant Labs

// Package daemonstate is the wire contract + gateway-side publisher for the
// daemon liveness event stream. The control-plane runs a derivation consumer
// that subscribes to this stream and writes the derived stores (daemon
// attachment, daemons.status, workspace CR). See
// .dev/simplification-proposal.md, Step 3.
//
// One subject family, three event types, one payload — kept in this file so
// any drift between publisher and consumer is a compile-time mismatch.
package daemonstate

import "time"

// EventType is the discriminator carried both in the subject suffix and in
// the JSON payload. Keeping it in both lets the consumer route on the subject
// (fast) AND validate the payload (defensive).
type EventType string

const (
	EventConnected    EventType = "connected"
	EventDisconnected EventType = "disconnected"
	EventActivity     EventType = "activity"
)

// SubjectPrefix is the common prefix for every state event subject. The full
// subject is `<prefix><daemonID>.<eventType>`.
const SubjectPrefix = "daemon.v1.state."

// SubjectWildcard matches every state event. TWO tokens after the prefix so
// it matches exactly "<daemonID>.<type>" and avoids 3-token siblings like
// "daemon.v1.state.managed" used by the gateway's ManagedReconciler.
const SubjectWildcard = "daemon.v1.state.*.*"

// Subject returns the NATS subject for an event of `t` about `daemonID`.
// Both the publisher (this package) and the control-plane derivation consumer
// use this helper, so any drift is caught at the type level.
func Subject(daemonID string, t EventType) string {
	return SubjectPrefix + daemonID + "." + string(t)
}

// Event is the JSON payload published on every state subject.
//
// Field names match the wire contract documented in the simplification
// proposal — DO NOT rename without coordinating with the control-plane
// derivation consumer.
type Event struct {
	DaemonID   string    `json:"daemon_id"`
	UserID     string    `json:"user_id"`
	Type       EventType `json:"type"`
	At         time.Time `json:"at"`
	DaemonType string    `json:"daemon_type,omitempty"`
}
