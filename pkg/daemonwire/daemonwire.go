// Copyright (c) 2025 Reliant Labs

// Package daemonwire is the NATS wire contract for daemon state shared by
// Reliant and the managed control plane, as a public, cross-module surface.
//
// The contract has two parts. The per-daemon state stream,
// daemon.v1.state.<daemonID>.<type>, carries one Event per message, and it
// flows in both directions:
//
//   - Reliant's daemon gateway publishes EventConnected and EventDisconnected
//     as a daemon's stream attaches and detaches, and EventActivity when a
//     person or an agent does work on the machine. The control plane
//     consumes them into its liveness cache, workspace idle clock,
//     daemon status and billing; Reliant's own registry consumes them too,
//     into the attachment leases every gateway replica reads.
//   - The control plane publishes EventLifecycle and EventRemoved, because it
//     is the authority for managed-machine lifecycle and for which daemons
//     exist. Reliant consumes them into its daemon registry, the one list the
//     UI reads. They also reach the control plane's own subscription on the
//     shared family, which ignores them.
//
// The registry snapshot, on SubjectRegistrySnapshot, is the second part: the
// control plane's periodic, per-owner reconcile of that registry, and the
// guarantee behind the per-daemon events, which ride plain NATS and get lost.
//
// This package is the only definition of the subjects, the event types and
// the payload shapes. Both publishers and both consumers import it, so the two
// sides cannot drift apart: a renamed constant is a compile error on both, and
// a renamed field or JSON tag fails the golden wire test beside this file.
// Before it existed, each service carried a hand-copied twin of this file, and
// keeping them in step was a comment asking whoever edited one to edit the
// other.
//
// The surface is deliberately data only: subject names, payload types and the
// parse/validate step every consumer needs. Publishing, subscribing, delivery
// and storage are each service's own business, which is why there is no NATS
// client here and the package imports only the standard library. Every
// exported name is wire surface, so changing one is a coordinated change to
// both services.
package daemonwire

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// EventType is the discriminator carried both in the subject suffix and in
// the JSON payload. Carrying it in both lets a consumer route on the subject
// and still validate the payload.
type EventType string

const (
	// EventConnected and EventDisconnected are liveness: the gateway
	// publishes them when a daemon's stream attaches and detaches, and the
	// control plane consumes them.
	//
	// EventActivity means a person or an agent did something to the machine
	// — an agent's tool call, terminal input, a command someone asked for —
	// and Event.Reason says which. It is NOT "the stream carried traffic":
	// the control plane republishes it onto the Workspace CR as the activity
	// that keeps a cloud machine from idle-suspending, so a daemon's own
	// replies, output and housekeeping must never produce one. The gateway's
	// policy lives in internal/daemonactivity. Rate-limited at the publisher.
	EventConnected    EventType = "connected"
	EventDisconnected EventType = "disconnected"
	EventActivity     EventType = "activity"

	// EventLifecycle carries managed-machine lifecycle state from the control
	// plane to Reliant's daemon registry. It travels the opposite way from the
	// liveness events on the same subject family, which is sound because both
	// services key a daemon by the same UUID.
	//
	// The control plane stays the authority for lifecycle: the Workspace CR
	// is, and only the control plane's operator watches it. The registry holds
	// a read replica so it can answer "is this machine provisioning, cloning,
	// suspended or failed" without a cross-service read on the hot path.
	// Without it the registry could say only attached or not attached, and a
	// machine mid-provision would be indistinguishable from one that crashed.
	EventLifecycle EventType = "lifecycle"

	// EventRemoved tells Reliant's registry a daemon no longer exists: the
	// control plane deleted it (owner delete, admin delete, a reaper, account
	// deletion). Same direction as EventLifecycle; the consumer deletes the
	// registry row.
	//
	// Without it nothing ever removed a row. The control plane deleted its own
	// record and the registry kept offering the machine forever: listed as
	// starting, picked as a clone target, and answered with not_found by every
	// control-plane call made against it.
	//
	// It is the FAST path only. Plain NATS loses events, so the guarantee is
	// the registry snapshot (SubjectRegistrySnapshot), which removes anything
	// a lost removal left behind.
	EventRemoved EventType = "removed"
)

// LifecyclePhase is the wire vocabulary for a managed machine's lifecycle,
// carried on Event.Phase and RegistryDaemon.Phase.
//
// It is the PUBLIC lifecycle vocabulary, mirroring the control plane's
// DaemonLifecyclePhase API enum, NOT the Workspace CR's raw Kubernetes phase
// strings. Those are an implementation detail of how managed machines happen
// to run today, so renaming one must not be a wire break. A publisher maps its
// own vocabulary onto these values and does not publish a phase it cannot map.
type LifecyclePhase string

// LifecyclePhase values.
const (
	LifecyclePhaseProvisioning LifecyclePhase = "provisioning"
	LifecyclePhaseCloning      LifecyclePhase = "cloning"
	LifecyclePhaseReady        LifecyclePhase = "ready"
	LifecyclePhaseSuspending   LifecyclePhase = "suspending"
	LifecyclePhaseSuspended    LifecyclePhase = "suspended"
	LifecyclePhaseFailed       LifecyclePhase = "failed"
)

// ValidLifecyclePhase reports whether p is a phase this contract defines.
//
// A consumer drops an unrecognized phase rather than storing it. The registry
// then falls back to attachment-derived status, which is always correct if
// less specific, and storing a phase no reader can interpret would be worse
// than storing none.
func ValidLifecyclePhase(p LifecyclePhase) bool {
	switch p {
	case LifecyclePhaseProvisioning, LifecyclePhaseCloning, LifecyclePhaseReady,
		LifecyclePhaseSuspending, LifecyclePhaseSuspended, LifecyclePhaseFailed:
		return true
	default:
		return false
	}
}

// SubjectPrefix is the common prefix of every state event subject. The full
// subject is SubjectPrefix + "<daemonID>.<type>"; build it with Subject.
const SubjectPrefix = "daemon.v1.state."

// SubjectWildcard matches every state event. It has exactly TWO tokens after
// the prefix so it matches "<daemonID>.<type>" and nothing else under the same
// prefix: the gateway's managed-daemon reconciler publishes on the one-token
// sibling "daemon.v1.state.managed", a different protocol that no state
// consumer may receive.
const SubjectWildcard = SubjectPrefix + "*.*"

// Subject returns the NATS subject for an event of type t about daemonID.
// Every publisher builds its subjects here, so a subject always matches
// SubjectWildcard.
func Subject(daemonID string, t EventType) string {
	return SubjectPrefix + daemonID + "." + string(t)
}

// Event is the JSON payload published on every state subject.
type Event struct {
	DaemonID   string    `json:"daemon_id"`
	UserID     string    `json:"user_id"`
	Type       EventType `json:"type"`
	At         time.Time `json:"at"`
	DaemonType string    `json:"daemon_type,omitempty"`

	// Reason is why an EventActivity counts as activity, in words an
	// operator can read: "tool call shell", "terminal input",
	// "command fs.write_file". It travels to the Workspace CR so "why did
	// this machine not go to sleep?" is answered by the CR and one log line.
	// Empty on every other type.
	Reason string `json:"reason,omitempty"`

	// Build is the build the daemon reported when its stream registered. It
	// rides on EventConnected only; nil on every other type, and nil on a
	// connect from a daemon that predates the report. A consumer treats nil as
	// "unknown build", never as an error, and must not read an absent Build as
	// "unchanged" on a later connect: each connect carries the build of the
	// stream that just attached.
	Build *Build `json:"build,omitempty"`

	// The fields below are populated only on EventLifecycle. They are on the
	// one Event struct rather than a second payload type because the subject
	// family, the key and the "newest wins" semantics are identical: a
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
	// accounting. Zero/nil when no OOM kill has been observed. Omitting them
	// means "unchanged", not "cleared": the consumer coalesces rather than
	// overwrites, so a plain phase transition does not erase OOM history.
	LastOOMKilledAt *time.Time `json:"last_oom_killed_at,omitempty"`
	OOMKillCount    int32      `json:"oom_kill_count,omitempty"`

	// Name is the name the owner gave the machine ("default", "gpu-box").
	// Carried on a lifecycle event together with UserID and DaemonType, which
	// are the machine's IDENTITY (see HasIdentity).
	Name string `json:"name,omitempty"`
	// CreatedAt is when the control plane created the machine. Optional; used
	// only when this event creates the registry row, so the list's "Created"
	// column is the machine's age rather than the moment the registry heard
	// of it.
	CreatedAt *time.Time `json:"created_at,omitempty"`
}

// Build identifies the binary a daemon runs, as DaemonRegister reported it.
// Every field is empty/zero for an unstamped build. CommitTimeUnix is the key
// that orders builds (0 = unordered); Version is display only, since semver
// cannot order two builds between the same pair of tags.
type Build struct {
	Version        string `json:"version,omitempty"`
	Commit         string `json:"commit,omitempty"`
	CommitTimeUnix int64  `json:"commit_time_unix,omitempty"`
	ForgeVersion   string `json:"forge_version,omitempty"`
}

// Known reports whether the daemon identified its build at all.
func (b Build) Known() bool {
	return b.Version != "" || b.Commit != ""
}

// HasIdentity reports whether a lifecycle event carries enough to CREATE a
// registry row rather than only update one: the owner and the daemon's type.
// Name is not required, because a self-hosted daemon that never registered
// one still exists.
//
// An identity-bearing lifecycle event is what lists a machine the moment it is
// created, in its provisioning state and under its own name, instead of
// minutes later when its pod first reaches the gateway. A lifecycle event
// without one stays update-only: some publish paths do not know the owner, and
// a row created without one would belong to nobody.
func (e Event) HasIdentity() bool {
	return e.UserID != "" && e.DaemonType != ""
}

// UnmarshalEvent parses a state event payload and validates the fields every
// event must carry: a daemon_id, a type this contract defines, and a non-zero
// at. Every error is permanent, so a consumer should log and drop the message
// rather than retry it.
func UnmarshalEvent(data []byte) (Event, error) {
	var e Event
	if err := json.Unmarshal(data, &e); err != nil {
		return Event{}, fmt.Errorf("unmarshal daemon state event: %w", err)
	}
	if e.DaemonID == "" {
		return Event{}, errors.New("daemon state event missing daemon_id")
	}
	switch e.Type {
	case EventConnected, EventDisconnected, EventActivity, EventLifecycle, EventRemoved:
	default:
		return Event{}, fmt.Errorf("daemon state event unknown type %q", e.Type)
	}
	if e.At.IsZero() {
		return Event{}, errors.New("daemon state event missing at")
	}
	return e, nil
}

// SubjectRegistrySnapshot carries the control plane's authoritative set of one
// owner's daemons to Reliant's registry. It is the reconcile between the two,
// and the GUARANTEE behind EventLifecycle and EventRemoved: those ride plain
// NATS and get lost, and before the snapshot a lost removal meant a ghost
// machine forever. The registry creates what it lacks and removes what the set
// no longer has.
//
// One message per owner, so a message stays small however many machines the
// fleet has. The control plane publishes one for every owner who has EVER had
// a daemon, including owners whose daemons are all deleted: their snapshot is
// empty, and it is exactly what removes their ghosts.
//
// A fixed subject, with the owner in the payload, because owner ids come from
// an identity provider and are not guaranteed to be valid subject tokens. A
// separate family from SubjectWildcard because it is not about one daemon, and
// because the control plane's own consumer of that family must not see it.
const SubjectRegistrySnapshot = "daemon.v1.registry.snapshot"

// RegistrySnapshot is the payload on SubjectRegistrySnapshot.
type RegistrySnapshot struct {
	// UserID is the owner's EXTERNAL id, the one the identity provider issued:
	// Reliant's user id, not the control plane's internal users.id.
	UserID string `json:"user_id"`
	// At is when the control plane read the set. The consumer leaves alone a
	// row it created shortly before At even when the set does not list it:
	// that row may be one the control plane had not yet heard of.
	At time.Time `json:"at"`
	// Daemons is every daemon the owner has right now. Never null on the wire:
	// an empty set is meaningful — the owner has none, and every row the
	// registry holds for them goes — and a consumer must not have to guess
	// whether null means "empty" or "absent".
	Daemons []RegistryDaemon `json:"daemons"`
}

// RegistryDaemon is one daemon in a RegistrySnapshot.
type RegistryDaemon struct {
	DaemonID   string    `json:"daemon_id"`
	Name       string    `json:"name,omitempty"`
	DaemonType string    `json:"daemon_type"`
	CreatedAt  time.Time `json:"created_at"`

	// Lifecycle, for managed daemons only. Applied through the same
	// newest-wins guard as EventLifecycle, keyed on PhaseChangedAt (the time
	// the phase was entered), so a snapshot can fill in a row it just created
	// without ever overwriting a newer transition the per-daemon events
	// already delivered.
	Phase           LifecyclePhase `json:"phase,omitempty"`
	PhaseChangedAt  *time.Time     `json:"phase_changed_at,omitempty"`
	Size            string         `json:"size,omitempty"`
	StatusMessage   string         `json:"status_message,omitempty"`
	LastOOMKilledAt *time.Time     `json:"last_oom_killed_at,omitempty"`
	OOMKillCount    int32          `json:"oom_kill_count,omitempty"`
}
