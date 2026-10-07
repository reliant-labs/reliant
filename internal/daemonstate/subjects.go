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

	// EventRemoved tells this registry a daemon no longer exists: the control
	// plane deleted it (owner delete, admin delete, reaper, account deletion).
	// Same direction as EventLifecycle. The consumer deletes the registry row.
	//
	// Without it nothing ever removed a row. The control plane soft-deleted
	// its own record and this registry, the one list the UI reads, kept
	// offering the machine forever: listed as "Starting", picked as a clone
	// target, and answered with not_found by every control-plane call made
	// against it (MACHINE_LIST_BUGS_2026-10-07).
	//
	// It is the FAST path only. Plain NATS loses events, so the guarantee is
	// the control plane's periodic registry snapshot (SubjectRegistrySnapshot),
	// which removes anything this event missed.
	EventRemoved EventType = "removed"
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

	// Name is the name the owner gave the machine ("default", "gpu-box").
	// Carried on a lifecycle event that also carries UserID — together they
	// are the machine's IDENTITY, and an identity-bearing lifecycle event may
	// CREATE the registry row rather than only update one. That is what puts a
	// machine in the list the moment it is created, instead of minutes later
	// when its pod first reaches the gateway.
	//
	// A lifecycle event with no UserID stays update-only: the control plane's
	// workspace-event path does not know the owner, and a row created without
	// one would belong to nobody.
	Name string `json:"name,omitempty"`
	// CreatedAt is when the control plane created the machine. Optional; used
	// only when this event creates the row, so the list's "Created" column is
	// the machine's age rather than the moment the mirror heard of it.
	CreatedAt *time.Time `json:"created_at,omitempty"`
}

// HasIdentity reports whether a lifecycle event carries enough to create a
// registry row: the owner and the daemon's type. Name is not required — a
// self-hosted daemon that never registered one still exists.
func (e Event) HasIdentity() bool {
	return e.UserID != "" && e.DaemonType != ""
}

// SubjectRegistrySnapshot carries the control plane's authoritative set of one
// owner's daemons. It is the reconcile between the control plane's daemon set
// and this registry, and it is the GUARANTEE behind the per-daemon events:
// EventLifecycle and EventRemoved are plain NATS and get lost, and before the
// snapshot a lost removal meant a ghost machine forever.
//
// One message per owner, so a message stays small however many machines the
// fleet has. The control plane publishes a snapshot for every owner who has
// EVER had a daemon — including owners whose daemons are all deleted, whose
// snapshot is empty and is exactly what removes their ghosts.
//
// A fixed subject (the owner is in the payload) because owner ids come from an
// identity provider and are not guaranteed to be valid subject tokens. A
// separate family from daemon.v1.state.*.* because it is not about one daemon,
// and because the control plane's own consumer of that family must not see it.
const SubjectRegistrySnapshot = "daemon.v1.registry.snapshot"

// RegistrySnapshot is the payload on SubjectRegistrySnapshot. Field names match
// the control-plane publisher exactly.
type RegistrySnapshot struct {
	// UserID is the owner, in this registry's user-id vocabulary (the
	// external id — the same value as daemons.user_id).
	UserID string `json:"user_id"`
	// At is when the control plane read the set. Rows this registry created
	// shortly before it are left alone even when absent from the set — see
	// db.RegistrySnapshotApply.
	At time.Time `json:"at"`
	// Daemons is every daemon the owner has right now. Empty is meaningful:
	// the owner has none, and every row this registry holds for them goes.
	Daemons []RegistryDaemon `json:"daemons"`
}

// RegistryDaemon is one daemon in a RegistrySnapshot.
type RegistryDaemon struct {
	DaemonID   string    `json:"daemon_id"`
	Name       string    `json:"name,omitempty"`
	DaemonType string    `json:"daemon_type"`
	CreatedAt  time.Time `json:"created_at"`

	// Lifecycle, for managed daemons only. Applied through the same
	// newest-wins guard as EventLifecycle, keyed on PhaseChangedAt, so a
	// snapshot can fill in a row it just created without ever overwriting a
	// newer transition the per-daemon events already delivered.
	Phase           LifecyclePhase `json:"phase,omitempty"`
	PhaseChangedAt  *time.Time     `json:"phase_changed_at,omitempty"`
	Size            string         `json:"size,omitempty"`
	StatusMessage   string         `json:"status_message,omitempty"`
	LastOOMKilledAt *time.Time     `json:"last_oom_killed_at,omitempty"`
	OOMKillCount    int32          `json:"oom_kill_count,omitempty"`
}
