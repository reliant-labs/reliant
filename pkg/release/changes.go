package release

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"unicode/utf8"
)

// Forge Changes declared in the release. A project declares each change it
// can serve — its key, value type, variants and default — once, in
// changes.yaml at its root, and forge copies that declaration into every
// env's shape. The control plane decides WHO gets which variant (live,
// audited delivery state); the release decides what a change CAN be, so the
// platform refuses to launch or ramp a variant the env's current release does
// not declare. See pkg/changes for the evaluation model.

// The value types a change declares ([ShapeChange.Type]).
const (
	ChangeTypeBool   = "bool"
	ChangeTypeString = "string"
	ChangeTypeNumber = "number"
	// ChangeTypeJSON is any JSON value but null: an object, an array or a
	// scalar the app decodes itself.
	ChangeTypeJSON = "json"
)

// Bounds of a change declaration. A shape is stored per bundle and read per
// env, so a declaration stays small; a large configuration document belongs
// in the app, keyed by a string variant.
const (
	// MaxShapeChanges is the most changes one shape declares.
	MaxShapeChanges = 200
	// MaxChangeVariants is the most variants one change declares.
	MaxChangeVariants = 32
	// MaxChangeValueBytes bounds one variant's compacted JSON value.
	MaxChangeValueBytes = 8 << 10
	// MaxChangeDescription bounds a change's description, in bytes.
	MaxChangeDescription = 500
)

var (
	// changeKeyRE is a change key: the snapshot's and the control plane's
	// key rule (forge.changes/v1). It never holds the hash separator "|".
	changeKeyRE = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)
	// changeVariantRE is a variant name.
	changeVariantRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
)

// ShapeChange is one declared change (Shape.Changes, keyed by the change
// key): the variants it can take, which one is the default, and the type
// every variant's value has. It carries no delivery state — who gets which
// variant is the control plane's, and changes without a deploy.
//
// The values are the author's declared configuration, committed to the repo
// and served to browsers in the env's snapshot. They are never secrets.
type ShapeChange struct {
	// Type is the value type of every variant: one of the ChangeType*
	// values.
	Type string `json:"type"`
	// Default names the variant that applies when the change is off, and
	// to every caller its delivery does not select. It is one of Variants.
	Default string `json:"default"`
	// Variants maps each variant name to its JSON value, compacted.
	Variants map[string]json.RawMessage `json:"variants"`
	// Description says what the change is for, for a person reading a list.
	Description string `json:"description,omitempty"`
}

// ValidChangeKey reports whether key is a valid change key:
// ^[a-z][a-z0-9_.-]{0,63}$.
func ValidChangeKey(key string) bool { return changeKeyRE.MatchString(key) }

// ValidChangeVariant reports whether name is a valid variant name:
// ^[a-z][a-z0-9_-]{0,31}$.
func ValidChangeVariant(name string) bool { return changeVariantRE.MatchString(name) }

// ValidChangeType reports whether typ is one of the ChangeType* values.
func ValidChangeType(typ string) bool {
	switch typ {
	case ChangeTypeBool, ChangeTypeString, ChangeTypeNumber, ChangeTypeJSON:
		return true
	}
	return false
}

// CheckChangeValue reports why value is not a valid variant value of a
// change of type typ, or nil when it is: well-formed JSON, never null, at
// most [MaxChangeValueBytes] compacted, and of the declared type — a finite
// number for "number".
func CheckChangeValue(typ string, value json.RawMessage) error {
	var buf bytes.Buffer
	if err := json.Compact(&buf, value); err != nil {
		return fmt.Errorf("is not valid JSON: %v", err)
	}
	if buf.Len() > MaxChangeValueBytes {
		return fmt.Errorf("is %d bytes of JSON (at most %d)", buf.Len(), MaxChangeValueBytes)
	}
	compact := buf.Bytes()
	if string(compact) == "null" {
		return fmt.Errorf("is null (a variant needs a value)")
	}
	switch typ {
	case ChangeTypeBool:
		if string(compact) != "true" && string(compact) != "false" {
			return fmt.Errorf("is %s, not a bool", compact)
		}
	case ChangeTypeString:
		var s string
		if json.Unmarshal(compact, &s) != nil {
			return fmt.Errorf("is %s, not a string", compact)
		}
	case ChangeTypeNumber:
		var n json.Number
		if compact[0] == '"' || json.Unmarshal(compact, &n) != nil {
			return fmt.Errorf("is %s, not a number", compact)
		}
		f, err := strconv.ParseFloat(n.String(), 64)
		if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
			return fmt.Errorf("is %s, not a finite 64-bit number", compact)
		}
	case ChangeTypeJSON:
	default:
		return fmt.Errorf("has an unknown type %q", typ)
	}
	return nil
}

// validate checks one declared change, keyed key.
func (c ShapeChange) validate(key string) error {
	if !ValidChangeKey(key) {
		return fmt.Errorf("%w: shape change key %q (expected %s)", ErrInvalid, key, changeKeyRE)
	}
	if !ValidChangeType(c.Type) {
		return fmt.Errorf("%w: shape change %q type %q (expected bool, string, number or json)", ErrInvalid, key, c.Type)
	}
	if len(c.Variants) == 0 || len(c.Variants) > MaxChangeVariants {
		return fmt.Errorf("%w: shape change %q declares %d variants (expected 1..%d)", ErrInvalid, key, len(c.Variants), MaxChangeVariants)
	}
	for name, value := range c.Variants {
		if !ValidChangeVariant(name) {
			return fmt.Errorf("%w: shape change %q variant name %q (expected %s)", ErrInvalid, key, name, changeVariantRE)
		}
		if err := CheckChangeValue(c.Type, value); err != nil {
			return fmt.Errorf("%w: shape change %q variant %q %v", ErrInvalid, key, name, err)
		}
	}
	if _, ok := c.Variants[c.Default]; !ok {
		return fmt.Errorf("%w: shape change %q default %q is not one of its variants", ErrInvalid, key, c.Default)
	}
	if len(c.Description) > MaxChangeDescription || !utf8.ValidString(c.Description) {
		return fmt.Errorf("%w: shape change %q description is not UTF-8 of at most %d bytes", ErrInvalid, key, MaxChangeDescription)
	}
	return nil
}

func validateShapeChanges(cs map[string]ShapeChange) error {
	if len(cs) > MaxShapeChanges {
		return fmt.Errorf("%w: shape declares %d changes (at most %d)", ErrInvalid, len(cs), MaxShapeChanges)
	}
	for key, c := range cs {
		if err := c.validate(key); err != nil {
			return err
		}
	}
	return nil
}

// canonicalShapeChanges copies cs with every value compacted. Nil for none,
// so a shape that declares no change carries no key.
func canonicalShapeChanges(cs map[string]ShapeChange) map[string]ShapeChange {
	if len(cs) == 0 {
		return nil
	}
	out := make(map[string]ShapeChange, len(cs))
	for key, c := range cs {
		vs := make(map[string]json.RawMessage, len(c.Variants))
		for name, v := range c.Variants {
			var buf bytes.Buffer
			if json.Compact(&buf, v) == nil {
				v = buf.Bytes()
			}
			vs[name] = bytes.Clone(v)
		}
		c.Variants = vs
		out[key] = c
	}
	return out
}
