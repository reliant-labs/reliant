// File: pkg/seedplan/expression_unique_test.go
//
// An EXPRESSION unique index — `UNIQUE (lower(name))`, `UNIQUE (supplier_id,
// lower(sku))` — is a constraint forge cannot plan against: placing a value
// that controls `lower(sku)` means evaluating the expression, and the
// introspected Columns hold only the bare-column part of the key.
//
// Before schemadef reported the expression flag, both shapes failed, and the
// mixed one failed silently in the expensive direction: pg_catalog stores an
// expression key as attnum 0, so an inner join dropped it and
// `UNIQUE (supplier_id, lower(sku))` arrived here as plain
// `UNIQUE (supplier_id)`. That is a STRICTER table than the real one, so the
// planner capped materials at one row per supplier — and the rows it wrote
// then collided on the index that actually exists. The seeder reported
// "forge generated both this schema and this data, and they disagree",
// which points at the data while the schema record was the thing that lied.

package seedplan

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

// applyDDL boots a real postgres, applies ddl, and returns the introspected
// tables — the same path `forge db seed apply` takes.
func applyDDL(t *testing.T, ddl string) []schemadef.Table {
	t.Helper()
	if testing.Short() {
		t.Skip("boots real postgres; skipped under -short")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "00001_init.up.sql"), []byte(ddl), 0o644); err != nil {
		t.Fatal(err)
	}
	tables, err := schemadef.ApplyAndIntrospect(dir)
	if err != nil {
		t.Fatalf("ApplyAndIntrospect: %v", err)
	}
	return tables
}

// The regression, end to end: seed a schema whose child table carries a mixed
// expression unique index, and insert the result into a real postgres. The
// insert is the assertion — it is what failed for the author.
func TestExpressionUniqueIndex_SeededRowsInsert(t *testing.T) {
	const ddl = `
CREATE TABLE suppliers (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL
);
CREATE UNIQUE INDEX suppliers_name_key ON suppliers (lower(name));

CREATE TABLE materials (
    id TEXT PRIMARY KEY,
    supplier_id TEXT NOT NULL REFERENCES suppliers(id),
    sku TEXT NOT NULL
);
CREATE UNIQUE INDEX materials_supplier_sku_key ON materials (supplier_id, lower(sku));
`
	tables := applyDDL(t, ddl)

	p, err := BuildPlan(tables, PoolsFromTables(tables), Config{Rows: 6, Salt: 7})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	p.SetBounds(BoundsFromTables(tables))

	// The mixed index must not have capped the child table at the parent's
	// row count — that was the visible symptom of reading it as
	// UNIQUE(supplier_id).
	if got := p.rowsOf["materials"]; got < 2 {
		t.Errorf("materials holds %d row(s): an expression index was read as a plain one-column UNIQUE", got)
	}

	// And the rows must actually go in.
	ctx := context.Background()
	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("pgtest.New: %v", err)
	}
	defer cleanup()
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		t.Fatalf("apply ddl: %v", err)
	}
	for _, stmt := range p.Statements() {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seeded row rejected by the schema forge introspected:\n  %v\n  stmt: %s", err, stmt)
		}
	}
}

// A case fold is the one expression forge PLANS rather than refuses:
// `UNIQUE (region, lower(name))` states "distinct ignoring case", which a
// value generator can satisfy without evaluating SQL. It must seed silently —
// a warning here would be forge reporting a limit it does not have.
func TestFoldedUniqueIndex_SeedsWithoutWarning(t *testing.T) {
	tables := applyDDL(t, `
CREATE TABLE crews (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    region TEXT NOT NULL
);
CREATE UNIQUE INDEX crews_region_name_key ON crews (region, lower(name));
`)
	const rows = 6
	p, err := BuildPlan(tables, PoolsFromTables(tables), Config{Rows: rows, Salt: 3})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	p.SetBounds(BoundsFromTables(tables))

	if joined := strings.Join(p.Warnings(), "\n"); joined != "" {
		t.Errorf("a fold is plannable, so nothing should be reported; warnings:\n%s", joined)
	}
	if got := p.rowsOf["crews"]; got != rows {
		t.Errorf("crews holds %d row(s), want %d — a fold must not cap the table", got, rows)
	}
	// Distinctness is judged the way the index judges it: case-folded.
	seen := map[string]bool{}
	for i := 0; i < p.rowsOf["crews"]; i++ {
		region, _ := p.SeedValue("crews", "region", i)
		name, _ := p.SeedValue("crews", "name", i)
		k := region + "|" + strings.ToLower(name)
		if seen[k] {
			t.Errorf("row %d repeats the folded tuple %q", i, k)
		}
		seen[k] = true
	}
}

// An expression forge CANNOT attribute to one column is refused, and named.
// Silence was the other half of the original defect: a skipped constraint
// still fails at INSERT, and a seeder that reported nothing left the author
// reading a duplicate-key error with no way to reach the cause.
func TestUnplaceableExpressionIndex_IsNamedNotSkippedSilently(t *testing.T) {
	tables := applyDDL(t, `
CREATE TABLE crews (
    id TEXT PRIMARY KEY,
    first_name TEXT NOT NULL,
    last_name TEXT NOT NULL
);
CREATE UNIQUE INDEX crews_full_name_key ON crews ((first_name || ' ' || last_name));
`)
	p, err := BuildPlan(tables, PoolsFromTables(tables), Config{Rows: 4, Salt: 3})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	p.SetBounds(BoundsFromTables(tables))

	joined := strings.Join(p.Warnings(), "\n")
	if !strings.Contains(joined, "crews_full_name_key") || !strings.Contains(joined, "EXPRESSION") {
		t.Errorf("no warning names the expression index it could not place; warnings:\n%s", joined)
	}
}

// A plain unique index must keep working exactly as before — the flag narrows
// nothing that was previously plannable.
func TestPlainUniqueIndex_StillDrawsDistinctValues(t *testing.T) {
	tables := applyDDL(t, `
CREATE TABLE crews (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL
);
CREATE UNIQUE INDEX crews_name_key ON crews (name);
`)
	var crews schemadef.Table
	for _, tb := range tables {
		if tb.Name == "crews" {
			crews = tb
		}
	}
	if !uniqueSingleColumn(crews, "name") {
		t.Fatal("a plain UNIQUE (name) must still be read as a one-column unique")
	}

	p, err := BuildPlan(tables, PoolsFromTables(tables), Config{Rows: 5, Salt: 2})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	p.SetBounds(BoundsFromTables(tables))
	seen := map[string]bool{}
	for row := 0; row < p.rowsOf["crews"]; row++ {
		v, _ := p.SeedValue("crews", "name", row)
		if seen[v] {
			t.Errorf("row %d repeats name %q under a plain UNIQUE index", row, v)
		}
		seen[v] = true
	}
}
