package seedplan

import (
	"fmt"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

// Composite UNIQUE — distinctness that no single column carries.
//
// The planner already models the one-column form: a UNIQUE data column gets a
// distinct value per row (constraints.go, assignUnique), and a UNIQUE foreign
// key gets a distinct PARENT per row (the uniqueFK bijection in plan.go). Both
// read the same fact off the same index. Neither of them looks at
//
//	UNIQUE (org_id, user_id)
//
// which is not a statement about org_id, and not a statement about user_id: it
// is a statement about the PAIR. Each column may — must — repeat; only the
// tuple may not. Drawing the two independently is exactly what the seeder did,
// and on the real control-plane schema it collided by row six:
//
//	duplicate key value violates unique constraint "org_memberships_org_id_user_id_key"
//
// which, the seed being one transaction, took every other table down with it.
//
// # The assignment
//
// A composite key is an ODOMETER. Give each member a supply of distinct values
// with a known size, order the members, and read row i off as a mixed-radix
// number: member 0 advances every row, member 1 every c0 rows, and so on. Two
// different row indices below the product of the sizes produce two different
// digit tuples, so the tuple is distinct BY CONSTRUCTION rather than by luck —
// the same guarantee the one-column paths give, generalized to k columns.
//
// Two properties of the reading are worth stating, because they are what keep
// it from being disruptive:
//
//   - Members are ordered by DESCENDING supply size, so the widest column is
//     the one that advances every row.
//   - A member whose stride has already reached the row target contributes a
//     digit that is CONSTANT over every row the plan writes. Such a member
//     carries no information, so it is not assigned at all — it keeps its
//     ordinary draw. On the common shape (two foreign keys, both parents at
//     least as numerous as the child's row target) exactly ONE column is
//     placed and the other keeps its natural scatter, which is precisely the
//     generalization of `uniqueFK`'s "child row i takes parent row i".
//
// # What counts as a supply
//
//   - A FOREIGN KEY member supplies its parent's rows: digit k means parent
//     row k. This is where a composite key differs most from a single-column
//     one — the members of `UNIQUE (org_id, user_id)` are usually BOTH
//     references, each of which repeats freely on its own, and the pair is
//     made distinct by moving one of them deterministically rather than by
//     making either column unique.
//   - A plain data member supplies the distinct values the single-column
//     machinery would give it (assignUnique): its CHECK/vocabulary members
//     drawn without replacement, or its per-row synthesized values. Calling
//     that code rather than restating it is what keeps one length model, one
//     pattern model and one probe.
//   - Anything else — a key member, a generated or array column, a column an
//     ordering or a discriminated union already places, a managed timestamp —
//     supplies nothing. It is left alone and counted as one, which is the
//     conservative accounting: an unassigned column may happen to vary, and
//     nothing here relies on it.
//
// # What is skipped, and what is refused
//
// Skipped in silence, because the constraint is already satisfied or cannot
// bind: a member that is itself distinct per row (a single-column UNIQUE, a
// UNIQUE foreign key, a key column), a member that is always NULL (a
// force-nulled cycle edge, a soft-delete marker — postgres never conflicts two
// NULLs), and a partial index whose predicate is false of every row this plan
// writes (partialIndexBinds, the same reading the one-column path uses).
//
// Refused loudly, in the two shapes the rest of the package already uses: a
// supply too small for the row target CAPS the table and says so, exactly as a
// one-column UNIQUE with a short vocabulary does; a constraint where NO member
// supplies anything gets the "forge cannot place its values" warning, ending
// in the same clause every other refusal here ends in.

// tupleMember is one column of a composite UNIQUE index, resolved into the
// supply it draws distinct values from.
type tupleMember struct {
	column string
	// card is how many distinct values the supply holds.
	card int
	// stride is how many rows pass between two advances of this digit.
	stride int
	// fk is true when the digit names a PARENT ROW rather than a value; the
	// literal then comes from the referential machinery as it always does.
	fk bool
	// values is digit -> rendered SQL literal, for a non-fk member.
	values []string
}

// digit is the value this member takes at row i.
func (m tupleMember) digit(i int) int {
	if m.card <= 0 {
		return 0
	}
	return (i / m.stride) % m.card
}

// tupleAssign is one composite UNIQUE index resolved into a per-row placement.
type tupleAssign struct {
	index   string
	members []tupleMember
}

// tupleMaxCells bounds the odometer's capacity arithmetic. The product of a
// wide key's supplies overflows an int long before it stops being "more
// combinations than any seed will ever want", and a saturating ceiling keeps
// the comparison against the row target honest without carrying big integers.
const tupleMaxCells = 1 << 40

// tupleParentRow returns the parent row a composite-UNIQUE assignment pins a
// foreign-key member to. It is consulted BEFORE the diamond machinery, and
// resolveDiamonds refuses to claim a column it owns, so the two can never
// disagree about one column.
func (p *Plan) tupleParentRow(table, column string, i int) (int, bool) {
	for _, ta := range p.tuples[table] {
		for _, m := range ta.members {
			if m.fk && m.column == column {
				return m.digit(i), true
			}
		}
	}
	return 0, false
}

// tupleLiteral returns the literal a composite-UNIQUE assignment places in a
// non-foreign-key member.
func (p *Plan) tupleLiteral(table, column string, i int) (string, bool) {
	for _, ta := range p.tuples[table] {
		for _, m := range ta.members {
			if !m.fk && m.column == column {
				if d := m.digit(i); d < len(m.values) {
					return m.values[d], true
				}
			}
		}
	}
	return "", false
}

// tupleOwns reports whether a composite-UNIQUE assignment places the column.
// Read by resolveDiamonds, for the same reason it consults uniqueFK: a
// declaration that would re-pick this column's parent breaks a distinctness
// the plan has already proven, and honouring it means aborting the INSERT.
func (p *Plan) tupleOwns(table, column string) bool {
	for _, ta := range p.tuples[table] {
		for _, m := range ta.members {
			if m.column == column {
				return true
			}
		}
	}
	return false
}

// assignTuples resolves every composite UNIQUE index on one table. It may
// LOWER tp.n (the same treatment a short one-column vocabulary gets) and
// returns the warnings for the caps it applied and the constraints it could
// not place.
func (p *Plan) assignTuples(tp *tablePlan) ([]tupleAssign, []string) {
	var (
		out   []tupleAssign
		warns []string
	)
	for _, ix := range tp.table.Indexes {
		// Expression keys are opaque: forge cannot place a value that
		// controls `lower(sku)` without evaluating the expression, and
		// Columns holds only the bare-column part of the key. Planning
		// against that prefix produces tuples that satisfy a constraint the
		// table does not have while violating the one it does.
		//
		// Say so rather than skip silently: the insert can still fail on
		// the real index, and a seeder that reported nothing left the
		// author reading `forge generated both this schema and this data,
		// and they disagree` with no way to reach the cause.
		if ix.Unique && ix.Expression && !foldOnlyKeys(ix) && partialIndexBinds(tp.table, ix) {
			warns = append(warns, fmt.Sprintf(
				"seed plan: %s unique index %q keys on an EXPRESSION forge cannot place (%s) — "+
					"seeded rows satisfy it only by chance. Index the bare column, or key on "+
					"lower()/upper(), if you want forge to seed it.",
				tp.table.Name, ix.Name, unplaceableKeys(ix)))
			continue
		}
		if !ix.Unique || len(ix.KeyList()) < 2 || !partialIndexBinds(tp.table, ix) {
			continue
		}
		ta, warn, ok := p.assignOneTuple(tp, ix)
		if warn != "" {
			warns = append(warns, warn)
		}
		if ok {
			out = append(out, ta)
		}
	}
	return out, warns
}

// foldOnlyKeys reports whether every EXPRESSION key of ix is a case fold over
// a single column — the one expression shape the planner can satisfy, because
// "distinct ignoring case" is a rule about values rather than about SQL.
// A key that is anything else (`(a || b)`, `(meta->>'k')`) makes the whole
// index unplaceable: its value cannot be controlled by choosing a column's.
func foldOnlyKeys(ix schemadef.Index) bool {
	for _, k := range ix.KeyList() {
		if k.Column == "" {
			return false
		}
	}
	return true
}

// unplaceableKeys renders the keys that made an index unplaceable, so the
// warning names the expression the author has to change rather than only the
// index.
func unplaceableKeys(ix schemadef.Index) string {
	var out []string
	for _, k := range ix.KeyList() {
		if k.Column == "" {
			out = append(out, k.Expr)
		}
	}
	return strings.Join(out, ", ")
}

// assignOneTuple resolves one composite UNIQUE index. ok is false when nothing
// needs placing (the constraint is already satisfied, or cannot bind) or when
// forge could not place it at all — warn then carries the reason.
func (p *Plan) assignOneTuple(tp *tablePlan, ix schemadef.Index) (tupleAssign, string, bool) {
	table := tp.table.Name

	// Keys, not Columns: a folded key (`lower(sku)`) names a real column
	// that must take part in the tuple. Iterating Columns would silently
	// drop it and place a tuple distinct on the REMAINING columns only —
	// which satisfies a constraint the table does not have.
	keyCols := make([]string, 0, len(ix.KeyList()))
	for _, k := range ix.KeyList() {
		if k.Column != "" {
			keyCols = append(keyCols, k.Column)
		}
	}

	// Already satisfied, or vacuous. Both are silent: there is nothing for the
	// author to do about either.
	for _, c := range keyCols {
		cp, found := tupleColumnPlan(tp, c)
		if !found {
			continue // a generated column: the database computes it
		}
		if p.tupleMemberAlwaysNull(table, cp) || p.tupleMemberDistinctPerRow(tp, cp) {
			return tupleAssign{}, "", false
		}
	}

	members := make([]tupleMember, 0, len(keyCols))
	for _, c := range keyCols {
		cp, found := tupleColumnPlan(tp, c)
		if !found {
			continue
		}
		if m, ok := p.tupleSupply(tp, cp); ok {
			members = append(members, m)
		}
	}
	if len(members) == 0 {
		return tupleAssign{}, fmt.Sprintf(
			"seed plan: %s index %q is a composite UNIQUE but forge cannot place its values "+
				"(no column of it supplies distinct values forge may assign: every member is a key column, "+
				"a self-reference, a reference to an unseeded or single-row table, or a column another "+
				"constraint already owns) — give one member a value set forge can deal out, either a "+
				"single-column `CHECK (%s IN (…))` vocabulary or a reference to a table that seeds more "+
				"than one row, and forge can place the tuple. Until then seeded rows satisfy it only by chance",
			table, ix.Name, firstKeyColumn(ix)), false
	}

	// Widest supply advances fastest, so the fewest columns have to move.
	sort.Slice(members, func(a, b int) bool {
		if members[a].card != members[b].card {
			return members[a].card > members[b].card
		}
		return members[a].column < members[b].column
	})

	total, stride := 1, 1
	for i := range members {
		members[i].stride = stride
		if total <= tupleMaxCells/members[i].card {
			total *= members[i].card
		} else {
			total = tupleMaxCells
		}
		stride = total
	}

	warn := ""
	if total < tp.n {
		warn = fmt.Sprintf(
			"seed plan: %s index %q is a composite UNIQUE over columns admitting only %d distinct combination(s) — %s capped to %d row(s) (a UNIQUE index cannot repeat a tuple)",
			table, ix.Name, total, table, total)
		tp.n = total
		if tp.n < 1 {
			tp.n = 1
		}
	}

	// A member whose stride has already reached the row target holds the same
	// digit on every row forge writes, so assigning it would only flatten its
	// values; the rows are told apart by the members below it.
	kept := members[:0]
	for _, m := range members {
		if m.stride < tp.n {
			kept = append(kept, m)
		}
	}
	if len(kept) == 0 {
		// One row: nothing can collide with anything.
		return tupleAssign{}, warn, false
	}
	return tupleAssign{index: ix.Name, members: kept}, warn, true
}

// tupleColumnPlan finds a table plan's column by name. A column the plan does
// not carry is a GENERATED one (BuildPlan drops those: the database computes
// them and postgres rejects a write to one).
func tupleColumnPlan(tp *tablePlan, column string) (columnPlan, bool) {
	for _, cp := range tp.cols {
		if cp.col.Name == column {
			return cp, true
		}
	}
	return columnPlan{}, false
}

// tupleMemberAlwaysNull reports whether the member is NULL on every row. A
// unique index never conflicts two NULLs, so one such member makes the whole
// constraint vacuous for this dataset.
func (p *Plan) tupleMemberAlwaysNull(table string, cp columnPlan) bool {
	return cp.forceNull || p.managedRole(table, cp.col) == managedDeletedAt
}

// tupleMemberDistinctPerRow reports whether the member ALREADY takes a
// different value on every row, which makes the tuple distinct on its own and
// leaves nothing to place.
func (p *Plan) tupleMemberDistinctPerRow(tp *tablePlan, cp columnPlan) bool {
	table := tp.table.Name
	if cp.fk != nil {
		return cp.uniqueFK // the 1-1 bijection: child row i takes parent row i
	}
	if cp.col.IsPK {
		return p.keyValueVariesPerRow(table, cp.col)
	}
	return len(p.uniques[table][cp.col.Name]) >= tp.n
}

// keyValueVariesPerRow reports whether keyLiteral gives the column a different
// value on every row. Every key type does except a boolean (two values), and
// except a COMPOSITE member drawing from a CHECK vocabulary, which repeats its
// members by construction.
func (p *Plan) keyValueVariesPerRow(table string, col schemadef.Column) bool {
	if len(p.byName[table].PKCols) > 1 {
		if _, pooled := p.closedPool(table, col); pooled {
			return false
		}
	}
	return col.Type != schemadef.TypeBool
}

// firstKeyColumn names a bare column of the index, for use as the concrete
// example in a refusal. It falls back to a placeholder for an index whose keys
// are all expressions, so the advice still reads as SQL.
func firstKeyColumn(ix schemadef.Index) string {
	for _, k := range ix.KeyList() {
		if k.Column != "" {
			return k.Column
		}
	}
	return "<column>"
}

// tupleSupply classifies one member into the supply of distinct values it can
// draw from. ok is false for a column forge may not place — the caller counts
// it as contributing nothing.
func (p *Plan) tupleSupply(tp *tablePlan, cp columnPlan) (tupleMember, bool) {
	table := tp.table.Name

	if cp.fk != nil {
		// A self-reference resolves per row against this same table (row i ->
		// row i-1); it is not a supply of parents to deal out.
		if cp.selfRef {
			return tupleMember{}, false
		}
		if _, seeded := p.byName[cp.fk.RefTable]; !seeded {
			return tupleMember{}, false
		}
		n := p.rowsOf[cp.fk.RefTable]
		if n < 2 {
			return tupleMember{}, false
		}
		return tupleMember{column: cp.col.Name, card: n, fk: true}, true
	}

	// Every other mechanism that owns a column's value outranks this one, for
	// the same reason it outranks the union pass: two placements of one column
	// cannot both be honoured.
	switch {
	case cp.col.IsPK, cp.col.IsArray, cp.col.IsGenerated:
		return tupleMember{}, false
	case cp.col.Type == schemadef.TypeJSON:
		// A json column carries the document the SCHEMA declares and nothing
		// else (see jsonDocument): what its keys mean is app semantics, so
		// forge has no second document to deal out.
		return tupleMember{}, false
	case p.managedRole(table, cp.col) != managedNone:
		return tupleMember{}, false
	case unionPlacesColumn(tp.table, cp.col.Name):
		return tupleMember{}, false
	}
	if _, ordered := p.orderChains[table][cp.col.Name]; ordered {
		return tupleMember{}, false
	}

	// The one-column distinct-value machinery, reused verbatim: a closed
	// vocabulary dealt without replacement, or the column's own per-row
	// synthesis probed apart. Its warning describes a one-column UNIQUE this
	// column does not carry, so it is dropped — the composite's own cap
	// warning is the one that fits.
	// No fold here: a composite member is made distinct on its own value.
	// The composite's own key comparison is handled by the tuple assignment.
	lits, _ := p.assignUnique(tp, cp.col, "")
	if len(lits) < 2 {
		return tupleMember{}, false
	}
	return tupleMember{column: cp.col.Name, card: len(lits), values: lits}, true
}
