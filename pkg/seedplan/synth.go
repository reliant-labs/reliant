package seedplan

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"regexp"
	"regexp/syntax"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

// What forge invents, and what it refuses to invent.
//
// Everything in this file derives a value from something the author DECLARED:
// the column's canonical type, its CHECK vocabulary, its pattern, its length
// and range bounds, its NOT NULL, and the referential structure around it. No
// branch reads the column's NAME to decide what the column MEANS.
//
// It used to. `price` / `amount` / `*_cents` were money, `last4` was a payment
// card, `date_of_birth` was a birthdate, `role` drew from {admin, member,
// viewer, editor, owner}, `name` drew from a company pool unless sibling
// columns made the table look person-ish. Every one of those is a DECISION —
// what noun belongs in a column — and a decision is not in the schema, so
// forge cannot derive it. Guessing produced values that were right for the
// vocabulary the guess was written against and silently wrong everywhere
// else: a `users(id UUID)` table whose row 0 was pinned to the non-UUID
// `dev-user-001` failed the INSERT and, because the seed is one transaction,
// took the entire dataset down with it.
//
// The declaration surface for what forge cannot derive is
// db/seeds/vocab.yaml. See doc.go.
//
// What an undeclared column gets instead is a value that is type-correct,
// deterministic, inside every declared constraint — and SELF-EVIDENTLY
// synthetic (see SyntheticStringPrefix). That last property is not cosmetic.
// Realistic-looking fake data is what let `["enterprise","free"]` sit in a
// clinic's order line items and pass its CHECK, and what let `dev-user-001`
// read like an id right up until it met a UUID column. Data that announces
// itself as invented can be told apart from data the app produced, at a
// glance, without a query.

// SyntheticStringPrefix is the stamp every string forge INVENTS carries.
//
// It is what makes a seeded database readable at a glance: a value carrying
// the stamp was made up by the generator, and a value without it came from
// somewhere that knows the domain — a CHECK vocabulary, db/seeds/vocab.yaml,
// or db/seeds/custom. Exported so that assertions about synthesized data
// derive the set they check from the emitter's own mark instead of a copied
// list of literals that dies silently the day the emitter changes.
const SyntheticStringPrefix = "sample_"

// seedNamespace is a fixed UUID namespace for deterministic UUIDv5
// generation. Arbitrary UUIDv4 chosen once — changing it changes every
// generated PK. (Preserved from the legacy emitter so upgraded projects
// keep stable PKs.)
var seedNamespace = [16]byte{
	0x6b, 0xa7, 0xb8, 0x10, 0x9d, 0xad, 0x11, 0xd1,
	0x80, 0xb4, 0x00, 0xc0, 0x4f, 0xd4, 0x30, 0xc8,
}

// deterministicUUID produces a stable UUIDv5-style value from salt + name.
// The same (salt, name) always yields the same UUID; different salts yield a
// different-but-stable dataset.
func deterministicUUID(salt int, name string) string {
	h := sha1.New()
	h.Write(seedNamespace[:])
	_, _ = fmt.Fprintf(h, "salt=%d|%s", salt, name)
	sum := h.Sum(nil)
	sum[6] = (sum[6] & 0x0f) | 0x50 // version 5
	sum[8] = (sum[8] & 0x3f) | 0x80 // variant
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}

// cellHash is the stateless per-cell selector: a pure function of
// (salt, table, column, row). Pool indexing and boolean/nullable dice all
// derive from it, so adding a column never reshuffles other columns' values
// (each column's hash is independent) and adding rows never changes prior
// rows. fnv-1a is stable across Go versions.
//
// The column name enters here as a HASH KEY, which is the one role a name
// plays in this package: it keeps each column's draw independent of its
// neighbours. It never selects between behaviors.
//
// The AVALANCHE step is not decoration. FNV-1a's low bit is a LINEAR
// function of its input — the prime is odd, so the multiply preserves bit
// 0, and only the XOR changes it — which made `cellHash(...) % 2` reduce to
// the parity of the input bytes. salt, table and row contribute identically
// to two columns of the same row, so the relationship between two boolean
// columns was fixed by the parity of their NAMES: opposite parity meant
// they NEVER agreed, same parity meant they never differed, at every salt
// and every row. A products table could only ever seed two of the four
// (active, requires_prescription) combinations, leaving the app path that
// needs the third with no row to run on. Mixing makes every output bit a
// nonlinear function of the whole input, so `% n` is sound for n = 2 the
// same way it already was for larger pools.
func cellHash(salt int, table, column string, row int) uint64 {
	h := fnv.New64a()
	_, _ = fmt.Fprintf(h, "%d|%s|%s|%d", salt, table, column, row)
	return avalanche(h.Sum64())
}

// avalanche is the splitmix64 finalizer: the standard, fixed sequence that
// spreads every input bit across every output bit. Deterministic and
// version-stable (plain arithmetic on uint64), so the seeder's "same
// (schema, config, vocab) renders byte-identically" guarantee is untouched
// — the values it renders are simply different from the pre-mix ones.
func avalanche(x uint64) uint64 {
	x ^= x >> 30
	x *= 0xbf58476d1ce4e5b9
	x ^= x >> 27
	x *= 0x94d049bb133111eb
	x ^= x >> 31
	return x
}

// pick returns cellHash(...) reduced into [0,n). n must be > 0.
func pick(salt int, table, column string, row, n int) int {
	if n <= 0 {
		return 0
	}
	return int(cellHash(salt, table, column, row) % uint64(n))
}

// sqlString single-quotes and escapes a string literal.
func sqlString(v string) string {
	return "'" + strings.ReplaceAll(v, "'", "''") + "'"
}

// pkLiteral returns the SQL literal for a primary-key column at row i, IN THE
// COLUMN'S OWN TYPE. FK values are derived from this same function against the
// referenced table, so every reference resolves by construction — which is
// exactly why the type matters on both sides.
//
// It used to have two arms, int and "everything else is a UUID". A sole `id`
// key is one of those two on every schema forge itself writes, so the shortcut
// held for as long as the only keys forge met were forge's own. It does not
// hold for a COMPOSITE key, which is where the other types live: a rollup keyed
// by (org_id, period_start), a daily table keyed by (org_id, usage_date). Those
// members are timestamps and dates, and a UUID in one is not a value postgres
// declines to like — it is a value postgres cannot parse:
//
//	invalid input syntax for type timestamp with time zone:
//	"4b7b0dcd-311a-5c51-a830-2dfc1f8597ef"
//
// and, because the seed is one transaction, that aborts the whole dataset. The
// same defect reached FOREIGN KEYS through referencedValue, whose job is to
// re-derive the parent's own key literal: an FK onto a timestamp key member got
// a UUID for the same reason, so the type switch fixes both.
//
// A TIME member steps one DAY per row (see keyTimeLiteral) rather than the
// 28-day cycle an ordinary time column takes: a key member has to be distinct
// per row, and a DATE column truncates anything finer than a day.
//
// composite reports whether the column is one member of a multi-column key.
// A sole key column keys its UUID on "<table>.<row>" — unchanged, because
// those ids are a documented stable surface (db/seeding: "stable for a given
// salt") that callers query and paste into fixtures. A composite member mixes
// its own name in, since members of one row would otherwise all derive from
// the same "<table>.<row>" string and collide with each other.
func pkLiteral(cfg Config, table string, col schemadef.Column, i int, composite bool) string {
	switch col.Type {
	case schemadef.TypeInt:
		return fmt.Sprintf("%d", i+1)
	case schemadef.TypeFloat:
		return fmt.Sprintf("%d.00", i+1)
	case schemadef.TypeBool:
		// Two values is all a boolean has. It cannot key a table on its own,
		// and as a composite member the OTHER members carry the distinctness.
		if i%2 == 0 {
			return "true"
		}
		return "false"
	case schemadef.TypeTime:
		return keyTimeLiteral(col, i)
	case schemadef.TypeBytes:
		return byteaOf(placeholderString(col.Name, i), i, 0, 0)
	case schemadef.TypeJSON:
		// A jsonb key member is exotic but legal; a UUID string is not a JSON
		// document, so emit one that is.
		return sqlString(fmt.Sprintf(`{"row": %d}`, i))
	default: // string / uuid
		key := fmt.Sprintf("%s.%d", table, i)
		if composite {
			key = fmt.Sprintf("%s.%s.%d", table, col.Name, i)
		}
		return sqlString(deterministicUUID(cfg.EffectiveSalt(), key))
	}
}

// keyTimeLiteral is the instant a TIME primary-key member carries: one
// distinct DAY per row, counted from a fixed base.
//
// The step is a day rather than the hours an ordinary time column spreads
// over, because a key member must be distinct per row and half of them are
// DATE columns, which truncate anything finer. It is rendered date-only for a
// DATE column and as a full instant otherwise — postgres accepts an ISO
// timestamp in a date column, but a seed file is read by people too, and a
// date column whose values all read `T08:00:00Z` says something about the
// column that is not true.
func keyTimeLiteral(col schemadef.Column, i int) string {
	at := keyTimeBase.AddDate(0, 0, i)
	if isDateColumn(col) {
		return sqlString(at.Format("2006-01-02"))
	}
	return sqlString(at.Format("2006-01-02T15:04:05Z"))
}

// keyTimeBase is the instant row 0 of a TIME key member sits at.
var keyTimeBase = time.Date(2024, 1, 1, 8, 0, 0, 0, time.UTC)

// isDateColumn reports whether the column's DECLARED type is a bare DATE —
// the one time type with no time-of-day to render.
func isDateColumn(col schemadef.Column) bool {
	return strings.EqualFold(strings.TrimSpace(col.DeclType), "DATE")
}

// SyntheticUUIDPrefix is the stamp every UUID forge INVENTS for a plain data
// column carries in its first field: `5eed` — "seed", in the hex digits a UUID
// is spelled with.
//
// It is the UUID column's version of SyntheticStringPrefix, and it exists for
// the same reason: a value that announces itself as invented can be told apart
// from data the app produced, at a glance, without a query. A UUID has no room
// for `sample_`, but it has sixteen bytes and the first four characters are
// free — the version and variant bits postgres validates live further in, so
// stamping the head costs nothing and the result is still a well-formed UUID.
//
// KEY columns are deliberately NOT stamped: their ids are a documented stable
// surface callers paste into fixtures, and re-spelling them would change every
// primary key in every existing seeded database.
const SyntheticUUIDPrefix = "5eed"

// syntheticUUID is the UUID a plain data column of type uuid carries at one
// row: a pure function of (table, column, row), stamped as invented.
//
// Deterministic in the same sense as everything else here — a hash, never a
// PRNG and never uuid.New() — and SALT-INDEPENDENT, which is the property it
// shares with the `sample_<column>_<n>` placeholder it replaces rather than
// with a key. Salt varies which value a column DRAWS from a pool; a column
// forge invents a value for outright has no pool to draw from, and SynthString
// (the one synthesizer both the seeder and the generated CRUD fixtures call)
// has no salt to consult.
func syntheticUUID(table, column string, row int) string {
	u := deterministicUUID(0, fmt.Sprintf("synthetic|%s|%s|%d", table, column, row))
	return SyntheticUUIDPrefix + u[len(SyntheticUUIDPrefix):]
}

// isUUIDColumn reports whether the column's DECLARED type is uuid.
//
// The canonical model calls a uuid column a string — correctly, for a wire
// contract — so the declared spelling is the only place the distinction
// survives, and it is the distinction postgres enforces on INSERT. Reading it
// here is not the name-heuristic this file removed: nothing about what the
// column is CALLED is consulted, only the type the author declared.
func isUUIDColumn(col schemadef.Column) bool {
	return strings.EqualFold(strings.TrimSpace(col.DeclType), "UUID")
}

// ──────────────────────────────────────────────────────────────────────
// Managed columns — the ONE classification the seeder acts on, and it does
// not own it.
// ──────────────────────────────────────────────────────────────────────

// managedRole is the role schemadef's column conventions give a column.
type managedRole int

const (
	managedNone managedRole = iota
	managedCreatedAt
	managedUpdatedAt
	managedDeletedAt
)

// managedRoleOf projects a table's schemadef.Conventions onto ONE column.
//
// This is the one place the seeder acts on a column's identity, and the
// judgement is not its own: schemadef.DetectConventions decides, from the
// same columns pkg/crud and the generated ORM read, and the seeder consumes
// the answer. That makes it a DECLARED convention rather than a duplicate
// guess — and the distinction has teeth, because the classification is
// TYPE-GATED. A `deleted_at BIGINT` is not a soft-delete marker (nothing can
// stamp it), so it is an ordinary integer column, and a seeder that matched
// the name alone would write NULL into it.
func managedRoleOf(conv schemadef.Conventions, col schemadef.Column) managedRole {
	switch {
	case conv.SoftDelete && col.Name == schemadef.ColDeletedAt:
		return managedDeletedAt
	case conv.Timestamps && col.Name == schemadef.ColCreatedAt:
		return managedCreatedAt
	case conv.Timestamps && col.Name == schemadef.ColUpdatedAt:
		return managedUpdatedAt
	}
	return managedNone
}

// managedTimestampLiteral is what a managed timestamp carries. The values are
// not a guess about the domain — they are the convention's own arithmetic:
// created is stamped once at insert, updated on every write, so updated sits
// after created; a soft-deleting table seeds LIVE rows, so the marker is
// NULL. TIMESTAMPTZ and the legacy TEXT spelling both take this literal,
// which is why the role is resolved before the type switch.
//
// Both sit inside the four weeks before now (see windowInstant), one day per
// row: created rows read as "made recently", never as "made in the future".
func managedTimestampLiteral(now time.Time, role managedRole, i int) string {
	switch role {
	case managedDeletedAt:
		return "NULL"
	case managedUpdatedAt:
		return sqlString(windowInstant(now, i, 12))
	default:
		return sqlString(windowInstant(now, i, 8))
	}
}

// syntheticWindowDays is how far before now a synthesized timestamp reaches.
const syntheticWindowDays = 28

// windowInstant is the instant row-day `day` of the synthetic window lands on:
// `day mod 28` days into the four weeks that END at the start of now's UTC day,
// at `hour` o'clock. Anchoring on the DAY rather than the instant keeps every
// seed run on one day byte-identical, and keeps the hours readable.
func windowInstant(now time.Time, day, hour int) string {
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).
		AddDate(0, 0, -syntheticWindowDays)
	at := start.AddDate(0, 0, day%syntheticWindowDays).Add(time.Duration(hour) * time.Hour)
	return at.Format(timeLiteralLayout)
}

// timeLiteralLayout is how every seeded instant is spelled: RFC 3339 in UTC.
// The ordering placement re-parses it (time.RFC3339), so one spelling
// everywhere keeps a vocab-drawn instant and a synthesized one comparable.
const timeLiteralLayout = "2006-01-02T15:04:05Z"

// ──────────────────────────────────────────────────────────────────────
// Synthesis
// ──────────────────────────────────────────────────────────────────────

// synthScalar produces the SQL literal for a non-PK, non-FK scalar column at
// row i that no vocabulary, CHECK pool or ordering constraint has claimed.
// b pins numeric output into the column's range-CHECK bounds (a zero NumBound
// is a no-op).
func (p *Plan) synthScalar(t schemadef.Table, col schemadef.Column, i int, b NumBound) string {
	if col.IsArray {
		return arrayLiteral(t, col, i)
	}
	if role := managedRoleOf(p.conv[t.Name], col); role != managedNone {
		return managedTimestampLiteral(p.cfg.EffectiveNow(), role, i)
	}

	switch col.Type {
	case schemadef.TypeBool:
		if cellHash(p.cfg.EffectiveSalt(), t.Name, col.Name, i)%2 == 0 {
			return "true"
		}
		return "false"
	case schemadef.TypeInt:
		return strconv.FormatInt(b.clamp(int64(i+1)), 10)
	case schemadef.TypeFloat:
		f := float64(i+1) * 10.5
		if b.Min != nil && f < float64(*b.Min) {
			f = float64(*b.Min)
		}
		if b.Max != nil && f > float64(*b.Max) {
			f = float64(*b.Max)
		}
		return fmt.Sprintf("%.2f", f)
	case schemadef.TypeBytes:
		return byteaLiteral(t, col, i)
	case schemadef.TypeTime:
		return timestampLiteral(p.cfg.EffectiveNow(), i)
	default: // string
		return sqlString(SynthString(t, col, i))
	}
}

// arrayLiteral renders a Postgres ARRAY[...] literal for a real array column.
// A uuid[] element is a uuid like any other — the placeholder string postgres
// rejects in a uuid column it rejects just as flatly inside an array of them.
func arrayLiteral(t schemadef.Table, col schemadef.Column, i int) string {
	switch {
	case col.Type == schemadef.TypeInt:
		return fmt.Sprintf("ARRAY[%d, %d]", i+1, i+2)
	case col.Type == schemadef.TypeFloat:
		return fmt.Sprintf("ARRAY[%.2f, %.2f]", float64(i+1)*1.5, float64(i+2)*1.5)
	case col.Type == schemadef.TypeBool:
		return "ARRAY[true, false]"
	case isUUIDArrayColumn(col):
		return fmt.Sprintf("ARRAY[%s, %s]::uuid[]",
			sqlString(syntheticUUID(t.Name, col.Name, i)),
			sqlString(syntheticUUID(t.Name, col.Name, i+1)))
	default: // string / other → text-ish array
		return fmt.Sprintf("ARRAY[%s, %s]",
			sqlString(placeholderString(col.Name, i)),
			sqlString(placeholderString(col.Name, i+1)))
	}
}

// isUUIDArrayColumn reports whether the column is a uuid[]. Introspection
// spells the declared type of an array with its element type and a `[]`
// suffix, which isUUIDColumn (an exact match) deliberately does not accept.
func isUUIDArrayColumn(col schemadef.Column) bool {
	return col.IsArray && strings.EqualFold(strings.TrimSpace(col.DeclType), "UUID[]")
}

// timestampLiteral is the instant an UNMANAGED time column carries: one
// deterministic point per row, spread across the four weeks before now (see
// windowInstant), offset a week from the managed columns' cycle. A column
// that must sit above another is not placed here — a two-column ordering
// CHECK is a DECLARATION, and ordering.go satisfies it from the constraint
// rather than from what the columns happen to be called. A column whose
// values MEAN something in time (a schedule in the future, a due date) is
// described in db/seeds/vocab.yaml with a range relative to now.
func timestampLiteral(now time.Time, i int) string {
	return sqlString(windowInstant(now, i+7, 8))
}

// placeholderString is the value an undeclared string column carries:
// the emitter's stamp, the column's own name as a LABEL, and the row number.
// Deterministic, distinct per row (so a UNIQUE column is satisfiable without
// probing), and unmistakably synthetic.
func placeholderString(column string, i int) string {
	return SyntheticStringPrefix + column + "_" + strconv.Itoa(i+1)
}

// byteaLiteral is what an OPAQUE binary column carries.
//
// It used to be the empty `'\x'::bytea` — one value, on every row, for every
// bytea column in the schema. Nothing in the type says what the bytes mean, so
// the constant read like the honest answer; it is not, because emptiness is
// not the only thing forge knows. It knows the ROW, and a value that does not
// vary with the row makes a `key_hash BYTEA NOT NULL UNIQUE` column admit
// exactly one distinct value, which caps its table at ONE ROW and leaves every
// dev or e2e scenario that needs two API keys unseedable.
//
// So the payload is the SAME synthetic string a text column would get,
// hex-encoded: deterministic (a pure function of column and row like every
// other cell), distinct per row, fitted to whatever `length(col)` bound the
// schema declares, and self-evidently invented — `convert_from(key_hash,
// 'UTF8')` reads back `sample_key_hash_1`, which no real hash ever does.
// Inventing plausible-looking RANDOM bytes would satisfy the same constraints
// and lose exactly that property.
func byteaLiteral(t schemadef.Table, col schemadef.Column, row int) string {
	minLen, maxLen := LengthBounds(t, col)
	return byteaOf(placeholderString(col.Name, row), row, minLen, maxLen)
}

// byteaOf renders raw as postgres's hex bytea literal, fitted to [minLen,
// maxLen] BYTES — which is what `length()`/`octet_length()` count on a bytea
// column, and what LengthBounds reads off such a CHECK.
//
// The payload is ASCII, so a byte is a rune and FitLength's rune arithmetic is
// the byte arithmetic. When the cap forces a TRUNCATION the row discriminator
// is re-applied at the tail, for the same reason fitStringLiteral does it:
// truncation cuts off exactly the part that varied, and a bytea column that
// stops varying is the one-row cap all over again.
func byteaOf(raw string, row, minLen, maxLen int) string {
	if maxLen > 0 && len(raw) > maxLen {
		if v, ok := distinctVariant(raw, row+1, minLen, maxLen); ok {
			raw = v
		}
	}
	return `'\x` + hex.EncodeToString([]byte(FitLength(raw, minLen, maxLen))) + `'::bytea`
}

// SynthString is the string forge invents for a column no vocabulary claims.
//
// A column whose CHECK declares a PATTERN gets a value derived from that
// pattern; every other column gets the placeholder. Both are derivations from
// the declaration — neither reads what the column is called for anything but
// the label — and the pattern branch is strictly more general than the name
// heuristics it replaced: those satisfied an email-format CHECK on a column
// spelled `email` and violated the identical CHECK on one spelled `contact`,
// while knowing nothing at all about a project's own `^SKU-[0-9]{4}$`.
//
// Exported for the generated CRUD lifecycle test, whose create fixtures mint
// rows against the very same schema and must satisfy the very same CHECKs.
func SynthString(t schemadef.Table, col schemadef.Column, row int) string {
	// The declared type comes FIRST: postgres parses a uuid column's value
	// before it evaluates any CHECK on it, so `sample_user_id_1` is not a
	// value that fails a rule — it is a value the column cannot hold, and the
	// INSERT dies with `invalid input syntax for type uuid` taking the whole
	// transactional seed with it.
	if isUUIDColumn(col) {
		return syntheticUUID(t.Name, col.Name, row)
	}
	if pats := patternsOf(t, col); len(pats) > 0 {
		minLen, _ := LengthBounds(t, col)
		if v, ok := patternSample(pats, row, minLen); ok {
			return v
		}
	}
	return placeholderString(col.Name, row)
}

// ──────────────────────────────────────────────────────────────────────
// Pattern-derived values
// ──────────────────────────────────────────────────────────────────────

// patternCheckRE reads a regular-expression CHECK out of the canonical
// constraint definition postgres renders: `CHECK ((code ~ '^A[0-9]+$'::text))`,
// or `CHECK (((code)::text ~* '...'::text))` when the column is a varchar.
// Both operators are captured — `~*` is the case-insensitive form, and a value
// that satisfies the pattern satisfies it either way.
var patternCheckRE = regexp.MustCompile(`\(?"?(\w+)"?\)?(?:::[a-z_ ]+)?\s*~\*?\s*'((?:[^']|'')*)'`)

// patternsOf returns every regular expression the column's own CHECK
// constraints require its value to match. Read off the APPLIED schema, which
// is where the requirement is declared — the same source LengthBounds and the
// range bounds already read.
func patternsOf(t schemadef.Table, col schemadef.Column) []string {
	var out []string
	for _, ck := range t.Checks {
		if len(ck.Columns) != 1 || ck.Columns[0] != col.Name {
			continue
		}
		for _, m := range patternCheckRE.FindAllStringSubmatch(ck.Def, -1) {
			if m[1] != col.Name {
				continue
			}
			out = append(out, strings.ReplaceAll(m[2], "''", "'"))
		}
	}
	return out
}

// patternSample derives a string that MATCHES every declared pattern, from the
// patterns' own parsed syntax. ok is false when nothing it can build provably
// satisfies them — the caller then falls back to the placeholder, and the
// INSERT reports the conflict rather than forge quietly writing a row that
// contradicts a rule the schema states.
//
// The value is built from the FIRST pattern and verified against all of them:
// a string satisfying several independent regexes is not constructible in
// general, and claiming otherwise is how a seeder ends up aborting a
// transaction it promised would apply.
//
// row enters as the filler for repeated single-character shapes, so
// `^[^@\s]+@[^@\s]+\.[^@\s]+$` yields 1@1.1, 2@2.2, … — deterministic,
// distinct per row (which is what a UNIQUE column needs), and visibly
// invented.
func patternSample(pats []string, row, minLen int) (string, bool) {
	compiled := make([]*regexp.Regexp, 0, len(pats))
	for _, p := range pats {
		re, err := regexp.Compile(p)
		if err != nil {
			return "", false
		}
		compiled = append(compiled, re)
	}
	// NOT Simplify()d: simplification expands `x{4}` into four independent
	// copies, and the count is exactly what tells the emitter how wide a fixed
	// field is — `^SKU-[0-9]{4}$` seeds SKU-0001, SKU-0002, … only because the
	// 4 survives to be read.
	rx, err := syntax.Parse(pats[0], syntax.Perl)
	if err != nil {
		return "", false
	}

	// runLen is how many characters an UNBOUNDED repetition (+ or *) emits. It
	// starts at one and grows until the value reaches any char_length minimum
	// the schema also declares, so a column carrying both a pattern and a
	// minimum satisfies both BY CONSTRUCTION instead of being padded afterwards
	// into something the pattern rejects. A counted repetition (`{4}`) is not
	// affected: its width is declared, not chosen. The loop terminates at
	// runLen > minLen, which also covers a pattern with no unbounded run at all
	// (its length never grows, and forcing it would be inventing characters the
	// pattern does not admit).
	filler := strconv.Itoa(row + 1)
	for runLen := 1; ; runLen++ {
		var b strings.Builder
		if !emitPattern(rx, filler, runLen, &b) {
			return "", false
		}
		s := b.String()
		if utf8.RuneCountInString(s) >= minLen || runLen > minLen {
			for _, re := range compiled {
				if !re.MatchString(s) {
					return "", false
				}
			}
			return s, true
		}
	}
}

// emitPattern writes one string matching re into b. false means the shape is
// one this generator does not build — the caller must not claim a match it
// cannot demonstrate.
func emitPattern(re *syntax.Regexp, filler string, runLen int, b *strings.Builder) bool { //nolint:gocognit // one flat branch per regexp/syntax operator.
	switch re.Op {
	case syntax.OpEmptyMatch, syntax.OpBeginLine, syntax.OpBeginText,
		syntax.OpEndLine, syntax.OpEndText, syntax.OpWordBoundary, syntax.OpNoWordBoundary:
		return true
	case syntax.OpLiteral:
		b.WriteString(string(re.Rune))
		return true
	case syntax.OpCharClass:
		r, ok := classMember(re.Rune)
		if !ok {
			return false
		}
		b.WriteRune(r)
		return true
	case syntax.OpAnyChar, syntax.OpAnyCharNotNL:
		b.WriteString(filler)
		return true
	case syntax.OpCapture:
		return emitPattern(re.Sub[0], filler, runLen, b)
	case syntax.OpConcat:
		for _, sub := range re.Sub {
			if !emitPattern(sub, filler, runLen, b) {
				return false
			}
		}
		return true
	case syntax.OpAlternate:
		for _, sub := range re.Sub {
			var alt strings.Builder
			if emitPattern(sub, filler, runLen, &alt) {
				b.WriteString(alt.String())
				return true
			}
		}
		return false
	case syntax.OpQuest:
		return true // zero occurrences satisfies it
	case syntax.OpStar:
		// A star is satisfied by zero occurrences, so a sub-shape this
		// generator cannot build is not a failure here — but emitting a run
		// when it CAN is what lets `[a-z]*` reach a char_length minimum.
		var run strings.Builder
		if emitRun(re.Sub[0], filler, runLen, &run) {
			b.WriteString(run.String())
		}
		return true
	case syntax.OpPlus:
		return emitRun(re.Sub[0], filler, runLen, b)
	case syntax.OpRepeat:
		return emitRepeat(re, filler, runLen, b)
	default: // OpNoMatch and anything a future Go release adds
		return false
	}
}

// emitRepeat writes the MINIMUM number of occurrences a counted repetition
// needs. A counted run of a character class that admits digits is filled from
// the right with the row's own digits and padded on the left, so a fixed-width
// field varies per row — `[0-9]{4}` seeds 0001, 0002, … instead of 0000 on
// every row, which is what lets such a column carry a UNIQUE index.
func emitRepeat(re *syntax.Regexp, filler string, runLen int, b *strings.Builder) bool {
	n := re.Min
	if n == 0 {
		return true
	}
	if sub := re.Sub[0]; sub.Op == syntax.OpCharClass {
		return emitClassRun(sub.Rune, filler, n, true, b)
	}
	for k := 0; k < n; k++ {
		if !emitPattern(re.Sub[0], filler, runLen, b) {
			return false
		}
	}
	return true
}

// emitRun writes an unbounded repetition's occurrences: a run of runLen
// characters when the repeated shape is a single character class, one
// occurrence otherwise (there is no sound way to repeat an arbitrary
// sub-expression and stay inside a length cap).
func emitRun(sub *syntax.Regexp, filler string, runLen int, b *strings.Builder) bool {
	if sub.Op == syntax.OpCharClass {
		return emitClassRun(sub.Rune, filler, runLen, false, b)
	}
	return emitPattern(sub, filler, runLen, b)
}

// emitClassRun writes characters of a character class into b.
//
// When the class admits the filler's decimal digits they go at the TAIL and the
// class's own member pads the head — `[0-9]{4}` becomes 0001, 0002, … — which
// is what makes a pattern-derived value vary per row without leaving the shape
// the pattern requires, and therefore what lets such a column carry a UNIQUE
// index. A class that admits no digit repeats its member and is row-invariant;
// the UNIQUE assignment then caps the table and says so, rather than writing
// rows postgres rejects.
//
// width is EXACT for a counted repetition — `{4}` is four characters and the
// filler's high-order digits are dropped to fit — and a FLOOR for an unbounded
// one, where dropping them would be the bug it looks like: row 10 and row 20
// would both end in "0" and a UNIQUE column would collide on rows the pattern
// could perfectly well have told apart.
func emitClassRun(ranges []rune, filler string, width int, exact bool, b *strings.Builder) bool {
	m, ok := classMember(ranges)
	if !ok {
		return false
	}
	tail := ""
	if classAdmits(ranges, filler) {
		tail = filler
		if exact && len(tail) > width {
			tail = tail[len(tail)-width:] // low-order digits; a fixed field cannot widen
		}
	}
	if n := width - len(tail); n > 0 {
		b.WriteString(strings.Repeat(string(m), n))
	}
	b.WriteString(tail)
	return true
}

// classAdmits reports whether every rune of s is inside the class.
func classAdmits(ranges []rune, s string) bool {
	for _, r := range s {
		if !inClass(ranges, r) {
			return false
		}
	}
	return true
}

// inClass reports whether r is inside the [lo, hi] pairs regexp/syntax uses to
// represent a character class (already complemented for a negated class).
func inClass(ranges []rune, r rune) bool {
	for i := 0; i+1 < len(ranges); i += 2 {
		if r >= ranges[i] && r <= ranges[i+1] {
			return true
		}
	}
	return false
}

// classMember picks the character a class contributes: the most obviously
// placeholder-ish member available, in a fixed order, so the result is
// deterministic and readable. The preference matters for NEGATED classes —
// `[^@\s]` complements to ranges that START at NUL, so "the first rune in the
// class" would put a control character in the database.
func classMember(ranges []rune) (rune, bool) {
	for _, c := range []rune{'0', 'x', 'a', 'A', '-', '_', '.'} {
		if inClass(ranges, c) {
			return c, true
		}
	}
	for i := 0; i+1 < len(ranges); i += 2 {
		for r := ranges[i]; r <= ranges[i+1] && r <= 0x7e; r++ {
			if r >= 0x21 { // printable, non-space ASCII
				return r, true
			}
		}
	}
	return 0, false
}
