package schemadef

import "testing"

// An EXPRESSION index key (`lower(name)`) is stored by pg_catalog as attnum
// 0, which has no pg_attribute row. Introspection used to inner-join
// pg_attribute, so those keys vanished — and the two ways they vanished had
// very different costs:
//
//   - An all-expression index produced NO rows, so `UNIQUE (lower(name))`
//     was reported as no index at all. Loud enough to notice.
//   - A MIXED index produced the bare-column rows only, so
//     `UNIQUE (supplier_id, lower(sku))` was reported as
//     `UNIQUE (supplier_id)`. That is not a malformed record, it is a
//     plausible one describing a STRICTER table than the one that exists,
//     and every consumer downstream believed it.
//
// The second is what made this expensive in practice: seedplan reads a
// one-column unique index as "this column is unique", so it drew one value
// per row for supplier_id and capped the child table at one row per
// supplier. The rows it then wrote violated the real index, and the seeder
// reported the disagreement as `forge generated both this schema and this
// data, and they disagree` — pointing at the seed data, while the schema
// record was the thing that was wrong.
//
// The contract these pin: an expression index is REPORTED, its Columns hold
// only the bare-column subset, and Expression says so, so no consumer can
// mistake that subset for the whole key.
func TestIntrospect_AllExpressionUniqueIndexIsReportedAsOpaque(t *testing.T) {
	requireRealPG(t)
	dir := t.TempDir()
	writeMig(t, dir, "00001_create_crews.up.sql", `
CREATE TABLE crews (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL
);
CREATE UNIQUE INDEX crews_name_key ON crews (lower(name));
`)

	tables, err := ApplyAndIntrospect(dir)
	if err != nil {
		t.Fatalf("ApplyAndIntrospect: %v", err)
	}
	if len(tables) != 1 {
		t.Fatalf("tables = %+v, want exactly [crews]", tables)
	}

	var ix *Index
	for i := range tables[0].Indexes {
		if tables[0].Indexes[i].Name == "crews_name_key" {
			ix = &tables[0].Indexes[i]
		}
	}
	if ix == nil {
		t.Fatalf("crews_name_key missing from Indexes = %+v; an expression index must still be reported", tables[0].Indexes)
	}
	if !ix.Unique {
		t.Errorf("Unique = false, want true for CREATE UNIQUE INDEX")
	}
	if !ix.Expression {
		t.Errorf("Expression = false, want true for UNIQUE (lower(name))")
	}
	// The key is lower(name), not name. Claiming the column would tell every
	// consumer that `name` itself is unique, which it is not.
	if len(ix.Columns) != 0 {
		t.Errorf("Columns = %v, want none: lower(name) is not the column name", ix.Columns)
	}
}

func TestIntrospect_MixedExpressionUniqueIndexDoesNotClaimAPrefix(t *testing.T) {
	requireRealPG(t)
	dir := t.TempDir()
	writeMig(t, dir, "00001_create_materials.up.sql", `
CREATE TABLE suppliers (
    id TEXT PRIMARY KEY
);
CREATE TABLE materials (
    id TEXT PRIMARY KEY,
    supplier_id TEXT NOT NULL REFERENCES suppliers(id),
    sku TEXT NOT NULL
);
CREATE UNIQUE INDEX materials_supplier_sku_key ON materials (supplier_id, lower(sku));
`)

	tables, err := ApplyAndIntrospect(dir)
	if err != nil {
		t.Fatalf("ApplyAndIntrospect: %v", err)
	}
	var mt *Table
	for i := range tables {
		if tables[i].Name == "materials" {
			mt = &tables[i]
		}
	}
	if mt == nil {
		t.Fatalf("materials missing from %+v", tables)
	}

	var ix *Index
	for i := range mt.Indexes {
		if mt.Indexes[i].Name == "materials_supplier_sku_key" {
			ix = &mt.Indexes[i]
		}
	}
	if ix == nil {
		t.Fatalf("materials_supplier_sku_key missing from Indexes = %+v", mt.Indexes)
	}
	if !ix.Expression {
		// This is the regression that mattered: without the flag the index
		// reads as a genuine one-column UNIQUE(supplier_id).
		t.Fatalf("Expression = false for UNIQUE (supplier_id, lower(sku)); "+
			"Columns = %v would then be read as the whole key", ix.Columns)
	}
	if len(ix.Columns) == 1 && ix.Columns[0] == "supplier_id" && !ix.Expression {
		t.Errorf("index reported as plain UNIQUE(supplier_id) — a stricter table than exists")
	}
}
