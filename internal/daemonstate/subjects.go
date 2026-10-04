// Copyright (c) 2025 Reliant Labs

// Package daemonstate is the wire contract + gateway-side publisher for the
// daemon liveness event stream. The control-plane runs a derivation consumer
// that subscribes to this stream and writes the derived stores (daemon
// attachment, daemons.status, workspace CR). See
// .dev/simplification-proposal.md, Step 3.
//
// One subject family, four event types, one payload — kept in this file so
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

	// EventLifecycle carries managed-machine lifecycle state INTO this
	// registry from the control plane, which is the only thing that watches
	// the Workspace CR. The three event types above travel the other way:
	// the gateway publishes them and control-plane mirrors them. This one
	// reverses the direction on the same subject family, because the key is
	// already the daemon UUID on both sides.
	//
	// It exists so the registry can answer "is this machine provisioning,
	// cloning, suspended or failed" — the whole vocabulary that used to be
	// reachable only through controlplane.v1.DaemonService. Without it the
	// registry can only say attached/not-attached, and a machine mid-provision
	// is indistinguishable from one that crashed.
	EventLifecycle EventType = "lifecycle"
)

// LifecyclePhase is the wire vocabulary for EventLifecycle's Phase field. It
// mirrors control-plane's public DaemonLifecyclePhase enum (NOT the raw
// Kubernetes phase strings, which stay an implementation detail of the
// operator) so renaming a k8s phase upstream is not a wire break here.
type LifecyclePhase string

const (
	LifecyclePhaseProvisioning LifecyclePhase = "provisioning"
	LifecyclePhaseCloning      LifecyclePhase = "cloning"
	LifecyclePhaseReady        LifecyclePhase = "ready"
	LifecyclePhaseSuspending   LifecyclePhase = "suspending"
	LifecyclePhaseSuspended    LifecyclePhase = "suspended"
	LifecyclePhaseFailed       LifecyclePhase = "failed"
)

// ValidLifecyclePhase reports whether p is a phase this consumer understands.
// An unrecognized phase is dropped rather than stored: the registry falls back
// to attachment-derived status, which is always correct if less specific, and
// storing a phase no reader can interpret would be worse than storing none.
func ValidLifecyclePhase(p LifecyclePhase) bool {
	switch p {
	case LifecyclePhaseProvisioning, LifecyclePhaseCloning, LifecyclePhaseReady,
		LifecyclePhaseSuspending, LifecyclePhaseSuspended, LifecyclePhaseFailed:
		return true
	default:
		return false
	}
}

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

	// The fields below are populated only on EventLifecycle. They are on the
	// one Event struct rather than a second payload type because the subject
	// family, the key and the "newest wins" semantics are identical — a
	// separate type would duplicate all three to express one extra branch.

	// Phase is the machine's current lifecycle phase. Required on
	// EventLifecycle; empty on every other type.
	Phase LifecyclePhase `json:"phase,omitempty"`
	// Size is the provisioned machine size ("small", "medium", …). Empty for
	// self-hosted daemons, which have no size.
	Size string `json:"size,omitempty"`
	// StatusMessage is the human-readable reason for the most recent
	// transition, e.g. "image pull failed". Empty when there is nothing to
	// explain.
	StatusMessage string `json:"status_message,omitempty"`
	// LastOOMKilledAt and OOMKillCount mirror the Workspace CR's OOM
	// accounting. Zero/nil when no OOM kill has been observed.
	LastOOMKilledAt *time.Time `json:"last_oom_killed_at,omitempty"`
	OOMKillCount    int32      `json:"oom_kill_count,omitempty"`
}
