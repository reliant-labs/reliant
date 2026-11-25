package seedplan

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

// singletonSchema is the singleton-table idiom — a table whose sole key a
// CHECK pins to one value — plus a child that references it:
//
//	CREATE TABLE legacy_credential_copy (id int PRIMARY KEY CHECK (id = 1), …);
//	CREATE TABLE copy_notes (id uuid PRIMARY KEY, copy_id int NOT NULL REFERENCES legacy_credential_copy(id));
func singletonSchema(keyType schemadef.CanonicalType, checkDef string) []schemadef.Table {
	single := schemadef.Table{
		Name:   "legacy_credential_copy",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			col("id", keyType, true, true),
			col("payload", schemadef.TypeString, true, false),
		},
		Checks: []schemadef.CheckConstraint{
			{Name: "legacy_credential_copy_id_check", Def: checkDef, Columns: []string{"id"}},
		},
	}
	child := schemadef.Table{
		Name:   "copy_notes",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			col("id", schemadef.TypeString, true, true),
			col("copy_id", keyType, true, false),
		},
		ForeignKeys: []schemadef.ForeignKey{
			{Column: "copy_id", RefTable: "legacy_credential_copy", RefColumn: "id"},
		},
	}
	return []schemadef.Table{single, child}
}

// A table whose sole primary key a CHECK pins to one value can hold one row.
// Planning the configured 20 wrote id = 2 on row two and the CHECK aborted the
// whole seed; the plan must cap the table at 1, seed the pinned value, point
// every child at that row, and say so.
func TestPlan_CheckPinnedSingletonSeedsOneRow(t *testing.T) {
	tests := []struct {
		name     string
		keyType  schemadef.CanonicalType
		check    string
		wantKey  string
		wantLit  string
		wantNote string
	}{
		{"integer pin", schemadef.TypeInt, `CHECK ((id = 1))`, "1", "1", "single value 1"},
		{"integer pin off row one", schemadef.TypeInt, `CHECK ((id = 7))`, "7", "7", "single value 7"},
		{"text pin", schemadef.TypeString, `CHECK ((id = 'singleton'::text))`, "singleton", "'singleton'", "single value singleton"},
		{"bare boolean pin", schemadef.TypeBool, `CHECK (id)`, "true", "true", "single value true"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tables := singletonSchema(tc.keyType, tc.check)
			plan, err := BuildPlan(tables, PoolsFromTables(tables), Config{Rows: 20, Salt: 3})
			if err != nil {
				t.Fatalf("BuildPlan: %v", err)
			}
			if n := plan.rowsOf["legacy_credential_copy"]; n != 1 {
				t.Fatalf("legacy_credential_copy plans %d rows, want 1: its key admits one value", n)
			}
			if n := plan.rowsOf["copy_notes"]; n != 20 {
				t.Errorf("copy_notes plans %d rows, want 20: a child of a singleton is not capped", n)
			}
			if v, ok := plan.SeedValue("legacy_credential_copy", "id", 0); !ok || v != tc.wantKey {
				t.Errorf("singleton key = %q (ok=%v), want the pinned %q", v, ok, tc.wantKey)
			}
			for i := range 20 {
				if v, ok := plan.SeedValue("copy_notes", "copy_id", i); !ok || v != tc.wantKey {
					t.Fatalf("copy_notes row %d: copy_id = %q (ok=%v), want the singleton's %q", i, v, ok, tc.wantKey)
				}
			}
			sql := plan.Render()
			if !strings.Contains(sql, "("+tc.wantLit+",") {
				t.Errorf("rendered INSERT does not carry the pinned key literal %s:\n%s", tc.wantLit, sql)
			}
			found := false
			for _, w := range plan.Warnings() {
				if strings.Contains(w, "legacy_credential_copy capped to 1 row(s)") && strings.Contains(w, tc.wantNote) {
					found = true
				}
			}
			if !found {
				t.Errorf("no warning reports the singleton cap (want one naming %q); got %q", tc.wantNote, plan.Warnings())
			}
		})
	}
}

// A sole key whose CHECK admits several values caps the table at that many,
// each row taking a distinct member — never a UUID the CHECK rejects.
func TestPlan_CheckVocabularyKeyCapsAtVocabularySize(t *testing.T) {
	tables := singletonSchema(schemadef.TypeString, `CHECK ((id = ANY (ARRAY['a'::text, 'b'::text, 'c'::text])))`)
	plan, err := BuildPlan(tables, PoolsFromTables(tables), Config{Rows: 20, Salt: 3})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if n := plan.rowsOf["legacy_credential_copy"]; n != 3 {
		t.Fatalf("planned %d rows, want 3 (one per admitted key)", n)
	}
	seen := map[string]bool{}
	for i := range 3 {
		v, _ := plan.SeedValue("legacy_credential_copy", "id", i)
		if v != "a" && v != "b" && v != "c" {
			t.Fatalf("row %d key %q is outside the CHECK vocabulary", i, v)
		}
		if seen[v] {
			t.Fatalf("row %d repeats key %q", i, v)
		}
		seen[v] = true
	}
}

// A plan that already asks for no more rows than the key admits is untouched
// and silent: the cap is not a warning about the minimal plan.
func TestPlan_CheckPinnedSingletonWithinTargetIsSilent(t *testing.T) {
	tables := singletonSchema(schemadef.TypeInt, `CHECK ((id = 1))`)
	plan, err := BuildPlan(tables, PoolsFromTables(tables), Config{Rows: 1, Salt: 3})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	for _, w := range plan.Warnings() {
		if strings.Contains(w, "capped") {
			t.Errorf("unexpected cap warning for a one-row plan: %s", w)
		}
	}
	if v, _ := plan.SeedValue("legacy_credential_copy", "id", 0); v != "1" {
		t.Errorf("key = %q, want 1", v)
	}
}
