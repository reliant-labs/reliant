package seedplan

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

// Vocab is the optional, user-authored domain-vocabulary overlay for seed
// synthesis: per-column value pools keyed "table.column", resolved from
// db/seeds/vocab.yaml. The app author (usually an LLM, right after writing
// the migrations while domain context is fresh) teaches the seeder the
// project's vocabulary ONCE; matched columns then draw from these pools with
// the exact same deterministic column-local hash-pick the built-ins use, so
// every stability guarantee holds — same (schema, config, vocab) renders
// byte-identically, and adding vocab for one column never reshuffles another.
//
// The overlay supplies VOCABULARY only. Referential machinery (primary keys,
// foreign keys) stays the seeder's job and is
// never overridable; values are validated against the applied schema's
// constraints at ApplyVocab time so a bad value degrades to a warning, never
// a failed seed.
type Vocab struct {
	// Columns maps "table.column" to its resolved value pool (named-pool
	// references are flattened by LoadVocab). A YAML null in a list is kept
	// as an entry that renders NULL.
	Columns map[string][]string
	// Warnings names the entries LoadVocab could resolve but not exactly as
	// written — today, a numeric range whose declared step is too fine to
	// cover it within numericRangeMaxValues. These are load-time notes about
	// the FILE, distinct from ApplyVocab's per-value validation warnings, and
	// ApplyVocab folds them into the plan's warnings so one surface reports
	// both.
	Warnings []string
	// times holds the `{from, to}` entries: instants relative to now, which
	// cannot be resolved to values until ApplyVocab knows the plan's anchor
	// (Config.Now) and the column (a DATE steps by whole days).
	times map[string]relTimeRange
}

// vocabFile is the on-disk YAML shape:
//
//	pools:                       # shared named pools
//	  peptide_names: [BPC-157, Semaglutide]
//	columns:                     # "table.column" -> inline list or {pool: name}
//	  products.name: {pool: peptide_names}
//	  brands.name: [VitalPep, PepCore Labs]
type vocabFile struct {
	Pools   map[string]vocabList  `yaml:"pools"`
	Columns map[string]vocabEntry `yaml:"columns"`
}

// vocabEntry is one columns: value — an inline list of values, a
// {pool: name} reference to a shared named pool, a {type: name} semantic
// type whose values gofakeit generates (see vocabtype.go), a
// {min, max, step} numeric range, or a {from, to, step} time range relative
// to now (see vocabscalar.go).
type vocabEntry struct {
	values    vocabList
	pool      string
	typ       string
	timeRange *relTimeRange
	// widened records a numeric range whose AUTHOR-DECLARED step was too fine
	// to cover it within numericRangeMaxValues, along with the step used
	// instead. The entry cannot phrase the warning itself — it does not know
	// which column it is — so LoadVocab names it against the key. nil means
	// the step was honored exactly, which is every other entry shape.
	widened *widenedStep
}

// widenedStep is one range whose declared granularity gave way to its span.
type widenedStep struct {
	rng      numericRange
	usedStep int64
}

// describe renders the range and its two steps in the author's own units, so
// a `decimals: 2` range reads back in the spelling they wrote rather than in
// the scaled integers this file works in.
func (w widenedStep) describe() (lo, hi, declared, used string) {
	render := func(v int64) string {
		if w.rng.float {
			return strconv.FormatFloat(float64(v)/pow10(w.rng.decimals), 'f', w.rng.decimals, 64)
		}
		return strconv.FormatInt(v, 10)
	}
	return render(w.rng.min), render(w.rng.max), render(w.rng.step), render(w.usedStep)
}

// numericRange is the {min, max, step} entry shape. It exists because an
// unconstrained numeric column otherwise synthesizes as the ROW INDEX
// (i+1), which makes every money column seed as 1,2,3… cents and makes
// two independent numeric columns on the same table perfectly correlated.
// A range expands to a value pool at load time, so everything downstream —
// UNIQUE draws without replacement, determinism, CHECK validation — treats
// it exactly like an authored list and needs no special case.
type numericRange struct {
	min, max, step int64
	float          bool
	decimals       int
}

// expand renders the range as pool literals, low to high, STRIDING across
// [min, max] rather than walking it. widened reports the step it actually
// used when that is coarser than the one declared, so the caller can name it.
//
// The stride is the whole point. Its predecessor walked `v += step` from min
// and stopped at numericRangeMaxValues, which silently truncated every range
// wider than the cap AT ITS FLOOR: a declared {min: 20480, max: 52428800}
// became [20480…20991] — 0.001% of the declared span, with the declared max
// unreachable by construction. Measured on a dogfood run of a document
// workspace, all 20 documents seeded at ~20KB and the GENERATED size_mb column
// read 0.02 for every row, so any UI that buckets or sorts by size was
// exercising nothing. It was invisible for a narrow range (view_count's 481
// values fit under the cap and drew perfectly) and total for a wide one, which
// is why the symptom read as magnitude-sensitive rather than as a cap.
//
// The cap itself is real and stays: a range wide enough to exhaust memory is
// an authoring mistake, not a seed. What changes is which of the author's two
// statements gives way when both cannot hold. The RANGE wins, because a pool
// covering the declaration is the thing they asked for and a pool hugging its
// floor is the useless dataset this exists to prevent; the step gives way, and
// only ever to a MULTIPLE of itself, so a declared granularity is coarsened
// but never violated.
func (r numericRange) expand() (values []string, widened int64) {
	step := r.step
	if step <= 0 {
		step = 1
	}
	// The coarsest the author's own step may stay while still covering the
	// range within the cap. Solving for a stride rather than clamping the
	// count is what puts the last pool member within one step of max.
	if span, ok := spanOf(r.min, r.max); ok && span > 0 {
		if want := ceilDiv(span, numericRangeMaxValues-1); want > step {
			// Round up to a multiple of the declared step: the author asked
			// for that granularity, and landing off-grid would honor neither
			// statement.
			step *= ceilDiv(want, step)
			widened = step
		}
	}
	// The count bound is belt to the stride's braces. The stride alone makes
	// it unreachable for every range that fits in an int64, but `v += step`
	// on a range spanning most of the int64 domain can overflow to a negative
	// v and loop forever, so termination must not depend on the arithmetic
	// being well-behaved.
	for v := r.min; v <= r.max && len(values) < numericRangeMaxValues; v += step {
		if r.float {
			values = append(values, strconv.FormatFloat(float64(v)/pow10(r.decimals), 'f', r.decimals, 64))
		} else {
			values = append(values, strconv.FormatInt(v, 10))
		}
		if v > r.max-step {
			break // the next add would overflow past max
		}
	}
	return values, widened
}

// spanOf returns max-min, reporting !ok when the subtraction overflows int64
// — a range spanning most of the domain, which no stride can divide sensibly.
func spanOf(lo, hi int64) (int64, bool) {
	span := hi - lo
	if (hi > 0 && lo < 0 && span < 0) || (hi < 0 && lo > 0 && span > 0) {
		return 0, false
	}
	return span, true
}

// ceilDiv divides two positive integers, rounding up.
func ceilDiv(a, b int64) int64 {
	if b <= 0 {
		return a
	}
	return (a + b - 1) / b
}

// numericRangeMaxValues bounds a {min,max,step} expansion. 512 distinct
// values is far more than the row counts seeding targets, and it keeps a
// fat-fingered `max: 100000000` from allocating a huge slice.
const numericRangeMaxValues = 512

func pow10(n int) float64 {
	out := 1.0
	for range n {
		out *= 10
	}
	return out
}

// UnmarshalYAML accepts the two sanctioned entry shapes and rejects
// everything else with a line-numbered error (a malformed file must fail
// loudly, not silently seed generic data the author believes is overridden).
func (e *vocabEntry) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.SequenceNode:
		return node.Decode(&e.values)
	case yaml.MappingNode:
		var ref struct {
			Pool     string    `yaml:"pool"`
			Type     string    `yaml:"type"`
			Min      *float64  `yaml:"min"`
			Max      *float64  `yaml:"max"`
			Step     yaml.Node `yaml:"step"` // a number for min/max, an offset for from/to
			Decimals *int      `yaml:"decimals"`
			From     *string   `yaml:"from"`
			To       *string   `yaml:"to"`
		}
		if err := node.Decode(&ref); err != nil {
			return err
		}
		hasRange := ref.Min != nil || ref.Max != nil
		hasTime := ref.From != nil || ref.To != nil
		switch {
		case ref.Pool != "" && ref.Type != "":
			return fmt.Errorf("line %d: a mapping entry sets pool or type, not both", node.Line)
		case hasTime && (hasRange || ref.Pool != "" || ref.Type != ""):
			return fmt.Errorf("line %d: a mapping entry sets pool, type, min/max, or from/to — not several", node.Line)
		case hasRange && (ref.Pool != "" || ref.Type != ""):
			return fmt.Errorf("line %d: a mapping entry sets pool, type, or min/max — not several", node.Line)
		case hasTime:
			r, err := decodeTimeRange(node.Line, ref.From, ref.To, optionalNode(&ref.Step))
			if err != nil {
				return err
			}
			e.timeRange = r
		case hasRange:
			if ref.Min == nil || ref.Max == nil {
				return fmt.Errorf("line %d: a numeric range needs both min and max", node.Line)
			}
			if *ref.Max < *ref.Min {
				return fmt.Errorf("line %d: numeric range max (%v) is below min (%v)", node.Line, *ref.Max, *ref.Min)
			}
			decimals := 0
			if ref.Decimals != nil {
				if *ref.Decimals < 0 || *ref.Decimals > 6 {
					return fmt.Errorf("line %d: decimals must be between 0 and 6", node.Line)
				}
				decimals = *ref.Decimals
			}
			scale := pow10(decimals)
			r := numericRange{
				min:      int64(*ref.Min * scale),
				max:      int64(*ref.Max * scale),
				step:     1,
				float:    decimals > 0,
				decimals: decimals,
			}
			declared := false
			if ref.Step.Kind != 0 {
				step, err := strconv.ParseFloat(ref.Step.Value, 64)
				if err != nil {
					return fmt.Errorf("line %d: step %q is not a number", node.Line, ref.Step.Value)
				}
				if step <= 0 {
					return fmt.Errorf("line %d: step must be positive", node.Line)
				}
				r.step, declared = int64(step*scale), true
			}
			var widened int64
			e.values, widened = r.expand()
			if len(e.values) == 0 {
				return fmt.Errorf("line %d: numeric range produced no values", node.Line)
			}
			// Only a step the AUTHOR wrote is worth reporting. Widening the
			// implicit default is the mechanism working, not a compromise.
			if widened > 0 && declared {
				e.widened = &widenedStep{rng: r, usedStep: widened}
			}
		case ref.Pool != "":
			e.pool = ref.Pool
		case ref.Type != "":
			if !IsVocabType(ref.Type) {
				return fmt.Errorf("line %d: unknown type %q (supported: %s)",
					node.Line, ref.Type, strings.Join(VocabTypeNames(), ", "))
			}
			e.typ = ref.Type
		default:
			return fmt.Errorf("line %d: a mapping entry must be {pool: <name>}, {type: <name>}, {min: <n>, max: <n>} or {from: <offset>, to: <offset>}", node.Line)
		}
		return nil
	default:
		return fmt.Errorf("line %d: a column entry must be a value list, {pool: <name>}, {type: <name>}, {min: <n>, max: <n>} or {from: <offset>, to: <offset>}", node.Line)
	}
}

// VocabPath returns the conventional overlay location for a project's
// migrations directory — db/seeds/vocab.yaml, sibling of the db/seeds/custom
// SQL overlay (both derive from the configured migrations dir the same way).
func VocabPath(migDir string) string {
	return filepath.Join(filepath.Dir(migDir), "seeds", "vocab.yaml")
}

// LoadVocab reads and resolves the vocabulary overlay. A missing file returns
// (nil, nil) — exactly the built-in behavior. A malformed file (bad YAML, an
// entry that is neither a list nor {pool: name}, a reference to an undefined
// pool, an empty pool, a key that is not table.column) is a hard error naming
// the problem. A fully-commented scaffold parses as empty and is a no-op.
func LoadVocab(path string) (*Vocab, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read seed vocab %s: %w", path, err)
	}
	// Strict decoding: an unknown top-level key (`column:` for `columns:`)
	// must fail loudly, not silently seed generic data. A comment-only
	// scaffold decodes to io.EOF — an empty overlay.
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var f vocabFile
	if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parse seed vocab %s: %w", path, err)
	}
	v := &Vocab{Columns: map[string][]string{}}
	for key, e := range f.Columns {
		if !strings.Contains(key, ".") {
			return nil, fmt.Errorf("seed vocab %s: column key %q must be table.column", path, key)
		}
		if e.timeRange != nil {
			// Resolved against the plan's anchor in ApplyVocab.
			if v.times == nil {
				v.times = map[string]relTimeRange{}
			}
			v.times[key] = *e.timeRange
			continue
		}
		vals := []string(e.values)
		switch {
		case e.pool != "":
			pool, ok := f.Pools[e.pool]
			if !ok {
				return nil, fmt.Errorf("seed vocab %s: %s references undefined pool %q", path, key, e.pool)
			}
			vals = []string(pool)
		case e.typ != "":
			// A semantic type is expanded into a value pool HERE, at load, so
			// everything downstream sees one shape: a list of values, already
			// validated against the column's constraints by ApplyVocab like
			// any author-supplied list. Typed values get no special trust —
			// gofakeit does not know the column's length cap, and a generated
			// value that violates one must be skipped with a warning exactly
			// as a hand-written one is.
			table, column, _ := strings.Cut(key, ".")
			generated, err := VocabTypeValues(e.typ, 0, table, column, vocabTypePoolSize)
			if err != nil {
				return nil, fmt.Errorf("seed vocab %s: %s: %w", path, key, err)
			}
			vals = generated
		}
		if len(vals) == 0 {
			return nil, fmt.Errorf("seed vocab %s: %s has no values", path, key)
		}
		if e.widened != nil {
			// The author's range and their step cannot both hold. The range
			// wins — a pool hugging the floor is the useless dataset the
			// stride exists to prevent — but silently is the wrong way to
			// win, so name which statement gave way and by how much.
			lo, hi, declStep, usedStep := e.widened.describe()
			v.Warnings = append(v.Warnings, fmt.Sprintf(
				"seed vocab: %s declares step %s across [%s, %s], which would need more than the %d "+
					"values a pool may hold. Seeding with step %s instead, so the values span the range "+
					"you declared rather than clustering at its floor. "+
					"Write that step to silence this, or narrow the range",
				key, declStep, lo, hi, numericRangeMaxValues, usedStep))
		}
		v.Columns[key] = vals
	}
	sort.Strings(v.Warnings) // deterministic order, like every other warning list
	if len(v.Columns) == 0 && len(v.times) == 0 {
		return nil, nil
	}
	return v, nil
}

// ApplyVocab attaches a vocabulary overlay to the plan: each valid
// (table.column, values) entry becomes the column's draw pool, taking
// precedence over built-in synthesis for that column. Values are validated
// against the column's introspected constraints — call it AFTER bounds are
// attached (BuildLivePlan does) so numeric range CHECKs are visible.
//
// A value it can skip never fails the seed: an invalid value is skipped, a
// column whose vocab is entirely invalid falls back to built-ins, and PK/FK
// columns are never overridable — each with a warning naming table.column and
// the constraint. Warnings are returned AND kept on the plan (VocabWarnings)
// so every consumer of a built plan can surface them. The exception is a null
// on a NOT NULL column: it is skipped here too, but recorded so Validate
// refuses to write the plan. nil vocab is a no-op.
//
// Boolean and time columns take values too: `true`/`false`, and instants
// written absolutely or relative to the plan's Config.Now (`now`, `-3d`,
// `{from: -90d, to: +30d}`).
func (p *Plan) ApplyVocab(v *Vocab) []string {
	if v == nil || (len(v.Columns) == 0 && len(v.times) == 0) {
		return nil
	}
	keys := make([]string, 0, len(v.Columns)+len(v.times))
	for k := range v.Columns {
		keys = append(keys, k)
	}
	for k := range v.times {
		if _, listed := v.Columns[k]; !listed {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys) // deterministic warning order
	p.vocabErrs = nil
	now := p.cfg.EffectiveNow()

	// A load-time note (a widened step) is about the same file and belongs on
	// the same surface as the per-value validation below, so it leads.
	warns := append([]string(nil), v.Warnings...)
	warnf := func(format string, args ...any) {
		warns = append(warns, fmt.Sprintf("seed vocab: "+format, args...))
	}
	for _, key := range keys {
		table, column, _ := strings.Cut(key, ".")
		tp, cp, ok := p.colPlan(table, column)
		if !ok {
			warnf("%s: not a seedable column in the applied schema — ignored", key)
			continue
		}
		col := cp.col
		switch {
		case col.IsPK:
			warnf("%s: primary-key column — the seeder owns referential columns; ignored", key)
			continue
		case cp.fk != nil:
			warnf("%s: foreign-key column — the seeder owns referential columns; ignored", key)
			continue
		case p.managedRole(table, col) == managedDeletedAt:
			warnf("%s: managed soft-delete column (always seeded NULL) — ignored", key)
			continue
		}
		if col.IsArray || !takesVocabulary(col.Type) {
			warnf("%s: column type %s does not take a vocabulary — ignored", key, col.DeclType)
			continue
		}

		candidates := v.Columns[key]
		if r, isRange := v.times[key]; isRange {
			if col.Type != schemadef.TypeTime {
				warnf("%s: a {from, to} time range describes a timestamp or date column, not column type %s — ignored", key, col.DeclType)
				continue
			}
			expanded, widened := r.expand(now, col)
			if widened > 0 {
				warnf("%s declares step %s across [%s, %s], which would need more than the %d values a pool may hold. "+
					"Seeding with step %s instead, so the values span the range you declared rather than clustering at its start. "+
					"Write that step to silence this, or narrow the range",
					key, formatOffset(r.step, false), formatOffset(r.from, true), formatOffset(r.to, true),
					numericRangeMaxValues, formatOffset(widened, false))
			}
			candidates = append(append([]string(nil), candidates...), expanded...)
		}

		pool, hasPool := p.pools.get(tp.table.Name, col.Name)
		bound, _ := p.bounds.get(tp.table.Name, col.Name)
		minLen, maxLen := LengthBounds(p.byName[table], col)
		var kept []string
		for _, val := range candidates {
			if val == vocabNull {
				// NULL satisfies every CHECK and every UNIQUE index; the one
				// thing that rejects it is NOT NULL. That is an authoring
				// mistake rather than a value to drop quietly, so it refuses
				// the seed (Validate) — but the plan still skips it, so the
				// generate-time consumers that never call Validate keep
				// drawing valid values.
				if col.NotNull {
					p.vocabErrs = append(p.vocabErrs, fmt.Sprintf(
						"%s: null on a NOT NULL column — remove the null from the list, or make the column nullable", key))
					continue
				}
				kept = append(kept, val)
				continue
			}
			norm, reason := normalizeVocabValue(col, val, now, pool, hasPool, bound, minLen, maxLen)
			if reason != "" {
				warnf("%s: value %q %s — skipped", key, val, reason)
				continue
			}
			kept = append(kept, norm)
		}
		if len(kept) == 0 {
			warnf("%s: no valid values remain — using built-in synthesis", key)
			continue
		}
		if p.vocab == nil {
			p.vocab = map[string]map[string][]string{}
		}
		if p.vocab[table] == nil {
			p.vocab[table] = map[string][]string{}
		}
		p.vocab[table][column] = kept
	}
	p.vocabWarns = warns
	// The overlay changes which columns draw from a CLOSED pool, which changes
	// how a UNIQUE column can be satisfied — re-resolve the constraint pass.
	p.finalize()
	return warns
}

// VocabWarnings returns the warnings the last ApplyVocab produced. The seed
// never fails on a value it can skip; the CLI surfaces these instead. The one
// vocab problem that DOES refuse the seed — a null on a NOT NULL column — is
// reported by Validate.
func (p *Plan) VocabWarnings() []string { return p.vocabWarns }

// takesVocabulary reports whether a (non-array) column of this canonical type
// draws from a value list. Binary columns are the one scalar kind that does
// not: nothing a YAML scalar spells is a meaningful bytea.
func takesVocabulary(t schemadef.CanonicalType) bool {
	switch t {
	case schemadef.TypeString, schemadef.TypeInt, schemadef.TypeFloat, schemadef.TypeJSON,
		schemadef.TypeBool, schemadef.TypeTime:
		return true
	}
	return false
}

// normalizeVocabValue validates one non-null list value against its column and
// returns the spelling the plan stores. Booleans and instants are normalized
// (`True` → true; `-3d`, `2026-10-01` → an RFC 3339 instant anchored at now);
// every other kind is stored as written and validated by vocabValueProblem.
// A non-empty reason means the value is skipped with a warning.
func normalizeVocabValue(col schemadef.Column, val string, now time.Time, pool []string, hasPool bool, b NumBound, minLen, maxLen int) (string, string) {
	switch col.Type {
	case schemadef.TypeBool:
		if norm, ok := normalizeBoolValue(val); ok {
			return norm, ""
		}
		return "", fmt.Sprintf("is not a boolean (column type %s) — write true or false", col.DeclType)
	case schemadef.TypeTime:
		if norm, ok := normalizeTimeValue(val, now); ok {
			return norm, ""
		}
		return "", fmt.Sprintf("is not a date or time (column type %s) — write an ISO date/time, %s", col.DeclType, relOffsetGrammar)
	}
	return val, vocabValueProblem(col, val, pool, hasPool, b, minLen, maxLen)
}

// uuidLiteralRE matches the canonical 8-4-4-4-12 hex UUID spelling — the only
// value shape a UUID-typed column accepts on INSERT.
var uuidLiteralRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// vocabValueProblem validates one vocab value against the column's type and
// introspected constraints. Returns "" when the value can seed, else a short
// human reason (the caller wraps it with table.column and "skipped"). The
// seed is transactional — one constraint-violating value would abort the
// whole seed — so anything not provably insertable is rejected here.
func vocabValueProblem(col schemadef.Column, val string, pool []string, hasPool bool, b NumBound, minLen, maxLen int) string {
	if hasPool {
		for _, allowed := range pool {
			if val == allowed {
				return ""
			}
		}
		return fmt.Sprintf("is not in the column's CHECK/enum vocabulary %v", pool)
	}
	switch col.Type {
	case schemadef.TypeJSON:
		// A jsonb/json column takes any parseable JSON document; postgres
		// rejects a non-JSON string at INSERT, so reject it here rather than
		// abort the transactional seed.
		if !json.Valid([]byte(val)) {
			return fmt.Sprintf("is not valid JSON (column type %s)", col.DeclType)
		}
	case schemadef.TypeInt:
		n, err := strconv.ParseInt(val, 10, 64)
		if err != nil {
			return fmt.Sprintf("is not an integer (column type %s)", col.DeclType)
		}
		if (b.Min != nil && n < *b.Min) || (b.Max != nil && n > *b.Max) {
			return fmt.Sprintf("is outside the CHECK range %s", b.describe())
		}
	case schemadef.TypeFloat:
		f, err := strconv.ParseFloat(val, 64)
		if err != nil {
			return fmt.Sprintf("is not a number (column type %s)", col.DeclType)
		}
		if (b.Min != nil && f < float64(*b.Min)) || (b.Max != nil && f > float64(*b.Max)) {
			return fmt.Sprintf("is outside the CHECK range %s", b.describe())
		}
	default: // string
		// A UUID-typed column rejects any non-UUID string at INSERT; other
		// exotic string-backed types (inet, macaddr, ...) are rare enough in
		// entity schemas that length/pool checks carry the validation load.
		if strings.EqualFold(col.DeclType, "UUID") && !uuidLiteralRE.MatchString(val) {
			return "is not a UUID (column type UUID)"
		}
		n := utf8.RuneCountInString(val)
		if maxLen > 0 && n > maxLen {
			return fmt.Sprintf("exceeds the %d-char cap (varchar/char_length CHECK)", maxLen)
		}
		if minLen > 0 && n < minLen {
			return fmt.Sprintf("is shorter than the %d-char minimum (char_length CHECK)", minLen)
		}
	}
	return ""
}

// describe renders a NumBound for warnings ("[1,5]", "[100,∞)").
func (b NumBound) describe() string {
	lo, hi := "-∞", "∞"
	if b.Min != nil {
		lo = strconv.FormatInt(*b.Min, 10)
	}
	if b.Max != nil {
		hi = strconv.FormatInt(*b.Max, 10)
	}
	return "[" + lo + "," + hi + "]"
}

// declLenRE captures the length cap of a varchar/char declaration
// ("character varying(20)", "VARCHAR(3)", "char(4)").
var declLenRE = regexp.MustCompile(`(?i)^(?:character varying|character|varchar|char|bpchar)\s*\(\s*(\d+)\s*\)`)

// LengthBounds merges char_length CHECK constraints and the declared
// varchar/char cap into (min, max) character lengths for a column. 0 means
// unbounded on that side; an exact `char_length(col) = N` sets both to N.
// Shared by vocab validation here and codegen's lifecycle-test fixtures.
func LengthBounds(t schemadef.Table, col schemadef.Column) (minLen, maxLen int) {
	// The introspected cap (information_schema.character_maximum_length).
	if col.MaxChars > 0 {
		maxLen = col.MaxChars
	}
	// A hand-built model may instead spell the cap inside DeclType.
	if m := declLenRE.FindStringSubmatch(strings.TrimSpace(col.DeclType)); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil && n > 0 && (maxLen == 0 || n < maxLen) {
			maxLen = n
		}
	}
	// pg_get_constraintdef spells these as e.g.
	// `CHECK ((char_length(code) = 3))` or, for varchar columns,
	// `CHECK ((char_length((code)::text) >= 2))`.
	lenRE := regexp.MustCompile(`(?:char_length|character_length|length)\(\(?` +
		regexp.QuoteMeta(col.Name) + `\)?(?:::[a-z_ ]+)?\)\s*(=|>=|<=|>|<)\s*(\d+)`)
	for _, ck := range t.Checks {
		if len(ck.Columns) != 1 || ck.Columns[0] != col.Name {
			continue
		}
		for _, m := range lenRE.FindAllStringSubmatch(ck.Def, -1) {
			n, err := strconv.Atoi(m[2])
			if err != nil {
				continue
			}
			switch m[1] {
			case "=":
				minLen, maxLen = n, n
			case ">=":
				if n > minLen {
					minLen = n
				}
			case ">":
				if n+1 > minLen {
					minLen = n + 1
				}
			case "<=":
				if maxLen == 0 || n < maxLen {
					maxLen = n
				}
			case "<":
				if maxLen == 0 || n-1 < maxLen {
					maxLen = n - 1
				}
			}
		}
	}
	return minLen, maxLen
}
