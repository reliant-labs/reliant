package seedplan

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

// The scalar kinds a value list used to be unable to say.
//
// vocab.yaml began as a way to name WORDS — a list of strings, later numeric
// ranges and JSON documents — and every value it held rendered as a quoted
// string or a bare number. Three kinds every application has fell through:
//
//   - NULL. yaml decodes a null into a []string as nothing at all, so
//     `[null, "x"]` silently became `["x"]` and `[null]` failed with "has no
//     values". A nullable column could not be told to be empty sometimes.
//   - Booleans. A BOOLEAN column was refused outright, so its true/false ratio
//     was always the hash's coin flip.
//   - Time. TIMESTAMPTZ and DATE columns were refused too, and synthesis put
//     every one of them in January 2024 — a dev database whose "upcoming jobs"
//     page was empty because every job was two years in the past.
//
// Each is now a value like any other, drawn with the same column-local pick:
// a repeated entry weights the draw, whatever its kind.

// vocabNull is how a YAML null travels through a value pool. A pool is a
// []string everywhere downstream (UNIQUE draws, ordering, unions), so the null
// is a sentinel no YAML scalar can spell — a NUL byte cannot appear in a YAML
// document — and the single renderer, poolLiteral, turns it into NULL.
const vocabNull = "\x00null"

// vocabList is a YAML sequence of plain values. It exists because decoding
// straight into []string drops nulls; this keeps them, as vocabNull.
type vocabList []string

// UnmarshalYAML accepts a sequence of scalars. A null (`null`, `~`, or an
// empty entry) becomes vocabNull; the QUOTED string "null" stays a string.
// Anything nested — a list in a list, a mapping — is an error naming the line,
// because a value pool holds values.
func (l *vocabList) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.SequenceNode {
		return fmt.Errorf("line %d: expected a list of values", node.Line)
	}
	out := make(vocabList, 0, len(node.Content))
	for _, item := range node.Content {
		if item.Kind == yaml.AliasNode && item.Alias != nil {
			item = item.Alias
		}
		if item.Kind != yaml.ScalarNode {
			return fmt.Errorf("line %d: a value list holds plain values (strings, numbers, true/false, null), not nested lists or mappings", item.Line)
		}
		if item.Tag == "!!null" {
			out = append(out, vocabNull)
			continue
		}
		out = append(out, item.Value)
	}
	*l = out
	return nil
}

// relTimeRange is the `{from, to, step}` entry shape: an interval of instants
// written as offsets from now. It is kept as offsets until ApplyVocab, which
// knows both the anchor (Config.Now) and the column (a DATE steps by days).
type relTimeRange struct {
	from, to time.Duration
	// step is the declared spacing, or 0 for the column's default (a day for
	// DATE, a minute otherwise).
	step time.Duration
}

// relOffsetRE is the offset grammar: `now`, or a signed whole number of
// minutes (m), hours (h), days (d) or weeks (w).
var relOffsetRE = regexp.MustCompile(`^([+-]?)(\d+)(m|h|d|w)$`)

// relOffsetGrammar is how the grammar reads in an error message.
const relOffsetGrammar = "`now` or a signed offset such as -90d, +2w, -6h, +30m"

// parseRelOffset reads one offset token. ok is false for anything else —
// callers decide whether that is an error (a range bound) or simply not a
// relative token (an absolute timestamp in a list).
func parseRelOffset(s string) (time.Duration, bool) {
	s = strings.TrimSpace(s)
	if strings.EqualFold(s, "now") {
		return 0, true
	}
	m := relOffsetRE.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	n, err := strconv.ParseInt(m[2], 10, 64)
	if err != nil || n > 100*366 { // a century of days bounds every unit sanely
		return 0, false
	}
	unit := map[string]time.Duration{
		"m": time.Minute,
		"h": time.Hour,
		"d": 24 * time.Hour,
		"w": 7 * 24 * time.Hour,
	}[m[3]]
	d := time.Duration(n) * unit
	if m[1] == "-" {
		d = -d
	}
	return d, true
}

// optionalNode returns n, or nil when the mapping did not set the field (a
// zero yaml.Node). The decoder fills a yaml.Node VALUE field; it will not
// allocate a *yaml.Node.
func optionalNode(n *yaml.Node) *yaml.Node {
	if n.Kind == 0 {
		return nil
	}
	return n
}

// formatOffset renders a duration in the offset grammar the author writes —
// the largest unit that divides it evenly, so a warning about `-2w` says
// `-2w` rather than `-336h0m0s`. signed prefixes a + on positive values (a
// range bound); a step is unsigned.
func formatOffset(d time.Duration, signed bool) string {
	if d == 0 && signed {
		return "now"
	}
	sign := ""
	if d < 0 {
		sign, d = "-", -d
	} else if signed {
		sign = "+"
	}
	for _, u := range []struct {
		name string
		size time.Duration
	}{{"w", 7 * 24 * time.Hour}, {"d", 24 * time.Hour}, {"h", time.Hour}, {"m", time.Minute}} {
		if d%u.size == 0 {
			return sign + strconv.FormatInt(int64(d/u.size), 10) + u.name
		}
	}
	return sign + d.String()
}

// decodeTimeRange builds a relTimeRange from a mapping's from/to/step fields,
// failing with the mapping's line on anything malformed.
func decodeTimeRange(line int, from, to *string, step *yaml.Node) (*relTimeRange, error) {
	if from == nil || to == nil {
		return nil, fmt.Errorf("line %d: a time range needs both from and to (each %s)", line, relOffsetGrammar)
	}
	lo, ok := parseRelOffset(*from)
	if !ok {
		return nil, fmt.Errorf("line %d: time range from %q is not %s", line, *from, relOffsetGrammar)
	}
	hi, ok := parseRelOffset(*to)
	if !ok {
		return nil, fmt.Errorf("line %d: time range to %q is not %s", line, *to, relOffsetGrammar)
	}
	if hi < lo {
		return nil, fmt.Errorf("line %d: time range to (%s) is before from (%s)", line, *to, *from)
	}
	r := &relTimeRange{from: lo, to: hi}
	if step != nil {
		d, ok := parseRelOffset(step.Value)
		if !ok || d <= 0 {
			return nil, fmt.Errorf("line %d: time range step %q must be a positive offset such as 1d, 6h or 30m", line, step.Value)
		}
		r.step = d
	}
	return r, nil
}

// expand renders the range as a pool of instants anchored at now, low to high.
// It reuses the numeric range's stride (in seconds), so a span too wide for
// numericRangeMaxValues at the declared step widens to a MULTIPLE of it and
// still reaches both ends — the same rule, and the same warning, as a
// numeric {min, max, step}.
func (r relTimeRange) expand(now time.Time, col schemadef.Column) (values []string, widened time.Duration) {
	step := r.step
	if step == 0 {
		step = time.Minute
		if isDateColumn(col) {
			step = 24 * time.Hour
		}
	}
	secs := numericRange{
		min:  int64(r.from / time.Second),
		max:  int64(r.to / time.Second),
		step: int64(step / time.Second),
	}
	offsets, wid := secs.expand()
	for _, o := range offsets {
		n, err := strconv.ParseInt(o, 10, 64)
		if err != nil {
			continue
		}
		values = append(values, now.Add(time.Duration(n)*time.Second).UTC().Format(timeLiteralLayout))
	}
	if wid > 0 && r.step != 0 {
		widened = time.Duration(wid) * time.Second
	}
	return values, widened
}

// absoluteTimeLayouts are the absolute spellings a time column's list accepts,
// besides the relative tokens. A zoneless spelling is read as UTC.
var absoluteTimeLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05Z07:00",
	"2006-01-02 15:04:05",
	"2006-01-02",
}

// normalizeTimeValue reads one list value for a time column — a relative token
// or an absolute date/time — as the instant it names, spelled the one way
// every seeded instant is spelled. ok is false when it is neither.
func normalizeTimeValue(val string, now time.Time) (string, bool) {
	if d, ok := parseRelOffset(val); ok {
		return now.Add(d).UTC().Format(timeLiteralLayout), true
	}
	for _, layout := range absoluteTimeLayouts {
		if at, err := time.Parse(layout, strings.TrimSpace(val)); err == nil {
			return at.UTC().Format(timeLiteralLayout), true
		}
	}
	return "", false
}

// normalizeBoolValue reads one list value for a boolean column. YAML already
// spells an unquoted true/false that way; case is folded so `True` reads too.
func normalizeBoolValue(val string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(val)) {
	case "true":
		return "true", true
	case "false":
		return "false", true
	}
	return "", false
}
