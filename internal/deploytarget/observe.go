package deploytarget

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Observe — the third verb, and why it is a verb rather than a field on
// Deploy's result.
//
// Deploy is IMPERATIVE and ONE-SHOT: it describes a transition ("ship
// this"), runs once, and when it returns it has no further opinion. It
// does not answer the question a
// reconciler is built around — WHAT IS ACTUALLY RUNNING RIGHT NOW — and
// that gap is the whole difference between a deploy tool and a
// reconciler. A deploy's own return value cannot answer it either: it
// reports what forge ASKED for and whether the ask was accepted, which
// stops being true the moment anything else touches the target (a manual
// kubectl edit, a crash-looping pod, a CDN pointed at a pruned release).
//
// So Observe is a READ, with no side effects, that a caller may run at
// any time — including against a target forge has never deployed to.
//
// # Unknown is a first-class answer, and the zero value IS unknown
//
// Not every target can be read back. `External` runs an OPAQUE `sh -c`
// command — forge does not know whether that command talked to Fly.io,
// systemd or a wet string, so there is genuinely nothing to read. The
// honest report is "I do not know", and this package makes that the
// DEFAULT rather than something a provider must remember to say:
//
//   - [Health]'s zero value is [HealthUnknown]. A provider that forgets
//     to set health cannot accidentally report healthy.
//   - [ObservedItem.Replicas] is a POINTER, so nil means "this tier has
//     no replica concept" rather than "zero replicas are running" —
//     which for a static site is the difference between "not applicable"
//     and "the site is down".
//   - A provider that cannot observe at all returns
//     [ErrObservationUnsupported], AND still returns an Observed whose
//     items are explicitly unknown, so a caller that logs the value
//     without checking the error still cannot read green.
//
// That last point is deliberate belt-and-braces. Go convention says a
// value returned beside a non-nil error is unusable, and a careful caller
// will check. The reason to populate it anyway is that the failure mode
// of the careless caller here is not a crash, it is a REPORT OF HEALTH
// THAT NOBODY MEASURED — and claiming reconciliation you do not have is
// worse than admitting the gap.

// Health is a provider's verdict on one observed item. It is an int enum
// precisely so its ZERO VALUE is [HealthUnknown]: an Observed built by
// any path — a provider, a test literal, a future caller — reads as
// unknown until something deliberately says otherwise.
//
// Four values, and each is a genuinely different decision for a caller:
// "nothing is there" and "something is there and it is broken" call for
// opposite actions (create vs. investigate), so collapsing them into one
// "not healthy" would force every consumer to re-derive the distinction.
type Health int

const (
	// HealthUnknown means the provider did not measure. The zero value.
	HealthUnknown Health = iota
	// HealthAbsent means the provider looked and found nothing deployed.
	// Distinct from unknown: this IS a measurement.
	HealthAbsent
	// HealthDegraded means something is deployed but is not fully serving
	// (replicas short of desired, a live pointer at a missing release).
	HealthDegraded
	// HealthHealthy means the target reports everything it was asked for
	// as running and ready.
	HealthHealthy
)

// String renders the health for logs and JSON. Unknown renders as
// "unknown" rather than "" so an unset value is visible in output rather
// than reading as an empty column.
func (h Health) String() string {
	switch h {
	case HealthAbsent:
		return "absent"
	case HealthDegraded:
		return "degraded"
	case HealthHealthy:
		return "healthy"
	default:
		return "unknown"
	}
}

// MarshalJSON emits the string form so `forge env observe --json` is
// readable by a human and by jq, rather than emitting the iota.
func (h Health) MarshalJSON() ([]byte, error) {
	return []byte(`"` + h.String() + `"`), nil
}

// ReplicaCounts is the scale half of an observation, for tiers that have
// a replica concept at all. The four fields are Kubernetes' own
// vocabulary because that is the tier that has them; a provider without
// replicas leaves [ObservedItem.Replicas] nil rather than filling zeros.
type ReplicaCounts struct {
	// Desired is what the target was asked for (Deployment.spec.replicas).
	Desired int `json:"desired"`
	// Ready is how many are passing their readiness probe.
	Ready int `json:"ready"`
	// Updated is how many are running the CURRENT pod template — the
	// count that distinguishes "rolled out" from "rolling out", which a
	// ready count alone cannot.
	Updated int `json:"updated"`
	// Available is how many have stayed ready past minReadySeconds.
	Available int `json:"available"`
}

// ObservedItem is what one provider currently sees for one service or
// frontend. Kept to four facts — identity, running digest, scale, health
// — on purpose.
//
// THE TEMPTATION TO GROW THIS IS THE THING TO RESIST. Every tier has
// something it could add (pod conditions, a CDN cache age, a Fly machine
// id), and a union struct carrying all of them would force every provider
// to fill fields that mean nothing to it, and every consumer to guess
// which are populated. Anything tier-specific belongs behind the kind
// dispatch — i.e. in the provider — not here.
type ObservedItem struct {
	// Name is the service or frontend name, matching
	// ResolvedService.Name / StaticSiteFrontend.Name so a caller can join
	// an observation back to what was declared.
	Name string `json:"name"`

	// Digest is the content-addressed identity of what is RUNNING: an
	// image digest for a container tier, the live release digest for a
	// static site. Empty means the provider could not determine it —
	// which includes the common, legitimate case of an image pinned by
	// mutable tag rather than by digest.
	Digest string `json:"digest,omitempty"`

	// Replicas is the scale observation, or nil when the tier has no
	// replica concept. Nil and &ReplicaCounts{} mean different things:
	// "not applicable" versus "nothing is running".
	Replicas *ReplicaCounts `json:"replicas,omitempty"`

	// Health is the provider's verdict. Zero value is HealthUnknown.
	Health Health `json:"health"`

	// Detail is one line of human-readable context, and it is REQUIRED
	// whenever Health is not HealthHealthy. An "unknown" with no reason
	// is indistinguishable from a provider that silently failed, which is
	// the exact confusion this whole verb exists to remove.
	Detail string `json:"detail,omitempty"`
}

// Observed is one provider's answer for one ServiceGroup.
type Observed struct {
	// ProviderID is the Name() of the provider that produced this, so a
	// merged multi-group report stays attributable.
	ProviderID string `json:"provider"`

	// ObservedAt is when the read happened, wall-clock UTC. A reconciler
	// comparing declared against observed needs to know how stale the
	// observation is; without it, a cached or retried observation is
	// indistinguishable from a fresh one.
	ObservedAt time.Time `json:"observed_at"`

	// Items is the per-service / per-frontend detail.
	Items []ObservedItem `json:"items"`
}

// ErrObservationUnsupported is the sentinel a provider returns when it
// cannot read its target back AT ALL. Match it with errors.Is.
//
// This is NOT ErrProviderNotImplemented. That sentinel means "forge has
// not built this yet"; this one covers both that case AND the permanent,
// structural case — External's `sh -c` is opaque by design, so no future
// version of forge will be able to observe it either. The two are
// distinguished by ObservationUnsupportedError.Reason rather than by two
// sentinels, because every caller treats them identically (report
// unknown) and only a human reading the message cares which it is.
var ErrObservationUnsupported = errors.New("forge: deploy provider cannot observe this target")

// ObservationUnsupportedError carries which provider declined and why.
type ObservationUnsupportedError struct {
	// Provider is the declining provider's Name().
	Provider string
	// Reason states what makes the target unreadable, in the user's
	// terms. Required — an unsupported with no reason tells a user
	// nothing they can act on.
	Reason string
}

func (e ObservationUnsupportedError) Error() string {
	return fmt.Sprintf("%s: cannot observe: %s", e.Provider, e.Reason)
}

// Unwrap makes errors.Is(err, ErrObservationUnsupported) true.
func (e ObservationUnsupportedError) Unwrap() error { return ErrObservationUnsupported }

// unsupported builds the (Observed, error) pair a provider that cannot
// observe must return: the sentinel error AND an Observed whose every
// item is explicitly unknown, carrying the same reason.
//
// Both halves matter. The error is the signal a careful caller checks;
// the unknown-filled Observed is what a CARELESS caller sees, and it must
// not be mistakable for a healthy report. Building both here rather than
// at each call site is what keeps the two from drifting apart — a
// provider that hand-rolled only the error would hand back a zero
// Observed with an empty Items slice, which renders as "nothing to
// report" rather than "I cannot tell".
func unsupported(providerID, reason string, names []string) (Observed, error) {
	out := Observed{
		ProviderID: providerID,
		ObservedAt: time.Now().UTC(),
		Items:      make([]ObservedItem, 0, len(names)),
	}
	for _, n := range names {
		out.Items = append(out.Items, ObservedItem{
			Name:   n,
			Health: HealthUnknown,
			Detail: reason,
		})
	}
	return out, ObservationUnsupportedError{Provider: providerID, Reason: reason}
}

// serviceNames returns the group's service names, for the unsupported
// path (which still names every item it cannot speak for).
func serviceNames(group ServiceGroup) []string {
	out := make([]string, 0, len(group.Services))
	for _, s := range group.Services {
		out = append(out, s.Name)
	}
	return out
}

// ObserveGroups reads every group through its registered provider and
// returns one Observed per group, in input order.
//
// It NEVER fails the whole read because one provider declined:
// ErrObservationUnsupported is folded into the returned Observed (whose
// items already read as unknown) and dropped from the error set, because
// "this tier is unobservable" is an ANSWER, not a failure. Any other
// error is joined and returned alongside whatever was successfully
// observed — a partial observation is still worth more than none, as long
// as the gaps are visible, which they are: an unreadable group's items
// stay HealthUnknown.
//
// A group whose ProviderID is not registered is reported as unknown
// rather than skipped. Skipping it would shrink the report to the tiers
// forge happens to understand, which is the silent-green shape this verb
// exists to prevent.
func ObserveGroups(ctx context.Context, reg *Registry, groups []ServiceGroup) ([]Observed, error) {
	out := make([]Observed, 0, len(groups))
	var errs []error
	for _, g := range groups {
		p := reg.Lookup(g.ProviderID)
		if p == nil {
			obs, _ := unsupported(g.ProviderID,
				"no provider registered under this id; forge cannot read this target back",
				observableNames(g))
			out = append(out, obs)
			continue
		}
		obs, err := p.Observe(ctx, g)
		if err != nil && !errors.Is(err, ErrObservationUnsupported) {
			errs = append(errs, fmt.Errorf("observe %s: %w", g.ProviderID, err))
		}
		out = append(out, obs)
	}
	return out, errors.Join(errs...)
}

// observableNames returns every name in a group that an observation could
// speak for — services AND frontends, because a group routes to exactly
// one provider and the frontend providers read StaticSites/Frontends
// rather than Services.
func observableNames(g ServiceGroup) []string {
	names := serviceNames(g)
	for _, fe := range g.StaticSites {
		names = append(names, fe.Name)
	}
	for _, fe := range g.Frontends {
		names = append(names, fe.Name)
	}
	return names
}
