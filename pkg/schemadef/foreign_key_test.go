package schemadef

import (
	"slices"
	"testing"
)

// TestApplyAndIntrospect_CompositeForeignKeyPairsItsColumns pins how a
// composite reference is reported: one entry per column, each paired with the
// referenced column at the SAME position and carrying the whole key. It used
// to come back as the cross product of its columns — strategy_id →
// strategies.tenant_id among them — which is how the seeder came to write a
// tenant's id into strategy_id.
func TestApplyAndIntrospect_CompositeForeignKeyPairsItsColumns(t *testing.T) {
	requireRealPG(t)
	dir := t.TempDir()
	writeMig(t, dir, "00001_init.up.sql", `
CREATE TABLE tenants (id TEXT PRIMARY KEY);
CREATE SCHEMA archive;
CREATE TABLE archive.strategies (id TEXT PRIMARY KEY);
CREATE TABLE strategies (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL REFERENCES tenants(id),
    UNIQUE (tenant_id, id)
);
CREATE TABLE hypotheses (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL REFERENCES tenants(id),
    strategy_id TEXT NOT NULL REFERENCES strategies(id) ON DELETE CASCADE,
    archived_strategy_id TEXT REFERENCES archive.strategies(id),
    CONSTRAINT hypotheses_same_tenant_fkey
        FOREIGN KEY (tenant_id, strategy_id) REFERENCES strategies (tenant_id, id)
);
COMMENT ON CONSTRAINT hypotheses_strategy_id_fkey ON hypotheses IS 'forge:ref independent';
`)
	tables, err := ApplyAndIntrospect(dir)
	if err != nil {
		t.Fatalf("ApplyAndIntrospect: %v", err)
	}
	var fks []ForeignKey
	for _, tb := range tables {
		if tb.Name == "hypotheses" {
			fks = tb.ForeignKeys
		}
	}

	type ref struct{ col, refTable, refCol, name string }
	var got []ref
	for _, fk := range fks {
		got = append(got, ref{fk.Column, fk.RefTable, fk.RefColumn, fk.Name})
	}
	// Ordered by each constraint's first column, then constraint name, then
	// key position: a composite's columns stay together. The reference into
	// archive.strategies is absent: RefTable is a bare name, and reporting it
	// would make it this schema's strategies.
	want := []ref{
		{"tenant_id", "strategies", "tenant_id", "hypotheses_same_tenant_fkey"},
		{"strategy_id", "strategies", "id", "hypotheses_same_tenant_fkey"},
		{"tenant_id", "tenants", "id", "hypotheses_tenant_id_fkey"},
		{"strategy_id", "strategies", "id", "hypotheses_strategy_id_fkey"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("foreign keys =\n  %v\nwant\n  %v", got, want)
	}
	for _, fk := range fks {
		switch fk.Name {
		case "hypotheses_same_tenant_fkey":
			if !slices.Equal(fk.KeyColumns, []string{"tenant_id", "strategy_id"}) || !slices.Equal(fk.RefKeyColumns, []string{"tenant_id", "id"}) {
				t.Errorf("composite member %s: key %v -> %v, want [tenant_id strategy_id] -> [tenant_id id]", fk.Column, fk.KeyColumns, fk.RefKeyColumns)
			}
		default:
			if fk.KeyColumns != nil || fk.RefKeyColumns != nil {
				t.Errorf("single-column %s carries a composite key %v", fk.Name, fk.KeyColumns)
			}
		}
		// The name and comment belong to the constraint, not the column: the
		// composite's strategy_id must not inherit the single reference's.
		wantComment := ""
		if fk.Name == "hypotheses_strategy_id_fkey" {
			wantComment = "forge:ref independent"
		}
		if fk.Comment != wantComment {
			t.Errorf("%s (%s): comment %q, want %q", fk.Name, fk.Column, fk.Comment, wantComment)
		}
		wantDelete := "NO ACTION"
		if fk.Name == "hypotheses_strategy_id_fkey" {
			wantDelete = "CASCADE"
		}
		if fk.OnDelete != wantDelete {
			t.Errorf("%s: OnDelete %q, want %q", fk.Name, fk.OnDelete, wantDelete)
		}
	}
}
