package seedplan

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

// The defect this pins, stated as the run that found it:
//
//	--- FAIL: TestCRUD_LineItem_Lifecycle
//	    seed parent rows: pq: cannot insert a non-DEFAULT value into column "total_cents"
//
// An author took forge's OWN advice — the `db` skill tells them to express a
// same-row derivation as `GENERATED ALWAYS AS (...) STORED` — and the seeded
// fixture SQL kept naming the derived column in its INSERT. The ORM half was
// already right (a generated column is projected `scanonly`, so writes exclude
// it); only the rendered SQL was stale, and postgres refused it.
//
// The skip that fixes this lives in BuildPlan, and it was previously
// unguarded: deleting it left the whole suite green while every born fixture
// over a generated column failed in the wild.
//
// The proof is not a string assertion about the rendered SQL — it is postgres
// executing it. A test that only grepped for the column name would still pass
// if the value were wrong, and would not be reading the authority that
// actually rejects the statement.
const generatedColumnMigration = `
CREATE TABLE estimates (
    id TEXT PRIMARY KEY,
    subtotal_cents BIGINT NOT NULL DEFAULT 0,
    tax_cents BIGINT NOT NULL DEFAULT 0,
    total_cents BIGINT GENERATED ALWAYS AS (subtotal_cents + tax_cents) STORED
);

CREATE TABLE line_items (
    id TEXT PRIMARY KEY,
    estimate_id TEXT NOT NULL REFERENCES estimates(id),
    quantity BIGINT NOT NULL DEFAULT 1,
    unit_price_cents BIGINT NOT NULL DEFAULT 0,
    line_total_cents BIGINT GENERATED ALWAYS AS (quantity * unit_price_cents) STORED
);
`

// TestSeedSQLOverGeneratedColumnsIsAcceptedByPostgres is the headline
// property: the SQL BuildPlan renders for a schema carrying generated columns
// is executed by the database that defines them, and it is accepted.
func TestSeedSQLOverGeneratedColumnsIsAcceptedByPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("needs a live Postgres; runs in task test")
	}
	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("pgtest.New: %v", err)
	}
	t.Cleanup(cleanup)
	if _, err := db.Exec(generatedColumnMigration); err != nil {
		t.Fatalf("apply migration: %v", err)
	}

	// Introspect the APPLIED schema — the same production path generate runs
	// on — so IsGenerated comes from postgres rather than a hand-built table
	// that could agree with the bug.
	tables, err := schemadef.Introspect(db)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	var generated []string
	for _, tb := range tables {
		for _, c := range tb.Columns {
			if c.IsGenerated {
				generated = append(generated, tb.Name+"."+c.Name)
			}
		}
	}
	if len(generated) != 2 {
		t.Fatalf("introspection found %d generated columns (%v), want 2 — "+
			"the rest of this test would be vacuous", len(generated), generated)
	}

	plan, err := BuildPlan(tables, PoolsFromTables(tables), Config{Rows: 2})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	sql := strings.Join(plan.Statements(), "\n")

	// THE PROOF: postgres executes the rendered seed SQL. Naming a generated
	// column in the INSERT fails here with the exact error the run hit.
	if _, err := db.Exec(sql); err != nil {
		t.Fatalf("postgres rejected the rendered seed SQL: %v\n\nSQL:\n%s", err, sql)
	}

	// And the derivation really ran: a generated column holds the value its
	// expression computes, which is only true if the seeder left it to the DB.
	rows, err := db.Query(`SELECT subtotal_cents, tax_cents, total_cents FROM estimates ORDER BY id`)
	if err != nil {
		t.Fatalf("read back estimates: %v", err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var subtotal, tax, total int64
		if err := rows.Scan(&subtotal, &tax, &total); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if total != subtotal+tax {
			t.Errorf("total_cents = %d, want %d (subtotal %d + tax %d) — "+
				"the generated expression did not produce this row's value",
				total, subtotal+tax, subtotal, tax)
		}
		seen++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}
	if seen == 0 {
		t.Fatal("no estimates rows were seeded — nothing was verified")
	}
}

// TestSeedPlanOmitsGeneratedColumns states the same contract at the level the
// skip lives at, so a regression names the cause rather than only the symptom.
// It reads the plan's own column set — not the rendered text — because a
// substring search for "total_cents" also matches "subtotal_cents", and a
// guard that can be satisfied by the wrong column is not a guard.
func TestSeedPlanOmitsGeneratedColumns(t *testing.T) {
	table := schemadef.Table{
		Name:   "estimates",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			{Name: "id", Type: schemadef.TypeString, DeclType: "text", TypeKnown: true, NotNull: true, IsPK: true},
			{Name: "subtotal_cents", Type: schemadef.TypeInt, DeclType: "bigint", TypeKnown: true, NotNull: true},
			{Name: "total_cents", Type: schemadef.TypeInt, DeclType: "bigint", TypeKnown: true, IsGenerated: true},
		},
	}
	plan, err := BuildPlan([]schemadef.Table{table}, EnumPools{}, Config{Rows: 2})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.tables) != 1 {
		t.Fatalf("plan has %d tables, want 1", len(plan.tables))
	}
	for _, cp := range plan.tables[0].cols {
		if cp.col.IsGenerated {
			t.Errorf("plan writes generated column %q — postgres rejects an INSERT that names it",
				cp.col.Name)
		}
		if cp.col.Name == "total_cents" {
			t.Errorf("plan writes total_cents, the GENERATED ALWAYS column")
		}
	}
	// The non-generated columns must still be planned, or "omits generated
	// columns" would be trivially satisfied by planning nothing.
	if len(plan.tables[0].cols) != 2 {
		t.Errorf("plan writes %d columns, want 2 (id, subtotal_cents)", len(plan.tables[0].cols))
	}
}
