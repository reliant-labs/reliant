package seedplan

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

// Biconditional lifecycle CHECKs — the spelling of a status rule that forge
// cannot place, and the one message that can turn it into a one-line fix.
//
//	CHECK ((status = 'APPROVED') = (approved_at IS NOT NULL))
//
// The same domain rule written as a one-way implication IS placeable:
//
//	CHECK (status <> 'APPROVED' OR approved_at IS NOT NULL)
//
// guard.go's grammar admits only the negative arm, and tableGuardSpecs merges
// every implication over one column into a single union — so any number of
// them seed together. A biconditional has no top-level OR at all, so neither
// parseStatusGuard nor parseUnionBranches reads it.
//
// # Why this file exists at all
//
// Nothing here places a value. It exists purely so two existing refusals can
// name the rewrite, because BOTH messages were measured sending authors
// somewhere useless:
//
//  1. A biconditional falls through every pass and lands on the ORDERING
//     refusal, "not a two-column ordering comparison" — a sentence about
//     ordering, for a constraint that states no ordering. It describes forge's
//     parser rather than the author's mistake.
//
//  2. A biconditional sitting beside well-formed implications over the same
//     column takes them down TOO: it still spans `status`, so the merged guard
//     union hits buildUnionSpec's joint-satisfiability refusal and every
//     implication goes unplaced with it. In the dogfood run this is what made
//     incremental repair look futile — fixing two of three constraints
//     produced the identical failure, so the author concluded the rewrite did
//     not work and started deleting constraints from a production schema.
//
// Detecting the shape SPECIFICALLY is the point. "Rewrite as a one-way
// implication" is excellent advice for a biconditional and wrong advice for
// any other multi-column CHECK, so a refusal offers it only when the
// constraint really is one.

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
			"owns the transition. See the db/seeding skill",
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
