package seedplan

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

// Constraint satisfaction for values forge SYNTHESIZES.
//
// The seeder deals with two kinds of value, and they get opposite treatment:
//
//   - Values the AUTHOR supplies (db/seeds/vocab.yaml) are VALIDATED: anything
//     not provably insertable is skipped with a warning and the column falls
//     back to built-in synthesis (see vocab.go). forge cannot fix someone
//     else's value, so refusing it is the only honest answer.
//
//   - Values FORGE synthesizes are satisfied BY CONSTRUCTION. Skipping one is
//     not even well-defined — a NOT NULL column has no "skip", and dropping the
//     row would make the plan's row count a lie and orphan the FK children that
//     index into it. Since forge derived the constraint (from the proto field
//     rules it projected into the migration), forge must generate inside it.
//
// This file is the second half: the per-column constraint model synthesis is
// fitted to. It closes the gap that made `forge db seed` unusable on a real
// schema — the planner modelled CHECK vocabularies (EnumPools) and numeric
// ranges (CheckBounds) but NOT the two other constraint shapes forge's own DDL
// emits: char_length/varchar length caps, and single-column UNIQUE.
//
// Anything still unsatisfiable after this pass is a SCHEMA fact, knowable
// before a single INSERT — a UNIQUE column whose CHECK vocabulary holds fewer
// values than the row target can never carry that many rows. Those cap the
// table's row count at plan time and say so, rather than letting postgres abort
// the transaction (and roll back every table that already succeeded) mid-run.

// FitLength forces s into [minLen, maxLen] CHARACTERS — 0 means unbounded on
// that side — by truncating, or padding with '0'. Counted in runes, not bytes,
// because that is what postgres's char_length() and a varchar(n) cap count.
// Shared by seed synthesis here and codegen's CRUD lifecycle-test fixtures.
func FitLength(s string, minLen, maxLen int) string {
	if maxLen > 0 && utf8.RuneCountInString(s) > maxLen {
		s = string([]rune(s)[:maxLen])
	}
	for utf8.RuneCountInString(s) < minLen {
		s += "0"
	}
	return s
}

// lengthBoundsOf returns a column's (min, max) character bounds, memoized.
// It reads the SAME model vocab validation reads (LengthBounds over the
// introspected table), so a value forge synthesizes and a value the author
// supplies are judged against one constraint model, not two.
func (p *Plan) lengthBoundsOf(table string, col schemadef.Column) (int, int) {
	if b, ok := p.lenBounds[table][col.Name]; ok {
		return b[0], b[1]
	}
	minLen, maxLen := LengthBounds(p.byName[table], col)
	if p.lenBounds == nil {
		p.lenBounds = map[string]map[string][2]int{}
	}
	if p.lenBounds[table] == nil {
		p.lenBounds[table] = map[string][2]int{}
	}
	p.lenBounds[table][col.Name] = [2]int{minLen, maxLen}
	return minLen, maxLen
}

// quotedRaw decodes a single-quoted SQL string literal back to its raw value.
// ok is false for every other literal shape (NULL, bare numerics, ARRAY[...],
// '\x'::bytea) — those carry no character length to fit.
func quotedRaw(lit string) (string, bool) {
	if len(lit) < 2 || !strings.HasPrefix(lit, "'") || !strings.HasSuffix(lit, "'") {
		return "", false
	}
	return strings.ReplaceAll(lit[1:len(lit)-1], "''", "'"), true
}

// fitStringLiteral re-fits an already-rendered string literal into the column's
// length bounds. Non-string literals pass through untouched.
//
// When the value has to be TRUNCATED, the row discriminator is re-applied at
// the tail: `sample_room_code_7` under a varchar(4) would otherwise collapse to
// a column of identical "samp" values, since truncation cuts off exactly the
// part that varied. Untruncated values are returned byte-for-byte — a
// pattern-derived value already satisfies the shape its CHECK declares, and a
// tail edit would corrupt it.
func fitStringLiteral(lit string, minLen, maxLen, row int) string {
	if minLen == 0 && maxLen == 0 {
		return lit
	}
	raw, ok := quotedRaw(lit)
	if !ok {
		return lit
	}
	if maxLen > 0 && utf8.RuneCountInString(raw) > maxLen {
		if v, ok := distinctVariant(raw, row+1, minLen, maxLen); ok {
			return sqlString(v)
		}
	}
	fitted := FitLength(raw, minLen, maxLen)
	if fitted == raw {
		return lit
	}
	return sqlString(fitted)
}

// closedPool returns the FINITE set of values a column draws from — the
// author's vocabulary overlay, else the column's CHECK/enum vocabulary. A
// column with a closed pool cannot be made unique by inventing new values (a
// suffixed enum label leaves the vocabulary and violates the CHECK), so
// uniqueness there means drawing WITHOUT replacement, and the pool's size is a
// hard ceiling on the table's row count.
func (p *Plan) closedPool(table string, col schemadef.Column) ([]string, bool) {
	if vals := p.vocab[table][col.Name]; len(vals) > 0 {
		return vals, true
	}
	if pool, ok := p.pools.get(table, col.Name); ok {
		return SeedEnumChoices(pool), true
	}
	return nil, false
}

// poolLiteral renders a pool value — the ONE renderer for anything drawn from
// a vocabulary or CHECK pool: NULL for a vocab null, bare for numeric and
// boolean columns, single-quoted otherwise.
func poolLiteral(col schemadef.Column, v string) string {
	switch {
	case v == vocabNull:
		return "NULL"
	case col.Type == schemadef.TypeInt || col.Type == schemadef.TypeFloat || col.Type == schemadef.TypeBool:
		return v
	}
	return sqlString(v)
}

// permute returns a deterministic, salt-stable shuffle of vals. Fisher-Yates
// driven by the same cellHash every other draw uses, and INDEPENDENT of the row
// count — so raising `rows` appends values rather than reshuffling the ones
// already seeded, exactly like every other column.
func permute(vals []string, salt int, table, column string) []string {
	out := append([]string(nil), vals...)
	for i := len(out) - 1; i > 0; i-- {
		j := int(cellHash(salt, table, column+"#perm", i) % uint64(i+1))
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// distinctVariant derives the k'th alternative spelling of s that still fits
// [minLen, maxLen]. The discriminator goes at the END, and the base is
// truncated to make room for it, so the result honors the length cap that
// forced the variation in the first place. ok is false when the cap cannot even
// hold the discriminator — the column has run out of distinct values.
func distinctVariant(s string, k, minLen, maxLen int) (string, bool) {
	suffix := strconv.Itoa(k)
	if maxLen > 0 {
		if len(suffix) > maxLen {
			return "", false
		}
		base := []rune(s)
		if keep := maxLen - len(suffix); len(base) > keep {
			base = base[:keep]
		}
		s = string(base) + suffix
	} else {
		s = s + "-" + suffix
	}
	return FitLength(s, minLen, maxLen), true
}

// assignUnique computes the per-row literals for a single-column-UNIQUE data
// column. The returned slice may be SHORTER than tp.n when the column cannot
// supply that many distinct values; the caller caps the table's row count at
// its length (the same treatment a 1-1 UNIQUE foreign key already gets) and
// surfaces the accompanying warning.
//
// fold is the case fold the index keys on ("lower"/"upper" for
// `UNIQUE (lower(name))`, "" for a plain one). It changes what DISTINCT
// means: under a fold, "Acme" and "ACME" are the same key, so the used-set
// is keyed case-insensitively. Values are still emitted in their natural
// casing — the index folds them, the data does not have to.
func (p *Plan) assignUnique(tp *tablePlan, col schemadef.Column, fold string) ([]string, string) {
	table := tp.table.Name
	n := tp.n
	// key maps a candidate literal to the value the INDEX compares.
	key := func(lit string) string { return lit }
	if fold != "" {
		key = strings.ToLower
	}

	// Closed vocabulary → draw WITHOUT replacement. This is the case pool
	// depth alone could never fix: with replacement, a 24-value pool drew only
	// 10 distinct values across 20 rows.
	if pool, ok := p.closedPool(table, col); ok {
		vals := permute(pool, p.cfg.EffectiveSalt(), table, col.Name)
		// Under a fold, two pool entries differing only in case are ONE
		// index key, so the usable depth is the folded-distinct count, not
		// len(pool). Collapsing here keeps the cap honest instead of
		// discovering the collision at INSERT time.
		if fold != "" {
			seen := make(map[string]bool, len(vals))
			kept := vals[:0]
			for _, v := range vals {
				if k := strings.ToLower(v); !seen[k] {
					seen[k] = true
					kept = append(kept, v)
				}
			}
			vals = kept
		}
		warn := ""
		if len(vals) < n {
			warn = fmt.Sprintf("seed plan: %s.%s is UNIQUE but draws from a %d-value vocabulary — %s capped to %d row(s) (a UNIQUE column cannot repeat a value)",
				table, col.Name, len(vals), table, len(vals))
		} else {
			vals = vals[:n]
		}
		lits := make([]string, len(vals))
		for i, v := range vals {
			lits[i] = poolLiteral(col, v)
		}
		return lits, warn
	}

	// Open synthesis → keep each row's natural value and probe deterministically
	// on a collision, so the seeded data still reads like the column's own
	// vocabulary instead of a mangled counter.
	minLen, maxLen := p.lengthBoundsOf(table, col)
	bound, _ := p.bounds.get(table, col.Name)
	// A numbered variant is a NEW value, and the column's pattern CHECK is a
	// declaration about every value — so a variant that leaves the pattern is
	// not a candidate. Without this the probe trades a duplicate-key abort for
	// a check-constraint abort, which is the same transactional rollback.
	admits := patternAdmitter(p.byName[table], col)
	limit := n + 64
	used := make(map[string]bool, n)
	lits := make([]string, 0, n)
	for i := 0; i < n; i++ {
		lit := p.valueLiteral(table, col, i)
		next, ok := "", false
		switch raw, isStr := quotedRaw(lit); {
		case col.Type == schemadef.TypeJSON && !col.IsArray:
			// A json/jsonb document renders as a quoted literal and would
			// otherwise probe as a STRING — and probeString's variants append
			// a discriminator, which turns `{}` into `{}-1`: not a document,
			// and rejected by postgres at parse time. The column has exactly
			// one candidate (the document the schema declares), so the honest
			// answer is the row cap below, not a mangled second value.
			next, ok = lit, !used[key(lit)]
		case isStr:
			next, ok = probeStringFold(raw, used, limit, minLen, maxLen, admits, key)
		case col.Type == schemadef.TypeInt:
			next, ok = probeInt(lit, used, limit, bound)
		case col.Type == schemadef.TypeBytes:
			// Tested AFTER the string case on purpose: an overlay value on a
			// bytea column renders as a quoted literal and probes as a string,
			// exactly like any other authored value.
			next, ok = probeBytes(col.Name, i, used, limit, minLen, maxLen)
		default:
			// Every other literal shape (timestamps, JSON documents, arrays):
			// no safe way to invent a distinct variant, so the natural value
			// is the only candidate.
			next, ok = lit, !used[key(lit)]
		}
		if !ok {
			return lits, fmt.Sprintf("seed plan: %s.%s is UNIQUE but its constraints admit only %d distinct value(s) — %s capped to %d row(s)",
				table, col.Name, len(lits), table, len(lits))
		}
		used[key(next)] = true
		lits = append(lits, next)
	}
	return lits, ""
}

// probeString finds the first unused spelling of raw: the value itself, then
// its length-fitted numbered variants. Both the lookup and the returned value
// are the rendered LITERAL, so they key the caller's used-set identically.
// admits rejects a variant the column's pattern CHECK would not accept; it is
// nil for a column with no such CHECK.
// key maps a candidate to the value the INDEX compares, which is how a
// case-folded UNIQUE index is satisfied: under `UNIQUE (lower(name))` "Acme"
// and "ACME" are ONE key, so the probe must treat them as equal while still
// emitting the value in its natural casing. An unfolded index passes identity.
func probeStringFold(raw string, used map[string]bool, limit, minLen, maxLen int, admits func(string) bool, key func(string) string) (string, bool) {
	if lit := sqlString(raw); !used[key(lit)] {
		return lit, true
	}
	for k := 1; k <= limit; k++ {
		alt, ok := distinctVariant(raw, k, minLen, maxLen)
		if !ok {
			return "", false
		}
		if admits != nil && !admits(alt) {
			continue
		}
		if lit := sqlString(alt); !used[key(lit)] {
			return lit, true
		}
	}
	return "", false
}

// probeBytes finds the first unused value for a UNIQUE binary column: the
// row's own synthetic payload, then its length-fitted numbered variants —
// byteaOf's own encoding both times, so the candidates key the caller's
// used-set identically to the values cellLiteral will render.
//
// This is the same probe probeString runs, over the same payload; the two
// differ only in the encoding of the literal. Without it a bytea column had no
// candidate at all beyond its natural value, and a UNIQUE one capped its table
// at a single row.
func probeBytes(column string, row int, used map[string]bool, limit, minLen, maxLen int) (string, bool) {
	base := placeholderString(column, row)
	if lit := byteaOf(base, row, minLen, maxLen); !used[lit] {
		return lit, true
	}
	for k := 1; k <= limit; k++ {
		alt, ok := distinctVariant(base, k, minLen, maxLen)
		if !ok {
			return "", false
		}
		if lit := byteaOf(alt, row, minLen, maxLen); !used[lit] {
			return lit, true
		}
	}
	return "", false
}

// patternAdmitter returns a predicate for the pattern CHECKs declared on a
// column, or nil when it has none. An unparseable pattern yields a predicate
// that admits nothing, so a probe degrades to the honest row cap rather than
// writing values postgres will reject.
func patternAdmitter(t schemadef.Table, col schemadef.Column) func(string) bool {
	pats := patternsOf(t, col)
	if len(pats) == 0 {
		return nil
	}
	res := make([]*regexp.Regexp, 0, len(pats))
	for _, p := range pats {
		re, err := regexp.Compile(p)
		if err != nil {
			return func(string) bool { return false }
		}
		res = append(res, re)
	}
	return func(s string) bool {
		for _, re := range res {
			if !re.MatchString(s) {
				return false
			}
		}
		return true
	}
}

// probeInt finds the first unused integer at or above the natural value that
// still satisfies the column's range CHECK.
func probeInt(lit string, used map[string]bool, limit int, b NumBound) (string, bool) {
	v, err := strconv.ParseInt(strings.TrimSpace(lit), 10, 64)
	if err != nil {
		return lit, !used[lit]
	}
	for k := 0; k <= limit; k++ {
		cand := v + int64(k)
		if b.Max != nil && cand > *b.Max {
			break
		}
		if b.Min != nil && cand < *b.Min {
			continue
		}
		s := strconv.FormatInt(cand, 10)
		if !used[s] {
			return s, true
		}
	}
	// Exhausted upward; try downward inside the bound before giving up.
	for k := 1; k <= limit; k++ {
		cand := v - int64(k)
		if b.Min != nil && cand < *b.Min {
			break
		}
		if b.Max != nil && cand > *b.Max {
			continue
		}
		s := strconv.FormatInt(cand, 10)
		if !used[s] {
			return s, true
		}
	}
	return "", false
}

// finalize resolves every row count and UNIQUE-column assignment. It runs at
// the end of the plan's ONLY three mutators (BuildPlan, SetBounds, ApplyVocab),
// so every consumer — Apply, Render, Status, the baked entity factories —
// observes a finalized plan without a new call for a caller to forget.
//
// Idempotent: row counts are recomputed from rowsTarget each time rather than
// ratcheted down, so repeated finalization converges on the same plan.
func (p *Plan) finalize() {
	// The ordering and discriminated-union notes are a property of the
	// SCHEMA, not of the row counts, so they survive every re-finalization
	// unchanged.
	p.planWarns = append([]string(nil), p.orderWarns...)
	p.planWarns = append(p.planWarns, p.unionWarns...)
	p.planWarns = append(p.planWarns, p.unionVocabWarnings()...)
	p.derivedRefs = nil
	p.authRefs = nil
	p.bucketMemo = nil
	p.undeclared = nil
	p.uniques = nil
	p.tuples = nil
	for i := range p.tables {
		name := p.tables[i].table.Name
		p.tables[i].n = p.rowsTarget[name]
		p.rowsOf[name] = p.tables[i].n
	}
	// A minimal plan's column set and union branches. Resolved before the
	// row-count caps and diamonds below, which read requireEdge through the
	// reference path.
	p.applyMinimal()

	// Topological order: a parent's final row count is settled before any child
	// caps against it.
	for i := range p.tables {
		tp := &p.tables[i]
		name := tp.table.Name

		// 1-1 cap: a UNIQUE foreign-key column can reference each parent row at
		// most once, so this table cannot seed more rows than the referenced
		// table has. Self-references are excluded — they resolve per-row
		// (row i -> row i-1), not by this cap.
		for _, fk := range tp.table.ForeignKeys {
			if fk.RefTable == name || !uniqueSingleColumn(tp.table, fk.Column) {
				continue
			}
			if _, seeded := p.byName[fk.RefTable]; !seeded {
				continue
			}
			if pr := p.rowsOf[fk.RefTable]; pr < tp.n {
				tp.n = pr
			}
		}
		p.rowsOf[name] = tp.n

		// Distinct assignments for single-column UNIQUE data columns.
		for _, cp := range tp.cols {
			if cp.col.IsPK || cp.fk != nil || p.managedRole(name, cp.col) == managedDeletedAt {
				continue // referential / managed columns are already unique
			}
			fold, isUnique := uniqueSingleColumnFold(tp.table, cp.col.Name)
			if !isUnique {
				continue
			}
			lits, warn := p.assignUnique(tp, cp.col, fold)
			if warn != "" {
				p.planWarns = append(p.planWarns, warn)
			}
			if len(lits) < tp.n {
				if tp.n = len(lits); tp.n < 1 {
					tp.n = 1
				}
			}
			if p.uniques == nil {
				p.uniques = map[string]map[string][]string{}
			}
			if p.uniques[name] == nil {
				p.uniques[name] = map[string][]string{}
			}
			p.uniques[name][cp.col.Name] = lits
		}

		// Distinct TUPLES for multi-column UNIQUE indexes. It runs after the
		// one-column pass because a member that pass already made distinct
		// settles the tuple on its own, and it may lower tp.n again — so the
		// live count is republished below, before any child caps against it.
		specs, tupleWarns := p.assignTuples(tp)
		p.planWarns = append(p.planWarns, tupleWarns...)
		if len(specs) > 0 {
			if p.tuples == nil {
				p.tuples = map[string][]tupleAssign{}
			}
			p.tuples[name] = specs
		}
		p.rowsOf[name] = tp.n
	}

	// Diamonds are resolved LAST: the walk uses real parent assignments, and
	// those hash-pick against the row counts settled above.
	p.derivedRefs, p.authRefs, p.undeclared = p.resolveDiamonds()

	// The ordering pass's overlay refusals are collected AFTER the row counts
	// settle: the walk asks each planned row whether its declared pool could
	// be honored, so it has to run against the live count rather than the
	// target one a UNIQUE cap may since have lowered.
	p.planWarns = append(p.planWarns, p.orderVocabWarnings()...)
}
