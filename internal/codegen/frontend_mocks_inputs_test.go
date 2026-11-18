package codegen

import (
	"reflect"
	"testing"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

// forge#2287: a fixture's inputs are every schema name its rows depend on —
// its table, the tables it references and the tables that reference it
// (both transitively: a grandchild migration stales its ancestors), and the column types the type mapping does not know — and
// nothing else, so a migration on an unrelated table does not stale it.
func TestFixtureInputNames(t *testing.T) {
	tables := []schemadef.Table{
		{Name: "orgs", Columns: []schemadef.Column{{Name: "id", DeclType: "TEXT", TypeKnown: true}}},
		{Name: "patients", Columns: []schemadef.Column{
			{Name: "id", DeclType: "TEXT", TypeKnown: true},
			{Name: "status", DeclType: `public."patient_status"`},
			{Name: "tags", DeclType: "label[]"},
		}, ForeignKeys: []schemadef.ForeignKey{{Column: "org_id", RefTable: "orgs"}}},
		{Name: "visits", ForeignKeys: []schemadef.ForeignKey{{Column: "patient_id", RefTable: "patients"}}},
		{Name: "visit_notes", ForeignKeys: []schemadef.ForeignKey{{Column: "visit_id", RefTable: "visits"}}},
		{Name: "audit_log"},
	}
	got := fixtureInputNames(tables)
	for table, want := range map[string][]string{
		"patients":    {"label", "orgs", "patient_status", "patients", "visit_notes", "visits"},
		"visits":      {"orgs", "patients", "visit_notes", "visits"},
		"visit_notes": {"orgs", "patients", "visit_notes", "visits"},
		"orgs":        {"orgs", "patients", "visit_notes", "visits"},
		"audit_log":   {"audit_log"},
	} {
		if !reflect.DeepEqual(got[table], want) {
			t.Errorf("%s inputs = %v, want %v", table, got[table], want)
		}
	}

	var none *SeedProjection
	if _, ok := none.FixtureInputs("patients"); ok {
		t.Error("a nil projection has no dataset, so no fixture inputs")
	}
}
