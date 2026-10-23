package seedplan

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

func TestScopeTables_PullsRequiredParentsOnly(t *testing.T) {
	tables := []schemadef.Table{
		{Name: "orgs", Columns: []schemadef.Column{{Name: "id", NotNull: true}}},
		{Name: "users", Columns: []schemadef.Column{{Name: "id", NotNull: true}, {Name: "org_id", NotNull: true}},
			ForeignKeys: []schemadef.ForeignKey{{Column: "org_id", RefTable: "orgs", RefColumn: "id"}}},
		{Name: "payments", Columns: []schemadef.Column{{Name: "id", NotNull: true}}},
		// A CRUD table with an OPTIONAL link into the ledger: following it
		// would seed the very table the scope exists to leave alone.
		{Name: "tasks", Columns: []schemadef.Column{{Name: "id", NotNull: true}, {Name: "user_id", NotNull: true}, {Name: "payment_id"}},
			ForeignKeys: []schemadef.ForeignKey{
				{Column: "user_id", RefTable: "users", RefColumn: "id"},
				{Column: "payment_id", RefTable: "payments", RefColumn: "id"},
			}},
	}
	got := scopedNames(ScopeTables(tables, []string{"tasks", "not_a_table"}))
	if want := []string{"orgs", "tasks", "users"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ScopeTables = %v, want %v (required parents in, nullable-referenced ledger out)", got, want)
	}
}

func TestScopeTables_EmptyRootsSeedNothing(t *testing.T) {
	tables := []schemadef.Table{{Name: "payments"}}
	if got := ScopeTables(tables, []string{}); len(got) != 0 {
		t.Errorf("an empty scope must seed nothing, got %v", scopedNames(got))
	}
}

// A scoped plan must still be buildable: the closure is what keeps a NOT NULL
// reference satisfiable.
func TestBuildPlan_ScopedPlanIsSatisfiable(t *testing.T) {
	tables := []schemadef.Table{
		{Name: "orgs", PKCols: []string{"id"}, Columns: []schemadef.Column{{Name: "id", DeclType: "TEXT", Type: schemadef.TypeString, TypeKnown: true, NotNull: true}}},
		{Name: "users", PKCols: []string{"id"}, Columns: []schemadef.Column{{Name: "id", DeclType: "TEXT", Type: schemadef.TypeString, TypeKnown: true, NotNull: true}, {Name: "org_id", DeclType: "TEXT", Type: schemadef.TypeString, TypeKnown: true, NotNull: true}},
			ForeignKeys: []schemadef.ForeignKey{{Column: "org_id", RefTable: "orgs", RefColumn: "id"}}},
		{Name: "payments", PKCols: []string{"id"}, Columns: []schemadef.Column{{Name: "id", DeclType: "TEXT", Type: schemadef.TypeString, TypeKnown: true, NotNull: true}}},
	}
	plan, err := BuildPlan(ScopeTables(tables, []string{"users"}), nil, DefaultConfig())
	if err != nil {
		t.Fatalf("scoped plan must build: %v", err)
	}
	if got := plan.Tables(); !reflect.DeepEqual(got, []string{"orgs", "users"}) {
		t.Errorf("plan tables = %v, want [orgs users]", got)
	}
}

// ledgerMigration models the shape that surfaced the defect: one CRUD table
// the generated pages list, beside a payments ledger no CRUD RPC owns.
const ledgerMigration = `
CREATE TABLE tasks (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    reservation_id TEXT
);

CREATE TABLE founding_reservations (
    id TEXT PRIMARY KEY,
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'succeeded', 'released', 'refunded')),
    amount_cents BIGINT NOT NULL CHECK (amount_cents > 0)
);

ALTER TABLE tasks ADD CONSTRAINT tasks_reservation_fk
    FOREIGN KEY (reservation_id) REFERENCES founding_reservations(id);
`

// TestMaterialize_ScopedSeedLeavesLedgerEmpty is the real-postgres proof for
// the scoped auto-seed: scoped to the CRUD table, the ledger gets NO rows
// (the optional link into it is written NULL). Unscoped, the same schema
// fills the ledger with fabricated "succeeded" payments.
func TestMaterialize_ScopedSeedLeavesLedgerEmpty(t *testing.T) {
	requirePG(t)
	ctx := context.Background()
	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("pgtest.New: %v", err)
	}
	t.Cleanup(cleanup)
	if _, err := db.Exec(ledgerMigration); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "00001_init.up.sql"), []byte(ledgerMigration), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Materialize(ctx, db, dir, "", Config{Rows: 5, Tables: []string{"tasks"}}); err != nil {
		t.Fatalf("scoped Materialize: %v", err)
	}
	assertCount(t, db, "tasks", 5)
	assertCount(t, db, "founding_reservations", 0)
}

// scopedNames returns the sorted table names ScopeTables kept.
func scopedNames(tables []schemadef.Table) []string {
	out := make([]string, 0, len(tables))
	for _, t := range tables {
		out = append(out, t.Name)
	}
	sort.Strings(out)
	return out
}
