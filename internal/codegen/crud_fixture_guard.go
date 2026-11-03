package codegen

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/pkg/schemadef"
	"github.com/reliant-labs/forge/pkg/seedplan"
)

// The guard that makes constraint-correct fixtures HONEST.
//
// Everything else in this package DERIVES a fixture from the schema model: a
// char_length bound becomes a string of the right width, a CHECK vocabulary
// becomes one of its members, a pattern becomes a value built from the parsed
// regexp. Each derivation is an opinion about what postgres will accept, and
// each one has a boundary. Beyond that boundary the derivation does not fail —
// it FALLS BACK, to `sample_<column>_1` or the type-blind `1`, and the fallback
// is indistinguishable from a value the generator understood.
//
// That silence is the actual defect. A regex postgres accepts but Go's RE2
// cannot compile (any lookahead: `^(?=.*[A-Z]).{8,}$`) makes patternSample
// return "no value", the column quietly takes the placeholder, and the schema
// forge itself recommended rejects it. Nothing says so at generate time. The
// author meets it much later as:
//
//	FAIL: TestCRUD_Order_Lifecycle
//	  create #1: invalid_argument: a field value violates a constraint
//
// — in a scaffold-once file they were told not to rewrite, with no indication
// which column or which constraint is at fault. The only escape the run found
// was deleting the test, which regenerated.
//
// So the fixtures are checked against the authority rather than the model.
// forge has already applied the migrations to a real shadow postgres; asking
// that postgres to EVALUATE each CHECK over the values about to be written
// into the test costs one round trip and converts every silent fallback into a
// loud, located generate-time error. It also marks the parser's own frontier:
// a form the derivation does not understand shows up here as a failure naming
// the column and the constraint, instead of as a mysterious test failure.

// fixtureValue is one derived create-request value, in the terms the guard
// needs: which column it targets and the literal that will reach postgres.
type fixtureValue struct {
	column string
	// sqlLit is the value as a SQL literal, ready to substitute into the
	// CHECK expression (already quoted/escaped for a string column).
	sqlLit string
	// goLit is how the fixture reads in the emitted Go source, for the error
	// message — that is the text the author will search for.
	goLit string
}

// FixtureConstraintError reports that generated fixtures violate the applied
// schema's own CHECK constraints.
//
// It is a distinct TYPE because its severity is distinct. Scaffolding the
// lifecycle test is otherwise best-effort — a project that cannot produce one
// still generates, and the failure is a warning — but a fixture the schema
// rejects is not an incidental miss. It is forge emitting a file it can prove
// wrong, and the whole point of checking is to refuse. Callers match on this
// type to fail the run while continuing to tolerate everything else.
type FixtureConstraintError struct {
	Violations []fixtureViolation
}

func (e *FixtureConstraintError) Error() string {
	msgs := make([]string, len(e.Violations))
	for i, v := range e.Violations {
		msgs[i] = v.Error()
	}
	return strings.Join(msgs, "\n")
}

// fixtureViolation is one fixture that its own schema rejects.
type fixtureViolation struct {
	Table      string
	Column     string
	Constraint string
	Def        string
	GoLiteral  string
	// SuggestedType is the semantic vocab type the column's own constraints
	// force, when they force one ("" otherwise). It turns the remedy from
	// "declare something" into a line the author can paste.
	SuggestedType string
}

// Error renders a violation as the generate-time message. It names the column
// and the constraint (the two facts the runtime failure withheld), shows the
// value, and states the fix — because a generator that refuses to emit must
// say what would let it proceed.
func (v fixtureViolation) Error() string {
	remedy := fmt.Sprintf("columns:\n    %s.%s: [<a value the constraint accepts>]", v.Table, v.Column)
	if v.SuggestedType != "" {
		remedy = fmt.Sprintf("columns:\n    %s.%s: {type: %s}", v.Table, v.Column, v.SuggestedType)
	}
	return fmt.Sprintf(
		"generated fixture for %s.%s is %s, which its own schema rejects: constraint %s %s\n"+
			"  forge derives fixtures from the applied schema, but this constraint is a form it cannot invert.\n"+
			"  Declare the column's vocabulary in db/seeds/vocab.yaml:\n  %s",
		v.Table, v.Column, v.GoLiteral, v.Constraint, v.Def, remedy)
}

// verifyFixtures asks postgres whether the derived fixtures satisfy the CHECK
// constraints of the schema they were derived from, and returns every
// violation.
//
// The judgement is postgres's own: each single-column CHECK is evaluated with
// the fixture substituted for the column, using the constraint definition read
// back from the catalog. That is the same expression the INSERT would be
// tested against, so a pass here and a pass at INSERT cannot disagree about
// the constraint — no second implementation of postgres's regexp dialect,
// collation, or numeric coercion is involved, which is precisely the class of
// disagreement (RE2 vs. POSIX) that produced the defect.
//
// Constraints spanning multiple columns are not evaluated: the fixture set is
// per-column, and a two-column constraint may legitimately reference a column
// the create request does not carry. Evaluating one with the other side
// missing would report a violation that does not exist.
// judged is the number of constraints postgres actually returned a verdict on.
// It is reported separately from the violations because "no violations" and
// "nothing was checked" are entirely different facts, and a guard that
// conflated them would report success for a schema it never looked at. Callers
// that need to know the guard did work assert on this.
func verifyFixtures(ctx context.Context, db *sql.DB, t schemadef.Table, values []fixtureValue) (out []fixtureViolation, judged int, err error) {
	byColumn := make(map[string]fixtureValue, len(values))
	for _, v := range values {
		byColumn[v.column] = v
	}
	for _, ck := range t.Checks {
		if len(ck.Columns) != 1 {
			continue
		}
		fv, ok := byColumn[ck.Columns[0]]
		if !ok {
			continue // the create request does not set this column
		}
		expr, ok := checkExpression(ck.Def)
		if !ok {
			continue // not a shape we can lift out of the catalog spelling
		}
		satisfied, decided, err := evalCheck(ctx, db, t, ck.Columns[0], fv.sqlLit, expr)
		if err != nil {
			return nil, judged, err
		}
		if decided {
			judged++
		}
		if !satisfied {
			suggested := ""
			if col, okC := tableColumn(t, ck.Columns[0]); okC {
				if typ, okT := seedplan.InferVocabType(t, col); okT {
					suggested = typ
				}
			}
			out = append(out, fixtureViolation{
				Table: t.Name, Column: ck.Columns[0],
				Constraint: ck.Name, Def: ck.Def, GoLiteral: fv.goLit,
				SuggestedType: suggested,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Column != out[j].Column {
			return out[i].Column < out[j].Column
		}
		return out[i].Constraint < out[j].Constraint
	})
	return out, judged, nil
}

// checkExpression strips the `CHECK (...)` wrapper off a catalog constraint
// definition, yielding the bare boolean expression. ok is false for any
// spelling that is not a plain CHECK (NOT VALID variants, or a definition the
// catalog renders differently) — the caller then skips it rather than
// evaluating something it has not understood.
func checkExpression(def string) (string, bool) {
	s := strings.TrimSpace(def)
	if !strings.HasPrefix(s, "CHECK (") || !strings.HasSuffix(s, ")") {
		return "", false
	}
	return strings.TrimSpace(s[len("CHECK (") : len(s)-1]), true
}

// evalCheck evaluates one CHECK expression with the column bound to the
// fixture value.
//
// The value is bound through a single-row subselect that gives it the
// COLUMN'S OWN TYPE (`SELECT <lit>::<type> AS <col>`), rather than pasted into
// the expression text. Two reasons, both load-bearing: the expression may
// reference the column several times (`char_length(x) >= 1 AND char_length(x)
// <= 120`) and textual substitution would have to get every occurrence right;
// and postgres's own casts then apply exactly as they do for an INSERT, so a
// value that only satisfies the constraint under a coercion the INSERT would
// not perform is not quietly passed.
//
// A NULL result means the expression was not decidable for the value — SQL's
// three-valued logic, and precisely how a CHECK treats NULL: not a violation.
// It is reported as satisfied, matching postgres's INSERT-time behaviour.
//
// decided reports whether postgres returned a verdict at all. It is what lets
// a caller tell "this fixture is fine" apart from "this constraint was never
// evaluated" — the distinction that keeps a green guard from being evidence of
// nothing.
func evalCheck(ctx context.Context, db *sql.DB, t schemadef.Table, column, sqlLit, expr string) (satisfied, decided bool, err error) {
	col, ok := tableColumn(t, column)
	if !ok {
		return true, false, nil
	}
	typ := col.DeclType
	if typ == "" {
		typ = "text"
	}
	q := fmt.Sprintf("SELECT (%s) FROM (SELECT %s::%s AS %s) AS fixture",
		expr, sqlLit, typ, quoteIdentifier(column))
	var result sql.NullBool
	if err := db.QueryRowContext(ctx, q).Scan(&result); err != nil {
		// The expression did not evaluate at all (it references another
		// column, uses a function the scratch DB lacks, or postgres refuses
		// the cast). That is not evidence the fixture is wrong, so it is not
		// reported as a violation — the guard only ever speaks when postgres
		// returns a definite false.
		return true, false, nil //nolint:nilerr // an unevaluable expression is not a verdict
	}
	if !result.Valid {
		return true, false, nil // NULL: a CHECK does not reject an undecidable row
	}
	return result.Bool, true, nil
}

// quoteIdentifier double-quotes a SQL identifier so a column named after a
// reserved word still binds.
func quoteIdentifier(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// goLiteralToSQL converts a rendered GO literal from the fixture model into
// the SQL literal for the same value. Only the shapes the fixture builder
// actually emits are converted; ok is false for anything else (a pb enum
// constant, a timestamppb call), which the guard then does not check — it
// verifies what it can READ, and says nothing about what it cannot.
func goLiteralToSQL(goLit, goType string) (string, bool) {
	s := strings.TrimSpace(goLit)
	if s == "" {
		return "", false
	}
	switch goType {
	case "string":
		v, err := strconv.Unquote(s)
		if err != nil {
			return "", false
		}
		return "'" + strings.ReplaceAll(v, "'", "''") + "'", true
	case "int32", "int64", "uint32", "uint64":
		if _, err := strconv.ParseInt(s, 10, 64); err != nil {
			return "", false
		}
		return s, true
	case "float32", "float64":
		if _, err := strconv.ParseFloat(s, 64); err != nil {
			return "", false
		}
		return s, true
	case "bool":
		if s != "true" && s != "false" {
			return "", false
		}
		return s, true
	}
	return "", false
}
