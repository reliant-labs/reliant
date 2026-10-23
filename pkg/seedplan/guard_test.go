// File: pkg/seedplan/guard_test.go
//
// Status-guard CHECKs — "when the row is in THIS state, these columns must be
// filled in" — are how a lifecycle is spelled in SQL, and they were the last
// multi-column shape the seeder could not place.
//
// The measured failure, on a real 12-table project: SEVEN guards across four
// tables, none placeable. `status` was drawn independently of the columns its
// value governs, so the planner produced a SCHEDULED job with no crew — legal
// for every single-column constraint, rejected by the table. Across six salts
// on that schema FOUR aborted the seed transaction with
//
//	new row for relation "jobs" violates check constraint
//	"jobs_scheduled_requires_assignment" (SQLSTATE 23514)
//
// and the author's only escape was to hand-author the entire dataset.

package seedplan

import (
	"context"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

// guardDDL is the shape of the measured schema: one lifecycle column carrying
// TWO guards (the case that made each refuse the other for spanning `status`),
// a guard over a NULLABLE FOREIGN KEY (the case the optional-edge coin flip
// broke), and both spellings of the exclusion — `<>` and `NOT IN`.
const guardDDL = `
CREATE TABLE crews (id TEXT PRIMARY KEY, name TEXT NOT NULL);
CREATE TABLE jobs (
    id TEXT PRIMARY KEY,
    status TEXT NOT NULL DEFAULT 'JOB_STATUS_DRAFT'
        CHECK (status IN ('JOB_STATUS_DRAFT','JOB_STATUS_SCHEDULED','JOB_STATUS_IN_PROGRESS','JOB_STATUS_COMPLETED')),
    crew_id TEXT REFERENCES crews(id),
    scheduled_start TIMESTAMPTZ,
    scheduled_end TIMESTAMPTZ,
    actual_completion TIMESTAMPTZ,
    CONSTRAINT jobs_scheduled_requires_assignment CHECK (
        status NOT IN ('JOB_STATUS_SCHEDULED','JOB_STATUS_IN_PROGRESS')
        OR (crew_id IS NOT NULL AND scheduled_start IS NOT NULL AND scheduled_end IS NOT NULL)
    ),
    CONSTRAINT jobs_completed_requires_completion_time CHECK (
        status <> 'JOB_STATUS_COMPLETED' OR actual_completion IS NOT NULL
    )
);
`

// The regression, end to end and across salts. A single salt proves nothing
// here: the old behaviour was a coin flip that happened to pass two runs in
// six, so a one-salt test would have been green on the broken code.
func TestStatusGuard_SeededRowsInsertAcrossSalts(t *testing.T) {
	tables := applyDDL(t, guardDDL)

	ctx := context.Background()
	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("pgtest.New: %v", err)
	}
	defer cleanup()

	for _, salt := range []int{5, 11, 23, 42, 77, 101} {
		p, err := BuildPlan(tables, PoolsFromTables(tables), Config{Rows: 8, Salt: salt})
		if err != nil {
			t.Fatalf("salt %d: BuildPlan: %v", salt, err)
		}
		p.SetBounds(BoundsFromTables(tables))

		if _, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS jobs, crews CASCADE"); err != nil {
			t.Fatalf("reset: %v", err)
		}
		if _, err := db.ExecContext(ctx, guardDDL); err != nil {
			t.Fatalf("apply ddl: %v", err)
		}
		for _, stmt := range p.Statements() {
			if _, err := db.ExecContext(ctx, stmt); err != nil {
				t.Errorf("salt %d: seeded row rejected by its own schema: %v", salt, err)
				break
			}
		}
	}
}

// Placing a guard must not be reported as a failure to place it. Both passes
// that could speak for a guard — ordering and union — used to warn about it,
// and a warning on a constraint forge now satisfies sends the author looking
// for a problem that no longer exists.
func TestStatusGuard_PlacedGuardsAreNotWarnedAbout(t *testing.T) {
	tables := applyDDL(t, guardDDL)
	p, err := BuildPlan(tables, PoolsFromTables(tables), Config{Rows: 8, Salt: 5})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	p.SetBounds(BoundsFromTables(tables))

	for _, w := range p.Warnings() {
		if strings.Contains(w, "jobs_scheduled_requires_assignment") ||
			strings.Contains(w, "jobs_completed_requires_completion_time") {
			t.Errorf("a guard forge now places is still reported as unplaceable:\n  %s", w)
		}
	}
}

// The dataset must still cover the UN-guarded states. Satisfying the guards by
// making every job COMPLETED would pass the insert and be useless: the whole
// point of the states is that the UI has a row in each.
func TestStatusGuard_UnguardedStatesStillAppear(t *testing.T) {
	tables := applyDDL(t, guardDDL)
	p, err := BuildPlan(tables, PoolsFromTables(tables), Config{Rows: 8, Salt: 5})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	p.SetBounds(BoundsFromTables(tables))

	seen := map[string]bool{}
	for i := 0; i < p.rowsOf["jobs"]; i++ {
		v, _ := p.SeedValue("jobs", "status", i)
		seen[v] = true
	}
	if len(seen) < 2 {
		t.Errorf("every seeded job carries the same status (%v) — the guard was satisfied by "+
			"collapsing the lifecycle instead of placing it", seen)
	}
	if !seen["JOB_STATUS_DRAFT"] {
		t.Errorf("no DRAFT job seeded; states = %v. An un-guarded state must still appear, or "+
			"the dataset has no row for the app's default path", seen)
	}
}

// The guarded rows must actually carry what the guard demands, not merely
// avoid the guarded state.
func TestStatusGuard_GuardedRowsCarryTheirRequiredColumns(t *testing.T) {
	tables := applyDDL(t, guardDDL)
	p, err := BuildPlan(tables, PoolsFromTables(tables), Config{Rows: 8, Salt: 5})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	p.SetBounds(BoundsFromTables(tables))

	guardedSeen := false
	for i := 0; i < p.rowsOf["jobs"]; i++ {
		status, _ := p.SeedValue("jobs", "status", i)
		switch status {
		case "JOB_STATUS_SCHEDULED", "JOB_STATUS_IN_PROGRESS":
			guardedSeen = true
			// A nullable FK is normally nulled on ~1 row in 5; under a guard
			// the edge must be resolved on every row.
			if v, ok := p.SeedValue("jobs", "crew_id", i); !ok || v == "" {
				t.Errorf("row %d is %s with no crew — the optional-edge coin flip was not "+
					"suppressed for a guarded foreign key", i, status)
			}
		}
	}
	if !guardedSeen {
		t.Skip("no guarded state in this dataset; the coverage test above owns that assertion")
	}
}

// A guard over a column with NO declared vocabulary cannot be placed: there is
// no value set to draw the un-guarded branch from, and forge will not invent
// one. It must be REFUSED BY NAME — the fix is one line of SQL, and silence
// leaves the author with a duplicate-key abort and nothing to act on.
func TestStatusGuard_NoVocabularyIsRefusedByName(t *testing.T) {
	tables := applyDDL(t, `
CREATE TABLE tickets (
    id TEXT PRIMARY KEY,
    state TEXT NOT NULL,
    resolved_at TIMESTAMPTZ,
    CONSTRAINT tickets_resolved_has_time CHECK (
        state <> 'resolved' OR resolved_at IS NOT NULL
    )
);
`)
	p, err := BuildPlan(tables, PoolsFromTables(tables), Config{Rows: 4, Salt: 3})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	p.SetBounds(BoundsFromTables(tables))

	joined := strings.Join(p.Warnings(), "\n")
	if !strings.Contains(joined, "tickets_resolved_has_time") {
		t.Errorf("an unplaceable guard was not named; warnings:\n%s", joined)
	}
	if !strings.Contains(joined, "IN (") {
		t.Errorf("the refusal does not tell the author what to add; warnings:\n%s", joined)
	}
}

// A consequent this grammar cannot read — here a column-to-column comparison —
// leaves the constraint alone rather than placing a partial reading of it.
func TestStatusGuard_UnreadableConsequentIsNotPlaced(t *testing.T) {
	body := `(status <> 'PAID'::text) OR ((amount_paid_cents = amount_cents) AND (paid_at IS NOT NULL))`
	if _, ok := parseStatusGuard(body); ok {
		t.Error("a guard whose consequent compares two COLUMNS was parsed; forge cannot place " +
			"one column equal to another, so reading it would place a false constraint")
	}
}

// Both spellings of the exclusion must parse: `<>` and the ALL(ARRAY[…]) form
// pg_get_constraintdef renders NOT IN as.
func TestStatusGuard_ParsesBothExclusionSpellings(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       []string
		exempt     bool
	}{
		{"not equal", `(status <> 'COMPLETED'::text) OR (done_at IS NOT NULL)`, []string{"COMPLETED"}, false},
		{"not in canonical", `(status <> ALL (ARRAY['A'::text, 'B'::text])) OR (x IS NOT NULL)`, []string{"A", "B"}, false},
		{"not in authored", `(status NOT IN ('A', 'B')) OR (x IS NOT NULL)`, []string{"A", "B"}, false},
		// The POSITIVE spelling: the listed values are the ones EXEMPT.
		{"in canonical", `(status = ANY (ARRAY['A'::text, 'B'::text])) OR (x IS NOT NULL)`, []string{"A", "B"}, true},
		{"in authored", `(status IN ('A', 'B')) OR (x IS NOT NULL)`, []string{"A", "B"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, ok := parseStatusGuard(tc.body)
			if !ok {
				t.Fatalf("did not parse: %s", tc.body)
			}
			if g.column != "status" {
				t.Errorf("column = %q, want status", g.column)
			}
			if strings.Join(g.listed, ",") != strings.Join(tc.want, ",") {
				t.Errorf("listed = %v, want %v", g.listed, tc.want)
			}
			if g.listedAreExempt != tc.exempt {
				t.Errorf("listedAreExempt = %v, want %v", g.listedAreExempt, tc.exempt)
			}
		})
	}
}

// A three-armed disjunction is not a guard. It is either a real discriminated
// union (which the union matcher owns) or something with no reading here, and
// guessing between them is the ambiguity a narrow matcher exists to avoid.
func TestStatusGuard_OnlyTwoArmedDisjunctionsAreGuards(t *testing.T) {
	if _, ok := parseStatusGuard(`(a <> 'x') OR (b IS NOT NULL) OR (c IS NOT NULL)`); ok {
		t.Error("a three-armed disjunction was read as a guard")
	}
}

// The literal-list splitter must respect quoting: a comma INSIDE a quoted
// value does not end an element. splitTopLevel could not be reused here (it
// matches a space-padded operator, so a bare comma never matches), and this
// pins the replacement.
func TestStatusGuard_LiteralListSplitsOnlyAtTopLevel(t *testing.T) {
	got, ok := parseGuardLiteralList(`'a,b', 'c'`)
	if !ok {
		t.Fatal("did not parse a list whose first literal contains a comma")
	}
	if len(got) != 2 || got[0] != "a,b" || got[1] != "c" {
		t.Errorf("got %q, want [\"a,b\" \"c\"] — a comma inside quotes must not split", got)
	}
}

// The POSITIVE spelling, placed end to end. `status IN ('DRAFT','CANCELLED')
// OR ordered_at IS NOT NULL` states the same rule as its NOT IN twin from the
// other side: the listed values are EXEMPT. A matcher that knew only the
// negative form read this as unplaceable, placed the table's OTHER guard, and
// then refused THAT one too for sharing `status` — so one unrecognised
// spelling cost both constraints on the table.
func TestStatusGuard_PositiveSpellingPlacesBothGuards(t *testing.T) {
	tables := applyDDL(t, `
CREATE TABLE material_orders (
    id TEXT PRIMARY KEY,
    status TEXT NOT NULL DEFAULT 'DRAFT'
        CHECK (status IN ('DRAFT','CANCELLED','PLACED','RECEIVED')),
    ordered_at TIMESTAMPTZ,
    received_at TIMESTAMPTZ,
    CONSTRAINT mo_placed_has_date CHECK (
        status IN ('DRAFT','CANCELLED') OR ordered_at IS NOT NULL
    ),
    CONSTRAINT mo_received_has_date CHECK (
        status <> 'RECEIVED' OR received_at IS NOT NULL
    )
);
`)
	p, err := BuildPlan(tables, PoolsFromTables(tables), Config{Rows: 8, Salt: 1})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	p.SetBounds(BoundsFromTables(tables))

	if got := p.Warnings(); len(got) != 0 {
		t.Errorf("both guards are placeable, so nothing should be reported:\n  %s",
			strings.Join(got, "\n  "))
	}

	// A DRAFT row is exempt and must NOT be forced to carry an order date;
	// a PLACED row is subject and must.
	for i := 0; i < p.rowsOf["material_orders"]; i++ {
		status, _ := p.SeedValue("material_orders", "status", i)
		ordered, hasOrdered := p.SeedValue("material_orders", "ordered_at", i)
		if status == "PLACED" && (!hasOrdered || ordered == "") {
			t.Errorf("row %d is PLACED with no ordered_at", i)
		}
	}
}

// Guard placement must not disturb a table that has none.
func TestStatusGuard_PlainUnionStillPlaced(t *testing.T) {
	tables := applyDDL(t, `
CREATE TABLE ledger (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL CHECK (kind IN ('wallet_credit','compute_minutes')),
    amount_cents BIGINT,
    compute_minutes BIGINT,
    CONSTRAINT ledger_shape CHECK (
        (kind = 'wallet_credit' AND amount_cents IS NOT NULL AND compute_minutes IS NULL)
        OR (kind = 'compute_minutes' AND compute_minutes IS NOT NULL AND amount_cents IS NULL)
    )
);
`)
	p, err := BuildPlan(tables, PoolsFromTables(tables), Config{Rows: 6, Salt: 9})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	p.SetBounds(BoundsFromTables(tables))
	for _, w := range p.Warnings() {
		if strings.Contains(w, "ledger_shape") {
			t.Errorf("a plain discriminated union regressed: %s", w)
		}
	}
}
