package seedplan

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

// Minimal plans — the smallest row the schema accepts (Config.Minimal).
//
// The dev dataset and a test factory want opposite rows from one schema. The
// dataset wants every column populated and every discriminated-union branch
// covered, so the app has something to show on every screen; that is what the
// rest of this package builds. A test factory wants the row a create would
// leave behind: its initial lifecycle state, with nothing set that the schema
// did not ask for. Handing a test the dataset's row is how a generated
// factory produced a LEAD job that already carried a crew, a schedule, a
// completion time and a lost reason — legal against every constraint, and a
// row no lifecycle ever reaches. Every test that used it had to override the
// fields back off before it could assert anything.
//
// So a minimal plan WRITES a column only when the database will not supply an
// acceptable value on its own:
//
//   - a primary-key column (the factory binds a fresh id);
//   - a NOT NULL foreign key (referential coherence is the one thing the
//     planner guarantees by construction, so it is never left to a DEFAULT);
//   - a NOT NULL column with no DEFAULT;
//   - a NOT NULL column whose DEFAULT the column's own CHECK rejects —
//     `title TEXT NOT NULL DEFAULT '' CHECK (char_length(title) >= 3)` is a
//     table where omitting the column cannot succeed;
//   - a NOT NULL column whose value must differ row to row (a UNIQUE member)
//     or sit relative to another column (an ordering CHECK);
//   - a column another planned table's foreign key points at.
//
// Everything else is left OUT of the INSERT: a defaulted column takes its
// DEFAULT, a nullable one is NULL, and a GENERATED one was never writable.
//
// # Multi-column CHECKs
//
// A discriminated union — and the status guards guard.go rewrites into one —
// is still satisfied by PLACING one branch whole; only the choice of branch
// changes. The dataset rotates branches across rows for coverage. A minimal
// row takes the branch closest to the default row (see minimalBranchIndex):
// first the one that keeps every pinned column at its DEFAULT, then the one
// that forces the fewest extra columns, then the constraint's own order. For
// a lifecycle guard that is the branch pinned to the status column's DEFAULT
// — its initial state, and the state a create that leaves the status unset
// actually stores — with whatever stamps that state requires written, and
// nothing else. Every row of a minimal plan takes
// the same branch: a caller minting rows through one request shape needs one
// field list, and which columns a branch requires absent is a property of
// the list.
//
// A guard forge cannot read at all (one whose consequent is not in the union
// grammar) is the case where minimal is most valuable rather than least:
// the dataset draws the status independently and lands on a guarded state by
// chance, while a minimal row leaves the status at its DEFAULT and never
// reaches the guard.
//
// # What it does not change
//
// Values are the planner's own: every column a minimal row writes carries
// exactly the value the full plan would have given it, so the two modes
// cannot disagree about what a valid value looks like — only about which
// columns are worth writing.

// minimalChoice is a minimal plan's resolved union branch for one spec.
// Branch indices are into unionSpec.branches.
type minimalChoice []int

// minimalBranchIndex picks the branch a minimal row takes for one union spec.
// Branches are ranked, in order, by:
//
//  1. how many columns the branch PINS away from their DEFAULT — a DEFAULT is
//     the schema's own statement of a fresh row's state, and it is also what
//     a create that leaves the column unset stores, so a branch that keeps
//     it is the one a real first write can satisfy;
//  2. how many columns it forces to be written beyond the base minimal row;
//  3. the order the constraint spells it.
func minimalBranchIndex(t schemadef.Table, base map[string]bool, spec unionSpec) int {
	best := 0
	var bestKey [2]int
	for i, branch := range spec.branches {
		var key [2]int
		for name, cell := range branch {
			col, ok := tableColumnByName(t, name)
			if !ok || base[name] {
				continue
			}
			if cell.lit != "" && !defaultIs(col, cell.raw) {
				key[0]++
			}
			if minimalCellWrites(col, false, cell) {
				key[1]++
			}
		}
		if i == 0 || key[0] < bestKey[0] || (key[0] == bestKey[0] && key[1] < bestKey[1]) {
			best, bestKey = i, key
		}
	}
	return best
}

// minimalCellWrites reports whether a minimal row must write col once the
// chosen branch's placement for it is applied on top of the base decision.
func minimalCellWrites(col schemadef.Column, base bool, cell unionCell) bool {
	switch {
	case cell.null:
		// Leaving the column out already yields NULL — unless a DEFAULT
		// would fill it, in which case the NULL has to be written.
		return col.Default != ""
	case cell.lit != "":
		// A pin the DEFAULT already supplies costs nothing.
		return base || !defaultIs(col, cell.raw)
	case cell.hasBound, cell.requireEdge:
		return true
	case cell.present:
		// NOT NULL and defaulted columns are never NULL; only a nullable
		// column with no DEFAULT has to be written to be present.
		return base || (!col.NotNull && col.Default == "")
	}
	return base
}

// minimalBaseWrites is the per-column half of the minimal decision — what a
// minimal row writes before any multi-column CHECK has a say. referenced is
// the set of this table's columns another planned table's foreign key points
// at (nil when unknown).
func minimalBaseWrites(t schemadef.Table, conv schemadef.Conventions, ordered map[string]orderSlot, pools EnumPools, referenced map[string]bool, col schemadef.Column) bool {
	switch {
	case col.IsGenerated:
		return false
	case col.IsPK:
		return true
	case managedRoleOf(conv, col) == managedDeletedAt:
		return false // a live row: the soft-delete marker stays NULL
	case referenced[col.Name]:
		return true // a child row's reference must name a value this row holds
	case !col.NotNull:
		// NULL: an optional reference declined, NULLs never collide in a
		// UNIQUE index, and a CHECK over a NULL operand does not fail.
		return false
	case isForeignKey(t, col.Name):
		return true
	case col.Default == "":
		return true
	case !defaultHonorsColumnChecks(t, col, pools):
		return true
	case inUniqueIndex(t, col.Name):
		return true // every row would take the same DEFAULT
	}
	if _, placed := ordered[col.Name]; placed {
		// The ordering pass places this column relative to its partners'
		// synthesized values; a DEFAULT (now(), 0) knows nothing of them.
		return true
	}
	return false
}

// applyMinimal resolves a minimal plan's decisions: the union branch each
// spec takes, which columns each table writes, and which optional references
// the chosen branches require present. Called from finalize; a no-op for a
// full plan.
func (p *Plan) applyMinimal() {
	p.minimalBranches = nil
	if !p.cfg.Minimal {
		return
	}
	referenced := map[string]map[string]bool{}
	for _, tp := range p.tables {
		for _, fk := range tp.table.ForeignKeys {
			if referenced[fk.RefTable] == nil {
				referenced[fk.RefTable] = map[string]bool{}
			}
			referenced[fk.RefTable][fk.RefColumn] = true
		}
	}
	p.minimalBranches = map[string]minimalChoice{}
	for i := range p.tables {
		tp := &p.tables[i]
		t := tp.table
		conv := p.conv[t.Name]
		ordered := p.orderChains[t.Name]
		base := make(map[string]bool, len(t.Columns))
		for _, c := range t.Columns {
			base[c.Name] = minimalBaseWrites(t, conv, ordered, p.pools, referenced[t.Name], c)
		}
		specs := p.unions[t.Name]
		choice := make(minimalChoice, len(specs))
		cells := map[string]unionCell{}
		for s, spec := range specs {
			if len(spec.branches) == 0 {
				continue
			}
			choice[s] = minimalBranchIndex(t, base, spec)
			for name, cell := range spec.branches[choice[s]] {
				cells[name] = cell
			}
		}
		p.minimalBranches[t.Name] = choice
		for j := range tp.cols {
			cp := &tp.cols[j]
			name := cp.col.Name
			write := base[name]
			cell, placed := cells[name]
			if placed {
				write = minimalCellWrites(cp.col, base[name], cell)
			}
			cp.omit = !write
			if cp.fk != nil {
				cp.requireEdge = placed && cell.requireEdge
			}
		}
	}
}

// unionBranch returns the branch row i of a table's s-th union spec takes:
// round robin for a full plan (coverage), the fixed minimal branch for a
// minimal one.
func (p *Plan) unionBranch(table string, s int, spec unionSpec, i int) map[string]unionCell {
	if choice, ok := p.minimalBranches[table]; ok && s < len(choice) {
		return spec.branches[choice[s]]
	}
	return spec.branches[i%len(spec.branches)]
}

// Writes reports whether the plan's INSERT for table names column. A full
// plan writes every non-GENERATED column it plans; a minimal plan (see
// Config.Minimal) leaves out the columns the database fills on its own —
// those take their DEFAULT, or NULL. A column the plan does not know is not
// written.
func (p *Plan) Writes(table, column string) bool {
	_, cp, ok := p.colPlan(table, column)
	return ok && !cp.omit
}

// MinimalUnionPlacement is UnionPlacement for a minimal row: what every
// placeable discriminated-union CHECK on t requires of the branch a minimal
// plan takes (see minimalBranchIndex) — the same branch on every row. nil
// when the table carries no such constraint, or none forge can place.
//
// It exists for the same reason UnionPlacement does: the generated CRUD
// create-request fixtures mint rows through the create RPC rather than this
// planner, and must land on the same branch the minimal factory row does, or
// the two would disagree about one schema. Columns the create leaves unset
// take their DEFAULT, so the branch a create can actually satisfy is the
// default-closest one, not whichever the dataset's rotation reaches first.
func MinimalUnionPlacement(t schemadef.Table, pools EnumPools) map[string]UnionCell {
	ordered, _ := tableOrderChains(t)
	specs, _ := tableUnionSpecs(t, pools, ordered)
	if len(specs) == 0 {
		return nil
	}
	conv := schemadef.DetectConventions(t)
	base := make(map[string]bool, len(t.Columns))
	for _, c := range t.Columns {
		base[c.Name] = minimalBaseWrites(t, conv, ordered, pools, nil, c)
	}
	out := map[string]UnionCell{}
	for _, spec := range specs {
		if len(spec.branches) == 0 {
			continue
		}
		for name, cell := range spec.branches[minimalBranchIndex(t, base, spec)] {
			out[name] = exportUnionCell(cell)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// tableColumnByName finds a column of t.
func tableColumnByName(t schemadef.Table, name string) (schemadef.Column, bool) {
	for _, c := range t.Columns {
		if c.Name == name {
			return c, true
		}
	}
	return schemadef.Column{}, false
}

// inUniqueIndex reports whether col is a key of ANY unique index on t — one
// column or several, full or partial, bare or case-folded. Deliberately the
// broadest reading: it decides whether a minimal row may leave a NOT NULL
// column to its DEFAULT, and the only cost of writing one it could have left
// out is a value the planner already knows how to make distinct.
func inUniqueIndex(t schemadef.Table, col string) bool {
	for _, ix := range t.Indexes {
		if !ix.Unique {
			continue
		}
		for _, c := range ix.Columns {
			if c == col {
				return true
			}
		}
		for _, k := range ix.KeyList() {
			if k.Column == col {
				return true
			}
		}
	}
	return false
}

// defaultCastRE strips one trailing `::type` cast from a rendered DEFAULT —
// pg_get_expr spells a literal default as `'OPEN'::text`, `0`, `false`,
// `'{}'::jsonb`, `'x'::character varying`.
var defaultCastRE = regexp.MustCompile(`::[a-zA-Z_][a-zA-Z0-9_ ."]*(?:\[\])?$`)

// literalDefault decodes a column DEFAULT that is a plain literal — a quoted
// string, a number, a boolean — into its raw value. ok is false for an
// expression (now(), gen_random_uuid(), nextval(...)) and for no DEFAULT.
func literalDefault(def string) (string, bool) {
	s := strings.TrimSpace(def)
	for {
		next := strings.TrimSpace(defaultCastRE.ReplaceAllString(unwrapParens(s), ""))
		if next == s {
			break
		}
		s = next
	}
	switch {
	case s == "":
		return "", false
	case len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'':
		return strings.ReplaceAll(s[1:len(s)-1], "''", "'"), true
	case s == "true" || s == "false":
		return s, true
	}
	if _, err := strconv.ParseFloat(s, 64); err == nil {
		return s, true
	}
	return "", false
}

// defaultIs reports whether col's DEFAULT is exactly the literal raw.
func defaultIs(col schemadef.Column, raw string) bool {
	def, ok := literalDefault(col.Default)
	if !ok {
		return false
	}
	if def == raw {
		return true
	}
	a, errA := strconv.ParseFloat(def, 64)
	b, errB := strconv.ParseFloat(raw, 64)
	return errA == nil && errB == nil && a == b
}

// defaultHonorsColumnChecks reports whether col's DEFAULT satisfies the
// single-column constraints the planner can read on it: a CHECK vocabulary, a
// numeric range, a length bound, a pattern. An expression DEFAULT is the
// database's own business and is trusted; so is any constraint the planner
// cannot read. The question it answers is narrow on purpose — "can a minimal
// row leave this column out?" — and a wrong "yes" is caught where it matters,
// by the caller inserting the row.
func defaultHonorsColumnChecks(t schemadef.Table, col schemadef.Column, pools EnumPools) bool {
	raw, ok := literalDefault(col.Default)
	if !ok {
		return true
	}
	if vals, pooled := pools.get(t.Name, col.Name); pooled {
		member := false
		for _, v := range vals {
			if v == raw {
				member = true
				break
			}
		}
		if !member {
			return false
		}
	}
	for _, ck := range t.Checks {
		if len(ck.Columns) != 1 || ck.Columns[0] != col.Name {
			continue
		}
		b, bounded := boundFromCheckDef(col.Name, ck.Def)
		if !bounded {
			continue
		}
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return false
		}
		if (b.Min != nil && v < float64(*b.Min)) || (b.Max != nil && v > float64(*b.Max)) {
			return false
		}
	}
	if col.Type == schemadef.TypeString && !col.IsArray {
		minLen, maxLen := LengthBounds(t, col)
		n := utf8.RuneCountInString(raw)
		if (minLen > 0 && n < minLen) || (maxLen > 0 && n > maxLen) {
			return false
		}
		for _, pat := range patternsOf(t, col) {
			// A pattern Go's RE2 cannot compile (a lookahead) is one the
			// planner cannot read; like any unreadable constraint, it does
			// not overrule the DEFAULT.
			if re, err := regexp.Compile(pat); err == nil && !re.MatchString(raw) {
				return false
			}
		}
	}
	return true
}
