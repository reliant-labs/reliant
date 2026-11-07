package seedplan

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

// TableResult reports what one table's INSERT did.
type TableResult struct {
	Table    string
	Inserted int64 // rows actually inserted (ON CONFLICT skips excluded)
}

// Result is the outcome of Apply/Reset.
type Result struct {
	Tables []TableResult
}

// Total returns the total rows inserted across all tables.
func (r *Result) Total() int64 {
	var t int64
	for _, tr := range r.Tables {
		t += tr.Inserted
	}
	return t
}

// TableStatus reports seeded-vs-expected counts for one table.
type TableStatus struct {
	Table    string
	Count    int64 // live row count
	Expected int   // rows the plan would insert
}

// Materialize is the one-call entry: introspect the applied schema (real
// foreign keys), read enum/CHECK pools from the target DB, build the
// deterministic FK-topological plan (with the db/seeds/vocab.yaml overlay
// when present), and INSERT it. migDir is the project's migrations
// directory; db is the already-migrated target. shadowBaseURL is the
// postgres SERVER the migrations get applied to for introspection (empty
// selects pgtest's embedded default). Callers that must surface vocab
// warnings use BuildLivePlan + Apply instead (the CLI does).
func Materialize(ctx context.Context, db *sql.DB, migDir, shadowBaseURL string, cfg Config) (*Result, error) {
	plan, err := BuildLivePlan(ctx, db, migDir, shadowBaseURL, cfg)
	if err != nil {
		return nil, err
	}
	return Apply(ctx, db, plan)
}

// BuildLivePlan builds the deterministic plan from the applied schema + target
// DB enum pools, and overlays the project's domain vocabulary
// (db/seeds/vocab.yaml) when present — a malformed vocab file is a hard
// error, invalid values degrade to plan warnings (VocabWarnings). Exposed so
// status/auto-seed can inspect the plan without applying it.
//
// shadowBaseURL names the server hosting the throwaway database the
// migrations are applied to. It is a PARAMETER rather than something this
// function resolves, because resolving it means reading forge.yaml, .env and
// the project's KCL config — knowledge of a forge project's layout that the
// CLI has and a library must not assume. The CLI passes
// shadowdb.Resolve(projectDir); an empty string falls back to pgtest's
// embedded postgres.
//
// A caller that already HAS the migrated schema in front of it wants
// BuildPlanFromDB instead: it introspects the live database and needs no
// shadow at all.
func BuildLivePlan(ctx context.Context, db *sql.DB, migDir, shadowBaseURL string, cfg Config) (*Plan, error) {
	tables, err := schemadef.ApplyAndIntrospectAt(migDir, shadowBaseURL)
	if err != nil {
		return nil, fmt.Errorf("introspect applied schema: %w", err)
	}
	plan, err := planFromTables(ctx, db, tables, cfg)
	if err != nil {
		return nil, err
	}
	// Nothing in scope (no tables, or database.seed.tables: []) means there is
	// no column for the overlay to describe — applying it would only warn
	// "not a seedable column" once per entry of a perfectly good vocab.yaml.
	if len(plan.tables) == 0 {
		return plan, nil
	}
	// Vocab applies AFTER bounds so numeric validation sees the real range
	// CHECKs. A missing file is a silent no-op (built-in synthesis).
	vocab, err := LoadVocab(VocabPath(migDir))
	if err != nil {
		return nil, err
	}
	plan.ApplyVocab(vocab)
	return plan, nil
}

// Apply executes the plan's INSERTs in a single transaction, in topological
// order, and reports rows inserted per table. Idempotent (ON CONFLICT DO
// NOTHING).
//
// It REFUSES a plan that would write rows contradicting an invariant the
// schema implies but does not state — an undeclared diamond (see diamond.go).
// The gate is here, at the one function that writes rows, rather than in
// BuildPlan: `forge generate` builds one-row plans for the entity factories,
// where two routes to a parent cannot disagree, and blocking codegen on a
// decision that only affects a real dataset would be wrong.
func Apply(ctx context.Context, db *sql.DB, plan *Plan) (*Result, error) {
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	res := &Result{}
	for _, tp := range plan.tables {
		stmt := plan.statement(tp)
		if stmt == "" {
			continue
		}
		r, err := tx.ExecContext(ctx, stmt)
		if err != nil {
			return nil, plan.explainInsertFailure(tp, err)
		}
		n, _ := r.RowsAffected()
		res.Tables = append(res.Tables, TableResult{Table: tp.table.Name, Inserted: n})
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return res, nil
}

// Status returns live-vs-expected row counts for every seedable table.
func Status(ctx context.Context, db *sql.DB, plan *Plan) ([]TableStatus, error) {
	var out []TableStatus
	for _, tp := range plan.tables {
		var n int64
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+QualifiedTable(tp.table)).Scan(&n); err != nil {
			return nil, fmt.Errorf("count %s: %w", tp.table.Name, err)
		}
		out = append(out, TableStatus{Table: tp.table.Name, Count: n, Expected: tp.n})
	}
	return out, nil
}

// AllSeedableTablesEmpty reports whether every seedable table has zero rows —
// the first-boot signal the auto-seed hook uses so it never touches a dev DB
// that already has data.
func AllSeedableTablesEmpty(ctx context.Context, db *sql.DB, plan *Plan) (bool, error) {
	for _, tp := range plan.tables {
		var n int64
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+QualifiedTable(tp.table)).Scan(&n); err != nil {
			return false, fmt.Errorf("count %s: %w", tp.table.Name, err)
		}
		if n > 0 {
			return false, nil
		}
	}
	return true, nil
}

// Reset wipes seeded rows and re-applies. It TRUNCATEs all seeded tables in a
// single CASCADE statement rather than issuing per-table DELETEs: a child-first
// DELETE aborts on the first // forge:append-only table (its BEFORE DELETE guard
// raises), whereas TRUNCATE bypasses that row-level trigger, resolves FK order
// via CASCADE, and RESTART IDENTITY resets sequences for a deterministic
// reseed. A dev convenience; destructive (removes ALL rows, as the DELETE did).
//
// The plan is validated BEFORE the TRUNCATE. Apply validates too, but by then
// the rows are gone: a refusal (a vocab null on a NOT NULL column, an
// undeclared diamond) that fires after the wipe leaves the database empty and
// reports an error, which is the worst of both outcomes.
func Reset(ctx context.Context, db *sql.DB, plan *Plan) (*Result, error) {
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	if len(plan.tables) == 0 {
		return Apply(ctx, db, plan)
	}
	idents := make([]string, len(plan.tables))
	for i, tp := range plan.tables {
		idents[i] = QualifiedTable(tp.table)
	}
	stmt := "TRUNCATE " + strings.Join(idents, ", ") + " RESTART IDENTITY CASCADE"
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		return nil, fmt.Errorf("reset truncate: %w", err)
	}
	return Apply(ctx, db, plan)
}

// checkPoolRE extracts the string literals from a single-column CHECK that
// normalizes to `col = ANY (ARRAY['a'::text, ...])` (postgres's canonical
// form for both `col IN (...)` and `col = ANY (...)`).
var (
	checkAnyRE     = regexp.MustCompile(`=\s*ANY`)
	checkLiteralRE = regexp.MustCompile(`'((?:[^']|'')*)'`)
	// checkEqLiteralRE matches a CHECK that is nothing but `col = 'literal'`
	// — what a one-member vocabulary normalizes to, since postgres only
	// spells a vocabulary `= ANY (ARRAY[...])` when it has more than one
	// member. Anchored on both ends so a conjunction or a function call
	// around the comparison is not mistaken for a pool.
	checkEqLiteralRE = regexp.MustCompile(`^\s*CHECK\s*\(+\s*"?[A-Za-z_][A-Za-z0-9_]*"?\s*=\s*'((?:[^']|'')*)'(?:::[a-z][a-z0-9_ ]*)?\s*\)+\s*$`)
	// checkArrayBodyRE isolates the member list of the canonical
	// `= ANY (ARRAY[...])` form so members are read from the array itself
	// and never from the rest of the expression.
	checkArrayBodyRE = regexp.MustCompile(`ARRAY\[(.*)\]`)
	// checkNumMemberRE matches one NUMERIC array member in either spelling
	// postgres renders: bare (`10`, an int4 column) or parenthesized with an
	// explicit cast (`(10)::bigint`, which is what an int8/numeric column
	// normalizes to). The cast's type NAME is deliberately outside the
	// capture — its own characters must never be read as a member.
	checkNumMemberRE = regexp.MustCompile(`^\(?(-?\d+(?:\.\d+)?)\)?(?:::[a-z0-9_ ]+)?$`)
)

// poolFromCheckDef extracts the allowed values from a CHECK definition that
// normalizes to `col = ANY (ARRAY[...])` — postgres's canonical form for a
// hand-written `col IN (...)`. Returns nil when the definition is not an
// IN / = ANY vocabulary (e.g. a range check or the id-non-empty guard).
//
// Both member spellings are read, because a vocabulary is a vocabulary
// whatever its column's type. Only the QUOTED form used to be, which made a
// numeric IN-list invisible twice over: poolFromCheckDef found no members, and
// boundFromCheckDef refuses `= ANY` outright (correctly — a set of admitted
// values is not a range, and treating {10,20,30} as [10,30] would admit 11).
// The column therefore carried no pool and no bound, and every consumer fell
// through to a type-blind default that the CHECK rejects:
//
//	CHECK ((level = ANY (ARRAY[(10)::bigint, (20)::bigint, (30)::bigint])))
//	  → fixture `1`, INSERT rejected by widgets_level_check
//
// A numeric member is returned in its DECIMAL spelling; callers render it for
// the column's own type (poolLiteral emits it bare for a numeric column, so
// the value reaches SQL as a number rather than a quoted string).
func poolFromCheckDef(def string) []string {
	if m := checkEqLiteralRE.FindStringSubmatch(def); m != nil {
		return []string{strings.ReplaceAll(m[1], "''", "'")}
	}
	if !checkAnyRE.MatchString(def) {
		return nil
	}
	var vals []string
	for _, m := range checkLiteralRE.FindAllStringSubmatch(def, -1) {
		vals = append(vals, strings.ReplaceAll(m[1], "''", "'"))
	}
	if len(vals) > 0 {
		return vals
	}
	return numericPoolFromCheckDef(def)
}

// numericPoolFromCheckDef reads the members of a NUMERIC `= ANY (ARRAY[...])`
// vocabulary. Members are split on the array body's top-level commas and each
// is required to parse ENTIRELY as a number (with postgres's optional
// parenthesis + cast decoration). A body carrying any member this does not
// recognise yields nil rather than a partial vocabulary: a pool that silently
// dropped a member would let a caller "satisfy" a constraint it never read.
func numericPoolFromCheckDef(def string) []string {
	m := checkArrayBodyRE.FindStringSubmatch(def)
	if m == nil {
		return nil
	}
	var vals []string
	for _, member := range strings.Split(m[1], ",") {
		num := checkNumMemberRE.FindStringSubmatch(strings.TrimSpace(member))
		if num == nil {
			return nil
		}
		vals = append(vals, num[1])
	}
	return vals
}

// parseBoundLit reads a CHECK comparison literal into the integer NumBound
// carries, rounding INWARD so the bound stays satisfiable.
//
// A fractional bound cannot be represented exactly here, and the direction of
// the rounding is what keeps that lossy step safe: a lower bound rounds UP
// (`>= 0.5` becomes `>= 1`) and an upper bound rounds DOWN (`<= 0.5` becomes
// `<= 0`). Every value the narrowed bound admits therefore still satisfies the
// real constraint. Rounding the other way would hand back a bound whose
// endpoints postgres rejects, which is the bug this whole path exists to
// prevent.
//
// The cost is a needlessly tight range on a fractional column — `<= 0.5`
// admits only 0 — but a fixture that is boring beats a fixture the schema
// refuses, and the guard verifies the result against postgres either way.
func parseBoundLit(s string, lower bool) (int64, error) {
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	if lower {
		return int64(math.Ceil(f)), nil
	}
	return int64(math.Floor(f)), nil
}

// boundFromCheckDef extracts the inclusive numeric range a CHECK definition
// pins column col to (`col >= N`, `col <= M`, `col BETWEEN a AND b`, and the
// split gte/lte pair protovalidate emits). ok is false when the definition
// carries no numeric comparison against col (enum pools, regex/function
// checks). Mirrors the projection IntrospectCheckBounds applies.
func boundFromCheckDef(col, def string) (NumBound, bool) {
	if checkAnyRE.MatchString(def) {
		return NumBound{}, false // enum pool, not a range
	}
	var b NumBound
	ok := false
	q := `\b` + regexp.QuoteMeta(col)
	// The literal as pg_get_constraintdef spells it. On an integer column
	// that is bare (`col <= 100`), but on any other numeric type postgres
	// canonicalizes the comparison value to a PARENTHESIZED, CAST literal:
	//
	//	CHECK ((waste_factor <= (1)::double precision))
	//
	// A pattern that accepted only the bare form silently read no bound off
	// a DOUBLE PRECISION column — so every rate/ratio/probability column
	// (`CHECK (col >= 0 AND col <= 1)`) looked unconstrained, and the
	// fixtures derived from it were free to land outside the range the same
	// schema then rejected.
	lit := `'?\(?(-?\d+(?:\.\d+)?)\)?'?(?:::[a-z ]+)?`
	geRE := regexp.MustCompile(q + `\s*(>=|>)\s*` + lit)
	leRE := regexp.MustCompile(q + `\s*(<=|<)\s*` + lit)
	betRE := regexp.MustCompile(q + `\s+BETWEEN\s+` + lit + `\s+AND\s+` + lit)
	setMin := func(n int64) {
		if b.Min == nil || n > *b.Min {
			v := n
			b.Min = &v
		}
		ok = true
	}
	setMax := func(n int64) {
		if b.Max == nil || n < *b.Max {
			v := n
			b.Max = &v
		}
		ok = true
	}
	for _, m := range geRE.FindAllStringSubmatch(def, -1) {
		if n, e := parseBoundLit(m[2], true); e == nil {
			if m[1] == ">" {
				n++
			}
			setMin(n)
		}
	}
	for _, m := range leRE.FindAllStringSubmatch(def, -1) {
		if n, e := parseBoundLit(m[2], false); e == nil {
			if m[1] == "<" {
				n--
			}
			setMax(n)
		}
	}
	for _, m := range betRE.FindAllStringSubmatch(def, -1) {
		if lo, e := parseBoundLit(m[1], true); e == nil {
			setMin(lo)
		}
		if hi, e := parseBoundLit(m[2], false); e == nil {
			setMax(hi)
		}
	}
	return b, ok
}

// PoolsFromTables builds EnumPools purely from an introspected schema
// model (schemadef.Table.Checks) — no live database. It covers the CHECK
// vocabulary half of IntrospectEnumPools (native postgres enum TYPES are
// not part of the schemadef model and are only visible to the live
// introspector). Used at generate time, where the introspected model is
// all there is.
func PoolsFromTables(tables []schemadef.Table) EnumPools {
	pools := EnumPools{}
	for _, t := range tables {
		for _, ck := range t.Checks {
			if len(ck.Columns) != 1 {
				continue // multi-column — not a per-column pool
			}
			vals := poolFromCheckDef(ck.Def)
			if len(vals) == 0 {
				continue
			}
			if pools[t.Name] == nil {
				pools[t.Name] = map[string][]string{}
			}
			for _, v := range vals {
				dup := false
				for _, have := range pools[t.Name][ck.Columns[0]] {
					if have == v {
						dup = true
						break
					}
				}
				if !dup {
					pools[t.Name][ck.Columns[0]] = append(pools[t.Name][ck.Columns[0]], v)
				}
			}
		}
	}
	return pools
}

// BoundsFromTables builds CheckBounds purely from an introspected schema
// model (schemadef.Table.Checks) — the no-live-DB sibling of
// IntrospectCheckBounds, for generate-time callers.
func BoundsFromTables(tables []schemadef.Table) CheckBounds {
	bounds := CheckBounds{}
	for _, t := range tables {
		for _, ck := range t.Checks {
			if len(ck.Columns) != 1 {
				continue
			}
			b, ok := boundFromCheckDef(ck.Columns[0], ck.Def)
			if !ok {
				continue
			}
			if bounds[t.Name] == nil {
				bounds[t.Name] = map[string]NumBound{}
			}
			have := bounds[t.Name][ck.Columns[0]]
			if b.Min != nil && (have.Min == nil || *b.Min > *have.Min) {
				have.Min = b.Min
			}
			if b.Max != nil && (have.Max == nil || *b.Max < *have.Max) {
				have.Max = b.Max
			}
			bounds[t.Name][ck.Columns[0]] = have
		}
	}
	return bounds
}

// IntrospectEnumPools reads, from the live target database, the allowed string
// values for every column constrained by a native Postgres enum or a simple
// single-column CHECK (col IN/ = ANY (...)) constraint. Synthesis must draw
// such columns from these pools or the INSERT would violate the constraint.
func IntrospectEnumPools(ctx context.Context, db schemadef.Queryer) (EnumPools, error) {
	pools := EnumPools{}
	add := func(table, column, val string) {
		if pools[table] == nil {
			pools[table] = map[string][]string{}
		}
		// de-dup while preserving first-seen order
		for _, v := range pools[table][column] {
			if v == val {
				return
			}
		}
		pools[table][column] = append(pools[table][column], val)
	}

	// Native enum columns, ordered by enum sort order (deterministic).
	enumRows, err := db.QueryContext(ctx, `
		SELECT c.relname, a.attname, e.enumlabel
		FROM pg_attribute a
		JOIN pg_class c ON c.oid = a.attrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_type t ON t.oid = a.atttypid
		JOIN pg_enum e ON e.enumtypid = t.oid
		WHERE c.relkind = 'r'
		  AND n.nspname NOT IN ('pg_catalog', 'information_schema')
		  AND a.attnum > 0 AND NOT a.attisdropped
		ORDER BY c.relname, a.attname, e.enumsortorder`)
	if err != nil {
		return nil, err
	}
	for enumRows.Next() {
		var table, column, label string
		if err := enumRows.Scan(&table, &column, &label); err != nil {
			_ = enumRows.Close()
			return nil, err
		}
		add(table, column, label)
	}
	if err := enumRows.Err(); err != nil {
		_ = enumRows.Close()
		return nil, err
	}
	_ = enumRows.Close()

	// Single-column CHECK constraints (col IN / = ANY (...)).
	checkRows, err := db.QueryContext(ctx, `
		SELECT c.relname, con.conkey, pg_get_constraintdef(con.oid)
		FROM pg_constraint con
		JOIN pg_class c ON c.oid = con.conrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE con.contype = 'c'
		  AND n.nspname NOT IN ('pg_catalog', 'information_schema')
		ORDER BY c.relname, con.conname`)
	if err != nil {
		return nil, err
	}
	type checkRow struct {
		table  string
		conkey []byte
		def    string
	}
	var checks []checkRow
	for checkRows.Next() {
		var cr checkRow
		if err := checkRows.Scan(&cr.table, &cr.conkey, &cr.def); err != nil {
			_ = checkRows.Close()
			return nil, err
		}
		checks = append(checks, cr)
	}
	if err := checkRows.Err(); err != nil {
		_ = checkRows.Close()
		return nil, err
	}
	_ = checkRows.Close()

	// Map (table, attnum) -> column name for single-column checks.
	for _, cr := range checks {
		vals := poolFromCheckDef(cr.def)
		if len(vals) == 0 {
			continue // not an IN / = ANY pool (e.g. `id <> ''`)
		}
		col := singleCheckColumn(ctx, db, cr.table, cr.conkey)
		if col == "" {
			continue // multi-column or unresolvable — skip
		}
		for _, v := range vals {
			add(cr.table, col, v)
		}
	}
	return pools, nil
}

// IntrospectCheckBounds reads, from the live target DB, the inclusive numeric
// range each column is pinned to by range CHECK constraints (`col >= N`,
// `col <= M`, `col BETWEEN a AND b`, and the split gte/lte pair protovalidate
// emits). An INSERT must satisfy these; the seed is transactional, so one
// out-of-range value aborts the whole seed. Enum `= ANY (...)` pools and
// function/regex checks (char_length(col)=N, col ~ '...') are NOT numeric
// bounds here — those are handled by IntrospectEnumPools and by the
// per-column length and pattern models (LengthBounds, patternsOf).
func IntrospectCheckBounds(ctx context.Context, db schemadef.Queryer) (CheckBounds, error) {
	bounds := CheckBounds{}
	ensure := func(table string) {
		if bounds[table] == nil {
			bounds[table] = map[string]NumBound{}
		}
	}
	setMin := func(table, col string, n int64) {
		ensure(table)
		b := bounds[table][col]
		if b.Min == nil || n > *b.Min {
			v := n
			b.Min = &v
		}
		bounds[table][col] = b
	}
	setMax := func(table, col string, n int64) {
		ensure(table)
		b := bounds[table][col]
		if b.Max == nil || n < *b.Max {
			v := n
			b.Max = &v
		}
		bounds[table][col] = b
	}
	rows, err := db.QueryContext(ctx, `
		SELECT c.relname, con.conkey, pg_get_constraintdef(con.oid)
		FROM pg_constraint con
		JOIN pg_class c ON c.oid = con.conrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE con.contype = 'c'
		  AND n.nspname NOT IN ('pg_catalog', 'information_schema')
		ORDER BY c.relname, con.conname`)
	if err != nil {
		return nil, err
	}
	type crow struct {
		table  string
		conkey []byte
		def    string
	}
	var crs []crow
	for rows.Next() {
		var r crow
		if err := rows.Scan(&r.table, &r.conkey, &r.def); err != nil {
			_ = rows.Close()
			return nil, err
		}
		crs = append(crs, r)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()
	for _, r := range crs {
		if checkAnyRE.MatchString(r.def) {
			continue // enum pool, not a range
		}
		col := singleCheckColumn(ctx, db, r.table, r.conkey)
		if col == "" {
			continue
		}
		b, ok := boundFromCheckDef(col, r.def)
		if !ok {
			continue
		}
		if b.Min != nil {
			setMin(r.table, col, *b.Min)
		}
		if b.Max != nil {
			setMax(r.table, col, *b.Max)
		}
	}
	return bounds, nil
}

// singleCheckColumn resolves the column name of a single-column CHECK from its
// conkey array (a text-encoded int2vector like "{3}"). Returns "" for
// multi-column checks.
func singleCheckColumn(ctx context.Context, db schemadef.Queryer, table string, conkey []byte) string {
	s := strings.TrimSpace(string(conkey))
	s = strings.TrimPrefix(s, "{")
	s = strings.TrimSuffix(s, "}")
	if s == "" || strings.Contains(s, ",") {
		return "" // multi-column
	}
	rows, err := db.QueryContext(ctx, `
		SELECT a.attname
		FROM pg_attribute a
		JOIN pg_class c ON c.oid = a.attrelid
		WHERE c.relname = $1 AND a.attnum = $2`, table, s)
	if err != nil {
		return ""
	}
	defer func() { _ = rows.Close() }()
	var name string
	if rows.Next() {
		if err := rows.Scan(&name); err != nil {
			return ""
		}
	}
	return name
}

// migrationVersionRE captures the leading numeric version of a golang-migrate
// filename (e.g. "00007_add_x.up.sql" -> "00007").
var migrationVersionRE = regexp.MustCompile(`^(\d+)_`)

// MigrationBlock describes WHY a database is not in a state seeds may be
// applied to. It is structured rather than a prose string because the two
// states need opposite advice and the caller cannot tell them apart from
// prose: a PENDING database is fixed by `forge db migrate up`, while a DIRTY
// one refuses that command again and is fixed by `forge db migrate force
// <version>`. Version and Latest carry the numbers the advice has to name —
// telling someone to force a version without saying which one leaves most of
// the dead end in place.
type MigrationBlock struct {
	// Dirty is set when golang-migrate recorded a migration that failed
	// part-way. The schema is in an unknown state and no further migration
	// will run until the flag is cleared.
	Dirty bool
	// Version is the version recorded in schema_migrations — for a dirty
	// database, the migration that failed. Empty when none is recorded.
	Version string
	// Latest is the highest version present on disk.
	Latest string
	// Reason is the human-readable diagnosis for the non-dirty cases.
	Reason string
}

// MigrationsPending reports why the target DB is not seedable, or nil when it
// is. Seeds apply only against a fully-migrated schema, so the CLI refuses on
// any non-nil result.
func MigrationsPending(ctx context.Context, db *sql.DB, migDir string) (*MigrationBlock, error) {
	maxFile := highestMigrationVersion(migDir)
	if maxFile == "" {
		return nil, nil // no migrations at all — nothing to be behind
	}

	var version sql.NullString
	var dirty sql.NullBool
	err := db.QueryRowContext(ctx, "SELECT version, dirty FROM schema_migrations LIMIT 1").Scan(&version, &dirty)
	if err != nil {
		// No schema_migrations table (or empty) — nothing applied yet.
		return &MigrationBlock{
			Latest: maxFile,
			Reason: fmt.Sprintf("no migrations applied; latest on disk is %s", maxFile),
		}, nil
	}
	if dirty.Valid && dirty.Bool {
		return &MigrationBlock{
			Dirty:   true,
			Version: version.String,
			Latest:  maxFile,
			Reason:  fmt.Sprintf("migration %s failed part-way and is marked dirty", version.String),
		}, nil
	}
	if !version.Valid || numericLess(version.String, maxFile) {
		return &MigrationBlock{
			Version: version.String,
			Latest:  maxFile,
			Reason:  fmt.Sprintf("applied version %s is behind latest on disk %s", version.String, maxFile),
		}, nil
	}
	return nil, nil
}

func highestMigrationVersion(migDir string) string {
	entries, err := os.ReadDir(migDir)
	if err != nil {
		return ""
	}
	var versions []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".up.sql") {
			continue
		}
		if m := migrationVersionRE.FindStringSubmatch(e.Name()); m != nil {
			versions = append(versions, m[1])
		}
	}
	if len(versions) == 0 {
		return ""
	}
	sort.Slice(versions, func(i, j int) bool { return numericLess(versions[i], versions[j]) })
	return versions[len(versions)-1]
}

// numericLess compares two numeric-string versions by length then lexically
// (both are zero-padded digit runs from migrate filenames / schema_migrations).
func numericLess(a, b string) bool {
	a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}
