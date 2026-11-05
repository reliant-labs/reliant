package seedplan

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

// Status-guard CHECK constraints — "when the row is in THIS state, these
// columns must be filled in".
//
//	CHECK (status <> 'JOB_STATUS_COMPLETED' OR actual_completion IS NOT NULL)
//
//	CHECK (status NOT IN ('JOB_STATUS_SCHEDULED', 'JOB_STATUS_IN_PROGRESS')
//	       OR (crew_id IS NOT NULL
//	           AND scheduled_start IS NOT NULL
//	           AND scheduled_end IS NOT NULL))
//
// This is how a lifecycle is spelled in SQL, and it is the single most common
// multi-column CHECK in a domain schema — a measured 12-table project carried
// SEVEN of them across four tables. None could be placed: they are not an
// ordering comparison (ordering.go) and not a discriminated union either,
// because a union branch must PIN its discriminator with `=` and a guard
// EXCLUDES values with `<>` / `NOT IN`. Both passes bailed, the status column
// was drawn independently of the columns its value governs, and the seeder
// produced a SCHEDULED job with no crew — legal for every single-column
// constraint and rejected by the table. Measured across six salts on the real
// schema, four aborted the seed transaction.
//
// The author's escape hatch was to hand-author the whole dataset, which is
// what this pass exists to make unnecessary.
//
// # The reading
//
// A guard is already a disjunction, so it needs no new solver — only the
// observation that `A <> 'x' OR C` is exactly the union
//
//	(A = <some value that is not 'x'>)  OR  (A = 'x' AND C)
//
// once the column's own vocabulary is known, which it is: a status column
// carries a single-column `IN (...)` CHECK that PoolsFromTables already reads
// into an EnumPools entry. So the guard is REWRITTEN into positive branches
// and handed to the union pass, which then does every bit of the validation
// it already does — column eligibility, joint satisfiability, branch
// coverage, round-robin selection. Nothing here places a value.
//
// Rewriting rather than extending the union grammar is deliberate. The
// alternative — teaching unionTerm a "not equal" kind — would put a negative
// term into a structure whose entire meaning is "this branch pins these
// columns to these values", and every consumer of unionCell would then need
// to know that some cells constrain nothing. The rewrite keeps the negation
// contained to this file: what leaves it is the same flat positive shape the
// union pass was designed for.
//
// # What is refused
//
// The guard shape is narrow on purpose:
//
//	<guard> := <negative> OR <consequent>
//	<negative> := <col> <> <literal>  |  <col> NOT IN (<literal>, …)
//	              |  <col> <> ALL (ARRAY[<literal>, …])
//	<consequent> := the existing union AND-group grammar
//
// Anything else is left to the existing refusal. In particular the guarded
// column must carry a readable vocabulary: with no `IN (...)` CHECK on it
// there is no set of values to pick the "not excluded" branch from, and
// inventing one would be forge asserting a domain fact the schema never
// stated. That refusal is reported by name rather than silently skipped.

var (
	// guardNotEqRE matches `col <> 'literal'` — one excluded value.
	guardNotEqRE = regexp.MustCompile(`^"?([a-z_][a-z0-9_]*)"?\s*(?:<>|!=)\s*(.+)$`)
	// guardNotInRE matches `col NOT IN ('a', 'b')`, the spelling an author
	// writes. postgres canonicalises it to the ALL(ARRAY[…]) form below,
	// but a constraint read straight from a migration file may carry either.
	guardNotInRE = regexp.MustCompile(`^"?([a-z_][a-z0-9_]*)"?\s+NOT\s+IN\s*\((.+)\)$`)
	// guardNotAllRE matches `col <> ALL (ARRAY['a', 'b'])` — how
	// pg_get_constraintdef renders NOT IN, and therefore the form
	// introspection actually returns.
	guardNotAllRE = regexp.MustCompile(`^"?([a-z_][a-z0-9_]*)"?\s*(?:<>|!=)\s*ALL\s*\(\s*ARRAY\s*\[(.+)\]\s*\)$`)

	// The POSITIVE spelling of the same guard, where the listed values are
	// the ones EXEMPT from the requirement rather than subject to it:
	//
	//	CHECK (status IN ('DRAFT','CANCELLED') OR ordered_at IS NOT NULL)
	//
	// Read as "a draft or cancelled order needs no order date", which is the
	// complement of `status NOT IN (…) OR …`. Authors reach for whichever
	// reads better in their domain, and a matcher that knew only the
	// negative form placed one of a table's two guards and then refused the
	// other for sharing the column — measured on a real schema.
	guardInRE  = regexp.MustCompile(`^"?([a-z_][a-z0-9_]*)"?\s+IN\s*\((.+)\)$`)
	guardAnyRE = regexp.MustCompile(`^"?([a-z_][a-z0-9_]*)"?\s*=\s*ANY\s*\(\s*ARRAY\s*\[(.+)\]\s*\)$`)
	// The single-value positive form, `col = 'x' OR …`, is deliberately NOT
	// matched: it is indistinguishable from a discriminated-union branch,
	// which parseUnionBranches already owns and reads differently.
)

// splitTopLevelComma splits a literal list on commas that sit at parenthesis
// depth zero and outside a string literal, reusing the same scanner
// splitTopLevel uses for boolean operators.
//
// A separate function because splitTopLevel matches a SPACE-PADDED operator
// (" AND "), which a bare comma never is — passing "," to it returns the
// whole list as one element, and every literal in it then fails to parse.
func splitTopLevelComma(s string) []string {
	var (
		parts []string
		sc    sqlScan
		start int
	)
	for i := 0; i < len(s); {
		if sc.atTop() && s[i] == ',' {
			parts = append(parts, s[start:i])
			i++
			start = i
			continue
		}
		i = sc.step(s, i)
	}
	return append(parts, s[start:])
}

// tableGuardSpecs resolves every status guard on a table, MERGED by the
// column each one guards.
//
// Merging is the whole point. A lifecycle column attracts several guards —
//
//	CHECK (status NOT IN ('SCHEDULED','IN_PROGRESS') OR crew_id IS NOT NULL …)
//	CHECK (status <> 'COMPLETED' OR actual_completion IS NOT NULL)
//
// — and each is individually placeable, but placing one and then the other
// independently is unsound: both write `status`, and buildUnionSpec's own
// joint-satisfiability rule correctly refuses that. So they are combined into
// ONE union over `status`, with one branch per value of its vocabulary
// carrying the consequents of EVERY guard that value triggers. A row pinned
// to 'SCHEDULED' then gets a crew and a schedule; a row pinned to 'COMPLETED'
// gets a completion time; and a DRAFT row gets neither, which is the point of
// the states existing.
//
// claimed names the constraints this pass spoke for — placed or refused — so
// the caller does not also report them.
func tableGuardSpecs(
	t schemadef.Table,
	conv schemadef.Conventions,
	pools EnumPools,
	ordered map[string]orderSlot,
) (specs []unionSpec, warns []string, claimed map[string]bool) {
	claimed = map[string]bool{}

	// Collect the guards, grouped by guarded column, in declaration order so
	// the output is deterministic.
	var columns []string
	byColumn := map[string][]guardRewrite{}
	names := map[string][]string{}
	for _, ck := range t.Checks {
		if len(ck.Columns) < 2 {
			continue
		}
		body, ok := checkBody(ck.Def)
		if !ok {
			continue
		}
		g, ok := parseStatusGuard(body)
		if !ok {
			continue
		}
		// The paranoia guard the other passes apply: the expression must
		// talk about the columns postgres says the constraint spans.
		spans := map[string]bool{}
		for _, c := range ck.Columns {
			spans[c] = true
		}
		if !spans[g.column] {
			continue
		}
		if _, seen := byColumn[g.column]; !seen {
			columns = append(columns, g.column)
		}
		byColumn[g.column] = append(byColumn[g.column], g)
		names[g.column] = append(names[g.column], ck.Name)
	}

	// The biconditionals on this table, by name. A rival in this set is not
	// just an unplaceable constraint — it is the REASON the guards below are
	// unplaceable, and it has a known one-line fix, so a refusal that names
	// it converts several constraints' worth of failure into one edit.
	biconditionals := biconditionalChecks(t)

	for _, column := range columns {
		guards := byColumn[column]
		label := quotedList(names[column])
		for _, n := range names[column] {
			claimed[n] = true
		}

		vocab, ok := pools.get(t.Name, column)
		if !ok {
			// With no declared value set there is nothing to draw the
			// un-guarded branch from, and forge will not invent one. Named
			// rather than skipped: the fix is one line of SQL, and the
			// author can only act on it if they are told.
			warns = append(warns, fmt.Sprintf(
				"seed plan: %s constraint%s %s guards %s, but %s declares no value set forge can "+
					"draw from — add a single-column `CHECK (%s IN (…))` and forge can place %s "+
					"— until then seeded rows satisfy it only by chance",
				t.Name, plural(len(guards)), label, column, column, column, itThem(len(guards))))
			continue
		}

		spec, why := buildGuardUnion(t, conv, pools, ordered, column, guards, vocab, names[column])
		if why != "" {
			// The collateral-damage case, and the single most expensive
			// failure in either dogfood run. These guards are well-formed;
			// they are unplaceable only because a BICONDITIONAL over the
			// same column cannot be merged with them, so it rivals the
			// union they were folded into and takes all of them down.
			//
			// Saying so is what makes the fix findable. An author who sees
			// only "forge cannot place their values" repairs one constraint
			// at a time, gets the identical failure every round because the
			// biconditional is still there, and concludes the rewrite does
			// not work — measured, four rounds, ending in deleted schema
			// constraints.
			if blocker, rewrite, blocking := guardBlockedByBiconditional(t, biconditionals, names[column], column); blocking {
				warns = append(warns, fmt.Sprintf(
					"seed plan: %s constraint%s %s over %s %s well-formed and would seed, but %q also spans %s and %s — "+
						"fix that ONE constraint and %s %s placed too. Until then seeded rows satisfy %s only by chance",
					t.Name, plural(len(guards)), label, column, isAre(len(guards)),
					blocker, column, biconditionalAdvice(rewrite, column),
					label, areGet(len(guards)), itThem(len(guards))))
				continue
			}
			warns = append(warns, fmt.Sprintf(
				"seed plan: %s constraint%s %s guard%s %s but forge cannot place %s values (%s) — "+
					"seeded rows satisfy %s only by chance",
				t.Name, plural(len(guards)), label, plural(len(guards)), column,
				itsTheir(len(guards)), why, itThem(len(guards))))
			continue
		}
		specs = append(specs, spec)
	}
	return specs, warns, claimed
}

// buildGuardUnion turns every guard over one column into a single union.
//
// One branch per vocabulary value: the value is pinned, and the consequents
// of every guard that EXCLUDES that value are conjoined onto it. A value no
// guard excludes gets a bare pin, which is what leaves the un-guarded states
// (a DRAFT job) in the dataset.
func buildGuardUnion(
	t schemadef.Table,
	conv schemadef.Conventions,
	pools EnumPools,
	ordered map[string]orderSlot,
	column string,
	guards []guardRewrite,
	vocab []string,
	names []string,
) (unionSpec, string) {
	speaksFor := make(map[string]bool, len(names))
	for _, n := range names {
		speaksFor[n] = true
	}
	groups := make([][]unionTerm, 0, len(vocab))
	for _, v := range vocab {
		terms := []unionTerm{{column: column, kind: termEq, lit: sqlString(v), raw: v}}
		for _, g := range guards {
			if g.appliesTo(v) {
				terms = append(terms, g.consequent...)
			}
		}
		groups = append(groups, terms)
	}

	// Reuse the union resolver wholesale, with a synthetic constraint whose
	// spanned columns are the union of every guard's. It re-validates column
	// eligibility, pin placeability and branch satisfiability exactly as it
	// does for a hand-written union — nothing here bypasses those checks.
	spanned := map[string]bool{column: true}
	for _, g := range guards {
		for _, term := range g.consequent {
			spanned[term.column] = true
		}
	}
	cols := make([]string, 0, len(spanned))
	for c := range spanned {
		cols = append(cols, c)
	}
	sort.Strings(cols)

	// The synthetic constraint carries the names of every guard it stands
	// for, so buildUnionSpec's joint-satisfiability scan skips them all —
	// they are this spec, not rivals to it — while still refusing if some
	// OTHER multi-column constraint touches the same columns.
	spec, why := buildUnionSpec(t, conv, pools, ordered, schemadef.CheckConstraint{
		Name:    names[0],
		Columns: cols,
	}, groups, speaksFor)
	if why != "" {
		return unionSpec{}, why
	}
	return spec, ""
}

// guardBlockedByBiconditional reports whether a biconditional over the same
// column is what made these guards unplaceable.
//
// It is deliberately narrow: only a constraint that (a) reads as a
// biconditional and (b) actually spans the guarded column can be the rival
// buildUnionSpec refused on. Anything else keeps the original message, because
// "rewrite it as an implication" is wrong advice for a constraint that is not
// one.
func guardBlockedByBiconditional(
	t schemadef.Table,
	biconditionals map[string]string,
	guardNames []string,
	column string,
) (blocker, rewrite string, ok bool) {
	mine := make(map[string]bool, len(guardNames))
	for _, n := range guardNames {
		mine[n] = true
	}
	for _, ck := range t.Checks {
		if mine[ck.Name] || len(ck.Columns) < 2 {
			continue
		}
		rw, isBiconditional := biconditionals[ck.Name]
		if !isBiconditional {
			continue
		}
		for _, c := range ck.Columns {
			if c == column {
				return ck.Name, rw, true
			}
		}
	}
	return "", "", false
}

// quotedList renders names as `"a"`, `"a" and "b"`, `"a", "b" and "c"`.
//
// A plain strings.Join with an escaped-quote separator, fed through %q,
// double-escapes every inner quote and prints `"a\", \"b"` in the user's
// terminal — observed in the guard refusal before this existed.
func quotedList(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = fmt.Sprintf("%q", n)
	}
	switch len(quoted) {
	case 0:
		return ""
	case 1:
		return quoted[0]
	}
	return strings.Join(quoted[:len(quoted)-1], ", ") + " and " + quoted[len(quoted)-1]
}

// isAre / areGet render a verb for a count, so one sentence reads correctly
// whether it speaks for one guard or several.
func isAre(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

func areGet(n int) string {
	if n == 1 {
		return "gets"
	}
	return "get"
}

// plural renders "" or "s" for a count, so one warning sentence reads
// correctly whether it speaks for one guard or several.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func itThem(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}

func itsTheir(n int) string {
	if n == 1 {
		return "its"
	}
	return "their"
}

// unionRequiresEdge reports whether any branch of any of this table's placed
// unions asserts col IS NOT NULL.
//
// ANY branch, not the chosen one: which branch a row takes is decided per row
// at render time, and the reference path resolves the edge without knowing
// that choice. Forcing the edge present whenever some branch could demand it
// costs only the ~1-in-5 NULL variety on that one column and makes the guard
// hold for every row, whichever branch it lands on.
func unionRequiresEdge(specs []unionSpec, col string) bool {
	for _, s := range specs {
		for _, branch := range s.branches {
			if cell, ok := branch[col]; ok && cell.requireEdge {
				return true
			}
		}
	}
	return false
}

// isForeignKey reports whether col is a declared REFERENCES column of t.
func isForeignKey(t schemadef.Table, col string) bool {
	for _, fk := range t.ForeignKeys {
		if fk.Column == col {
			return true
		}
	}
	return false
}

// notNullOnlyFor reports whether every term naming col is IS NOT NULL.
//
// That is the test for "this branch constrains the column's PRESENCE but not
// its value", which is what makes it safe to let a branch name a column some
// other mechanism owns — a foreign key, a UNIQUE column. The moment the same
// branch also pins or bounds that column, the two mechanisms would be writing
// the same cell and the ordinary eligibility refusal must stand.
func notNullOnlyFor(terms []unionTerm, col string) bool {
	for _, t := range terms {
		if t.column == col && t.kind != termNotNull {
			return false
		}
	}
	return true
}

// guardRewrite is a status guard resolved into the union shape.
type guardRewrite struct {
	// column is the guarded column — the one the exempting arm names.
	column string
	// listed are the raw (unquoted) values that arm names.
	listed []string
	// listedAreExempt distinguishes the two spellings of the same rule:
	// `status IN ('DRAFT','CANCELLED') OR …` EXEMPTS the listed values,
	// while `status NOT IN ('SCHEDULED') OR …` SUBJECTS them. Resolving
	// which values the consequent applies to therefore needs the column's
	// vocabulary — see appliesTo.
	listedAreExempt bool
	// consequent is the AND-group that must hold for every value the guard
	// subjects to it.
	consequent []unionTerm
}

// appliesTo reports whether the consequent must hold when the column carries
// v. For the negative spelling that is the listed values; for the positive
// spelling it is everything NOT listed.
func (g guardRewrite) appliesTo(v string) bool {
	for _, l := range g.listed {
		if l == v {
			return !g.listedAreExempt
		}
	}
	return g.listedAreExempt
}

// parseStatusGuard reads `<col> <> <lit> OR <group>` and its NOT IN / ALL
// spellings. ok is false for anything else, which leaves the constraint to
// whatever refusal already covers it.
//
// Only a TWO-armed disjunction is a guard. A three-armed one is either a real
// union (which parseUnionBranches already handles) or something this matcher
// has no reading for, and guessing between them is exactly the ambiguity a
// narrow matcher exists to avoid.
func parseStatusGuard(expr string) (guardRewrite, bool) {
	parts := splitTopLevel(unwrapParens(expr), "OR")
	if len(parts) != 2 {
		return guardRewrite{}, false
	}
	column, listed, listedAreExempt, ok := parseGuardNegative(unwrapParens(parts[0]))
	if !ok {
		return guardRewrite{}, false
	}
	// The consequent is read with the union grammar, minus its requirement
	// that a group pin something with `=`: the whole point of a guard is
	// that the consequent constrains OTHER columns (usually IS NOT NULL),
	// and the discrimination lives in the negative arm instead.
	terms, ok := parseGuardConsequent(unwrapParens(parts[1]))
	if !ok {
		return guardRewrite{}, false
	}
	return guardRewrite{column: column, listed: listed, listedAreExempt: listedAreExempt, consequent: terms}, true
}

// parseGuardNegative reads the exempting arm, returning the column, the values
// it names, and whether those values are the EXEMPT set (positive spelling,
// `IN (…)`) or the SUBJECT set (negative spelling, `NOT IN (…)`).
//
// Both spellings state the same rule from opposite sides, and which values the
// consequent applies to is exactly the difference — so the caller resolves the
// listed set against the column's vocabulary rather than assuming either.
func parseGuardNegative(s string) (column string, listed []string, listedAreExempt, ok bool) {
	s = strings.TrimSpace(s)
	// The ALL/ANY(ARRAY[…]) forms are tried first: each also matches the
	// looser scalar regex below, whose `(.+)` right-hand side would swallow
	// the whole array as one literal and fail to parse it.
	if m := guardNotAllRE.FindStringSubmatch(s); m != nil {
		vals, ok := parseGuardLiteralList(m[2])
		return m[1], vals, false, ok
	}
	if m := guardAnyRE.FindStringSubmatch(s); m != nil {
		vals, ok := parseGuardLiteralList(m[2])
		return m[1], vals, true, ok
	}
	if m := guardNotInRE.FindStringSubmatch(s); m != nil {
		vals, ok := parseGuardLiteralList(m[2])
		return m[1], vals, false, ok
	}
	if m := guardInRE.FindStringSubmatch(s); m != nil {
		vals, ok := parseGuardLiteralList(m[2])
		return m[1], vals, true, ok
	}
	if m := guardNotEqRE.FindStringSubmatch(s); m != nil {
		_, raw, ok := unionLiteralOf(strings.TrimSpace(m[2]))
		if !ok {
			return "", nil, false, false
		}
		return m[1], []string{raw}, false, true
	}
	return "", nil, false, false
}

// parseGuardLiteralList reads a comma-separated literal list.
//
// It cannot use splitTopLevel: that helper matches a PADDED word operator
// (" AND ", " OR "), so a bare comma never matches and the whole list comes
// back as one unparseable element. Commas are split here with the same
// paren/quote-aware scanner, so a comma inside a quoted value does not end an
// element.
func parseGuardLiteralList(s string) ([]string, bool) {
	parts := splitTopLevelComma(s)
	if len(parts) == 0 {
		return nil, false
	}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		_, raw, ok := unionLiteralOf(strings.TrimSpace(p))
		if !ok {
			return nil, false
		}
		out = append(out, raw)
	}
	return out, true
}

// parseGuardConsequent reads the guarded arm as an AND-group.
//
// It is parseUnionGroup without the "at least one termEq" rule. That rule
// exists to stop an arbitrary predicate under an OR being mistaken for a
// union branch; here the negative arm has ALREADY identified the constraint
// as a guard, so the consequent is free to be pure IS NOT NULL — which is
// what a lifecycle guard almost always is.
func parseGuardConsequent(expr string) ([]unionTerm, bool) {
	conj := splitTopLevel(unwrapParens(expr), "AND")
	if len(conj) == 0 {
		return nil, false
	}
	terms := make([]unionTerm, 0, len(conj))
	for _, c := range conj {
		term, ok := parseUnionTerm(unwrapParens(c))
		if !ok {
			return guardConsequentUnreadable(c)
		}
		terms = append(terms, term)
	}
	return terms, true
}

// guardConsequentUnreadable is the single bail-out point for a consequent
// this grammar cannot read — a column-to-column comparison
// (`amount_paid_cents = amount_cents`), arithmetic, a function call.
//
// It is a named function rather than a bare `return nil, false` because the
// column-to-column case is worth understanding: forge CAN place two columns
// equal to each other, but only by teaching the union pass a term whose value
// comes from a sibling column rather than a literal. That is a real feature
// and a separate one; until it exists, refusing is honest.
func guardConsequentUnreadable(string) ([]unionTerm, bool) { return nil, false }
