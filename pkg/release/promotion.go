package release

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// PromotionKind records what a ledger entry is. There is ONE kind: a
// promotion binds an environment to a release. Moving an environment to an
// OLDER release is still a promotion — the plan labels its direction
// `behind` — because there is no such thing as undoing a release: the newer
// one already wrote data and ran migrations. Recovery is roll forward.
//
// The field survives because it is the ledger's wire format, and an
// explicit kind keeps a future entry type from being read as a promotion.
type PromotionKind string

const (
	// KindPromote binds an environment to a release.
	KindPromote PromotionKind = "promote"

	// legacyKindRollback is what the retired promote-as-rollback flag
	// recorded before rollback was removed. It is READ ONLY: existing ledgers still
	// carry it, and an entry that bound an env to a release is a promotion
	// of that release whatever intent it was labelled with. It decodes as
	// KindPromote and can never be written.
	legacyKindRollback = "rollback"
)

// Valid reports whether k is a kind a ledger entry may carry.
func (k PromotionKind) Valid() bool { return k == KindPromote }

// ParsePromotionKind reads a STORED kind — a ledger line, a database row —
// into the kind it means today. It is the one place the retired "rollback"
// kind is recognised, so every backend that holds old entries reads them the
// same way: as the promote of their release. Unknown and empty are refused,
// never defaulted — a kind nobody recognises must not read as a binding.
func ParsePromotionKind(stored string) (PromotionKind, error) {
	if stored == legacyKindRollback {
		return KindPromote, nil
	}
	if k := PromotionKind(stored); k.Valid() {
		return k, nil
	}
	return "", fmt.Errorf("%w: unknown promotion kind %q (expected %s)", ErrInvalid, stored, KindPromote)
}

// UnmarshalJSON decodes through ParsePromotionKind.
func (k *PromotionKind) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("%w: promotion kind must be a string: %v", ErrInvalid, err)
	}
	parsed, err := ParsePromotionKind(raw)
	if err != nil {
		return err
	}
	*k = parsed
	return nil
}

// Actor is who recorded a promotion: a human user, or a named automation
// ("ci", "preview-bot"). A file ledger records whatever the caller states.
type Actor struct {
	User  string `json:"user,omitempty"`
	Actor string `json:"actor,omitempty"`
}

// Promotion is ONE append-only ledger entry: environment Env ran release
// Release from At onward. An environment's CURRENT binding is simply its
// most recent entry; there is no separate pointer that could disagree with
// the history.
type Promotion struct {
	// Frozen complete desired state; absent only on historical promotions.
	Snapshot       *Snapshot `json:"snapshot,omitempty"`
	SnapshotDigest string    `json:"snapshot_digest,omitempty"`
	BundleID       string    `json:"bundle_id,omitempty"`
	BundleDigest   string    `json:"bundle_digest,omitempty"`
	// ID is assigned by the backend that recorded the entry.
	ID string `json:"id,omitempty"`
	// Env is the environment name.
	Env string `json:"env"`
	// Release is the version label bound.
	Release string        `json:"release"`
	Kind    PromotionKind `json:"kind"`
	// FromEnv is the environment this release was promoted FROM, or empty
	// for a first deploy or a direct promote. Makes the promotion PATH auditable.
	FromEnv string `json:"from_env,omitempty"`
	// Resolved is the image → digest pin set FROZEN at promote time. A
	// deploy pins exactly these; nothing re-reads the release later.
	Resolved map[string]string `json:"resolved"`
	// Sources is the source-built half of the same snapshot.
	Sources map[string]Source `json:"sources,omitempty"`
	// Targets names the components a SCOPED deploy moved (`--target api`,
	// `--frontends-only`): workload, frontend or group names as the env
	// declares them, sorted. Empty means the whole release moved.
	//
	// It is what keeps "prod runs v2" honest after `forge env deploy prod v2
	// --target api`: the entry still binds prod to v2 — the release whose
	// pins the deploy shipped — and Targets says only api took them, so a
	// reader asking what a workload runs walks back to the newest entry that
	// [Promotion.Moved] it. Resolved and Sources stay the release's whole
	// pin set either way.
	Targets    []string `json:"targets,omitempty"`
	PromotedBy Actor    `json:"promoted_by,omitempty"`
	// Gates is the PRE-promote evidence, frozen into the entry: what had
	// already passed when the environment was bound.
	Gates []Gate `json:"gates,omitempty"`
	// RecordedGates is the POST-promote evidence — the wait, the smoke, a
	// manual sign-off — appended after the binding. It is a separate field
	// because the promotion entry is append-only: evidence that arrives
	// later cannot be written into Gates without rewriting history, and
	// the distinction is itself meaningful (what was known BEFORE the
	// environment moved, versus what was learned after).
	//
	// A file ledger leaves it empty: there is nowhere to append to a line
	// already written. A hosted backend fills it from its child table.
	RecordedGates []Gate `json:"recorded_gates,omitempty"`
	// Run is the pipeline attempt that made this promotion, or the zero
	// Run for a promotion that belongs to none.
	Run Run `json:"run,omitempty"`
	// FromPromotionID is the SOURCE promotion this one copied pins from —
	// the exact entry, not just the environment. FromEnv names where the
	// bytes came from; this names WHEN, which is what makes a promote
	// chain reconstructible after the source env has moved on.
	FromPromotionID string `json:"from_promotion_id,omitempty"`
	// SupersededInFlight records that this promotion replaced one whose
	// rollout had not finished — a deliberate override, not an accident.
	// Set by the backend that admitted the override; it is the audit trail
	// for "who decided to interrupt a rollout".
	SupersededInFlight bool `json:"superseded_in_flight,omitempty"`
	// Note is the free-form "why" — the most valuable field on a promote
	// that moves an environment backwards.
	Note string `json:"note,omitempty"`
	// PlanDigest is the approved deploy plan this promotion was written
	// under ([Plan.Digest]). Writing the promotion IS the deploy wherever a
	// converger applies it, so the review precedes the record, and this
	// names which review.
	PlanDigest string `json:"plan_digest,omitempty"`
	// ApprovedBy is who approved that plan. A backend sets it from the
	// credential; a client never supplies it as identity.
	ApprovedBy string `json:"approved_by,omitempty"`
	// AcknowledgedFindings are the stop-class finding codes the approver
	// accepted by name.
	AcknowledgedFindings []string `json:"acknowledged_findings,omitempty"`
	// PromotedAt is when the entry was written. It is PROMOTE time, not
	// deploy time: a promotion moves no bytes.
	PromotedAt time.Time `json:"promoted_at"`
}

// Validate checks a promotion before it is appended, and every entry read
// back.
func (p Promotion) Validate() error {
	if p.Snapshot != nil || p.SnapshotDigest != "" {
		if p.Snapshot == nil || !ValidDigest(p.BundleDigest) || p.BundleID == "" {
			return fmt.Errorf("%w: incomplete frozen promotion snapshot/bundle", ErrInvalid)
		}
		d, err := p.Snapshot.Digest()
		if err != nil {
			return err
		}
		if d != p.SnapshotDigest || p.Snapshot.Environment != p.Env {
			return fmt.Errorf("%w: promotion snapshot mismatch", ErrInvalid)
		}
	}
	switch {
	case p.Env == "":
		return fmt.Errorf("%w: promotion environment is required", ErrInvalid)
	case p.Release == "":
		return fmt.Errorf("%w: promotion release is required", ErrInvalid)
	case !p.Kind.Valid():
		return fmt.Errorf("%w: promotion kind %q (expected promote)", ErrInvalid, p.Kind)
	case p.FromEnv != "" && p.FromEnv == p.Env:
		return fmt.Errorf("%w: environment %q cannot be promoted from itself", ErrInvalid, p.Env)
	case p.FromPromotionID != "" && p.FromPromotionID == p.ID:
		return fmt.Errorf("%w: promotion %q cannot be promoted from itself", ErrInvalid, p.ID)
	}
	for image, d := range p.Resolved {
		if !ValidDigest(d) {
			return fmt.Errorf("%w: promotion of %q: resolved digest %q for %q is not canonical", ErrInvalid, p.Env, d, image)
		}
	}
	if err := p.Run.Validate(); err != nil {
		return fmt.Errorf("promotion of %q: %w", p.Env, err)
	}
	if p.PlanDigest != "" && !ValidDigest(p.PlanDigest) {
		return fmt.Errorf("%w: promotion of %q: plan digest %q is not canonical", ErrInvalid, p.Env, p.PlanDigest)
	}
	if err := validateTargets(p.Targets); err != nil {
		return fmt.Errorf("promotion of %q: %w", p.Env, err)
	}
	// Gates read back from a ledger written before the status set closed
	// carry a RawStatus, and must still READ. So validation here covers
	// only what makes a gate uninterpretable rather than merely old: a
	// gate with no name cannot be attributed to a check at all.
	for i, g := range p.Gates {
		if strings.TrimSpace(g.Name) == "" {
			return fmt.Errorf("%w: promotion of %q: gate %d has no name", ErrInvalid, p.Env, i)
		}
	}
	for i, g := range p.RecordedGates {
		if strings.TrimSpace(g.Name) == "" {
			return fmt.Errorf("%w: promotion of %q: recorded gate %d has no name", ErrInvalid, p.Env, i)
		}
	}
	return nil
}

// AllGates is the whole evidence trail in reading order: the pre-promote
// gates frozen into the entry, then the post-promote gates appended after
// it. One call, so a renderer cannot show half the evidence by forgetting a
// field.
func (p Promotion) AllGates() []Gate {
	if len(p.RecordedGates) == 0 {
		return p.Gates
	}
	out := make([]Gate, 0, len(p.Gates)+len(p.RecordedGates))
	out = append(out, p.Gates...)
	return append(out, p.RecordedGates...)
}

// NewPromotion freezes a release's pin set into a promotion of env. The
// digests are resolved HERE, at promote time, so a later edit to the release
// cannot change what this promotion ships.
func NewPromotion(env string, r Release, kind PromotionKind) Promotion {
	return Promotion{
		Env:      env,
		Release:  r.Version,
		Kind:     kind,
		Resolved: r.SharedDigests(),
		Sources:  r.Sources(),
	}
}

// MaxTargets bounds a promotion's Targets, and MaxTargetBytes each name.
const (
	MaxTargets     = 64
	MaxTargetBytes = 253
)

// Scoped reports whether the promotion moved only some components.
func (p Promotion) Scoped() bool { return len(p.Targets) > 0 }

// Moved reports whether this promotion moved component: every component for a
// whole-release promotion, only its Targets for a scoped one.
func (p Promotion) Moved(component string) bool {
	if !p.Scoped() {
		return true
	}
	for _, t := range p.Targets {
		if t == component {
			return true
		}
	}
	return false
}

// covers reports whether p already moved everything requested would.
func (p Promotion) covers(requested Promotion) bool {
	if !p.Scoped() {
		return true
	}
	if !requested.Scoped() {
		return false
	}
	for _, t := range requested.Targets {
		if !p.Moved(t) {
			return false
		}
	}
	return true
}

// NormalizeTargets is the canonical form of a scope: trimmed, empty names
// dropped, duplicates removed, sorted. Nil for an unscoped deploy.
func NormalizeTargets(targets []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range targets {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

func validateTargets(targets []string) error {
	if len(targets) > MaxTargets {
		return fmt.Errorf("%w: %d targets, over the bound of %d", ErrInvalid, len(targets), MaxTargets)
	}
	for i, t := range targets {
		switch {
		case t == "" || strings.TrimSpace(t) != t:
			return fmt.Errorf("%w: target %d (%q) is empty or padded", ErrInvalid, i, t)
		case len(t) > MaxTargetBytes:
			return fmt.Errorf("%w: target %q is over %d bytes", ErrInvalid, t, MaxTargetBytes)
		case strings.ContainsFunc(t, func(r rune) bool { return r < 0x20 || r == 0x7f }):
			return fmt.Errorf("%w: target %q contains a control character", ErrInvalid, t)
		case i > 0 && targets[i-1] >= t:
			return fmt.Errorf("%w: targets must be sorted and unique (%q after %q) — see NormalizeTargets", ErrInvalid, t, targets[i-1])
		}
	}
	return nil
}

// Decide applies the ledger's append rule to one environment's history
// (OLDEST FIRST) and a requested entry. It returns:
//
//   - (current, nil) when the request is already the current state — the
//     same release, and the current entry already moved every component the
//     request names. A CI retry must not append a second entry claiming the
//     environment moved where it already was. The key is the CURRENT
//     entry, not "ever promoted": v1→v2→v1 is three real moves, and the
//     third must be recorded. So is a scoped v2 followed by v2 for the
//     rest: the second entry moves components the first did not.
//   - (nil, nil) when the entry should be appended.
//
// Both backends call this inside whatever serializes their appends (the
// hosted one under a row lock), so the rule has one implementation. The
// error return is kept so a future rule can refuse without a signature
// change across both backends.
func Decide(history []Promotion, requested Promotion) (*Promotion, error) {
	if n := len(history); n > 0 {
		current := history[n-1]
		if current.Release == requested.Release && current.covers(requested) {
			return &current, nil
		}
	}
	return nil, nil
}
