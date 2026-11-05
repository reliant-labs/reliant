package seedplan

import (
	"sort"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

// Composite foreign keys: one parent row, every column.
//
// `FOREIGN KEY (tenant_id, strategy_id) REFERENCES strategies (tenant_id, id)`
// is the standard way to make "a child references only a parent in its own
// tenant" a database fact. It says more than its columns do: strategy_id must
// be some strategy, tenant_id must be some strategy's tenant, AND both must be
// read off the SAME strategy row. Seeding each column on its own satisfies the
// first two and violates the third on nearly every row.
//
// So a composite is placed whole. The planner picks ONE parent row for it —
// for its anchor, the member that references the parent's primary key when
// one does — and every column the composite places takes the value that row
// holds in the referenced column. A single-column foreign key beside it
// (hypotheses.tenant_id -> tenants.id, born with the table) is then satisfied
// by the parent's own reference: the strategy's tenant_id is a real tenant.
//
// # Two composites sharing a column
//
// content_items (tenant_id, hypothesis_id) -> hypotheses and (tenant_id,
// channel_id) -> channels share tenant_id, which can hold one value. The
// first composite (constraint-name order, so the plan is the same on every
// run) places it. The second picks its parent row among the rows that AGREE
// on every column an earlier composite already placed — channels of that
// tenant — and places only the columns nothing placed before it. When no
// parent row agrees, the second composite degrades to its own pick and
// postgres reports the violation by name; forge does not invent a parent.
//
// A composite settles its columns outright: it is the schema's own statement
// of which values go together, so a `forge:ref` declaration on one of those
// columns cannot move it (see findDiamonds), and the diamond it would
// otherwise describe is not reported.
//
// # A composite that references its own table
//
// nodes (tenant_id, parent_id) -> nodes (tenant_id, id) builds a tree that
// never crosses a tenant. Its tenant_id is CIRCULAR: the column is also one
// the key references, so reading it off the parent row would read it off a
// row whose own tenant_id is being decided the same way. A circular member is
// therefore never placed by its composite.
//
// Left to its own placement, each row's tenant_id is an independent pick, and
// a row whose tenant no earlier row happens to share has no parent to point
// at — with as many tenants as nodes, most rows. So the table is shaped into
// trees instead: row 0 and about one row in five after it are roots, every
// other row hangs under a hash-picked earlier row, and each circular column
// is read off the row's root, which keeps its own placement (a real tenant).
// A node and its parent share a root, so they agree on every circular column
// by construction, and every non-root row has a parent. A root declines the
// reference (a NULL anchor) when the anchor is optional, and points at itself
// — which agrees with itself — when it is not.
//
// That shaping needs each circular column to be free: placed by its own
// single-column reference and nothing else (no composite, composite UNIQUE,
// 1-1 deal or forge:ref declaration ties it to another column of the row).
// Otherwise row i points at the latest earlier row that holds the same
// values in the circular columns, and declines like a root when there is
// none. Every other member is read off the parent row. With no circular
// member it is the one-column self-reference policy: row i -> row i-1.
//
// # Composite UNIQUE over composite members
//
// A composite UNIQUE index (tuple.go) may deal a composite's anchor a parent
// row only when nothing pins the composite — every member is its own — and
// the anchor references the parent's primary key: distinct parent rows are
// then distinct anchor values, and every other member follows the dealt row.
// Any other member a composite places is not the tuple's to deal; the tuple
// places what else it can, or reports that it cannot (see tupleSupply).

// compositeFK is one multi-column foreign-key constraint on a table.
type compositeFK struct {
	name     string
	refTable string
	members  []schemadef.ForeignKey // one per column, key order
	// anchor indexes the member the parent row is picked for: the one that
	// references the parent's single-column primary key, else the first
	// (the first non-circular one, for a self-reference).
	anchor int
	// owns[k] reports whether this composite places members[k]: no composite
	// earlier in the table's list contains that column, and the column is
	// not circular.
	owns []bool
	// self reports a composite that references its own table; circular[k]
	// reports that members[k]'s column is one the key itself references.
	self     bool
	circular []bool
}

// selfCircularMember reports whether fk is a member of a self-referencing
// composite whose column the same key references (nodes.tenant_id in
// (tenant_id, parent_id) -> nodes (tenant_id, id)). Such an entry says only
// "this column equals itself on the parent", so it is not the column's
// reference: BuildPlan does not resolve the column through it.
func selfCircularMember(fk schemadef.ForeignKey, table string) bool {
	if len(fk.KeyColumns) < 2 || fk.RefTable != table {
		return false
	}
	for _, rc := range fk.RefKeyColumns {
		if rc == fk.Column {
			return true
		}
	}
	return false
}

func (c compositeFK) column(k int) string { return c.members[k].Column }

// buildCompositeFKs groups a table's composite constraints, in constraint-name
// order, and resolves which one places each member column. owner maps a column
// to the index of the composite that places it.
func buildCompositeFKs(t schemadef.Table, byName map[string]schemadef.Table) (list []compositeFK, owner map[string]int) {
	byConstraint := map[string][]schemadef.ForeignKey{}
	for _, fk := range t.ForeignKeys {
		if len(fk.KeyColumns) > 1 && fk.Name != "" {
			byConstraint[fk.Name] = append(byConstraint[fk.Name], fk)
		}
	}
	if len(byConstraint) == 0 {
		return nil, nil
	}
	names := make([]string, 0, len(byConstraint))
	for n := range byConstraint {
		names = append(names, n)
	}
	sort.Strings(names)

	owner = map[string]int{}
	for _, n := range names {
		members := byConstraint[n]
		c := compositeFK{
			name: n, refTable: members[0].RefTable, members: members,
			owns: make([]bool, len(members)), circular: make([]bool, len(members)),
			self: members[0].RefTable == t.Name,
		}
		placeable := false
		for k, m := range members {
			c.circular[k] = c.self && selfCircularMember(m, t.Name)
			placeable = placeable || !c.circular[k]
		}
		if !placeable {
			// (a, b) -> self (b, a): every member is circular, so no column
			// can be read off a parent row. Each keeps its own placement.
			continue
		}
		c.anchor = -1
		ref := byName[c.refTable]
		for k, m := range members {
			if !c.circular[k] && len(ref.PKCols) == 1 && ref.PKCols[0] == m.RefColumn {
				c.anchor = k
				break
			}
		}
		for k := range members {
			if c.anchor < 0 && !c.circular[k] {
				c.anchor = k
			}
		}
		placesAny := false
		for k, m := range members {
			if c.circular[k] {
				continue
			}
			if _, taken := owner[m.Column]; !taken {
				c.owns[k] = true
				placesAny = true
			}
		}
		if !placesAny {
			continue // every column already placed: nothing left to decide
		}
		for k, m := range members {
			if c.owns[k] {
				owner[m.Column] = len(list)
			}
		}
		list = append(list, c)
	}
	if len(list) == 0 {
		return nil, nil
	}
	return list, owner
}

// compositeOwnerOf returns the composite that places (table, column).
func (p *Plan) compositeOwnerOf(table, column string) (int, bool) {
	ci, ok := p.compositeOwner[table][column]
	return ci, ok
}

// compositeMemberRow is fkParentRow for a column a composite places: the
// composite's parent row. Only the anchor can decline the reference (a NULL
// anchor turns the constraint off under MATCH SIMPLE); a NOT NULL partner
// still takes a real parent's value, so it stays a valid reference on its own.
func (p *Plan) compositeMemberRow(tp tablePlan, ci int, column string, i, depth int) (int, bool) {
	row, anchorNull := p.compositeRow(tp, ci, i, depth)
	c := p.composites[tp.table.Name][ci]
	if anchorNull && c.column(c.anchor) == column {
		return 0, false
	}
	return row, true
}

// compositeRow resolves the parent row composite ci points at on row i.
func (p *Plan) compositeRow(tp tablePlan, ci, i, depth int) (row int, anchorNull bool) {
	table := tp.table.Name
	c := p.composites[table][ci]
	if c.self {
		return p.selfCompositeRow(tp, c, i, depth)
	}

	// The anchor's pick, as a single-column reference would make it — the
	// row-0 spine, the optional-edge coin, the 1-1 deal — but against the
	// composite's member, so it lands on the composite's parent table.
	anchorCP := columnPlan{}
	if _, cp, ok := p.colPlan(table, c.column(c.anchor)); ok {
		anchorCP = cp
	}
	anchorCP.fk = &c.members[c.anchor]
	anchorCP.selfRef = false
	base, ok := p.tupleParentRow(table, c.column(c.anchor), i)
	if !ok {
		base, ok = p.fkParentRowIndependent(tp, anchorCP, i)
	}
	if !ok {
		// Declined. The partners still need a parent for their own sake — one
		// the composites they pin can agree with.
		if cands := p.compositeCandidates(tp, ci, depth); len(cands) > 0 {
			return cands[pick(p.cfg.EffectiveSalt(), table, c.name, i, len(cands))], true
		}
		return pick(p.cfg.EffectiveSalt(), table, c.name, i, p.rowsOf[c.refTable]), true
	}

	// Columns an earlier composite placed pin this one's parent to rows that
	// agree with them.
	var positions []int
	var want []string
	for k := range c.members {
		if c.owns[k] || depth >= maxFKDepth {
			continue
		}
		oi, owned := p.compositeOwnerOf(table, c.column(k))
		if !owned {
			continue
		}
		o := p.composites[table][oi]
		orow, _ := p.compositeRow(tp, oi, i, depth+1)
		for _, m := range o.members {
			if m.Column == c.column(k) {
				positions = append(positions, k)
				want = append(want, p.referencedValue(o.refTable, m.RefColumn, orow, depth+1))
				break
			}
		}
	}
	if len(positions) == 0 {
		// Not pinned by an earlier composite — but a later one may be pinned
		// by THIS one, and a parent row whose shared value no later parent
		// carries leaves that composite nothing to agree with. Draw from the
		// rows every partner can match. A dealt anchor (1-1, composite
		// UNIQUE) is a bijection and is never narrowed.
		if anchorCP.uniqueFK || p.tupleOwns(table, c.column(c.anchor)) {
			return base, false
		}
		cands := p.compositeCandidates(tp, ci, depth)
		if len(cands) == 0 {
			return base, false // no partner, or none satisfiable: degrade
		}
		if i == 0 {
			return cands[0], false
		}
		return cands[pick(p.cfg.EffectiveSalt(), table, c.name, i, len(cands))], false
	}
	matches := p.compositeMatches(table, c, positions, depth)[tupleKey(want)]
	if len(matches) == 0 {
		return base, false // nothing agrees: degrade rather than invent a parent
	}
	if i == 0 {
		return matches[0], false
	}
	return matches[pick(p.cfg.EffectiveSalt(), table, c.name, i, len(matches))], false
}

// compositeCandidates returns the parent rows of composite ci that every later
// composite pinned only by ci can agree with, or nil when no later composite
// is pinned by ci alone. Memoized per finalize.
func (p *Plan) compositeCandidates(tp tablePlan, ci, depth int) []int {
	table := tp.table.Name
	key := table + "\x00" + strconv.Itoa(ci)
	if cands, ok := p.compositeCands[key]; ok {
		return cands
	}
	comps := p.composites[table]
	c := comps[ci]
	refColOf := map[string]string{}
	for _, m := range c.members {
		refColOf[m.Column] = m.RefColumn
	}

	type partner struct {
		c         compositeFK
		positions []int
		cols      []string
	}
	var partners []partner
	for j := ci + 1; j < len(comps); j++ {
		if comps[j].self {
			continue // narrows its parent among its own rows (selfCompositeRow)
		}
		pc := partner{c: comps[j]}
		solely := true
		for k := range comps[j].members {
			if comps[j].owns[k] {
				continue
			}
			if oi, _ := p.compositeOwnerOf(table, comps[j].column(k)); oi != ci {
				solely = false
				break
			}
			pc.positions = append(pc.positions, k)
			pc.cols = append(pc.cols, comps[j].column(k))
		}
		if solely && len(pc.positions) > 0 {
			partners = append(partners, pc)
		}
	}

	var cands []int
	if len(partners) > 0 {
		cands = []int{}
		for r := 0; r < p.rowsOf[c.refTable]; r++ {
			ok := true
			for _, pc := range partners {
				vals := make([]string, len(pc.cols))
				for i, col := range pc.cols {
					vals[i] = p.referencedValue(c.refTable, refColOf[col], r, depth+1)
				}
				if len(p.compositeMatches(table, pc.c, pc.positions, depth)[tupleKey(vals)]) == 0 {
					ok = false
					break
				}
			}
			if ok {
				cands = append(cands, r)
			}
		}
	}
	if p.compositeCands == nil {
		p.compositeCands = map[string][]int{}
	}
	p.compositeCands[key] = cands
	return cands
}

// compositeMatches indexes the composite's parent rows by the values they hold
// at the given key positions. Memoized per finalize: the index is a pure
// function of the plan and its row counts.
func (p *Plan) compositeMatches(table string, c compositeFK, positions []int, depth int) map[string][]int {
	parts := make([]string, len(positions))
	for i, k := range positions {
		parts[i] = strconv.Itoa(k)
	}
	key := table + "\x00" + c.name + "\x00" + strings.Join(parts, ",")
	if idx, ok := p.compositeMemo[key]; ok {
		return idx
	}
	idx := map[string][]int{}
	vals := make([]string, len(positions))
	for r := 0; r < p.rowsOf[c.refTable]; r++ {
		for i, k := range positions {
			vals[i] = p.referencedValue(c.refTable, c.members[k].RefColumn, r, depth+1)
		}
		v := tupleKey(vals)
		idx[v] = append(idx[v], r)
	}
	if p.compositeMemo == nil {
		p.compositeMemo = map[string]map[string][]int{}
	}
	p.compositeMemo[key] = idx
	return idx
}

// tupleKey encodes a tuple of literals as one map key. Each value is length-
// prefixed, so no value — whatever bytes it holds — can make two distinct
// tuples encode alike (a bare separator join reads ["a\x1fb", "c"] and
// ["a", "b\x1fc"] as the same key).
func tupleKey(vals []string) string {
	var b strings.Builder
	for _, v := range vals {
		b.WriteString(strconv.Itoa(len(v)))
		b.WriteByte(':')
		b.WriteString(v)
	}
	return b.String()
}

// selfTreeComposite returns the self-referencing composite that shapes its
// table into trees: the first (constraint-name order) whose circular columns
// all have a free single-column reference of their own — nothing else (a
// composite, a composite UNIQUE, a 1-1 deal, a forge:ref declaration) decides
// them. Its circular columns are then read off each row's tree root, so
// every non-root row has a parent that agrees with it by construction.
func (p *Plan) selfTreeComposite(table string) (compositeFK, bool) {
	for _, c := range p.composites[table] {
		if !c.self {
			continue
		}
		free, hasCircular := true, false
		for k := range c.members {
			if !c.circular[k] {
				continue
			}
			hasCircular = true
			if !p.freeCircular(table, c.column(k)) {
				free = false
				break
			}
		}
		if hasCircular && free {
			return c, true
		}
	}
	return compositeFK{}, false
}

// freeCircular reports whether a circular column's value comes from its own
// single-column reference alone, so reading it off another row of the same
// table moves nothing else the plan has tied to it.
func (p *Plan) freeCircular(table, column string) bool {
	_, cp, ok := p.colPlan(table, column)
	if !ok || cp.fk == nil || cp.selfRef || cp.uniqueFK || cp.forceNull || cp.col.IsPK {
		return false
	}
	if _, owned := p.compositeOwnerOf(table, column); owned {
		return false
	}
	if p.tupleOwns(table, column) {
		return false
	}
	if _, derived := p.derivedRefs[table][column]; derived {
		return false
	}
	if _, _, paired := p.authPairFor(table, column); paired {
		return false
	}
	return true
}

// selfTreeIsRoot reports whether row i of a tree-shaped table starts a tree:
// row 0, and about one row in five after it.
func (p *Plan) selfTreeIsRoot(table string, c compositeFK, i int) bool {
	return i == 0 || cellHash(p.cfg.EffectiveSalt(), table, c.name, i)%5 == 0
}

// selfTreeParent is the earlier row a non-root row i hangs under.
func (p *Plan) selfTreeParent(table string, c compositeFK, i int) int {
	return pick(p.cfg.EffectiveSalt(), table, c.name+"\x00parent", i, i)
}

// selfTreeRoot is the root of row i's tree: the row whose own placement
// every circular column of the tree takes.
func (p *Plan) selfTreeRoot(table string, c compositeFK, i int) int {
	for !p.selfTreeIsRoot(table, c, i) {
		i = p.selfTreeParent(table, c, i)
	}
	return i
}

// selfTreeCircularRow redirects a circular column of the table's tree
// composite to the root of row i's tree; ok is false for any other column.
func (p *Plan) selfTreeCircularRow(table, column string, i int) (int, bool) {
	c, ok := p.selfTreeComposite(table)
	if !ok {
		return 0, false
	}
	for k := range c.members {
		if c.circular[k] && c.column(k) == column {
			return p.selfTreeRoot(table, c, i), true
		}
	}
	return 0, false
}

// selfCompositeRow resolves the row of its own table a self-referencing
// composite points at on row i (see the package comment above). For the
// table's tree composite it is row i's tree parent, which shares row i's
// root and so every circular value; for any other, the latest earlier row
// that agrees with row i on every circular column.
func (p *Plan) selfCompositeRow(tp tablePlan, c compositeFK, i, depth int) (row int, anchorNull bool) {
	table := tp.table.Name
	anchorOptional := true
	for _, col := range tp.table.Columns {
		if col.Name == c.column(c.anchor) {
			anchorOptional = !col.NotNull
		}
	}
	if _, cp, ok := p.colPlan(table, c.column(c.anchor)); ok && cp.requireEdge {
		anchorOptional = false
	}
	if tree, ok := p.selfTreeComposite(table); ok && tree.name == c.name {
		if !p.selfTreeIsRoot(table, c, i) {
			return p.selfTreeParent(table, c, i), false
		}
		if anchorOptional {
			return 0, true
		}
		// A NOT NULL root points at itself, which agrees with itself.
		return i, false
	}
	var positions []int
	for k := range c.members {
		if c.circular[k] {
			positions = append(positions, k)
		}
	}
	if len(positions) == 0 {
		// Nothing to agree on: the one-column self-reference tree.
		if i == 0 {
			return 0, anchorOptional
		}
		return i - 1, false
	}
	if depth >= maxFKDepth {
		return i, false
	}
	want := make([]string, len(positions))
	for x, k := range positions {
		want[x] = p.referencedValue(table, c.column(k), i, depth+1)
	}
	// Rows are indexed in ascending order: the latest one before i.
	rows := p.compositeMatches(table, c, positions, depth)[tupleKey(want)]
	if n := sort.SearchInts(rows, i); n > 0 {
		return rows[n-1], false
	}
	if anchorOptional {
		return 0, true
	}
	// A NOT NULL anchor with no predecessor: the row itself agrees with
	// itself wherever a circular column references itself.
	return i, false
}

// compositeDealable reports whether a composite UNIQUE assignment may deal
// (table, column) a parent row: the column is the anchor of a composite that
// nothing pins and that references its parent's primary key, so distinct
// parent rows are distinct values and every partner follows the dealt row.
// placed is false when no composite places the column.
func (p *Plan) compositeDealable(table, column string) (dealable, placed bool) {
	ci, placed := p.compositeOwnerOf(table, column)
	if !placed {
		return false, false
	}
	c := p.composites[table][ci]
	if c.self || c.column(c.anchor) != column {
		return false, true
	}
	for k := range c.members {
		if !c.owns[k] {
			return false, true
		}
	}
	ref := p.byName[c.refTable]
	return len(ref.PKCols) == 1 && ref.PKCols[0] == c.members[c.anchor].RefColumn, true
}
