package changes

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
)

// schemaV1 is the only snapshot schema this package reads.
const schemaV1 = "forge.changes/v1"

// maxRevision is the largest revision both twins read exactly: JavaScript
// numbers are IEEE doubles, so a revision past 2^53-1 would compare
// differently in the browser.
const maxRevision = 1<<53 - 1

// separator joins the fields of a hash input. A change key or salt that
// contains it could make two different (key, salt) pairs hash the same input,
// so it makes the change invalid. The unit id comes last and may contain it.
const separator = "|"

// fullBP is 100% in basis points: a split's weights sum to it, and a
// percentage is at most it.
const fullBP = 10000

// Snapshot is a parsed, validated policy snapshot: every change's declared
// variants and its current delivery. It is immutable and safe for concurrent
// use. A change that failed validation is kept, so Evaluate can report it as
// ReasonInvalid without affecting the others.
type Snapshot struct {
	revision int64
	changes  map[string]*change
}

// Revision is the snapshot's revision number, which grows with every
// published edit.
func (s *Snapshot) Revision() int64 {
	if s == nil {
		return 0
	}
	return s.revision
}

type mode int

const (
	modeOff mode = iota
	modePreview
	modeEveryone
	modeRollout
	modeExperiment
)

var modes = map[string]mode{
	"off":        modeOff,
	"preview":    modePreview,
	"everyone":   modeEveryone,
	"rollout":    modeRollout,
	"experiment": modeExperiment,
}

// units maps a delivery's unit to where Evaluate reads its id: "" is the
// context's TargetingKey, anything else an attribute of that name.
var units = map[string]string{
	"user":    "",
	"account": "account",
	"visitor": "visitor",
}

// targetingKeyAttribute names the context's TargetingKey in an audience.
const targetingKeyAttribute = "targetingKey"

var unitHashRE = regexp.MustCompile(`^[0-9a-f]{8}$`)

// change is one change as Evaluate reads it. When invalid is set nothing
// else is read.
type change struct {
	invalid        bool
	variants       map[string]json.RawMessage
	defaultVariant string
	mode           mode
	hasUnit        bool
	unitAttr       string
	run            string
	salt           string
	audience       *audience
	percentBP      int
	split          []splitEntry
	rolloutVariant string
	variant        string
	preview        map[string]string // unit hash → variant; the first listed wins
}

type audience struct {
	attribute string
	in        map[string]bool
}

type splitEntry struct {
	variant  string
	weightBP int
}

// ParseSnapshot parses and validates a "forge.changes/v1" snapshot. It fails
// only when the snapshot as a whole cannot be read — malformed JSON, an
// unknown schema, a missing or non-integer revision, or changes that are not
// an object — and the caller should then keep its last good snapshot. A
// single malformed change does not fail the parse: it evaluates as
// ReasonInvalid and the others are unaffected. Unknown fields are ignored,
// so a v1 snapshot may grow fields without breaking older readers.
func ParseSnapshot(data []byte) (*Snapshot, error) {
	root, err := decodeObject(data)
	if err != nil {
		return nil, fmt.Errorf("changes: snapshot: %w", err)
	}
	schema, ok, err := root.str("schema")
	if err != nil || !ok {
		return nil, errors.New("changes: snapshot: schema is missing or not a string")
	}
	if schema != schemaV1 {
		return nil, fmt.Errorf("changes: snapshot: unknown schema %q (this reader knows %q)", schema, schemaV1)
	}
	rev, ok, err := root.integer("revision", 0, maxRevision)
	if err != nil || !ok {
		return nil, errors.New("changes: snapshot: revision is missing or not an integer in 0..2^53-1")
	}
	s := &Snapshot{revision: rev, changes: map[string]*change{}}
	if !root.has("changes") {
		return s, nil
	}
	list, err := decodeObject(root["changes"])
	if err != nil {
		return nil, fmt.Errorf("changes: snapshot: changes: %w", err)
	}
	for key, raw := range list {
		c, err := parseChange(raw)
		if err != nil || strings.Contains(key, separator) {
			c = &change{invalid: true}
		}
		s.changes[key] = c
	}
	return s, nil
}

func parseChange(raw json.RawMessage) (*change, error) {
	obj, err := decodeObject(raw)
	if err != nil {
		return nil, err
	}
	c := &change{variants: map[string]json.RawMessage{}}

	if !obj.has("variants") {
		return nil, errors.New("variants is missing")
	}
	vars, err := decodeObject(obj["variants"])
	if err != nil {
		return nil, fmt.Errorf("variants: %w", err)
	}
	for name, value := range vars {
		if name == "" {
			return nil, errors.New("a variant has an empty name")
		}
		if isNull(value) {
			return nil, fmt.Errorf("variant %q has a null value", name)
		}
		var buf bytes.Buffer
		if err := json.Compact(&buf, value); err != nil {
			return nil, err
		}
		c.variants[name] = json.RawMessage(buf.Bytes())
	}
	declared := func(name string) bool { _, ok := c.variants[name]; return ok }

	if c.defaultVariant, err = obj.declaredVariant("default_variant", declared); err != nil {
		return nil, err
	}

	if !obj.has("delivery") {
		return nil, errors.New("delivery is missing")
	}
	d, err := decodeObject(obj["delivery"])
	if err != nil {
		return nil, fmt.Errorf("delivery: %w", err)
	}
	modeName, _, err := d.str("mode")
	if err != nil {
		return nil, err
	}
	m, ok := modes[modeName]
	if !ok {
		return nil, fmt.Errorf("unknown mode %q", modeName)
	}
	c.mode = m

	unitName, hasUnit, err := d.str("unit")
	if err != nil {
		return nil, err
	}
	if hasUnit {
		attr, ok := units[unitName]
		if !ok {
			return nil, fmt.Errorf("unknown unit %q", unitName)
		}
		c.hasUnit, c.unitAttr = true, attr
	}
	if c.run, _, err = d.str("run"); err != nil {
		return nil, err
	}
	if c.salt, _, err = d.str("salt"); err != nil {
		return nil, err
	}
	if strings.Contains(c.salt, separator) {
		return nil, errors.New("salt contains the hash separator")
	}
	pct, hasPct, err := d.integer("percent_bp", 0, fullBP)
	if err != nil {
		return nil, err
	}
	c.percentBP = int(pct)
	if c.variant, err = d.declaredVariant("variant", declared); err != nil {
		return nil, err
	}
	if c.rolloutVariant, err = d.declaredVariant("rollout_variant", declared); err != nil {
		return nil, err
	}
	if c.audience, err = parseAudience(d); err != nil {
		return nil, err
	}
	if c.split, err = parseSplit(d, declared); err != nil {
		return nil, err
	}
	if c.preview, err = parsePreview(d, declared); err != nil {
		return nil, err
	}

	// What each mode needs to decide anything.
	if len(c.preview) > 0 && !c.hasUnit {
		return nil, errors.New("preview needs a unit")
	}
	switch c.mode {
	case modeEveryone:
		if c.variant == "" {
			return nil, errors.New("everyone needs a variant")
		}
	case modeRollout, modeExperiment:
		if !c.hasUnit || c.salt == "" || !hasPct {
			return nil, errors.New("rollout and experiment need a unit, a salt and percent_bp")
		}
		if c.mode == modeRollout && c.rolloutVariant == "" {
			return nil, errors.New("rollout needs a rollout_variant")
		}
		if c.mode == modeExperiment && (c.run == "" || len(c.split) == 0) {
			return nil, errors.New("experiment needs a run and a split")
		}
	}
	return c, nil
}

func parseAudience(d object) (*audience, error) {
	if !d.has("audience") {
		return nil, nil
	}
	obj, err := decodeObject(d["audience"])
	if err != nil {
		return nil, fmt.Errorf("audience: %w", err)
	}
	attr, _, err := obj.str("attribute")
	if err != nil || attr == "" {
		return nil, errors.New("audience needs an attribute")
	}
	// Each value is checked as a string: encoding/json would decode a null
	// element into "" where the web twin rejects it.
	var in []json.RawMessage
	if !obj.has("in") || json.Unmarshal(obj["in"], &in) != nil {
		return nil, errors.New("audience needs an array of strings in \"in\"")
	}
	a := &audience{attribute: attr, in: map[string]bool{}}
	for _, raw := range in {
		var v string
		if isNull(raw) || json.Unmarshal(raw, &v) != nil {
			return nil, errors.New("audience \"in\" holds a value that is not a string")
		}
		a.in[v] = true
	}
	return a, nil
}

func parseSplit(d object, declared func(string) bool) ([]splitEntry, error) {
	if !d.has("split") {
		return nil, nil
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(d["split"], &raw); err != nil {
		return nil, errors.New("split is not an array")
	}
	split := make([]splitEntry, 0, len(raw))
	sum := 0
	for _, r := range raw {
		obj, err := decodeObject(r)
		if err != nil {
			return nil, fmt.Errorf("split: %w", err)
		}
		name, _, err := obj.str("variant")
		if err != nil || !declared(name) {
			return nil, fmt.Errorf("split names undeclared variant %q", name)
		}
		w, ok, err := obj.integer("weight_bp", 0, fullBP)
		if err != nil || !ok {
			return nil, errors.New("split weight_bp is missing or not an integer in 0..10000")
		}
		sum += int(w)
		split = append(split, splitEntry{variant: name, weightBP: int(w)})
	}
	if len(split) > 0 && sum != fullBP {
		return nil, fmt.Errorf("split weights sum to %d, not %d", sum, fullBP)
	}
	return split, nil
}

func parsePreview(d object, declared func(string) bool) (map[string]string, error) {
	if !d.has("preview") {
		return nil, nil
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(d["preview"], &raw); err != nil {
		return nil, errors.New("preview is not an array")
	}
	preview := map[string]string{}
	for _, r := range raw {
		obj, err := decodeObject(r)
		if err != nil {
			return nil, fmt.Errorf("preview: %w", err)
		}
		h, _, err := obj.str("unit_hash")
		if err != nil || !unitHashRE.MatchString(h) {
			return nil, fmt.Errorf("preview unit_hash %q is not 8 lowercase hex digits", h)
		}
		name, _, err := obj.str("variant")
		if err != nil || !declared(name) {
			return nil, fmt.Errorf("preview names undeclared variant %q", name)
		}
		if _, dup := preview[h]; !dup {
			preview[h] = name
		}
	}
	return preview, nil
}

// object is a JSON object read by exact key. encoding/json matches struct
// fields case-insensitively, which JSON.parse in the web twin does not; the
// twins must agree on every snapshot, so neither decodes into a struct.
type object map[string]json.RawMessage

func decodeObject(data []byte) (object, error) {
	var o object
	if err := json.Unmarshal(data, &o); err != nil || o == nil {
		return nil, errors.New("not a JSON object")
	}
	return o, nil
}

func isNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// has reports whether key is present and not null: both twins read an
// explicit null as absent.
func (o object) has(key string) bool {
	raw, ok := o[key]
	return ok && !isNull(raw)
}

// str reads an optional string field.
func (o object) str(key string) (string, bool, error) {
	if !o.has(key) {
		return "", false, nil
	}
	var s string
	if err := json.Unmarshal(o[key], &s); err != nil {
		return "", false, fmt.Errorf("%s is not a string", key)
	}
	return s, true, nil
}

// integer reads an optional integer field in lo..hi. A number is an integer
// when its value is, as JavaScript's Number.isInteger decides: 2000.0 and
// 2e3 are both 2000.
func (o object) integer(key string, lo, hi int64) (int64, bool, error) {
	if !o.has(key) {
		return 0, false, nil
	}
	var f float64
	if err := json.Unmarshal(o[key], &f); err != nil {
		return 0, false, fmt.Errorf("%s is not a number", key)
	}
	if f < float64(lo) || f > float64(hi) || f != math.Trunc(f) {
		return 0, false, fmt.Errorf("%s is not an integer in %d..%d", key, lo, hi)
	}
	return int64(f), true, nil
}

// declaredVariant reads an optional variant reference, which must name a
// declared variant when it is set.
func (o object) declaredVariant(key string, declared func(string) bool) (string, error) {
	name, ok, err := o.str(key)
	if err != nil {
		return "", err
	}
	if ok && !declared(name) {
		return "", fmt.Errorf("%s names undeclared variant %q", key, name)
	}
	return name, nil
}
