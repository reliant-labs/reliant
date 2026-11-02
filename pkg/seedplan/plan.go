package seedplan

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

// EnumPools maps table -> column -> the allowed string values for a column
// constrained by a native Postgres enum or a simple CHECK (col IN (...))
// constraint. Synthesis must draw enum/CHECK columns from this pool or the
// INSERT would violate the constraint. Introspected from the target DB (see
// apply.go); nil is fine (an unpooled column falls back to synthesis).
type EnumPools map[string]map[string][]string

func (p EnumPools) get(table, column string) ([]string, bool) {
	cols, ok := p[table]
	if !ok {
		return nil, false
	}
	v, ok := cols[column]
	return v, ok && len(v) > 0
}

// NumBound is the inclusive numeric range a column is constrained to by range
// CHECK constraints. A nil end is unbounded on that side.
type NumBound struct {
	Min *int64
	Max *int64
}

// clamp returns v pinned into the bound. Unbounded ends pass through.
func (b NumBound) clamp(v int64) int64 {
	if b.Min != nil && v < *b.Min {
		v = *b.Min
	}
	if b.Max != nil && v > *b.Max {
		v = *b.Max
	}
	return v
}

// CheckBounds maps table -> column -> the numeric range a column is pinned to
// by `col >= N` / `col <= M` / `BETWEEN` CHECK constraints — the protovalidate
// gte/lte projections. Synthesis must generate within them or the INSERT
// violates the constraint (the seed is transactional: one bad row aborts the
// whole seed). Introspected from the target DB (see apply.go); nil is fine
// (an unbounded column falls back to unclamped synthesis).
type CheckBounds map[string]map[string]NumBound

func (b CheckBounds) get(table, column string) (NumBound, bool) {
	cols, ok := b[table]
	if !ok {
		return NumBound{}, false
	}
	v, ok := cols[column]
	return v, ok
}

// columnPlan is one insertable column of a table.
type columnPlan struct {
	col       schemadef.Column
	fk        *schemadef.ForeignKey // non-nil when the column is a foreign key
	selfRef   bool                  // fk references this same table
	forceNull bool                  // nullable FK edge broken to resolve a cycle
	// uniqueFK marks a foreign-key column that carries a single-column UNIQUE
	// constraint — a 1-1 relationship (each parent referenced at most once).
	// The renderer must then assign a DISTINCT parent row per child row (a
	// bijection) instead of hash-picking, or the INSERT violates the unique
	// constraint. BuildPlan caps this table's row count at the parent's so a
	// distinct parent always exists. Never set for a self-reference.
	uniqueFK bool
	// requireEdge marks a NULLABLE foreign key that a status-guard CHECK
	// requires to be present on some rows (`status <> 'x' OR crew_id IS NOT
	// NULL`). An optional FK is normally nulled on ~1 row in 5 for variety;
	// on a guarded table that coin flip is what produced a SCHEDULED job
	// with no crew and aborted the seed. Set, the edge is always resolved,
	// so the column is non-NULL on every row and the guard holds whichever
	// status the row carries.
	//
	// The variety is given up deliberately and only for these columns: a
	// guarded optional FK cannot be both "sometimes absent" and "present
	// whenever the status demands it" without the seeder reasoning about
	// the two columns jointly, and always-present is the safe half.
	//
	// A MINIMAL plan instead sets it only from the one branch its rows take
	// (see applyMinimal), since every row takes the same branch.
	requireEdge bool
	// omit leaves the column out of the INSERT entirely, so the database
	// supplies its DEFAULT, or NULL. Only a minimal plan omits a column (see
	// Config.Minimal and minimal.go); a full plan writes every one it plans.
	omit bool
}

// uniqueSingleColumn reports whether the table has a UNIQUE index/constraint on
// EXACTLY the given single column — the shape that makes a foreign key a 1-1
// relationship. Postgres backs every UNIQUE constraint with a unique index, so
// schema introspection surfaces `UNIQUE(col)` and `intakes_order_id_key`-style
// constraints alike as a one-column unique Index. The primary key is excluded
// by introspection (it is handled by the PK path), so a shared-PK FK never
// trips this.
//
// A PARTIAL unique index counts only when it can actually bind a row this plan
// writes — see partialIndexBinds.
// A one-column UNIQUE over a CASE FOLD — `UNIQUE (lower(name))` — counts
// too, and reports its fold: the rule it states is "distinct ignoring
// case", which the value assigner can satisfy by comparing case-folded.
//
// Any OTHER expression key is skipped. Its Columns are only the bare-column
// subset, so `UNIQUE (supplier_id, lower(sku))` would otherwise read here
// as a one-column UNIQUE(supplier_id) — a stricter claim than the table
// makes, which capped the child table at one row per supplier and then
// wrote rows the real index rejected. Uniqueness of `f(x)` never implies
// uniqueness of `x`.
func uniqueSingleColumn(t schemadef.Table, col string) bool {
	_, ok := uniqueSingleColumnFold(t, col)
	return ok
}

// uniqueSingleColumnFold is uniqueSingleColumn plus the case fold the index
// keys on: "" for a plain UNIQUE (col), "lower"/"upper" for
// UNIQUE (lower(col)) / UNIQUE (upper(col)).
func uniqueSingleColumnFold(t schemadef.Table, col string) (fold string, ok bool) {
	for _, ix := range t.Indexes {
		keys := ix.KeyList()
		if !ix.Unique || len(keys) != 1 {
			continue
		}
		k := keys[0]
		if k.Column != col {
			continue
		}
		// A non-fold expression is unattributed (Column == ""), so it never
		// reaches here; a fold names the column it wraps.
		if !partialIndexBinds(t, ix) {
			continue
		}
		return k.Fold, true
	}
	return "", false
}

// partialNullPredRE matches the WHERE clause of a partial index in the one
// shape this package reads: a nullness test on a single column, as
// pg_get_expr renders it (`(revoked_at IS NULL)`).
var partialNullPredRE = regexp.MustCompile(`^"?([a-z_][a-z0-9_]*)"?\s+IS\s+(NOT\s+)?NULL$`)

// partialIndexBinds reports whether a partial index's WHERE clause can be TRUE
// of any row this plan writes. It exists because a partial unique index is a
// weaker statement than a plain one, and reading it as plain describes a
// stricter table than the one that exists:
//
//	CREATE UNIQUE INDEX … ON reliant_api_keys(user_id) WHERE revoked_at IS NULL
//
// says ONE ACTIVE key per user, and treating it as `UNIQUE (user_id)` caps the
// table at one row per user — a dataset in which no user can ever have a
// revoked key plus a live one, which is most of what such a table is for.
//
// The reading is deliberately narrow, and the fallback is the strict one: an
// unrecognized predicate, or a column whose nullness forge cannot settle from
// the schema, BINDS, which is exactly today's behaviour. Only a predicate
// forge can prove false of every row it writes is set aside.
func partialIndexBinds(t schemadef.Table, ix schemadef.Index) bool {
	pred := strings.TrimSpace(ix.Predicate)
	if pred == "" {
		return true // a full index always binds
	}
	m := partialNullPredRE.FindStringSubmatch(unwrapParens(pred))
	if m == nil {
		return true
	}
	alwaysNull, neverNull, known := nullnessOf(t, m[1])
	if !known {
		return true
	}
	if m[2] != "" { // ... WHERE col IS NOT NULL
		return !alwaysNull
	}
	return !neverNull
}

// nullnessOf answers, from the SCHEMA alone, whether a column is NULL in every
// row this planner writes or in none of them. known is false when neither can
// be shown.
//
// The answer is short because the planner's NULL policy is: only a managed
// soft-delete marker (seeded live, so always NULL) and a foreign key (which may
// decline an optional edge, or have had a cycle broken through it) are ever
// NULL. Every other synthesized column carries a value on every row — see
// synthScalar, which has no NULL branch. A column a discriminated union places
// is excluded too: that pass DOES write NULL, per branch, per row.
func nullnessOf(t schemadef.Table, name string) (alwaysNull, neverNull, known bool) {
	var col schemadef.Column
	found := false
	for _, c := range t.Columns {
		if c.Name == name {
			col, found = c, true
			break
		}
	}
	if !found || col.IsGenerated {
		return false, false, false
	}
	for _, fk := range t.ForeignKeys {
		if fk.Column == name {
			return false, false, false
		}
	}
	if unionPlacesColumn(t, name) {
		return false, false, false
	}
	if managedRoleOf(schemadef.DetectConventions(t), col) == managedDeletedAt {
		return true, false, true
	}
	return false, true, true
}

// tablePlan is one table's insert, in the topologically-ordered plan.
type tablePlan struct {
	table schemadef.Table
	n     int
	cols  []columnPlan
}

// Plan is a fully-resolved, deterministic seed plan: tables in FK-topological
// order, each with its insertable columns and row count. Render(Plan) and
// exec are pure functions of it.
type Plan struct {
	cfg    Config
	pools  EnumPools
	bounds CheckBounds
	tables []tablePlan
	byName map[string]schemadef.Table
	rowsOf map[string]int
	// conv holds each table's schemadef.DetectConventions classification —
	// the managed-column judgement the seeder CONSUMES rather than makes (see
	// managedRoleOf). It is the only thing in this package that turns on a
	// column's identity, and schemadef owns it.
	conv map[string]schemadef.Conventions
	// vocab is the validated domain-vocabulary overlay (see ApplyVocab):
	// table -> column -> the values the column draws from instead of
	// built-in synthesis. vocabWarns keeps the validation warnings for the
	// CLI to surface.
	vocab      map[string]map[string][]string
	vocabWarns []string
	// vocabErrs holds the vocab problems that refuse the seed rather than
	// warn — a null declared for a NOT NULL column. Reported by Validate.
	vocabErrs []string
	// planWarns holds constraint-satisfaction notes recorded by finalize.
	planWarns []string
	// derivedRefs maps table -> foreign-key column -> the transitive route its
	// value is DERIVED from, per a `forge:ref derived-from=` declaration on
	// that column's constraint. Set by finalize; read by fkParentRow.
	derivedRefs map[string]map[string][]fkHop
	// authRefs maps table -> the `forge:ref authoritative` pairs declared on
	// it: one column is the truth and the other is narrowed to agree. Set by
	// finalize; read by fkParentRow.
	authRefs map[string][]authPair
	// bucketMemo memoizes the row groupings authoritative pairs draw from,
	// keyed by (table, route). Same role as lenBounds: a per-run cache of a
	// pure function of the plan.
	bucketMemo map[string]routeBucketSet
	// undeclared holds the diamonds no constraint comment resolved. They are
	// the refusal (see diamondRefusal), not a warning: seeding them writes
	// rows that contradict a rule the schema implies.
	undeclared []diamond
	// rowsTarget is the CONFIGURED row count per table, before finalize's
	// constraint caps. Kept separate from rowsOf (the live count) so
	// re-finalizing recomputes the caps instead of ratcheting them down.
	rowsTarget map[string]int
	// uniques holds the finalized distinct value assignment for each
	// single-column-UNIQUE data column: table -> column -> per-row literal.
	// See constraints.go.
	uniques map[string]map[string][]string
	// tuples holds the finalized composite-UNIQUE placements: table -> one
	// odometer per multi-column UNIQUE index, dealing distinct TUPLES across
	// rows out of the members' own supplies. See tuple.go — the one-column
	// forms above (uniques, and columnPlan.uniqueFK) say nothing about a
	// constraint that no single column carries.
	tuples map[string][]tupleAssign
	// lenBounds memoizes LengthBounds per (table, column) — [min, max]
	// characters from char_length CHECKs and declared varchar caps.
	lenBounds map[string]map[string][2]int
	// orderChains places the columns of two-column ordering CHECKs
	// (`expires_at > issued_at`) so the synthesized values satisfy them by
	// construction; orderWarns names the ordering constraints forge could
	// not place. See ordering.go — this is one of the two constraint shapes
	// that are not a property of a single column.
	orderChains map[string]map[string]orderSlot
	orderWarns  []string
	// unions places the columns of discriminated-union CHECKs — a top-level
	// OR of AND-groups, each pinning a discriminator and constraining its
	// siblings — by satisfying ONE branch of the OR whole; unionWarns names
	// the unions forge could not place. See union.go, the other
	// multi-column shape.
	unions     map[string][]unionSpec
	unionWarns []string
	// minimalBranches is a MINIMAL plan's fixed branch per union spec:
	// table -> one index per entry of unions[table]. nil for a full plan,
	// whose rows rotate through the branches instead. Set by finalize
	// (applyMinimal); read by unionBranch.
	minimalBranches map[string]minimalChoice
}

// Warnings returns every plan-level warning: vocabulary validation problems
// plus the constraint-satisfaction notes finalize records (a row target capped
// to a UNIQUE column's vocabulary, a value fitted to a length cap, a
// multi-column CHECK forge could not place — see ordering.go). The seed
// never fails on these — the CLI surfaces them. An undeclared diamond is NOT
// among them: that one refuses the seed (see Validate / diamondRefusal).
func (p *Plan) Warnings() []string {
	out := make([]string, 0, len(p.vocabWarns)+len(p.planWarns))
	out = append(out, p.vocabWarns...)
	out = append(out, p.planWarns...)
	return out
}

// Validate reports the conditions under which forge refuses to write this
// plan's rows: a db/seeds/vocab.yaml null declared for a NOT NULL column, and
// a foreign-key reference reachable by two paths whose authority nobody
// declared (see diamond.go). Both are checked at APPLY time rather than at
// BuildPlan, so `forge generate` — which builds one-row plans for entity
// factories and fixtures — is never blocked by a decision that only affects a
// real dataset.
func (p *Plan) Validate() error {
	if len(p.vocabErrs) == 0 {
		return p.diamondRefusal()
	}
	vocabErr := fmt.Errorf("refusing to seed: db/seeds/vocab.yaml declares a value the schema cannot hold:\n  %s",
		strings.Join(p.vocabErrs, "\n  "))
	return errors.Join(vocabErr, p.diamondRefusal())
}

// Tables returns the planned table names in insert (topological) order.
func (p *Plan) Tables() []string {
	out := make([]string, len(p.tables))
	for i, tp := range p.tables {
		out[i] = tp.table.Name
	}
	return out
}

// SeedableTables returns the set of table names the plan will seed. Used by
// status/auto-seed to decide "are all seedable tables empty".
func (p *Plan) SeedableTables() []string { return p.Tables() }

// SetBounds attaches range-CHECK bounds to the plan so numeric synthesis
// stays inside them. BuildLivePlan wires the live-introspected bounds;
// generate-time callers wire BoundsFromTables.
func (p *Plan) SetBounds(b CheckBounds) {
	p.bounds = b
	p.finalize()
}

// BuildPlan resolves the introspected schema into a deterministic,
// FK-topological seed plan. A NOT NULL foreign-key cycle is a hard error
// naming the cycle (it is equally unsatisfiable for the app itself).
func BuildPlan(tables []schemadef.Table, pools EnumPools, cfg Config) (*Plan, error) { //nolint:gocognit,funlen // one flat, independent branch per column kind and constraint form.
	byName := make(map[string]schemadef.Table, len(tables))
	names := make([]string, 0, len(tables))
	for _, t := range tables {
		byName[t.Name] = t
		names = append(names, t.Name)
	}
	sort.Strings(names) // stable base order before topo

	rowsOf := make(map[string]int, len(tables))
	for _, n := range names {
		rowsOf[n] = cfg.EffectiveRows(n)
	}

	// The managed-column conventions, resolved once per table by schemadef.
	conv := make(map[string]schemadef.Conventions, len(names))
	for _, n := range names {
		conv[n] = schemadef.DetectConventions(byName[n])
	}

	// Build FK edges (parent -> child) for ordering, plus per-(table,col)
	// force-null decisions for broken cycle edges and dangling references.
	type edge struct {
		parent, child, col string
		nullable           bool
	}
	var edges []edge
	forceNull := map[string]map[string]bool{}
	markNull := func(table, col string) {
		if forceNull[table] == nil {
			forceNull[table] = map[string]bool{}
		}
		forceNull[table][col] = true
	}

	for _, n := range names {
		t := byName[n]
		colByName := map[string]schemadef.Column{}
		for _, c := range t.Columns {
			colByName[c.Name] = c
		}
		for _, fk := range t.ForeignKeys {
			if fk.RefTable == n {
				continue // self-reference: handled per-row, not an ordering edge
			}
			c := colByName[fk.Column]
			_, refSeeded := byName[fk.RefTable]
			if !refSeeded {
				// References a table forge won't seed. Null it if we can;
				// otherwise it is unsatisfiable.
				if c.NotNull {
					return nil, fmt.Errorf("seeddata: %s.%s is a NOT NULL foreign key to un-seedable table %q", n, fk.Column, fk.RefTable)
				}
				markNull(n, fk.Column)
				continue
			}
			edges = append(edges, edge{parent: fk.RefTable, child: n, col: fk.Column, nullable: !c.NotNull})
		}
	}

	// Kahn's algorithm with nullable-edge cycle breaking.
	remaining := make([]edge, len(edges))
	copy(remaining, edges)
	var order []string
	for {
		indeg := map[string]int{}
		for _, n := range names {
			indeg[n] = 0
		}
		for _, e := range remaining {
			indeg[e.child]++
		}
		// zero-in-degree frontier, name-sorted for determinism
		var queue []string
		placed := map[string]bool{}
		for _, n := range names {
			if indeg[n] == 0 {
				queue = append(queue, n)
			}
		}
		order = order[:0]
		for len(queue) > 0 {
			sort.Strings(queue)
			n := queue[0]
			queue = queue[1:]
			if placed[n] {
				continue
			}
			placed[n] = true
			order = append(order, n)
			for _, e := range remaining {
				if e.parent == n {
					indeg[e.child]--
					if indeg[e.child] == 0 {
						queue = append(queue, e.child)
					}
				}
			}
		}
		if len(order) == len(names) {
			break
		}
		// Cycle among the unplaced nodes. Break it at a nullable edge whose
		// endpoints are both unplaced; force that FK NULL.
		broke := false
		var kept []edge
		for _, e := range remaining {
			if !broke && e.nullable && !placed[e.parent] && !placed[e.child] {
				markNull(e.child, e.col)
				broke = true
				continue // drop this edge
			}
			kept = append(kept, e)
		}
		if !broke {
			var cyc []string
			for _, n := range names {
				if !placed[n] {
					cyc = append(cyc, n)
				}
			}
			return nil, fmt.Errorf("seeddata: NOT NULL foreign-key cycle among tables: %s — the schema is unsatisfiable without deferred constraints", strings.Join(cyc, " -> "))
		}
		remaining = kept
	}

	// Assemble the ordered table plans. Row counts (the 1-1 foreign-key cap and
	// the UNIQUE-column caps) are resolved by finalize below, which is also
	// re-run whenever bounds or vocabulary change the constraint picture.
	rowsTarget := make(map[string]int, len(rowsOf))
	for k, v := range rowsOf {
		rowsTarget[k] = v
	}
	plan := &Plan{cfg: cfg, pools: pools, byName: byName, rowsOf: rowsOf, rowsTarget: rowsTarget, conv: conv}
	// Ordering first: the union pass must not claim a column an ordering
	// CHECK already places, and the two shapes are disjoint by construction
	// (a union branch's comparisons are against LITERALS, never a sibling
	// column), so neither pass can steal the other's constraint.
	plan.orderChains, plan.orderWarns = buildOrderChains(tables)
	plan.unions, plan.unionWarns = buildUnionPlans(tables, pools, plan.orderChains)
	for _, n := range order {
		t := byName[n]
		fkByCol := map[string]schemadef.ForeignKey{}
		for _, fk := range t.ForeignKeys {
			fkByCol[fk.Column] = fk
		}

		tp := tablePlan{table: t, n: rowsOf[n]}
		for _, c := range t.Columns {
			if c.IsGenerated {
				continue // GENERATED ALWAYS AS (...) STORED — DB computes it
			}
			cp := columnPlan{col: c}
			if fk, ok := fkByCol[c.Name]; ok {
				fkCopy := fk
				cp.fk = &fkCopy
				cp.selfRef = fk.RefTable == n
				cp.uniqueFK = !cp.selfRef && uniqueSingleColumn(t, c.Name)
				if forceNull[n] != nil && forceNull[n][c.Name] {
					cp.forceNull = true
				}
				// A status guard asserting `<fk> IS NOT NULL` needs the
				// optional edge resolved on every row — see
				// columnPlan.requireEdge. Read from the union specs, which
				// were resolved just above.
				cp.requireEdge = unionRequiresEdge(plan.unions[n], c.Name)
			}
			tp.cols = append(tp.cols, cp)
		}
		plan.tables = append(plan.tables, tp)
	}
	plan.finalize()
	return plan, nil
}

// managedRole is the seeder's view of a column's managed-timestamp role,
// resolved from the table's schemadef conventions (see managedRoleOf).
func (p *Plan) managedRole(table string, col schemadef.Column) managedRole {
	return managedRoleOf(p.conv[table], col)
}
