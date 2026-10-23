package deploystate

import (
	"encoding/json"
	"fmt"
	"time"
)

// State is the verdict for one target: five values, not two.
//
// A boolean "in sync" collapses three genuinely different situations —
// still settling, actively wrong, and not measured — into one "no", and
// every consumer then re-derives the distinction from fields the boolean
// was supposed to save it from reading.
//
// [StateUnknown] is the ZERO VALUE, and that is load-bearing. An opaque
// `External` target runs a command forge cannot read back; there is
// genuinely nothing to observe, and the honest answer is "I do not
// know". Making it the zero value means a Record built by any path —
// a backend that failed halfway, a test literal, a file written by an
// older forge — reads as unknown rather than as green. A dashboard that
// shows green for something nobody measured is worse than one that shows
// nothing, because it is confidently wrong and people stop reading it.
type State int

const (
	// StateUnknown means nothing measured this target. The zero value.
	StateUnknown State = iota

	// StateConverging means the target matches what was declared but has
	// not yet been stable long enough to trust. See [Record.StableSince].
	StateConverging

	// StateConverged means the target matches AND has held that shape
	// past the stability window.
	StateConverged

	// StateDiverged means an owned field does not match the declaration.
	// This is drift.
	StateDiverged

	// StateDegraded means the target is not serving — regardless of
	// whether it matches. A perfect apply onto a crash-looping pod is
	// degraded, not converged.
	StateDegraded
)

// String renders the state for logs and JSON. Unknown renders as
// "unknown" rather than "" so an unset value is visible as a value.
func (s State) String() string {
	switch s {
	case StateConverging:
		return "converging"
	case StateConverged:
		return "converged"
	case StateDiverged:
		return "diverged"
	case StateDegraded:
		return "degraded"
	default:
		return "unknown"
	}
}

// MarshalJSON emits the string form.
func (s State) MarshalJSON() ([]byte, error) { return json.Marshal(s.String()) }

// UnmarshalJSON accepts the string form; "" and null decode to unknown.
func (s *State) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*s = StateUnknown
		return nil
	}
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("deploystate: state must be a string: %w", err)
	}
	switch raw {
	case "", "unknown":
		*s = StateUnknown
	case "converging":
		*s = StateConverging
	case "converged":
		*s = StateConverged
	case "diverged":
		*s = StateDiverged
	case "degraded":
		*s = StateDegraded
	default:
		return fmt.Errorf("deploystate: unknown state %q", raw)
	}
	return nil
}

// Field names a comparable property of a target. NOT every difference is
// drift, and this type is how forge says which differences are its
// business.
//
// The failure this prevents is specific and common: an autoscaler moves
// a replica count, forge reports drift, the engineer confirms it is
// correct behaviour, and after the third such report nobody reads the
// drift badge again. A signal people have learned to ignore is worse
// than no signal, because it occupies the place where a real one would
// have gone.
//
// So ownership is per-target and explicit. A target under an HPA lists
// [FieldDigest] and not [FieldReplicas]; a target forge fully owns lists
// both. Anything not listed is the world's, and a difference there is
// recorded and never reported as drift.
type Field string

const (
	// FieldDigest is the content-addressed identity of what is running.
	FieldDigest Field = "digest"
	// FieldReplicas is the scale. Frequently NOT forge's to own — an
	// autoscaler owning this field is the motivating example for this
	// whole type.
	FieldReplicas Field = "replicas"
)

// Desired is what the declaration asks for. Only the fields a [Field]
// can name — this is the comparison half of a Record, not a copy of the
// whole spec.
type Desired struct {
	// Digest is the canonical image digest declared, when the
	// declaration pins one. Empty when the target is pinned by mutable
	// tag instead, which is a legitimate and common case and must not be
	// reported as drift — see [Record.Evaluate].
	Digest string `json:"digest,omitempty"`

	// Image and Tag are the human-facing reference, carried for
	// reporting (a non-cluster provider has no cluster to ask, so the
	// deployed tag has to be remembered somewhere).
	Image string `json:"image,omitempty"`
	Tag   string `json:"tag,omitempty"`

	// Replicas is the declared scale, or nil when the tier has no
	// replica concept. Nil and a pointer to 0 mean different things:
	// "not applicable" versus "deliberately scaled to nothing".
	Replicas *int `json:"replicas,omitempty"`

	// DeclaredAt is when this declaration was recorded.
	DeclaredAt time.Time `json:"declared_at,omitzero"`
}

// Observation is what was actually found running. Mirrors
// deploytarget.Observed's facts without importing it: forge's internal
// package may not be imported from pkg/, and an operator implementing a
// Store should not have to depend on forge's provider tree to do it.
type Observation struct {
	// Digest is what is RUNNING. Empty means the observer could not
	// determine it.
	Digest string `json:"digest,omitempty"`

	// Replicas is the observed scale, nil when not applicable.
	Replicas *int `json:"replicas,omitempty"`

	// Ready is how many of those replicas are passing readiness. Nil
	// when not applicable. This is the field that separates "applied" —
	// which a deploy's own return value already told you — from
	// "working", which only a read can.
	Ready *int `json:"ready,omitempty"`

	// Serving is the observer's verdict on whether the target is doing
	// its job. FALSE IS THE ZERO VALUE, so an Observation that nobody
	// filled in cannot read as serving. Meaningful only when Measured is
	// true.
	Serving bool `json:"serving"`

	// Measured distinguishes "looked, and here is what I found" from "did
	// not look". Without it, a zero Observation is indistinguishable
	// from a target that is genuinely absent and unhealthy, and those
	// call for opposite actions: investigate versus go and look.
	Measured bool `json:"measured"`

	// Detail is one line of human-readable context. REQUIRED whenever
	// the observation is anything other than a clean serving read — an
	// unknown with no reason is indistinguishable from an observer that
	// silently failed.
	Detail string `json:"detail,omitempty"`

	// ObservedAt is when the read happened, wall-clock UTC. A caller
	// comparing declared against observed needs to know how stale the
	// answer is; without it a cached observation reads as a fresh one.
	ObservedAt time.Time `json:"observed_at,omitzero"`
}

// Key identifies one target within one project. Three segments, matching
// the on-disk layout that already exists (.forge/state/<provider>-<env>-<service>.json),
// so adopting this package does not orphan any state a user already has.
type Key struct {
	// Env is the environment name (dev, staging, prod).
	Env string `json:"env"`
	// Provider is the deploy provider id (k8s, compose, external, ...).
	Provider string `json:"provider"`
	// Service is the service or frontend name.
	Service string `json:"service"`
}

// String renders the key for logs and error messages.
func (k Key) String() string { return k.Env + "/" + k.Provider + "/" + k.Service }

// Valid reports whether every segment is populated. A key with an empty
// segment would collide with another key on disk after sanitization, so
// it is refused at the boundary rather than silently overwriting.
func (k Key) Valid() bool {
	return k.Env != "" && k.Provider != "" && k.Service != ""
}

// Record is one target's two halves side by side, plus the bookkeeping
// that turns a comparison into a trustworthy verdict.
type Record struct {
	Key Key `json:"key"`

	Desired  Desired     `json:"desired"`
	Observed Observation `json:"observed"`

	// Owned lists the fields forge is responsible for. EMPTY MEANS
	// NOTHING IS OWNED, which is deliberate: a Record that predates this
	// field, or that a backend forgot to populate, reports no drift
	// rather than reporting every difference as drift. The direction of
	// the default matters — over-reporting is what destroys trust in the
	// signal, and under-reporting is visible as a badge that never moves.
	Owned []Field `json:"owned,omitempty"`

	// StableSince is when the target most recently ENTERED its current
	// matching, serving shape. Nil whenever it is not in one.
	//
	// This field is why "converged" means something. A clean apply is
	// not a successful promotion: the apply succeeds, the pod starts,
	// the pod crashes, and a reconciler that reported success at apply
	// time has already told everyone the promotion worked. Requiring the
	// shape to HOLD for a window is what makes the report honest, and it
	// is the same clock an automatic rollback would arm itself against.
	StableSince *time.Time `json:"stable_since,omitempty"`
}

// DefaultStabilityWindow is how long a target must hold its matching,
// serving shape before [Record.Evaluate] will call it converged.
//
// Thirty seconds is chosen to outlast the failure it exists to catch: a
// container that starts, passes nothing, and is restarted. A crash loop
// with a backoff longer than this window will still be caught by the
// serving check on the next pass — the window is not the only defence,
// it is the one that stops a single optimistic snapshot from being
// reported as a successful promotion.
const DefaultStabilityWindow = 30 * time.Second

// Evaluate computes the verdict for this record as of `now`, given a
// stability window.
//
// The order of the checks is the design, so it is spelled out:
//
//  1. NOT MEASURED WINS OVER EVERYTHING. An unobservable target is
//     unknown even if its desired half looks perfect, because the
//     desired half is a wish and wishes are free.
//  2. NOT SERVING BEATS MATCHING. Degraded outranks converged: an exact
//     match onto a crash-looping workload is degraded. This is the check
//     that stops "the apply succeeded" from being reported as success.
//  3. DRIFT IS COMPUTED OVER OWNED FIELDS ONLY. A replica count that
//     moved under an autoscaler forge does not own is recorded and not
//     reported.
//  4. MATCHING BUT YOUNG IS CONVERGING, NOT CONVERGED.
func (r Record) Evaluate(now time.Time, window time.Duration) State {
	if !r.Observed.Measured {
		return StateUnknown
	}
	if !r.Observed.Serving {
		return StateDegraded
	}
	if len(r.DriftedFields()) > 0 {
		return StateDiverged
	}
	if r.StableSince == nil {
		return StateConverging
	}
	if now.Sub(*r.StableSince) < window {
		return StateConverging
	}
	return StateConverged
}

// DriftedFields returns the owned fields whose observed value does not
// match the declared one. Empty when there is no drift — including the
// case where forge owns nothing, which is the safe default.
//
// A field the observer could not determine is NOT drift. An image pinned
// by mutable tag yields an empty desired digest and an observer that
// cannot resolve a digest yields an empty observed one; calling either
// case drift would flag a config that is both legal and common, which is
// exactly the noise that makes people stop reading the badge.
func (r Record) DriftedFields() []Field {
	var drifted []Field
	for _, f := range r.Owned {
		switch f {
		case FieldDigest:
			if r.Desired.Digest == "" || r.Observed.Digest == "" {
				continue
			}
			if r.Desired.Digest != r.Observed.Digest {
				drifted = append(drifted, FieldDigest)
			}
		case FieldReplicas:
			if r.Desired.Replicas == nil || r.Observed.Replicas == nil {
				continue
			}
			if *r.Desired.Replicas != *r.Observed.Replicas {
				drifted = append(drifted, FieldReplicas)
			}
		}
	}
	return drifted
}

// Owns reports whether forge claims this field for this target.
func (r Record) Owns(f Field) bool {
	for _, owned := range r.Owned {
		if owned == f {
			return true
		}
	}
	return false
}

// WithStability returns a copy of r whose StableSince reflects a fresh
// observation, carrying the previous record's clock forward when the
// target has held its shape.
//
// THE CARRY-FORWARD IS THE WHOLE POINT. A writer that stamped
// StableSince=now on every pass would reset the clock every time it
// looked, so nothing would ever age past the window and no target would
// ever be reported converged. A writer that never stamped it would call
// a target converged the instant it first matched, which is the crash-
// loop bug. The clock advances only on a genuine transition INTO the
// good shape, and is cleared on any transition out of it.
//
// prev may be nil (first observation of this target).
func (r Record) WithStability(prev *Record, now time.Time) Record {
	good := r.Observed.Measured && r.Observed.Serving && len(r.DriftedFields()) == 0
	if !good {
		r.StableSince = nil
		return r
	}
	if prev != nil && prev.StableSince != nil &&
		prev.Observed.Measured && prev.Observed.Serving && len(prev.DriftedFields()) == 0 &&
		prev.Desired.Digest == r.Desired.Digest {
		carried := *prev.StableSince
		r.StableSince = &carried
		return r
	}
	stamped := now.UTC()
	r.StableSince = &stamped
	return r
}
