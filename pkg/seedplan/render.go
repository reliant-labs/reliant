package seedplan

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

const maxFKDepth = 8

// quoteIdent double-quotes a SQL identifier.
func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// QualifiedTable is the SQL spelling of a table's name: schema-qualified when
// the table lives outside `public`, bare otherwise. A seed statement that names
// only the table fails with `relation "users" does not exist` against a project
// whose tables are `controlplane.users`, because the connection's search_path
// is not the project's.
func QualifiedTable(t schemadef.Table) string {
	if t.Schema == "" || t.Schema == "public" {
		return quoteIdent(t.Name)
	}
	return quoteIdent(t.Schema) + "." + quoteIdent(t.Name)
}

// colPlan finds the planned column for a table.column, if seedable.
func (p *Plan) colPlan(table, column string) (tablePlan, columnPlan, bool) {
	for _, tp := range p.tables {
		if tp.table.Name != table {
			continue
		}
		for _, cp := range tp.cols {
			if cp.col.Name == column {
				return tp, cp, true
			}
		}
	}
	return tablePlan{}, columnPlan{}, false
}

// cellLiteral produces the SQL literal for one (table, column, row). depth
// guards FK derivation against pathological reference chains.
func (p *Plan) cellLiteral(tp tablePlan, cp columnPlan, i, depth int) string {
	name := tp.table.Name
	col := cp.col

	// Managed soft-delete marker: seeded live. WHICH column that is comes
	// from schemadef.DetectConventions, so a `deleted_at` of a type nothing
	// can stamp is an ordinary column here too — see managedRoleOf.
	if p.managedRole(name, col) == managedDeletedAt {
		return "NULL"
	}

	// Foreign key — tested BEFORE the primary key, because a column can be
	// both. In a composite key like (company_id, kind), company_id is a key
	// member AND a reference; seeding it as a key synthesizes an id from
	// table+row that names no parent row, and the INSERT fails the constraint
	// at runtime rather than being resolved at plan time.
	//
	// The reverse precedence costs nothing: a plain (non-FK) key column still
	// falls through to pkLiteral below, and a single-column `id` PK is never
	// an FK to begin with.
	if cp.fk != nil {
		return p.fkLiteral(tp, cp, i, depth)
	}

	// Primary key. Composite members are distinguished by column name — a
	// literal derived from table+row alone hands every member of the same row
	// the identical value, which collides on the second row of any two-member
	// key whose columns share a type.
	if col.IsPK {
		return p.keyLiteral(name, col, i)
	}

	// Single-column UNIQUE data column: the finalized distinct assignment
	// (see constraints.go). It is derived FROM valueLiteral, so it already
	// carries the vocabulary overlay and the length fitting.
	if lits, ok := p.uniques[name][col.Name]; ok && i < len(lits) {
		return lits[i]
	}

	// Composite UNIQUE (see tuple.go): a column whose value alone means
	// nothing to the constraint, but whose ROW's tuple must not repeat. The
	// odometer deals it one of its own distinct values; foreign-key members
	// are placed on the referential path below instead.
	if lit, ok := p.tupleLiteral(name, col.Name, i); ok {
		return lit
	}

	// Discriminated-union CHECK (see union.go): the row's shape follows from
	// the branch of the OR forge picked for this row, so the discriminator's
	// literal and its siblings' presence/absence are placed together rather
	// than drawn independently. Like ordering below, the pass claims only
	// plain data columns, so it cannot reach this point ahead of the
	// referential and UNIQUE branches above.
	if lit, ok := p.unionLiteral(name, col, i); ok {
		return lit
	}

	// Column-ordering CHECK (see ordering.go): a column the schema requires
	// to sit above another is PLACED relative to it rather than drawn
	// independently. Ordering never claims a key, reference or UNIQUE column,
	// so it cannot reach this point ahead of the branches above.
	if lit, ok := p.orderedLiteral(tp, col, i); ok {
		return lit
	}

	return p.valueLiteral(name, col, i)
}

// keyLiteral is the value a PRIMARY-KEY column carries at row i.
//
// It is pkLiteral plus the one thing pkLiteral cannot see: the column's own
// CHECK vocabulary. A key member is still an ordinary column, and a CHECK is a
// declaration about EVERY value it holds —
//
//	PRIMARY KEY (org_id, period_start, period_end, group_by, group_key),
//	CONSTRAINT chk_group_by CHECK (group_by IN ('provider', 'model', …))
//
// — so a key function that invents an id for group_by writes a value the
// constraint rejects, and the INSERT dies on the CHECK instead of on the key.
// A vocabulary member is what the column admits, so that is what it gets, and
// the OTHER members of the composite key carry the row's distinctness (with
// ON CONFLICT DO NOTHING as the backstop the statement already carries).
//
// A SOLE key column never takes this path: a one-column key with a CHECK
// vocabulary can hold as many rows as the vocabulary has members, and quietly
// collapsing a table's ids into four strings is not a trade the key function
// gets to make on its own. Its id stays the documented, stable UUID.
func (p *Plan) keyLiteral(table string, col schemadef.Column, i int) string {
	composite := len(p.byName[table].PKCols) > 1
	if composite {
		if _, pooled := p.closedPool(table, col); pooled {
			return p.valueLiteral(table, col, i)
		}
	}
	return pkLiteral(p.cfg, table, col, i, composite)
}

// valueLiteral produces the literal for a plain (non-PK, non-FK) data cell:
// the user's vocabulary overlay first, then the introspected enum/CHECK pool,
// then built-in synthesis. All draws are salt-stable and column-local. The
// UNIQUE-column assignment (constraints.go) and the ordering placement
// (ordering.go) call it directly, so each draws the very same natural value it
// then has to make distinct or place.
func (p *Plan) valueLiteral(table string, col schemadef.Column, i int) string {
	salt := p.cfg.EffectiveSalt()

	// Vocabulary overlay: validated against the column's constraints at
	// ApplyVocab time, drawn with the same column-local hash-pick as every
	// built-in pool (adding vocab for one column never reshuffles another).
	if vals := p.vocab[table][col.Name]; len(vals) > 0 {
		return poolLiteral(col, vals[pick(salt, table, col.Name, i, len(vals))])
	}

	// Enum / CHECK-constrained pool: draw from the allowed set, preferring
	// real values over the proto zero-value sentinel (…_UNSPECIFIED). Seeding
	// the "unset" placeholder reads as missing data and leaves enum facets
	// matching nothing (a status/species filter that can never hit a row).
	if pool, ok := p.pools.get(table, col.Name); ok {
		choices := SeedEnumChoices(pool)
		// poolLiteral, not sqlString: a CHECK vocabulary on a NUMERIC column
		// (`level IN (10, 20, 30)`) has numeric members, and quoting one emits
		// `'10'` into an INSERT — a type postgres rejects outright for an
		// int8 column. Rendering by the COLUMN's type is what every other pool
		// consumer already does (see poolLiteral in constraints.go).
		// poolLiteral, not sqlString: a CHECK vocabulary on a NUMERIC column
		// (`level IN (10, 20, 30)`) has numeric members. postgres would coerce
		// the quoted spelling for the column types forge emits, so this is a
		// CONSISTENCY fix rather than a bug fix — it makes this draw render a
		// pool member exactly as assignUnique (constraints.go) already renders
		// the same member for a UNIQUE column, so one column's literal does
		// not depend on whether an index happens to be on it.
		return poolLiteral(col, choices[pick(salt, table, col.Name, i, len(choices))])
	}

	// A json/jsonb column carries a DOCUMENT, and no part of the schema says
	// what the document means — which keys, which shape, which of its values
	// reference other rows. So the seeder emits what the schema DECLARES and
	// invents nothing; see jsonDocument.
	if col.Type == schemadef.TypeJSON && !col.IsArray {
		return p.jsonDocument(table, col)
	}

	// Built-in synthesis. Its output is fitted to the column's char_length /
	// varchar bounds: forge derived that cap from the field's own `max_len`, so
	// a synthesized value that overflows it is forge contradicting itself. The
	// pool branches above need no fitting — a CHECK vocabulary's values satisfy
	// their own CHECK, and overlay values were length-validated by ApplyVocab.
	minLen, maxLen := p.lengthBoundsOf(table, col)
	b, _ := p.bounds.get(table, col.Name)
	return fitStringLiteral(p.synthScalar(p.byName[table], col, i, b), minLen, maxLen, i)
}

// SeedEnumChoices returns the enum/CHECK pool values worth seeding: it drops
// the proto zero-value sentinel (…_UNSPECIFIED / …_UNKNOWN, or a bare
// UNSPECIFIED/UNKNOWN) so seed rows carry real, meaningful values rather than
// the "unset" placeholder — which surfaces as "Unspecified" labels throughout
// a UI and leaves status/species facets matching nothing. Order is preserved
// (deterministic draw). Falls back to the full pool when filtering would leave
// it empty (a pool that is only the sentinel), so a draw always succeeds.
func SeedEnumChoices(pool []string) []string {
	out := make([]string, 0, len(pool))
	for _, v := range pool {
		if isEnumZeroSentinel(v) {
			continue
		}
		out = append(out, v)
	}
	if len(out) == 0 {
		return pool
	}
	return out
}

// isEnumZeroSentinel reports whether a label is the conventional proto
// zero/unset value. buf's ENUM_ZERO_VALUE_SUFFIX lint (default _UNSPECIFIED)
// makes the suffix a reliable signal for forge-generated enums; _UNKNOWN is
// accepted as the common alternative. Only these two conventions are treated
// as sentinels — _NONE/_INVALID and the like are often meaningful states.
func isEnumZeroSentinel(v string) bool {
	u := strings.ToUpper(strings.TrimSpace(v))
	return u == "UNSPECIFIED" || u == "UNKNOWN" ||
		strings.HasSuffix(u, "_UNSPECIFIED") ||
		strings.HasSuffix(u, "_UNKNOWN")
}

// fkParentRow resolves WHICH parent row a foreign-key cell points at.
//
// ok is false when the cell is NULL: a cycle edge BuildPlan broke, or an
// optional relationship this row declines. The returned index is into
// cp.fk.RefTable (which is tp.table.Name for a self-reference).
//
// Two layers. The NULL decision and the default pick are column-local
// (fkParentRowIndependent). On top of that sit the two shapes a DECLARATION on
// the foreign-key constraint can ask for (see diamond.go):
//
//   - DERIVED — `forge:ref derived-from=<col>`: take the parent from the
//     transitive route, so both routes to it agree by construction.
//   - AUTHORITATIVE — a `forge:ref authoritative` pair: the declared column
//     draws from the parents the other edge can actually partner with, and the
//     other edge then draws from that parent's partners.
//
// Neither resurrects a NULL: a row that declines the edge still declines it,
// and a declaration forge cannot satisfy on some row degrades to the
// column-local pick rather than inventing a reference.
//
// Termination is structural: routes only follow the DAG that BuildPlan's
// topological order leaves behind, and the depth argument bounds the walk
// regardless.
func (p *Plan) fkParentRow(tp tablePlan, cp columnPlan, i int) (int, bool) {
	return p.fkParentRowAt(tp, cp, i, 0)
}

func (p *Plan) fkParentRowAt(tp tablePlan, cp columnPlan, i, depth int) (int, bool) {
	// A composite UNIQUE index over this column has already dealt it a parent
	// (see tuple.go), and that assignment is a PROOF the tuple does not
	// repeat. Nothing below may re-pick it — resolveDiamonds declines to claim
	// such a column for the same reason, so this is the belt to that braces.
	if row, ok := p.tupleParentRow(tp.table.Name, cp.col.Name, i); ok {
		return row, true
	}
	idx, ok := p.fkParentRowIndependent(tp, cp, i)
	if !ok {
		return 0, false
	}
	if route, derived := p.derivedRefs[tp.table.Name][cp.col.Name]; derived {
		if viaRow, viaOK := p.walkRoute(tp.table.Name, route, i, depth); viaOK {
			return viaRow, true
		}
		return idx, true
	}
	if ap, isDirect, ok := p.authPairFor(tp.table.Name, cp.col.Name); ok {
		if row, found := p.authoritativeRow(tp, cp, ap, isDirect, i, depth); found {
			return row, true
		}
	}
	return idx, true
}

// fkParentRowIndependent is the column-local answer: the seeder's original
// referential policy, with no knowledge of any other column.
func (p *Plan) fkParentRowIndependent(tp tablePlan, cp columnPlan, i int) (int, bool) {
	if cp.forceNull {
		return 0, false
	}
	name := tp.table.Name
	nullable := !cp.col.NotNull

	// Self-reference: a plausible tree. Row 0 points to itself (or NULL),
	// later rows point to the previous row.
	if cp.selfRef {
		if i == 0 {
			return 0, !nullable
		}
		return i - 1, true
	}

	// Row 0 is the demo-coherent spine. Every non-self, non-broken foreign
	// key on row 0 resolves to its parent's row 0 (never NULL, even when the
	// column is optional), so one fully-connected happy-path dataset exists:
	// brand row 0 owns customer/product/variant/price/cart row 0, each
	// referencing the others — a complete checkout graph, not
	// FK-valid-but-scattered rows. Parents precede children in topological
	// order and every table seeds ≥ 1 row, so parent row 0 always exists.
	// Later rows keep the realistic hash-picked scatter (and optional-FK
	// nulls) below, so the dataset still exercises variety and pagination.
	if i == 0 {
		return 0, true
	}

	salt := p.cfg.EffectiveSalt()

	// Optional relationship: null ~1 in 5 rows — unless a status guard
	// requires the edge present (see columnPlan.requireEdge), in which case
	// the coin flip is what would violate the constraint.
	if nullable && !cp.requireEdge && cellHash(salt, name, cp.col.Name, i)%5 == 0 {
		return 0, false
	}

	if cp.uniqueFK {
		// 1-1 relationship (UNIQUE FK column): assign a DISTINCT parent per
		// child row so the unique constraint holds. BuildPlan capped this
		// table's row count at the parent's, so i is always a valid parent
		// index (i < tp.n <= refRows). A hash-pick would collide two children
		// onto one parent and violate the constraint.
		return i, true
	}
	return pick(salt, name, cp.col.Name, i, p.rowsOf[cp.fk.RefTable]), true
}

// fkLiteral resolves a foreign-key cell to a real parent value by
// construction (derived from the referenced table's own PK function).
func (p *Plan) fkLiteral(tp tablePlan, cp columnPlan, i, depth int) string {
	idx, ok := p.fkParentRow(tp, cp, i)
	if !ok {
		return "NULL"
	}
	refTable := cp.fk.RefTable
	if cp.selfRef {
		refTable = tp.table.Name
	}
	return p.referencedValue(refTable, cp.fk.RefColumn, idx, depth)
}

// referencedValue returns the literal a parent row exposes on its referenced
// column — the PK function for the common id reference, else the parent's own
// synthesized cell (so the value matches what the parent inserts).
func (p *Plan) referencedValue(refTable, refColumn string, idx, depth int) string {
	rt, ok := p.byName[refTable]
	if !ok {
		return "NULL"
	}
	var refCol schemadef.Column
	found := false
	for _, c := range rt.Columns {
		if c.Name == refColumn {
			refCol = c
			found = true
			break
		}
	}
	if !found {
		return "NULL"
	}
	// The referenced column's own table decides whether its key is composite:
	// this re-derives the PARENT's literal, so it must be computed exactly as
	// the parent's own row computed it.
	if refCol.IsPK {
		return p.keyLiteral(refTable, refCol, idx)
	}
	if depth >= maxFKDepth {
		// Give up deriving through a long chain; a key-shaped guess keeps the
		// insert well-typed even if referential integrity can't be proven.
		// "Well-typed" is the whole value of the fallback, so it goes through
		// the same type-aware key function rather than assuming a UUID.
		return p.keyLiteral(refTable, refCol, idx)
	}
	if rtp, rcp, ok := p.colPlan(refTable, refColumn); ok {
		return p.cellLiteral(rtp, rcp, idx, depth+1)
	}
	b, _ := p.bounds.get(refTable, refCol.Name)
	return p.synthScalar(rt, refCol, idx, b)
}

// SeedValue reports the raw (unquoted) value the plan seeds at
// (table, column, row) — the value a consumer can hand back to the
// application layer to reference a seeded row (e.g. a generated test's
// create request pointing at a seeded FK parent). ok is false when the
// plan doesn't seed that cell, the cell is NULL, or the cell isn't a
// plain scalar literal (arrays, bytea casts).
//
// A column a minimal plan leaves out (see Writes) reports ok=false too: the
// plan seeds nothing there, the database does. NaturalValue reads the value
// the planner WOULD write, for a caller that has to supply one anyway.
func (p *Plan) SeedValue(table, column string, row int) (string, bool) {
	tp, cp, ok := p.colPlan(table, column)
	if !ok || cp.omit || row < 0 || row >= tp.n {
		return "", false
	}
	return decodeScalarLiteral(p.cellLiteral(tp, cp, row, 0))
}

// NaturalValue is SeedValue without the minimal plan's omission: the raw
// value the planner places at (table, column, row) whether or not the INSERT
// names the column. A caller that writes rows through some other path — a
// create RPC whose plain proto field always writes a value, set or not —
// needs a valid value for a column the database would otherwise have
// defaulted. ok is false when the cell is NULL or not a plain scalar.
func (p *Plan) NaturalValue(table, column string, row int) (string, bool) {
	tp, cp, ok := p.colPlan(table, column)
	if !ok || row < 0 || row >= tp.n {
		return "", false
	}
	return decodeScalarLiteral(p.cellLiteral(tp, cp, row, 0))
}

// decodeScalarLiteral turns a rendered SQL literal back into its raw value:
// 'quoted' strings are unquoted/unescaped, bare numerics and the boolean
// keywords pass through, and everything else (NULL, ARRAY[...],
// '\x'::bytea) is not a plain scalar.
//
// `true`/`false` are scalars like any other. They were previously rejected
// by the bare-numeric scan below — the keyword letters are not digits — so
// every consumer reading a seeded cell got "not a scalar" for a boolean
// column and fell back to inventing its own value. That is how the frontend
// mocks came to disagree with the database on exactly the columns whose
// seeded distribution is deliberate (see the independence property pinned
// by TestSeedBooleans_AreIndependentAcrossColumns).
func decodeScalarLiteral(lit string) (string, bool) {
	if lit == "NULL" {
		return "", false
	}
	if len(lit) >= 2 && strings.HasPrefix(lit, "'") && strings.HasSuffix(lit, "'") {
		return strings.ReplaceAll(lit[1:len(lit)-1], "''", "'"), true
	}
	if lit == "true" || lit == "false" {
		return lit, true
	}
	for _, r := range lit {
		if (r < '0' || r > '9') && r != '-' && r != '.' {
			return "", false
		}
	}
	return lit, lit != ""
}

// statement renders one table's INSERT. Only the columns the plan writes are
// named (a minimal plan leaves some to the database — see Writes).
func (p *Plan) statement(tp tablePlan) string {
	if tp.n == 0 || len(tp.cols) == 0 {
		return ""
	}
	written := make([]columnPlan, 0, len(tp.cols))
	for _, cp := range tp.cols {
		if !cp.omit {
			written = append(written, cp)
		}
	}
	if len(written) == 0 {
		// Every column is the database's to fill. A multi-row VALUES list
		// cannot be empty, so each row is its own DEFAULT VALUES insert.
		var b strings.Builder
		for i := 0; i < tp.n; i++ {
			fmt.Fprintf(&b, "INSERT INTO %s DEFAULT VALUES;\n", QualifiedTable(tp.table))
		}
		return b.String()
	}
	var b strings.Builder
	cols := make([]string, len(written))
	for i, cp := range written {
		cols[i] = quoteIdent(cp.col.Name)
	}
	fmt.Fprintf(&b, "INSERT INTO %s (%s) VALUES\n", QualifiedTable(tp.table), strings.Join(cols, ", "))
	for i := 0; i < tp.n; i++ {
		vals := make([]string, len(written))
		for j, cp := range written {
			vals[j] = p.cellLiteral(tp, cp, i, 0)
		}
		b.WriteString("    (")
		b.WriteString(strings.Join(vals, ", "))
		b.WriteString(")")
		if i < tp.n-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	if len(tp.table.PKCols) > 0 {
		targets := make([]string, len(tp.table.PKCols))
		for i, c := range tp.table.PKCols {
			targets[i] = quoteIdent(c)
		}
		fmt.Fprintf(&b, "ON CONFLICT (%s) DO NOTHING;\n", strings.Join(targets, ", "))
	} else {
		b.WriteString("ON CONFLICT DO NOTHING;\n")
	}
	return b.String()
}

// Statements returns the per-table INSERT statements in topological order.
func (p *Plan) Statements() []string {
	var stmts []string
	for _, tp := range p.tables {
		if s := p.statement(tp); s != "" {
			stmts = append(stmts, s)
		}
	}
	return stmts
}

// Render returns the full deterministic SQL: a stable header (no timestamps)
// plus every INSERT in topological order. Byte-identical for identical
// (schema, pools, config).
func (p *Plan) Render() string {
	var b strings.Builder
	b.WriteString("-- Deterministic seed data materialized by `forge db seed`.\n")
	fmt.Fprintf(&b, "-- salt=%d, default rows=%d\n",
		p.cfg.EffectiveSalt(), p.cfg.EffectiveRows(""))
	b.WriteString("-- Source: the APPLIED schema (db/migrations) + real foreign keys.\n\n")
	for i, tp := range p.tables {
		if s := p.statement(tp); s != "" {
			if i > 0 {
				b.WriteString("\n")
			}
			fmt.Fprintf(&b, "-- %s\n", tp.table.Name)
			b.WriteString(s)
		}
	}
	return b.String()
}

// jsonDocument is what the seeder puts in a json/jsonb column: the column's
// declared DEFAULT when it has a literal one, otherwise the empty document
// its shape CHECK admits.
//
// It USED to guess from the column name — a column whose default was '[]'
// (or whose name was plural-ish) received two strings drawn from a pool of
// SaaS plan tiers; anything else received {"label": …, "seq": …}. Measured
// consequence: a clinic app's orders shipped
// line_items = ["enterprise","free"]. Valid JSON, passed
// CHECK (jsonb_typeof(line_items) = 'array'), and the app's flagship RPC
// rejected all twenty seeded rows because they were not order line items.
// The app could not be demonstrated on the data it shipped with.
//
// The seeder cannot fix that by guessing better. A JSON document's contents
// are APP semantics, not schema: only the author knows that
// line_items[].product_id must name a row in products. So the guess is
// gone, and the column's own DEFAULT — the one value the schema itself
// declares a row starts at — takes its place. An author who DOES know the
// shape supplies it through db/seeds/vocab.yaml, which accepts a JSON
// document as a value and validates it (see vocabValueProblem) — that
// overlay is consulted before this and stays the place domain knowledge
// lives.
func (p *Plan) jsonDocument(table string, col schemadef.Column) string {
	if doc, ok := jsonDefaultDocument(col.Default); ok {
		return sqlString(doc)
	}
	if jsonShapeIsArray(p.byName[table], col.Name) {
		return "'[]'"
	}
	return "'{}'"
}

// jsonDefaultDocument unwraps a column DEFAULT that is a literal JSON
// document ('[]'::jsonb, '{"theme": "dark"}'::jsonb). ok=false for a
// function call, an expression, or text that is not JSON — forge has
// nothing to seed from those and falls back to the empty document.
func jsonDefaultDocument(def string) (string, bool) {
	raw := strings.TrimSpace(pgJSONDefaultCastRE.ReplaceAllString(strings.TrimSpace(def), ""))
	if len(raw) < 2 || !strings.HasPrefix(raw, "'") || !strings.HasSuffix(raw, "'") {
		return "", false
	}
	doc := strings.ReplaceAll(raw[1:len(raw)-1], "''", "'")
	if !json.Valid([]byte(doc)) {
		return "", false
	}
	return doc, true
}

// pgJSONDefaultCastRE strips the trailing ::type cast postgres renders on a
// literal default ('[]'::jsonb).
var pgJSONDefaultCastRE = regexp.MustCompile(`::[\w ]+$`)

// jsonTypeofRE matches the shape CHECK forge and hand-written migrations
// use to pin a json column to one JSON type:
// CHECK ((jsonb_typeof(line_items) = 'array'::text)).
var jsonTypeofRE = regexp.MustCompile(`jsonb?_typeof\(\s*"?(\w+)"?\s*\)\s*=\s*'(\w+)'`)

// jsonShapeIsArray reports whether a shape CHECK on col pins it to a JSON
// ARRAY, so the empty document is [] rather than {}. Read off the applied
// schema's CHECK text — the only place the requirement is DECLARED. A
// column with no such CHECK gets {}, the JSON type with no better claim.
func jsonShapeIsArray(t schemadef.Table, col string) bool {
	for _, c := range t.Checks {
		for _, m := range jsonTypeofRE.FindAllStringSubmatch(c.Def, -1) {
			if m[1] == col && m[2] == "array" {
				return true
			}
		}
	}
	return false
}
