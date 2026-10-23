// File: pkg/seedplan/ordering_test.go
//
// The two-column CHECK is the one constraint shape that is not a property
// of a single column, and every model in this package keyed on one column
// until now. These tests pin both halves of the answer: the shapes forge
// PLACES (so the rows satisfy the constraint by construction) and the
// shapes it refuses to guess at (so it says so instead of writing rows that
// contradict a rule the schema states).

package seedplan

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

// timeCol / intCol / textCol build introspected columns without repeating
// the struct literal in every case.
func timeCol(name string) schemadef.Column {
	return schemadef.Column{Name: name, DeclType: "TIMESTAMPTZ", Type: schemadef.TypeTime, NotNull: true}
}

func intCol(name string) schemadef.Column {
	return schemadef.Column{Name: name, DeclType: "BIGINT", Type: schemadef.TypeInt, NotNull: true}
}

func idCol() schemadef.Column {
	return schemadef.Column{Name: "id", DeclType: "TEXT", Type: schemadef.TypeString, NotNull: true, IsPK: true}
}

func check(name, def string, cols ...string) schemadef.CheckConstraint {
	return schemadef.CheckConstraint{Name: name, Def: def, Columns: cols}
}

// planFor builds the plan a real seed would build for one synthetic table.
func planFor(t *testing.T, table schemadef.Table, rows int) *Plan {
	t.Helper()
	tables := []schemadef.Table{table}
	p, err := BuildPlan(tables, PoolsFromTables(tables), Config{Rows: rows, Salt: 1})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	p.SetBounds(BoundsFromTables(tables))
	return p
}

func seedTime(t *testing.T, p *Plan, table, column string, row int) time.Time {
	t.Helper()
	raw, ok := p.SeedValue(table, column, row)
	if !ok {
		t.Fatalf("%s.%s row %d has no seeded scalar value", table, column, row)
	}
	at, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		t.Fatalf("%s.%s row %d = %q, which is not a timestamp: %v", table, column, row, raw, err)
	}
	return at
}

// The SHAPE corpus: every constraint spelling in it was produced by applying
// real SQL to real postgres and reading pg_get_constraintdef back, so these
// are the exact strings the introspector hands the detector — not a guess at
// how postgres canonicalizes (it parenthesizes every boolean operand, drops
// redundant parens and identifier quoting, and keeps operand order).
//
// The accepted half is the ordering requirement written the way an author
// writes it over columns that may be absent. The refused half is the point of
// the exercise in the other direction: forge's placement does not PROVE those
// expressions, so they must keep reaching the warning rather than being
// silently assumed satisfied.
func TestOrderRelsFromCheck_Shapes(t *testing.T) {
	cases := []struct {
		name string
		ck   schemadef.CheckConstraint
		want []orderRel // nil means "must be refused"
	}{{
		name: "bare comparison",
		ck:   check("c", "CHECK ((expires_at > issued_at))", "expires_at", "issued_at"),
		want: []orderRel{{lo: "issued_at", hi: "expires_at"}},
	}, {
		name: "reversed operands",
		ck:   check("c", "CHECK ((issued_at < expires_at))", "issued_at", "expires_at"),
		want: []orderRel{{lo: "issued_at", hi: "expires_at"}},
	}, {
		// The constraint that cost two consecutive measured runs.
		name: "both sides NULL-guarded",
		ck: check("c", "CHECK (((issued_at IS NULL) OR (expires_at IS NULL) OR (expires_at > issued_at)))",
			"issued_at", "expires_at"),
		want: []orderRel{{lo: "issued_at", hi: "expires_at"}},
	}, {
		name: "guards trailing the comparison",
		ck: check("c", "CHECK (((expires_at > issued_at) OR (issued_at IS NULL) OR (expires_at IS NULL)))",
			"expires_at", "issued_at"),
		want: []orderRel{{lo: "issued_at", hi: "expires_at"}},
	}, {
		name: "NULL-guarded non-strict",
		ck: check("c", "CHECK (((min_qty IS NULL) OR (max_qty IS NULL) OR (max_qty >= min_qty)))",
			"min_qty", "max_qty"),
		want: []orderRel{{lo: "min_qty", hi: "max_qty"}},
	}, {
		name: "one-sided guard",
		ck:   check("c", "CHECK (((expires_at IS NULL) OR (expires_at > issued_at)))", "expires_at", "issued_at"),
		want: []orderRel{{lo: "issued_at", hi: "expires_at"}},
	}, {
		name: "an unrelated escape hatch among the guards",
		ck: check("c", "CHECK (((issued_at IS NULL) OR (expires_at IS NULL) OR (expires_at > issued_at) OR (max_qty IS NULL)))",
			"issued_at", "expires_at", "max_qty"),
		want: []orderRel{{lo: "issued_at", hi: "expires_at"}},
	}, {
		// One constraint naming a whole chain: an AND needs every edge, so
		// every edge is placed.
		name: "conjunction of two orderings under a guard",
		ck: check("c", "CHECK (((issued_at IS NULL) OR (expires_at IS NULL) OR ((expires_at > issued_at) AND (max_qty > min_qty))))",
			"issued_at", "expires_at", "max_qty", "min_qty"),
		want: []orderRel{{lo: "issued_at", hi: "expires_at"}, {lo: "min_qty", hi: "max_qty"}},
	}, {
		name: "constraint added NOT VALID is still enforced on inserts",
		ck:   check("c", "CHECK ((expires_at > issued_at)) NOT VALID", "expires_at", "issued_at"),
		want: []orderRel{{lo: "issued_at", hi: "expires_at"}},
	}, {
		// An offset: a 30-day step does not prove `b > a + interval '1 day'`
		// in general, and forge must not act as though it does.
		name: "comparison against an arithmetic offset",
		ck:   check("c", "CHECK ((expires_at > (issued_at + '1 day'::interval)))", "expires_at", "issued_at"),
	}, {
		name: "COALESCE around an operand",
		ck: check("c", "CHECK ((COALESCE(expires_at, 'infinity'::timestamp with time zone) > issued_at))",
			"expires_at", "issued_at"),
	}, {
		name: "CASE expression",
		ck: check("c", "CHECK (\n CASE\n     WHEN (expires_at IS NULL) THEN true\n     ELSE (expires_at > issued_at)\nEND)",
			"expires_at", "issued_at"),
	}, {
		name: "negation of the inverse comparison",
		ck:   check("c", "CHECK ((NOT (expires_at <= issued_at)))", "expires_at", "issued_at"),
	}, {
		// An AND is only as placeable as its WEAKEST branch: forge does not
		// own whether a column is NULL, so it cannot claim this one.
		name: "conjunction with a non-null assertion",
		ck: check("c", "CHECK (((issued_at IS NOT NULL) AND (expires_at > issued_at)))",
			"issued_at", "expires_at"),
	}, {
		name: "not a comparison at all",
		ck:   check("c", "CHECK ((num_nonnulls(signed_at, voided_at) = 1))", "signed_at", "voided_at"),
	}, {
		name: "a column compared with itself",
		ck:   check("c", "CHECK ((issued_at >= issued_at))", "issued_at"),
	}, {
		// The paranoia guard: the expression must talk about the columns
		// postgres says the constraint spans.
		name: "parsed columns disagree with the conkey",
		ck:   check("c", "CHECK ((expires_at > issued_at))", "expires_at", "something_else"),
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := orderRelsFromCheck(tc.ck)
			if len(tc.want) == 0 {
				if ok {
					t.Fatalf("%s must be REFUSED (forge's placement does not prove it), got %+v", tc.ck.Def, got)
				}
				return
			}
			if !ok {
				t.Fatalf("%s must be read as an ordering comparison; it was refused", tc.ck.Def)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("%s: got %d edge(s) %+v, want %d %+v", tc.ck.Def, len(got), got, len(tc.want), tc.want)
			}
			for i := range got {
				if got[i].lo != tc.want[i].lo || got[i].hi != tc.want[i].hi {
					t.Errorf("%s: edge %d = %s -> %s, want %s -> %s",
						tc.ck.Def, i, got[i].lo, got[i].hi, tc.want[i].lo, tc.want[i].hi)
				}
				if got[i].constraint != tc.ck.Name {
					t.Errorf("edge %d must carry the constraint name %q, got %q", i, tc.ck.Name, got[i].constraint)
				}
			}
		})
	}
}

// The plan-level half of the same fix: a NULL-guarded window is PLACED, so
// the rows satisfy it by arithmetic and the plan needs no warning. Before the
// detector read through the guard, this schema produced
//
//	seed plan: prescriptions constraint "prescriptions_expires_after_issued"
//	spans two columns and forge cannot place its values (not a two-column
//	ordering comparison) — seeded rows satisfy it only by chance
//
// and `forge db seed apply` wrote zero rows.
func TestOrdering_NullGuardedWindowIsPlaced(t *testing.T) {
	table := schemadef.Table{
		Name:   "prescriptions",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			idCol(),
			// Optional timestamps: nullable is exactly why the author wrote
			// the guard, and forge births a Timestamp column nullable.
			{Name: "issued_at", DeclType: "TIMESTAMPTZ", Type: schemadef.TypeTime},
			{Name: "expires_at", DeclType: "TIMESTAMPTZ", Type: schemadef.TypeTime},
		},
		Checks: []schemadef.CheckConstraint{
			check("prescriptions_expires_after_issued",
				"CHECK (((issued_at IS NULL) OR (expires_at IS NULL) OR (expires_at > issued_at)))",
				"issued_at", "expires_at"),
		},
	}
	p := planFor(t, table, 5)
	if warns := p.Warnings(); len(warns) > 0 {
		t.Fatalf("a NULL guard does not change the ordering requirement; the plan must place it without compromise, got:\n  %s",
			strings.Join(warns, "\n  "))
	}
	for row := 0; row < 5; row++ {
		issued := seedTime(t, p, "prescriptions", "issued_at", row)
		expires := seedTime(t, p, "prescriptions", "expires_at", row)
		if !expires.After(issued) {
			t.Errorf("row %d: expires_at (%s) must be after issued_at (%s)",
				row, expires.Format(time.RFC3339), issued.Format(time.RFC3339))
		}
	}
}

// A validity window — the exact constraint a measured real-workflow run hit.
// Before the ordering pass, timestampLiteral handed both columns the same
// instant, so `expires_at > issued_at` was false on EVERY row.
func TestOrdering_TimeWindowIsPlacedAboveItsLowerBound(t *testing.T) {
	table := schemadef.Table{
		Name:   "prescriptions",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			idCol(), timeCol("issued_at"), timeCol("expires_at"),
		},
		Checks: []schemadef.CheckConstraint{
			check("prescriptions_validity_window", "CHECK ((expires_at > issued_at))", "expires_at", "issued_at"),
		},
	}
	p := planFor(t, table, 5)
	if warns := p.Warnings(); len(warns) > 0 {
		t.Fatalf("a placeable ordering constraint must produce no warning, got:\n  %s", strings.Join(warns, "\n  "))
	}
	for row := 0; row < 5; row++ {
		issued := seedTime(t, p, "prescriptions", "issued_at", row)
		expires := seedTime(t, p, "prescriptions", "expires_at", row)
		if !expires.After(issued) {
			t.Errorf("row %d: expires_at (%s) must be after issued_at (%s) — CHECK (expires_at > issued_at)",
				row, expires.Format(time.RFC3339), issued.Format(time.RFC3339))
		}
		if got := expires.Sub(issued); got != OrderStepDays*24*time.Hour {
			t.Errorf("row %d: window is %s, want %d days — the placement must read as a plausible window, not a nudge",
				row, got, OrderStepDays)
		}
	}
}

// A three-column chain ranks transitively: each link sits above the last.
// Rank is the LONGEST path, so a column reachable two ways still lands above
// both of its lower bounds.
func TestOrdering_ChainRanksTransitively(t *testing.T) {
	table := schemadef.Table{
		Name:   "campaigns",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			idCol(), timeCol("opens_at"), timeCol("closes_at"), timeCol("settles_at"),
		},
		Checks: []schemadef.CheckConstraint{
			check("campaigns_open_before_close", "CHECK ((closes_at > opens_at))", "closes_at", "opens_at"),
			check("campaigns_close_before_settle", "CHECK ((settles_at > closes_at))", "settles_at", "closes_at"),
			// The long way round: settles_at is also directly above opens_at.
			check("campaigns_open_before_settle", "CHECK ((settles_at > opens_at))", "settles_at", "opens_at"),
		},
	}
	p := planFor(t, table, 3)
	if warns := p.Warnings(); len(warns) > 0 {
		t.Fatalf("unexpected warnings:\n  %s", strings.Join(warns, "\n  "))
	}
	for row := 0; row < 3; row++ {
		opens := seedTime(t, p, "campaigns", "opens_at", row)
		closes := seedTime(t, p, "campaigns", "closes_at", row)
		settles := seedTime(t, p, "campaigns", "settles_at", row)
		if !closes.After(opens) || !settles.After(closes) {
			t.Errorf("row %d: want opens_at < closes_at < settles_at, got %s / %s / %s",
				row, opens.Format(time.RFC3339), closes.Format(time.RFC3339), settles.Format(time.RFC3339))
		}
	}
}

// Integers order too — `max_qty >= min_qty` is the same shape with a
// different type.
func TestOrdering_IntegerPair(t *testing.T) {
	table := schemadef.Table{
		Name:   "tiers",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			idCol(), intCol("min_qty"), intCol("max_qty"),
		},
		Checks: []schemadef.CheckConstraint{
			check("tiers_qty_range", "CHECK ((max_qty >= min_qty))", "max_qty", "min_qty"),
		},
	}
	p := planFor(t, table, 4)
	for row := 0; row < 4; row++ {
		lo, ok1 := p.SeedValue("tiers", "min_qty", row)
		hi, ok2 := p.SeedValue("tiers", "max_qty", row)
		if !ok1 || !ok2 {
			t.Fatalf("row %d: min_qty/max_qty not seeded", row)
		}
		if !(len(hi) > len(lo) || hi > lo) {
			t.Errorf("row %d: max_qty %s must exceed min_qty %s", row, hi, lo)
		}
	}
}

// A root column keeps the value it would have had anyway. Ordering must not
// reshuffle a dataset that has no ordering constraint in it — the whole
// package's determinism contract depends on adding a constraint changing
// only the columns that constraint governs.
func TestOrdering_RootKeepsItsNaturalValue(t *testing.T) {
	cols := []schemadef.Column{idCol(), timeCol("issued_at"), timeCol("expires_at")}
	plain := planFor(t, schemadef.Table{Name: "rx", PKCols: []string{"id"}, Columns: cols}, 3)
	ordered := planFor(t, schemadef.Table{
		Name: "rx", PKCols: []string{"id"}, Columns: cols,
		Checks: []schemadef.CheckConstraint{
			check("rx_window", "CHECK ((expires_at > issued_at))", "expires_at", "issued_at"),
		},
	}, 3)
	for row := 0; row < 3; row++ {
		a, _ := plain.SeedValue("rx", "issued_at", row)
		b, _ := ordered.SeedValue("rx", "issued_at", row)
		if a != b {
			t.Errorf("row %d: the lower bound must keep its natural value; %q became %q", row, a, b)
		}
	}
}

// The refusals. Each of these is a constraint forge cannot place, and each
// must be NAMED — a silent skip is how a seeder ends up writing rows that
// contradict a rule the schema states.
func TestOrdering_UnplaceableConstraintsAreNamed(t *testing.T) {
	cases := []struct {
		name    string
		table   schemadef.Table
		wantCon string
		wantWhy string
	}{
		{
			name: "not a two-column comparison",
			table: schemadef.Table{
				Name: "docs", PKCols: []string{"id"},
				Columns: []schemadef.Column{idCol(), timeCol("signed_at"), timeCol("voided_at")},
				Checks: []schemadef.CheckConstraint{
					check("docs_exclusive", "CHECK ((num_nonnulls(signed_at, voided_at) = 1))", "signed_at", "voided_at"),
				},
			},
			wantCon: "docs_exclusive",
			wantWhy: "not a two-column ordering comparison",
		},
		{
			// Reading through a NULL guard must NOT become "read through
			// anything": an offset comparison is a different requirement,
			// and a 30-day step does not prove it.
			name: "an ordering comparison against an offset",
			table: schemadef.Table{
				Name: "leases", PKCols: []string{"id"},
				Columns: []schemadef.Column{idCol(), timeCol("starts_at"), timeCol("ends_at")},
				Checks: []schemadef.CheckConstraint{
					check("leases_min_term", "CHECK ((ends_at > (starts_at + '1 day'::interval)))", "ends_at", "starts_at"),
				},
			},
			wantCon: "leases_min_term",
			wantWhy: "not a two-column ordering comparison",
		},
		{
			name: "an operand wrapped in COALESCE",
			table: schemadef.Table{
				Name: "grants", PKCols: []string{"id"},
				Columns: []schemadef.Column{idCol(), timeCol("granted_at"), timeCol("revoked_at")},
				Checks: []schemadef.CheckConstraint{
					check("grants_window",
						"CHECK ((COALESCE(revoked_at, 'infinity'::timestamp with time zone) > granted_at))",
						"revoked_at", "granted_at"),
				},
			},
			wantCon: "grants_window",
			wantWhy: "not a two-column ordering comparison",
		},
		{
			name: "mismatched column types",
			table: schemadef.Table{
				Name: "mixed", PKCols: []string{"id"},
				Columns: []schemadef.Column{idCol(), timeCol("starts_at"), intCol("limit_n")},
				Checks: []schemadef.CheckConstraint{
					check("mixed_bad", "CHECK ((limit_n > starts_at))", "limit_n", "starts_at"),
				},
			},
			wantCon: "mixed_bad",
			wantWhy: "is time but",
		},
		{
			name: "one side is a key column",
			table: schemadef.Table{
				Name: "seqs", PKCols: []string{"id"},
				Columns: []schemadef.Column{
					{Name: "id", DeclType: "BIGINT", Type: schemadef.TypeInt, NotNull: true, IsPK: true},
					intCol("floor_n"),
				},
				Checks: []schemadef.CheckConstraint{
					check("seqs_above_id", "CHECK ((floor_n > id))", "floor_n", "id"),
				},
			},
			wantCon: "seqs_above_id",
			wantWhy: "one side is a key",
		},
		{
			name: "the constraints form a cycle",
			table: schemadef.Table{
				Name: "knots", PKCols: []string{"id"},
				Columns: []schemadef.Column{idCol(), timeCol("a_at"), timeCol("b_at")},
				Checks: []schemadef.CheckConstraint{
					check("knots_fwd", "CHECK ((b_at > a_at))", "b_at", "a_at"),
					check("knots_back", "CHECK ((a_at > b_at))", "a_at", "b_at"),
				},
			},
			wantCon: "knots_fwd",
			wantWhy: "cycle",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := planFor(t, tc.table, 3)
			joined := strings.Join(p.Warnings(), "\n")
			if !strings.Contains(joined, tc.wantCon) || !strings.Contains(joined, tc.wantWhy) {
				t.Errorf("the plan must name constraint %q and why it could not be placed (%q); got:\n%s",
					tc.wantCon, tc.wantWhy, joined)
			}
		})
	}
}

// orderingMigration is the born-shaped schema whose validity window the
// seeder used to violate on every row.
const orderingMigration = `
CREATE TABLE prescriptions (
    id TEXT PRIMARY KEY CHECK (id <> ''),
    issued_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT (now()),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT (now())
);
ALTER TABLE prescriptions ADD CONSTRAINT prescriptions_validity_window CHECK (expires_at > issued_at);
`

// Real postgres is the judge: it enforces the two-column CHECK on every row,
// so the INSERT landing IS the proof that the ordering pass satisfied it.
func TestMaterialize_TwoColumnCheckOnRealPostgres(t *testing.T) {
	requirePG(t)
	ctx := context.Background()
	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("pgtest.New: %v", err)
	}
	t.Cleanup(cleanup)
	if _, err := db.Exec(orderingMigration); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "00001_init.up.sql"), []byte(orderingMigration), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Materialize(ctx, db, dir, "", Config{Rows: 10, Salt: 1}); err != nil {
		t.Fatalf("%v", err)
	}
	var bad int
	if err := db.QueryRow(`SELECT count(*) FROM prescriptions WHERE expires_at <= issued_at`).Scan(&bad); err != nil {
		t.Fatal(err)
	}
	if bad != 0 {
		t.Errorf("%d seeded row(s) do not satisfy CHECK (expires_at > issued_at)", bad)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM prescriptions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 10 {
		t.Errorf("prescriptions holds %d row(s), want 10 — a table skipped is the other way to fail this", n)
	}
}

// A DERIVED column — `total = subtotal + tax` — is not an ordering edge and
// forge's placement cannot satisfy it: three independently placed values make
// the equality true only by coincidence. That much was already correct.
//
// What was missing is the REMEDY. The generic refusal ("not a two-column
// ordering comparison") describes the parser, not the author's mistake, and
// two measured forge-one-shot runs both read it and went looking for a
// seeding workaround — one hand-wrote 192 lines of CRUD override to maintain
// the column — when postgres will maintain it outright and forge already
// skips GENERATED columns when seeding.
//
// So this asserts the warning NAMES the derived column and the declaration
// that fixes it. Without the derivation detector it fails on both counts.
func TestOrdering_DerivedColumnWarningNamesGeneratedAlways(t *testing.T) {
	table := schemadef.Table{
		Name:   "estimates",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			idCol(), intCol("subtotal_cents"), intCol("tax_cents"), intCol("total_cents"),
		},
		Checks: []schemadef.CheckConstraint{
			// Verbatim shape from the measured runs, as postgres renders it.
			check("estimates_total_is_subtotal_plus_tax",
				"CHECK ((total_cents = (subtotal_cents + tax_cents)))",
				"total_cents", "subtotal_cents", "tax_cents"),
		},
	}
	p := planFor(t, table, 5)
	warns := p.Warnings()
	if len(warns) == 0 {
		t.Fatal("a derivation forge cannot place must still warn — silence here is the false green")
	}
	joined := strings.Join(warns, "\n  ")
	for _, want := range []string{
		"total_cents",         // WHICH column is derived
		"GENERATED ALWAYS AS", // the declaration that fixes it
		"forge:computed",      // the escape hatch for cross-ROW derivations
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the warning must mention %q so the author can act on it; got:\n  %s", want, joined)
		}
	}
}

// The guard on the above: a plain column-to-column equality states an
// invariant, not a derivation, and must NOT be told to become a GENERATED
// column — nothing computes it from anything.
func TestOrdering_PlainEqualityIsNotReportedAsDerivation(t *testing.T) {
	table := schemadef.Table{
		Name:   "transfers",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			idCol(), intCol("debit_cents"), intCol("credit_cents"),
		},
		Checks: []schemadef.CheckConstraint{
			check("transfers_balanced", "CHECK ((debit_cents = credit_cents))",
				"debit_cents", "credit_cents"),
		},
	}
	p := planFor(t, table, 5)
	if joined := strings.Join(p.Warnings(), "\n  "); strings.Contains(joined, "GENERATED ALWAYS AS") {
		t.Errorf("a plain equality is an invariant, not a derivation — it has no expression to generate from; got:\n  %s", joined)
	}
}
