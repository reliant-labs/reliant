package seedplan

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

// Biconditional CHECKs — a two-way rule, stated as the equality of two
// predicates:
//
//	CHECK ((kind = 'workspace') = (daemon_id IS NOT NULL))
//
// "a workspace grant has a daemon, and only a workspace grant has one". It is
// a perfectly good constraint, and the seeder places it: no project should
// ever weaken its schema to satisfy a seeder.
//
// # The reading
//
// `(P) = (Q)` is exactly the union `(P AND Q) OR (NOT P AND NOT Q)`, and its
// xor `(P) <> (Q)` is `(P AND NOT Q) OR (NOT P AND Q)`. So nothing new places
// a value: each side is read as an ATOM whose truth and falsehood can both be
// written as positive union terms, and the constraint is handed to the passes
// that already exist.
//
//	<biconditional> := ( <atom> ) { = | <> } ( <atom> )
//	<atom> := <col> = <literal>  |  <col> <> <literal>
//	        | <col> IN (<literal>, …)  |  <col> NOT IN (<literal>, …)
//	        | <col> = ANY (ARRAY[…])   |  <col> <> ALL (ARRAY[…])
//	        | <col> IS NULL  |  <col> IS NOT NULL
//
// A presence atom (`IS [NOT] NULL`) negates for free. A value atom negates
// into "one of the column's OTHER values", which needs the column's declared
// vocabulary — the same precondition a one-way guard has (guard.go).
//
// Two routes, chosen by shape:
//
//   - A value atom over a column WITH a vocabulary beside a presence atom —
//     the lifecycle shape, and reliant's connector_grants_daemon_by_kind — is
//     a PAIR of guards over that column ("the listed values require Q", "the
//     rest require NOT Q"), merged in tableGuardSpecs with every other guard
//     over it. Merging is what lets `kind` carry a biconditional AND its
//     one-way guards at once; placed separately they would rival each other.
//     The union then has one branch per vocabulary value, so both truth
//     values appear in the dataset.
//
//   - Everything else — two presence atoms, two value atoms, a value atom
//     whose column declares no vocabulary — is the union of whichever truth
//     values can be written (groups). A side forge cannot write without
//     inventing a value is left out and named in a warning: the rows that
//     ARE written satisfy the constraint, and only coverage is lost.
//
// A NULL on the presence side of a nullable foreign key places no value: the
// reference path declines the edge on that row (Plan.unionForbidsEdge).
//
// Forms outside the grammar — a comparison, a function call, a nested
// boolean — are not claimed, and keep the ordering pass's refusal. The
// advice text below survives for the one place a biconditional can still be
// refused as a RIVAL of a hand-written union.

// bicondAtom is one side of a biconditional.
type bicondAtom struct {
	column string
	// nullTest marks `col IS NULL` (negated: IS NOT NULL); otherwise the
	// atom tests the column's value against lits/raws.
	nullTest   bool
	lits, raws []string
	// negated makes the atom true when the value is NOT listed (`<>`,
	// NOT IN, `<> ALL`), or for a null test when the column IS NOT NULL.
	negated bool
}

// biconditional is a parsed `(P) = (Q)`, or `(P) <> (Q)` when xor.
type biconditional struct {
	left, right bicondAtom
	xor         bool
}

// parseBiconditional reads a CHECK body as a biconditional. ok is false for
// every other shape.
func parseBiconditional(body string) (biconditional, bool) {
	left, right, xor, ok := splitTopLevelIff(unwrapParens(body))
	if !ok {
		return biconditional{}, false
	}
	l, ok := parseBicondAtom(left)
	if !ok {
		return biconditional{}, false
	}
	r, ok := parseBicondAtom(right)
	if !ok || l.column == r.column {
		return biconditional{}, false
	}
	return biconditional{left: l, right: r, xor: xor}, true
}

// parseBicondAtom reads one side. The ALL/ANY(ARRAY[…]) forms are tried
// before the scalar ones, whose `(.+)` right-hand side would swallow an array
// whole and fail to read it as a literal.
func parseBicondAtom(s string) (bicondAtom, bool) {
	s = strings.TrimSpace(unwrapParens(s))
	if m := unionNullRE.FindStringSubmatch(s); m != nil {
		return bicondAtom{column: m[1], nullTest: true, negated: m[2] != ""}, true
	}
	list := func(col, body string, negated bool) (bicondAtom, bool) {
		a := bicondAtom{column: col, negated: negated}
		for _, p := range splitTopLevelComma(body) {
			lit, raw, ok := unionLiteralOf(strings.TrimSpace(p))
			if !ok {
				return bicondAtom{}, false
			}
			a.lits, a.raws = append(a.lits, lit), append(a.raws, raw)
		}
		return a, len(a.raws) > 0
	}
	for _, form := range []struct {
		re      *regexp.Regexp
		negated bool
	}{
		{guardNotAllRE, true},
		{guardAnyRE, false},
		{guardNotInRE, true},
		{guardInRE, false},
		{guardNotEqRE, true},
		{unionEqRE, false},
	} {
		if m := form.re.FindStringSubmatch(s); m != nil {
			return list(m[1], m[2], form.negated)
		}
	}
	return bicondAtom{}, false
}

// splitTopLevelIff splits on the one top-level `=` (a biconditional) or
// `<>` / `!=` (an xor), outside parentheses and string literals. Exactly one
// such operator is required, and both sides must be non-empty.
func splitTopLevelIff(expr string) (left, right string, xor, ok bool) {
	var (
		sc        sqlScan
		at, width = -1, 0
		seen      int
	)
	for i := 0; i < len(expr); {
		if sc.atTop() {
			switch {
			case strings.HasPrefix(expr[i:], "<>"), strings.HasPrefix(expr[i:], "!="):
				seen++
				at, width, xor = i, 2, true
				i += 2
				continue
			case expr[i] == '=':
				prev := byte(0)
				if i > 0 {
					prev = expr[i-1]
				}
				if prev != '<' && prev != '>' && (i+1 >= len(expr) || expr[i+1] != '=') {
					seen++
					at, width, xor = i, 1, false
				}
			}
		}
		i = sc.step(expr, i)
	}
	if seen != 1 {
		return "", "", false, false
	}
	left, right = strings.TrimSpace(expr[:at]), strings.TrimSpace(expr[at+width:])
	return left, right, xor, left != "" && right != ""
}

// alternatives returns the ways the atom can be made to hold the given truth
// value, each a positive AND-group. expressible is false when that truth
// value can only be written by inventing a value: "some value other than the
// listed ones" on a column that declares no vocabulary. An expressible but
// EMPTY answer means the vocabulary rules that truth value out entirely.
func (a bicondAtom) alternatives(t schemadef.Table, pools EnumPools, truth bool) (alts [][]unionTerm, expressible bool) {
	if a.nullTest {
		kind := termNotNull
		if truth != a.negated {
			kind = termIsNull
		}
		return [][]unionTerm{{{column: a.column, kind: kind}}}, true
	}
	wantListed := truth != a.negated
	vocab, hasVocab := pools.get(t.Name, a.column)
	if !hasVocab {
		if !wantListed {
			return nil, false
		}
		for i := range a.raws {
			alts = append(alts, []unionTerm{{column: a.column, kind: termEq, lit: a.lits[i], raw: a.raws[i]}})
		}
		return alts, true
	}
	for _, v := range vocab {
		if a.lists(v) != wantListed {
			continue
		}
		alts = append(alts, []unionTerm{{column: a.column, kind: termEq, lit: vocabLit(t, a.column, v), raw: v}})
	}
	return alts, true
}

// lists reports whether v is one of the atom's literals.
func (a bicondAtom) lists(v string) bool {
	for _, r := range a.raws {
		if r == v {
			return true
		}
	}
	return false
}

// vocabLit spells a vocabulary member as the INSERT needs it: bare for a
// number or a boolean, quoted otherwise.
func vocabLit(t schemadef.Table, column, v string) string {
	for _, c := range t.Columns {
		if c.Name != column {
			continue
		}
		switch c.Type {
		case schemadef.TypeInt, schemadef.TypeFloat, schemadef.TypeBool:
			return v
		}
	}
	return sqlString(v)
}

// groups expands the biconditional into the AND-groups of a union: every
// combination of the two sides that makes them agree (or, for an xor,
// disagree). note is non-empty when one truth value cannot be written — it
// completes a warning sentence about the constraint — or, when groups is
// empty, says why none can.
func (b biconditional) groups(t schemadef.Table, pools EnumPools) (groups [][]unionTerm, note string) {
	var missing []string
	for _, truth := range []bool{true, false} {
		la, lok := b.left.alternatives(t, pools, truth)
		ra, rok := b.right.alternatives(t, pools, truth != b.xor)
		if !lok || !rok {
			for _, side := range []struct {
				a  bicondAtom
				ok bool
			}{{b.left, lok}, {b.right, rok}} {
				if !side.ok && !containsString(missing, side.a.column) {
					missing = append(missing, side.a.column)
				}
			}
			continue
		}
		for _, l := range la {
			for _, r := range ra {
				g := make([]unionTerm, 0, len(l)+len(r))
				groups = append(groups, append(append(g, l...), r...))
			}
		}
	}
	if len(missing) == 0 {
		return groups, ""
	}
	cols := strings.Join(missing, " and ")
	fix := fmt.Sprintf("declare its values with a single-column `CHECK (%s IN (…))`", missing[0])
	if len(groups) == 0 {
		return nil, fmt.Sprintf("neither side of it can be written without inventing a value for %s — %s", cols, fix)
	}
	return groups, fmt.Sprintf(
		"is seeded on only one side: the rows where it is false need a value of %s outside the ones it lists, "+
			"and %s declares no value set to draw one from — %s and forge seeds both sides",
		cols, cols, fix)
}

// guards rewrites the biconditional as a pair of status guards over its
// value side, when it is the lifecycle shape: one value atom over a column
// with a vocabulary, one presence atom. ok is false for every other shape,
// which the union pass then places through groups.
func (b biconditional) guards(table string, pools EnumPools) ([]guardRewrite, bool) {
	disc, presence := b.left, b.right
	if disc.nullTest {
		disc, presence = presence, disc
	}
	if disc.nullTest || !presence.nullTest {
		return nil, false
	}
	if _, ok := pools.get(table, disc.column); !ok {
		return nil, false
	}
	whenNull := []unionTerm{{column: presence.column, kind: termIsNull}}
	whenPresent := []unionTerm{{column: presence.column, kind: termNotNull}}
	// What the presence side must say when the value side holds.
	qTrue, qFalse := whenNull, whenPresent
	if presence.negated {
		qTrue, qFalse = whenPresent, whenNull
	}
	if b.xor {
		qTrue, qFalse = qFalse, qTrue
	}
	// guardRewrite.appliesTo: a listed value takes the consequent unless
	// the listed values are the EXEMPT set. The value side holds on the
	// listed values unless it is negated, so the "holds" guard exempts the
	// listed values exactly when the atom is negated.
	return []guardRewrite{
		{column: disc.column, listed: disc.raws, listedAreExempt: disc.negated, consequent: qTrue},
		{column: disc.column, listed: disc.raws, listedAreExempt: !disc.negated, consequent: qFalse},
	}, true
}

func containsString(xs []string, x string) bool {
	for _, s := range xs {
		if s == x {
			return true
		}
	}
	return false
}

// # The refusal advice
//
// biconditionalRewrite and biconditionalAdvice predate the placement above.
// They name the one-way implication a biconditional implies, and survive for
// the rival refusals in union.go and guard.go, which a biconditional still
// reaches when it overlaps a constraint forge cannot merge it with.

var (
	// biconditionalEqRE matches the discriminator arm, `col = 'literal'`,
	// after unwrapParens has stripped its parentheses.
	biconditionalEqRE = regexp.MustCompile(`^"?([a-z_][a-z0-9_]*)"?\s*=\s*(.+)$`)
	// biconditionalNullRE matches the presence arm, `col IS [NOT] NULL`.
	biconditionalNullRE = regexp.MustCompile(`^"?([a-z_][a-z0-9_]*)"?\s+IS\s+(NOT\s+)?NULL$`)
)

// biconditionalRewrite reads a CHECK definition and, when it states a
// lifecycle rule as a biconditional, returns the equivalent one-way
// implication as pasteable SQL.
//
// The rewrite is exact, not an approximation. `(A = 'x') = (B IS NOT NULL)`
// asserts both directions; the implication `A <> 'x' OR B IS NOT NULL` asserts
// only the forward one. That is a deliberate WEAKENING and the message says
// so — the reverse direction ("only an approved row may carry a stamp") is a
// rule the application enforces at the write that sets the column, which is
// the guidance the db/seeding skill gives at length.
//
// ok is false for every other shape, which is what keeps the advice honest.
func biconditionalRewrite(def string) (string, bool) {
	body, ok := checkBody(def)
	if !ok {
		return "", false
	}
	// A biconditional is a top-level `=` between two PARENTHESIZED
	// predicates. Splitting on it textually would also match `a = b` and
	// every ordinary equality, so each arm is required to parse as one of
	// the two predicate shapes below — that is the real discriminator.
	left, right, ok := splitTopLevelEq(unwrapParens(body))
	if !ok {
		return "", false
	}

	// Either arm may be the discriminator; authors write it both ways.
	col, lit, nullCol, negated, ok := biconditionalArms(left, right)
	if !ok {
		return "", false
	}

	presence := "IS NULL"
	if negated {
		presence = "IS NOT NULL"
	}
	return fmt.Sprintf("%s <> %s OR %s %s", col, sqlString(lit), nullCol, presence), true
}

// biconditionalArms resolves the two arms into (discriminator column, its
// literal, presence column, whether the presence arm is IS NOT NULL),
// accepting either order.
func biconditionalArms(left, right string) (col, lit, nullCol string, negated, ok bool) {
	if c, l, nc, neg, ok := biconditionalOriented(left, right); ok {
		return c, l, nc, neg, true
	}
	return biconditionalOriented(right, left)
}

// biconditionalOriented reads eq as the discriminator arm and null as the
// presence arm.
func biconditionalOriented(eq, null string) (col, lit, nullCol string, negated, ok bool) {
	nm := biconditionalNullRE.FindStringSubmatch(strings.TrimSpace(unwrapParens(null)))
	if nm == nil {
		return "", "", "", false, false
	}
	em := biconditionalEqRE.FindStringSubmatch(strings.TrimSpace(unwrapParens(eq)))
	if em == nil {
		return "", "", "", false, false
	}
	// The right-hand side must be a plain literal. This is what keeps
	// `amount_paid_cents = amount_cents` — a column-to-column equality —
	// out of this reading.
	_, raw, ok := unionLiteralOf(strings.TrimSpace(em[2]))
	if !ok {
		return "", "", "", false, false
	}
	return em[1], raw, nm[1], nm[2] != "", true
}

// splitTopLevelEq splits on a single `=` at parenthesis depth zero, outside
// any string literal.
//
// It cannot use splitTopLevel: that helper matches a SPACE-PADDED word
// operator (" AND "), and `=` is punctuation that postgres renders with
// surrounding spaces but which also appears inside each arm. Exactly two parts
// is required — anything else is not a biconditional.
func splitTopLevelEq(expr string) (left, right string, ok bool) {
	var (
		sc   sqlScan
		at   = -1
		seen int
	)
	for i := 0; i < len(expr); {
		if sc.atTop() && expr[i] == '=' {
			// Skip the comparison operators that merely CONTAIN '='
			// (<=, >=, <>) and the rendered `!=`.
			prev := byte(0)
			if i > 0 {
				prev = expr[i-1]
			}
			next := byte(0)
			if i+1 < len(expr) {
				next = expr[i+1]
			}
			if prev != '<' && prev != '>' && prev != '!' && next != '=' {
				seen++
				at = i
			}
		}
		i = sc.step(expr, i)
	}
	if seen != 1 || at < 0 {
		return "", "", false
	}
	return strings.TrimSpace(expr[:at]), strings.TrimSpace(expr[at+1:]), true
}

// biconditionalAdvice is the shared sentence both refusals append. It names
// the shape, the rewrite as pasteable SQL, the precondition that makes the
// rewrite work, and where the full reasoning lives.
//
// The vocabulary precondition is not optional detail. guard.go refuses a
// guard whose column declares no value set, because there is no set to draw
// the non-excluded branch from — so an author who rewrites a biconditional on
// a bare TEXT column is refused AGAIN, which is a worse dead end than the one
// this message is fixing. A scaffolded enum column carries the `IN (...)`
// CHECK already; a hand-written migration may not.
func biconditionalAdvice(rewrite, discriminator string) string {
	return fmt.Sprintf(
		"states a status rule as a biconditional, which forge cannot place — rewrite it as a one-way implication "+
			"and forge seeds it: `CHECK (%s)`. "+
			"This requires %s to declare its values with a single-column `CHECK (%s IN (…))` — a column born from a "+
			"proto enum has one already. The implication is deliberately weaker: it keeps \"this status requires the "+
			"column\" and drops the reverse, \"only this status may set it\" — enforce that direction in the writer that "+
			"owns the transition. See the db/seeding/constraints skill",
		rewrite, discriminator, discriminator)
}

// biconditionalChecks names the multi-column CHECKs of t that state a
// biconditional, mapped to the rewrite each one needs.
func biconditionalChecks(t schemadef.Table) map[string]string {
	out := map[string]string{}
	for _, ck := range t.Checks {
		if len(ck.Columns) < 2 {
			continue
		}
		if rewrite, ok := biconditionalRewrite(ck.Def); ok {
			out[ck.Name] = rewrite
		}
	}
	return out
}

// biconditionalDiscriminator is the guarded column of a rewrite this package
// produced — the text before the first space, which biconditionalRewrite
// always renders as `<col> <> …`.
func biconditionalDiscriminator(rewrite string) string {
	if i := strings.IndexByte(rewrite, ' '); i > 0 {
		return rewrite[:i]
	}
	return rewrite
}
