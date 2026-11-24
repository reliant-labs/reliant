package changes

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Reason says why Evaluate returned what it did.
type Reason string

// The reasons. Only ReasonRollout and ReasonExperiment are enrolled.
const (
	// ReasonUnknown: the snapshot has no change of that key (or there is no
	// snapshot). The caller's default applies.
	ReasonUnknown Reason = "unknown"
	// ReasonDefault: the change is off, or in preview and the unit is not
	// on the preview list. Its declared default variant applies, or the
	// caller's default when it declares none.
	ReasonDefault Reason = "default"
	// ReasonInvalid: the change failed validation. The caller's default
	// applies; the snapshot's other changes are unaffected.
	ReasonInvalid Reason = "invalid"
	// ReasonPreview: the unit is on the change's preview list, which
	// applies under every mode but off. Never measured.
	ReasonPreview Reason = "preview"
	// ReasonEveryone: the change gives every caller one variant.
	ReasonEveryone Reason = "everyone"
	// ReasonNoUnit: the context carries no id for the change's unit, so the
	// default applies. Not enrolled, and never counted as a control.
	ReasonNoUnit Reason = "no_unit"
	// ReasonIneligible: the unit is outside the change's audience.
	ReasonIneligible Reason = "ineligible"
	// ReasonNotEnrolled: the unit is in the audience but outside the
	// percentage.
	ReasonNotEnrolled Reason = "not_enrolled"
	// ReasonRollout: the unit is enrolled in a gradual rollout.
	ReasonRollout Reason = "rollout"
	// ReasonExperiment: the unit is enrolled in an experiment and was
	// assigned a variant of its split.
	ReasonExperiment Reason = "experiment"
)

// EvalContext is who a change is evaluated for. TargetingKey is the user id
// (a change whose unit is "user"); Attributes carry the account id
// ("account"), the anonymous visitor id ("visitor") and the attribute an
// audience names.
type EvalContext struct {
	TargetingKey string
	Attributes   map[string]string
}

// Result is one evaluation.
type Result struct {
	// Variant is the variant chosen, or "" when the caller's default
	// applies.
	Variant string
	// Value is the variant's JSON value, compacted, or nil when the caller's
	// default applies.
	Value  json.RawMessage
	Reason Reason
	// Run is the measured run whose delivery decided this result: set for
	// the rollout and experiment modes once preview is passed, including a
	// unit that was ineligible, not enrolled or had no id; "" otherwise.
	Run string
	// Enrolled reports whether the unit counts in Run: true only for
	// ReasonRollout and ReasonExperiment.
	Enrolled bool
}

// Domain labels keep the three hashes of one unit independent.
const (
	previewDomain = schemaV1 + separator + "preview" + separator
	enrollDomain  = schemaV1 + separator + "enroll" + separator
	variantDomain = schemaV1 + separator + "variant" + separator
)

// PreviewHash is how a preview list names a unit of the change key: 8
// lowercase hex digits of the unit's hash, so a snapshot served to a browser
// never carries raw user ids.
func PreviewHash(key, unitID string) string {
	return fmt.Sprintf("%08x", murmur3([]byte(previewDomain+key+"|"+unitID), 0))
}

// Evaluate decides the change key for ec. It never fails: a missing
// snapshot or key is ReasonUnknown and a malformed change ReasonInvalid, both
// with the caller's default. The same snapshot, key and context always give
// the same result.
func Evaluate(s *Snapshot, key string, ec EvalContext) Result {
	if s == nil {
		return Result{Reason: ReasonUnknown}
	}
	c, ok := s.changes[key]
	if !ok {
		return Result{Reason: ReasonUnknown}
	}
	if c.invalid {
		return Result{Reason: ReasonInvalid}
	}
	// Off is the kill switch: it silences previews too.
	if c.mode == modeOff {
		return c.fallback(ReasonDefault, "")
	}
	// Under every other mode a listed unit keeps its preview variant, and is
	// never measured.
	unitID := c.unitID(ec)
	if unitID != "" && len(c.preview) > 0 {
		if v, ok := c.preview[PreviewHash(key, unitID)]; ok {
			return c.chose(v, ReasonPreview, "", false)
		}
	}
	switch c.mode {
	case modePreview:
		return c.fallback(ReasonDefault, "")
	case modeEveryone:
		return c.chose(c.variant, ReasonEveryone, "", false)
	}

	if unitID == "" {
		return c.fallback(ReasonNoUnit, c.run)
	}
	if a := c.audience; a != nil {
		v, ok := attribute(ec, a.attribute)
		if !ok || !a.in[v] {
			return c.fallback(ReasonIneligible, c.run)
		}
	}
	if bucket(enrollDomain+key+"|"+c.salt+"|"+unitID) >= c.percentBP {
		return c.fallback(ReasonNotEnrolled, c.run)
	}
	if c.mode == modeRollout {
		return c.chose(c.rolloutVariant, ReasonRollout, c.run, true)
	}
	v := bucket(variantDomain + key + "|" + c.salt + "|" + unitID)
	cumulative := 0
	for _, e := range c.split {
		cumulative += e.weightBP
		if v < cumulative {
			return c.chose(e.variant, ReasonExperiment, c.run, true)
		}
	}
	// Unreachable: ParseSnapshot holds a split's weights to 10000.
	return Result{Reason: ReasonInvalid}
}

func (c *change) unitID(ec EvalContext) string {
	if !c.hasUnit {
		return ""
	}
	if c.unitAttr == "" {
		return ec.TargetingKey
	}
	return ec.Attributes[c.unitAttr]
}

// attribute reads an audience attribute: targetingKey is the context's
// TargetingKey (absent when empty), anything else an attribute by name.
func attribute(ec EvalContext, name string) (string, bool) {
	if name == targetingKeyAttribute {
		return ec.TargetingKey, ec.TargetingKey != ""
	}
	v, ok := ec.Attributes[name]
	return v, ok
}

// chose returns variant with a copy of its value, so a caller that edits the
// result cannot reach into the shared snapshot.
func (c *change) chose(variant string, reason Reason, run string, enrolled bool) Result {
	return Result{Variant: variant, Value: bytes.Clone(c.variants[variant]), Reason: reason, Run: run, Enrolled: enrolled}
}

// fallback is the default behavior: the declared default variant, or the
// caller's default when there is none.
func (c *change) fallback(reason Reason, run string) Result {
	if c.defaultVariant == "" {
		return Result{Reason: reason, Run: run}
	}
	return c.chose(c.defaultVariant, reason, run, false)
}
