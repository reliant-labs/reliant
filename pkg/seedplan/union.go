package seedplan

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

// Discriminated-union CHECK constraints — one row shape per kind.
//
// The other multi-column shape this package places is an ORDERING
// (ordering.go): a comparison between two columns, satisfied by ranking them
// and stepping the values apart. A discriminated union is the other
// overwhelmingly common one, and it is not an ordering at all:
//
//	CHECK (
//	    (kind = 'wallet_credit'
//	        AND amount_cents IS NOT NULL AND amount_cents > 0
//	        AND compute_minutes IS NULL)
//	    OR
//	    (kind = 'compute_minutes'
//	        AND compute_minutes IS NOT NULL AND compute_minutes > 0
//	        AND amount_cents IS NULL)
//	)
//
// One column DISCRIMINATES, and which of the sibling columns must be present
// and which must be absent follows from its value. Drawing each column
// independently — which is what every other pass in this package does — makes
// that constraint true only by coincidence, and postgres then aborts the whole
// seed transaction. Before this pass the plan said exactly that, and wrote no
// rows for the table.
//
// # What is satisfied, and how
//
// The reading is the definition of OR: the whole expression is TRUE as soon as
// ONE branch is. So forge PICKS a branch and satisfies it WHOLE — pins the
// discriminator to that branch's literal, nulls the columns that branch
// requires absent, keeps the columns it requires present inside the range it
// states — and the other branches need no attention at all, because a true
// disjunct is a true disjunction whatever the rest says.
//
// The branch is chosen ROUND-ROBIN by row index, not by hash. Every other
// draw in this package is a hash-pick, and for a value that is one of a
// vocabulary that is right. A union's branches are not a vocabulary: they are
// the DISTINCT ROW SHAPES the table can hold, and a dataset missing one of
// them is a dataset the app's other code path has no row to run on. Round
// robin makes coverage a guarantee (every branch appears within the first
// len(branches) rows) instead of a probability, and it stays deterministic and
// salt-stable like everything else here.
//
// # The shape the matcher accepts, and everything it refuses
//
// This is deliberately NOT a CHECK-expression solver. It matches ONE flat
// shape:
//
//	<union> := <group> OR <group> [OR <group>]…      (two or more)
//	<group> := <term> [AND <term>]…                  (at least one termEq)
//	<term>  := <col> = <literal>
//	         | <col> IS NULL
//	         | <col> IS NOT NULL
//	         | <col> {> >= < <=} <numeric literal>
//
// Anything else — a nested OR inside a group, NOT, a function call, a
// subquery, a column-to-column comparison, arithmetic, IN, <>, a bare boolean
// column — fails to parse and the constraint keeps the existing "forge cannot
// place its values" warning. A general solver is the rabbit hole; a narrow
// matcher plus an honest bail is the whole design.
//
// # When a well-formed union is still refused
//
// Placement has to be PROVABLE against everything else the schema says, so a
// recognized union is refused (loudly, by name) when:
//
//   - a term names a column another mechanism owns — a key, a foreign key, a
//     single-column UNIQUE, a managed timestamp, a generated or array column,
//     or one an ordering CHECK already places;
//   - a term pins a literal of a type forge will not place (a timestamp, a
//     bytea) or of the wrong type for its column;
//   - ANOTHER multi-column constraint spans any of the same columns, so the
//     two cannot be shown jointly satisfiable — including a second union;
//   - no branch survives: every one of them contradicts something the schema
//     states elsewhere (NULL on a NOT NULL column, a pin outside the column's
//     own CHECK vocabulary or length cap, an empty range).
//
// A branch that is well-formed but individually unsatisfiable is DROPPED
// rather than fatal — the remaining branches still satisfy the OR — and only
// running out of branches is the refusal.

// unionTermKind is the one predicate shape a branch term may take.
type unionTermKind int

const (
	termEq     unionTermKind = iota // col = <literal>
	termIsNull                      // col IS NULL
	termNotNull
	termCmp // col {> >= < <=} <numeric literal>
)

// unionTerm is one parsed atom of an AND-group. It is pure syntax: whether
// the column may be placed at all is decided later, against the table.
type unionTerm struct {
	column string
	kind   unionTermKind
	// lit is how the INSERT must spell a termEq literal (quoted for a
	// string, bare for a number or boolean); raw is its undecorated value,
	// which is what a CHECK vocabulary and a length cap are checked against.
	lit, raw string
	// bound is the half-open range a termCmp states, rounded INWARD by
	// parseBoundLit exactly as the single-column range model rounds it.
	bound NumBound
}

// unionCell is the placement one branch imposes on one column: NULL, an exact
// literal, or a narrowed numeric range the column's natural value is clamped
// into. A column a branch only asserts IS NOT NULL gets a PRESENT cell, which
// places no value: a full plan never synthesizes NULL into a plain data column,
// so its natural value already satisfies it (and unionEligible is what makes
// "plain data column" true). A MINIMAL plan leaves nullable columns NULL, and
// the present cell is what tells it this one must be written (see minimal.go).
type unionCell struct {
	null bool
	// present marks a plain data column the branch asserts IS NOT NULL and
	// places nothing else on.
	present bool
	// requireEdge marks a nullable FOREIGN KEY the branch asserts IS NOT
	// NULL. It places no value — the reference path still picks the parent
	// — it only forbids that path from declining the edge on this table.
	// See columnPlan.requireEdge.
	requireEdge bool
	// lit is how an INSERT spells the pin; raw is the same value undecorated,
	// which is what a consumer that is not writing SQL — the generated CRUD
	// fixtures, see UnionPlacement — needs.
	lit, raw string
	bound    NumBound
	hasBound bool
}

// unionSpec is one placeable discriminated-union CHECK: the branches, in the
// order the constraint spells them, each mapping column -> placement.
type unionSpec struct {
	constraint string
	branches   []map[string]unionCell
}

// The atom grammar, in postgres's canonical spelling. Both operands of a
// comparison being a bare column is an ORDERING constraint, not a union
// branch, and none of these match it: the right-hand side of every one is a
// LITERAL.
var (
	unionNullRE = regexp.MustCompile(`^"?([a-z_][a-z0-9_]*)"?\s+IS\s+(NOT\s+)?NULL$`)
	unionEqRE   = regexp.MustCompile(`^"?([a-z_][a-z0-9_]*)"?\s*=\s*(.+)$`)
	unionCmpRE  = regexp.MustCompile(`^"?([a-z_][a-z0-9_]*)"?\s*(>=|>|<=|<)\s*(.+)$`)
	// The literal forms pg_get_constraintdef renders: a quoted string with an
	// optional cast, and a number that may be parenthesized and cast.
	unionStrLitRE = regexp.MustCompile(`^'((?:[^']|'')*)'(?:::[a-z_ "]+)?$`)
	unionNumLitRE = regexp.MustCompile(`^\(?(-?\d+(?:\.\d+)?)\)?(?:::[a-z_ ]+)?$`)
)

// parseUnionBranches reads the AND-groups out of a top-level OR. ok is false
// for every expression that is not the flat shape documented above — that is
// the whole of the matcher's scope discipline, and the caller must then leave
// the constraint to the existing refusal.
func parseUnionBranches(expr string) ([][]unionTerm, bool) {
	parts := splitTopLevel(unwrapParens(expr), "OR")
	if len(parts) < 2 {
		return nil, false
	}
	groups := make([][]unionTerm, 0, len(parts))
	for _, part := range parts {
		terms, ok := parseUnionGroup(part)
		if !ok {
			return nil, false
		}
		groups = append(groups, terms)
	}
	return groups, true
}

// parseUnionGroup reads one AND-group. Every conjunct must be a term this
// grammar knows, and at least one must pin a column to a literal — that
// equality is what makes the group a DISCRIMINATED branch rather than an
// arbitrary predicate that happens to sit under an OR.
func parseUnionGroup(expr string) ([]unionTerm, bool) {
	conj := splitTopLevel(unwrapParens(expr), "AND")
	terms := make([]unionTerm, 0, len(conj))
	discriminated := false
	for _, c := range conj {
		term, ok := parseUnionTerm(unwrapParens(c))
		if !ok {
			return nil, false
		}
		if term.kind == termEq {
			discriminated = true
		}
		terms = append(terms, term)
	}
	if !discriminated {
		return nil, false
	}
	return terms, true
}

// parseUnionTerm matches one atom. A nested OR, a NOT, a function call, an
// arithmetic operand or a column-to-column comparison matches nothing here and
// takes the whole constraint out of scope.
func parseUnionTerm(s string) (unionTerm, bool) {
	if m := unionNullRE.FindStringSubmatch(s); m != nil {
		kind := termIsNull
		if m[2] != "" {
			kind = termNotNull
		}
		return unionTerm{column: m[1], kind: kind}, true
	}
	if m := unionEqRE.FindStringSubmatch(s); m != nil {
		lit, raw, ok := unionLiteralOf(strings.TrimSpace(m[2]))
		if !ok {
			return unionTerm{}, false
		}
		return unionTerm{column: m[1], kind: termEq, lit: lit, raw: raw}, true
	}
	if m := unionCmpRE.FindStringSubmatch(s); m != nil {
		b, ok := unionCmpBound(m[2], strings.TrimSpace(m[3]))
		if !ok {
			return unionTerm{}, false
		}
		return unionTerm{column: m[1], kind: termCmp, bound: b}, true
	}
	return unionTerm{}, false
}

// unionCmpBound turns one `<op> <numeric literal>` into the inclusive range it
// admits. A strict operator steps one INTO the range, and parseBoundLit has
// already rounded a fractional literal inward, so every value the bound admits
// still satisfies the comparison postgres will evaluate.
func unionCmpBound(op, lit string) (NumBound, bool) {
	num := unionNumLitRE.FindStringSubmatch(lit)
	if num == nil {
		return NumBound{}, false
	}
	lower := strings.HasPrefix(op, ">")
	n, err := parseBoundLit(num[1], lower)
	if err != nil {
		return NumBound{}, false
	}
	var b NumBound
	switch op {
	case ">":
		n++
		b.Min = &n
	case ">=":
		b.Min = &n
	case "<":
		n--
		b.Max = &n
	default: // "<="
		b.Max = &n
	}
	return b, true
}

// unionLiteralOf decodes the right-hand side of an equality. lit is the
// spelling the INSERT needs, raw the undecorated value. ok is false for
// anything that is not a plain literal — a column reference, an expression, a
// function call — which is what keeps `a = b` out of this pass.
func unionLiteralOf(s string) (lit, raw string, ok bool) {
	if m := unionStrLitRE.FindStringSubmatch(s); m != nil {
		raw = strings.ReplaceAll(m[1], "''", "'")
		return sqlString(raw), raw, true
	}
	if m := unionNumLitRE.FindStringSubmatch(s); m != nil {
		return m[1], m[1], true
	}
	if s == "true" || s == "false" {
		return s, s, true
	}
	return "", "", false
}

// unionClaimedChecks names the multi-column CHECKs of t whose expression PARSES
// as a discriminated union. It is a pure parse — nothing about whether the
// placement is possible — and it exists so the ordering pass can step over
// exactly the constraints this one speaks for, instead of both passes warning
// about the same line of SQL.
func unionClaimedChecks(t schemadef.Table) map[string]bool {
	out := map[string]bool{}
	for _, ck := range t.Checks {
		if len(ck.Columns) < 2 {
			continue
		}
		body, ok := checkBody(ck.Def)
		if !ok {
			continue
		}
		if _, ok := parseUnionBranches(body); ok {
			out[ck.Name] = true
			continue
		}
		// A status guard is this pass's too (guard.go) — it is a union
		// spelled negatively. Without this the ordering pass reports every
		// guard as "not a two-column ordering comparison", which is both
		// noise and wrong: the constraint IS placed.
		if _, ok := parseStatusGuard(body); ok {
			out[ck.Name] = true
		}
	}
	return out
}

// unionPlacesColumn reports whether some union-shaped CHECK on t names col.
// Like unionClaimedChecks it reads only the PARSE, never the validated
// placement: the nullness model (see nullnessOf) consults it, and the
// placement's own eligibility check consults the nullness model.
func unionPlacesColumn(t schemadef.Table, col string) bool {
	for _, ck := range t.Checks {
		if len(ck.Columns) < 2 {
			continue
		}
		body, ok := checkBody(ck.Def)
		if !ok {
			continue
		}
		groups, ok := parseUnionBranches(body)
		if !ok {
			continue
		}
		for _, terms := range groups {
			for _, term := range terms {
				if term.column == col {
					return true
				}
			}
		}
	}
	return false
}

// buildUnionPlans resolves every table's discriminated-union CHECKs into
// per-branch placements, and returns the warnings for the ones it could not
// place. ordered is the ordering pass's result, so a column that pass already
// owns is never claimed twice.
func buildUnionPlans(tables []schemadef.Table, pools EnumPools, ordered map[string]map[string]orderSlot) (map[string][]unionSpec, []string) {
	out := map[string][]unionSpec{}
	var warns []string
	for _, t := range tables {
		specs, w := tableUnionSpecs(t, pools, ordered[t.Name])
		warns = append(warns, w...)
		if len(specs) > 0 {
			out[t.Name] = specs
		}
	}
	sort.Strings(warns)
	return out, warns
}

// tableUnionSpecs resolves one table's union CHECKs.
func tableUnionSpecs(t schemadef.Table, pools EnumPools, ordered map[string]orderSlot) ([]unionSpec, []string) {
	var (
		specs []unionSpec
		warns []string
	)
	refuse := func(name, why string) {
		warns = append(warns, fmt.Sprintf(
			"seed plan: %s constraint %q is a discriminated union but forge cannot place its values (%s) — seeded rows satisfy it only by chance",
			t.Name, name, why))
	}
	conv := schemadef.DetectConventions(t)

	// Status guards are resolved FIRST, and TOGETHER. Several guards over one
	// lifecycle column is the normal shape — a jobs table states one rule for
	// SCHEDULED and another for COMPLETED — and resolving them one at a time
	// makes each refuse the other for spanning `status` too. Merged, they are
	// ONE union over that column whose branches satisfy every guard at once.
	guarded, guardWarns, claimed := tableGuardSpecs(t, conv, pools, ordered)
	specs = append(specs, guarded...)
	warns = append(warns, guardWarns...)

	for _, ck := range t.Checks {
		if len(ck.Columns) < 2 || claimed[ck.Name] {
			continue
		}
		body, ok := checkBody(ck.Def)
		if !ok {
			continue
		}
		groups, ok := parseUnionBranches(body)
		if !ok {
			continue // not this shape — the ordering pass reports it
		}
		spec, why := buildUnionSpec(t, conv, pools, ordered, ck, groups, nil)
		if why != "" {
			refuse(ck.Name, why)
			continue
		}
		specs = append(specs, spec)
	}
	return specs, warns
}

// buildUnionSpec validates one parsed union against everything else the table
// declares and resolves it into per-branch placements. A non-empty why is a
// refusal naming the reason; the caller warns and leaves the constraint alone.
//
// speaksFor names every real constraint this spec stands for — one for a
// hand-written union, several for a merged status-guard union. Members are
// exempt from the joint-satisfiability scan below, since sharing a column is
// exactly why they were merged rather than a reason to refuse. Empty means
// "just ck".
func buildUnionSpec(
	t schemadef.Table,
	conv schemadef.Conventions,
	pools EnumPools,
	ordered map[string]orderSlot,
	ck schemadef.CheckConstraint,
	groups [][]unionTerm,
	speaksFor map[string]bool,
) (unionSpec, string) {
	if len(speaksFor) == 0 {
		speaksFor = map[string]bool{ck.Name: true}
	}
	spans := make(map[string]bool, len(ck.Columns))
	for _, c := range ck.Columns {
		spans[c] = true
	}

	touched := map[string]bool{}
	branches := make([]map[string]unionCell, 0, len(groups))
	for _, terms := range groups {
		for _, term := range terms {
			touched[term.column] = true
			// The paranoia guard the ordering pass already applies: the
			// expression must talk about the columns postgres says the
			// constraint spans, or it was not what the matcher thought.
			if !spans[term.column] {
				return unionSpec{}, fmt.Sprintf("it names %s, which postgres does not report as a column of this constraint", term.column)
			}
		}
		cells, sat, why := unionBranchCells(t, conv, pools, ordered, terms)
		if why != "" {
			return unionSpec{}, why
		}
		if !sat {
			continue // well-formed but contradicts the schema — a true branch elsewhere still satisfies the OR
		}
		branches = append(branches, cells)
	}
	if len(branches) == 0 {
		return unionSpec{}, "no branch of it is satisfiable against the rest of the schema"
	}

	// Joint satisfiability is not provable across two multi-column
	// constraints that talk about the same columns — including a second
	// union — so forge refuses rather than placing one and hoping the other
	// survives it.
	//
	// `speaksFor` is the set this spec IS. For a hand-written union that is
	// the one constraint; for a MERGED status-guard union it is every guard
	// folded into it (guard.go), which must not be mistaken for rivals to
	// itself — they were combined precisely because they share a column.
	for _, other := range t.Checks {
		if speaksFor[other.Name] || len(other.Columns) < 2 {
			continue
		}
		for _, c := range other.Columns {
			if !touched[c] {
				continue
			}
			// When the rival is a biconditional it is not merely an
			// obstacle — it is a constraint with a known one-line fix, and
			// fixing it releases THIS union as well. Saying only "cannot
			// prove the two are jointly satisfiable" is what sent a
			// dogfood author to delete a production schema constraint.
			if rewrite, isBiconditional := biconditionalRewrite(other.Def); isBiconditional {
				return unionSpec{}, fmt.Sprintf(
					"constraint %q spans %s too and %s — fixing that one releases this constraint as well",
					other.Name, c, biconditionalAdvice(rewrite, biconditionalDiscriminator(rewrite)))
			}
			return unionSpec{}, fmt.Sprintf(
				"constraint %q spans %s too and forge cannot prove the two are jointly satisfiable — "+
					"state the combined rule as ONE constraint over those columns (forge can place a single "+
					"multi-column CHECK, but not two that overlap), or narrow one of them so they no longer share %s",
				other.Name, c, c)
		}
	}
	return unionSpec{constraint: ck.Name, branches: branches}, ""
}

// unionBranchCells resolves one AND-group into per-column placements.
//
// The three answers are distinct on purpose. why != "" is a REFUSAL of the
// whole constraint: the branch names something forge may not place, so the
// matcher was out of its depth. sat == false is a branch that is perfectly
// readable and simply cannot be true (NULL on a NOT NULL column, a pin outside
// the column's own vocabulary); the OR survives on its other branches. Only
// when none survive does the caller refuse.
func unionBranchCells(
	t schemadef.Table,
	conv schemadef.Conventions,
	pools EnumPools,
	ordered map[string]orderSlot,
	terms []unionTerm,
) (cells map[string]unionCell, sat bool, why string) {
	type agg struct {
		col             schemadef.Column
		isNull, notNull bool
		eq              *unionTerm
		bound           NumBound
		hasBound        bool
	}
	byCol := map[string]*agg{}
	var order []string
	for i := range terms {
		term := terms[i]
		a, ok := byCol[term.column]
		if !ok {
			col, reason := unionEligible(t, conv, ordered, term.column)
			// A term that only asserts IS NOT NULL places no VALUE, so a
			// column another mechanism owns is still compatible with it:
			// the reference path picks the parent, and all this asks is
			// that it pick one. Without this an ordinary lifecycle guard
			// (`… OR crew_id IS NOT NULL`) is refused for naming a foreign
			// key, which is most of them.
			if reason != "" && term.kind == termNotNull && notNullOnlyFor(terms, term.column) {
				reason = ""
			}
			if reason != "" {
				return nil, false, reason
			}
			a = &agg{col: col}
			byCol[term.column] = a
			order = append(order, term.column)
		}
		switch term.kind {
		case termIsNull:
			a.isNull = true
		case termNotNull:
			a.notNull = true
		case termEq:
			if a.eq != nil && a.eq.lit != term.lit {
				return nil, false, "" // two different pins on one column: never true
			}
			a.eq = &term
		case termCmp:
			a.bound = intersectBounds(a.bound, term.bound)
			a.hasBound = true
		}
	}

	cells = make(map[string]unionCell, len(order))
	for _, name := range order {
		a := byCol[name]
		if a.isNull {
			// A branch that both requires and forbids a value is never true.
			if a.notNull || a.eq != nil || a.hasBound || a.col.NotNull {
				return nil, false, ""
			}
			cells[name] = unionCell{null: true}
			continue
		}
		declared := declaredBound(t, name)
		switch {
		case a.eq != nil:
			ok, reason := unionPinPlaceable(t, a.col, pools, *a.eq, intersectBounds(declared, a.bound))
			if reason != "" {
				return nil, false, reason
			}
			if !ok {
				return nil, false, ""
			}
			cells[name] = unionCell{lit: a.eq.lit, raw: a.eq.raw}
		case a.hasBound:
			if a.col.Type != schemadef.TypeInt && a.col.Type != schemadef.TypeFloat {
				return nil, false, fmt.Sprintf("it states a numeric range on %s, which is %s", name, a.col.Type)
			}
			b := intersectBounds(declared, a.bound)
			if b.Min != nil && b.Max != nil && *b.Min > *b.Max {
				return nil, false, ""
			}
			cells[name] = unionCell{bound: b, hasBound: true}
		default:
			// IS NOT NULL only. For a plain data column this holds by
			// construction — forge synthesizes a value on every row.
			//
			// A NULLABLE FOREIGN KEY is the exception, and it is why this
			// case is no longer a no-op: the reference path nulls an
			// optional edge on ~1 row in 5 for variety, which is exactly
			// the row that then violates the guard. Record it so the plan
			// can force the edge present (columnPlan.requireEdge).
			//
			// Any other column records that it must be present, which a
			// full plan already guarantees and a minimal plan (which leaves
			// nullable columns NULL) must act on.
			if !a.col.NotNull && isForeignKey(t, name) {
				cells[name] = unionCell{requireEdge: true}
			} else {
				cells[name] = unionCell{present: true}
			}
		}
	}
	return cells, true, ""
}

// unionEligible reports whether a union branch may PLACE a column: it must be
// an ordinary synthesized data column that nothing else in the plan owns. A
// non-empty reason is the refusal text, phrased to complete the warning's
// parenthesis.
func unionEligible(t schemadef.Table, conv schemadef.Conventions, ordered map[string]orderSlot, name string) (schemadef.Column, string) {
	var col schemadef.Column
	found := false
	for _, c := range t.Columns {
		if c.Name == name {
			col, found = c, true
			break
		}
	}
	if !found {
		return col, fmt.Sprintf("%s is not a column of the table", name)
	}
	switch {
	case col.IsPK:
		return col, fmt.Sprintf("%s is a key column, whose value the key function owns", name)
	case col.IsGenerated:
		return col, fmt.Sprintf("%s is a generated column, which the database computes", name)
	case col.IsArray:
		return col, fmt.Sprintf("%s is an array column", name)
	}
	for _, fk := range t.ForeignKeys {
		if fk.Column == name {
			return col, fmt.Sprintf("%s is a foreign key, whose value referential derivation owns", name)
		}
	}
	if uniqueSingleColumn(t, name) {
		return col, fmt.Sprintf("%s is UNIQUE, whose values the distinct-value assignment owns", name)
	}
	if managedRoleOf(conv, col) != managedNone {
		return col, fmt.Sprintf("%s is a managed timestamp, whose value the convention owns", name)
	}
	if _, placed := ordered[name]; placed {
		return col, fmt.Sprintf("%s is already placed by an ordering constraint", name)
	}
	return col, ""
}

// unionPinPlaceable reports whether a branch's `col = <literal>` pin can be
// written. ok is false for a pin the rest of the schema rejects (a value
// outside the column's own CHECK vocabulary, length cap or range) — that
// branch is simply never true. A non-empty reason refuses the whole
// constraint: the pin's TYPE is one forge will not place.
func unionPinPlaceable(t schemadef.Table, col schemadef.Column, pools EnumPools, term unionTerm, b NumBound) (bool, string) {
	quoted := strings.HasPrefix(term.lit, "'")
	switch col.Type {
	case schemadef.TypeString, schemadef.TypeJSON:
		if !quoted {
			return false, fmt.Sprintf("it pins %s, a %s column, to the non-string literal %s", col.Name, col.Type, term.lit)
		}
		minLen, maxLen := LengthBounds(t, col)
		if n := len([]rune(term.raw)); n < minLen || (maxLen > 0 && n > maxLen) {
			return false, ""
		}
	case schemadef.TypeInt, schemadef.TypeFloat:
		if quoted {
			return false, fmt.Sprintf("it pins %s, a %s column, to the string literal %s", col.Name, col.Type, term.lit)
		}
		n, err := parseBoundLit(term.raw, true)
		if err != nil {
			return false, fmt.Sprintf("it pins %s to %s, which is not a number", col.Name, term.lit)
		}
		if b.clamp(n) != n {
			return false, ""
		}
	case schemadef.TypeBool:
		if term.lit != "true" && term.lit != "false" {
			return false, fmt.Sprintf("it pins %s, a boolean column, to %s", col.Name, term.lit)
		}
	default:
		// A timestamp or a bytea pin would have to be validated in that
		// type's own grammar before forge could claim the row satisfies the
		// constraint, and guessing is how a seed aborts a transaction it
		// promised would apply.
		return false, fmt.Sprintf("it pins %s, which is %s, and forge places literals of that type only through the vocabulary overlay", col.Name, col.Type)
	}
	// The column's own CHECK vocabulary is a declaration about EVERY value, so
	// a pin outside it can never be written — however plausible the branch
	// reads.
	if pool, ok := pools.get(t.Name, col.Name); ok {
		for _, v := range pool {
			if v == term.raw {
				return true, ""
			}
		}
		return false, ""
	}
	return true, ""
}

// declaredBound is the numeric range the column's own single-column range
// CHECKs pin it to — the same model BoundsFromTables builds, read here
// directly so this pass stays a pure function of the schema and does not
// depend on whether a caller wired SetBounds.
func declaredBound(t schemadef.Table, name string) NumBound {
	var have NumBound
	for _, ck := range t.Checks {
		if len(ck.Columns) != 1 || ck.Columns[0] != name {
			continue
		}
		b, ok := boundFromCheckDef(name, ck.Def)
		if !ok {
			continue
		}
		have = intersectBounds(have, b)
	}
	return have
}

// intersectBounds returns the tightest range satisfying both.
func intersectBounds(a, b NumBound) NumBound {
	if b.Min != nil && (a.Min == nil || *b.Min > *a.Min) {
		a.Min = b.Min
	}
	if b.Max != nil && (a.Max == nil || *b.Max < *a.Max) {
		a.Max = b.Max
	}
	return a
}

// unionLiteral returns the literal a discriminated-union CHECK places in one
// cell, and whether it places one at all. The branch is chosen by row index,
// so every branch's row shape appears in the dataset and one row's columns all
// come from the SAME branch — which is the entire point: a row assembled from
// two branches satisfies neither.
func (p *Plan) unionLiteral(table string, col schemadef.Column, i int) (string, bool) {
	for s, spec := range p.unions[table] {
		if len(spec.branches) == 0 {
			continue
		}
		cell, ok := p.unionBranch(table, s, spec, i)[col.Name]
		if !ok {
			continue
		}
		switch {
		case cell.null:
			return "NULL", true
		case cell.lit != "":
			return cell.lit, true
		case cell.hasBound:
			return unionBounded(p.valueLiteral(table, col, i), col, cell.bound), true
		}
	}
	return "", false
}

// unionVocabWarnings names every overlay entry a union placement OVERRIDES.
//
// A CHECK is not negotiable and the overlay is a preference, so the placement
// wins — but silently is the wrong way to win. `coupons.kind: [launch_promo]`
// in vocab.yaml against a union that pins `kind` reads as a pin that did not
// take, and the author has no way to see why without this line.
func (p *Plan) unionVocabWarnings() []string {
	var out []string
	for table, specs := range p.unions {
		cols := p.vocab[table]
		if len(cols) == 0 {
			continue
		}
		named := map[string]bool{}
		for _, spec := range specs {
			for _, branch := range spec.branches {
				for name, cell := range branch {
					// Only a cell that decides the VALUE overrides the
					// overlay: a range keeps drawing from it, and a
					// presence requirement places nothing at all.
					if cell.hasBound || cell.present || cell.requireEdge || named[name] || len(cols[name]) == 0 {
						continue
					}
					named[name] = true
					out = append(out, fmt.Sprintf(
						"seed plan: db/seeds/vocab.yaml declares values for %s.%s, but constraint %q pins that column per row — the CHECK wins and the overlay entry is not used",
						table, name, spec.constraint))
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// unionBounded pins a column's NATURAL value into the range its branch states,
// so the value still varies per row (and still honors the vocabulary overlay)
// instead of collapsing to the range's endpoint.
func unionBounded(lit string, col schemadef.Column, b NumBound) string {
	if col.Type == schemadef.TypeFloat {
		v, err := strconv.ParseFloat(strings.TrimSpace(lit), 64)
		if err != nil {
			v = 0
		}
		if b.Min != nil && v < float64(*b.Min) {
			v = float64(*b.Min)
		}
		if b.Max != nil && v > float64(*b.Max) {
			v = float64(*b.Max)
		}
		return fmt.Sprintf("%.2f", v)
	}
	v, err := strconv.ParseInt(strings.TrimSpace(lit), 10, 64)
	if err != nil {
		v = 0
	}
	return strconv.FormatInt(b.clamp(v), 10)
}

// ──────────────────────────────────────────────────────────────────────
// The exported half: one union model, two consumers.
// ──────────────────────────────────────────────────────────────────────

// UnionCell is what a discriminated-union CHECK requires of ONE column of one
// row. Exactly one of the four states holds: the column must be absent
// (Null), it must carry a specific value (Value), it must stay inside a
// narrowed numeric range (Bound), or it must merely hold SOME value (Present).
type UnionCell struct {
	// Null is true when the branch requires the column to hold no value.
	Null bool
	// Value is the raw (undecorated) literal the branch pins the column to,
	// "" when it pins none.
	Value string
	// Bound is the range the branch narrows the column to; HasBound says
	// whether it states one.
	Bound    NumBound
	HasBound bool
	// Present is true when the branch requires the column to be non-NULL
	// and says nothing else about it — `status <> 'RESOLVED' OR resolved_at
	// IS NOT NULL` on its RESOLVED branch. Any ordinary value satisfies it;
	// a caller that would otherwise leave the column unset must not.
	Present bool
}

// exportUnionCell projects a placement onto the exported shape.
func exportUnionCell(cell unionCell) UnionCell {
	return UnionCell{
		Null:     cell.null,
		Value:    cell.raw,
		Bound:    cell.bound,
		HasBound: cell.hasBound,
		Present:  cell.present || cell.requireEdge,
	}
}

// UnionPlacement returns what every PLACEABLE discriminated-union CHECK on t
// requires of row i: column -> cell. nil when the table carries no such
// constraint, or none forge can place.
//
// This is the union counterpart of OrderRanks (ordering.go), and it exists for
// the identical reason. The seed plan is not forge's only generated dataset:
// the scaffolded CRUD lifecycle test mints its own rows against the very same
// schema, through create requests, and a value chosen per FIELD cannot satisfy
// a constraint that spans several. Before this, a project whose schema carried
// a union got a born test that failed on its first run —
//
//	--- FAIL: TestCRUD_Coupon_Lifecycle
//	    create #1: invalid_argument: create coupon: a field value violates a constraint
//
// — in a scaffold-once file the author is told not to rewrite.
//
// The branch is chosen ROUND ROBIN by row index — the seeder's own rule, and
// the reason there is one model rather than two: row 0 resolves to the same
// branch here as it does in the plan, so a born fixture and the dev dataset
// can never disagree about one schema.
//
// A caller that mints rows through a SHARED request shape (which is what the
// lifecycle test's two creates are: one field list, two sets of values) must
// ask for one row and use it for all of them. Which columns a branch requires
// to be ABSENT is a property of the field list, not of the values in it, so
// two rows on two different branches cannot be expressed by one list.
//
// pools carries the CHECK/enum vocabularies (PoolsFromTables, or the live
// introspection); a pin outside a column's vocabulary is unsatisfiable and its
// branch is dropped, exactly as it is for the seeded rows.
func UnionPlacement(t schemadef.Table, pools EnumPools, row int) map[string]UnionCell {
	if row < 0 {
		return nil
	}
	ordered, _ := tableOrderChains(t)
	specs, _ := tableUnionSpecs(t, pools, ordered)
	if len(specs) == 0 {
		return nil
	}
	out := map[string]UnionCell{}
	for _, spec := range specs {
		if len(spec.branches) == 0 {
			continue
		}
		for name, cell := range spec.branches[row%len(spec.branches)] {
			out[name] = exportUnionCell(cell)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
