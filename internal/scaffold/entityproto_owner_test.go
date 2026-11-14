package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

// A `// forge:owner` field is born with its catalog declaration in the same
// migration that creates the column, so the first `forge generate` — the
// one bare `forge scaffold` runs right after the birth — sees the owner and
// births handlers_crud.go with the scoping wrapper.
func TestRenderEntityMigrationFromProto_OwnerMarker(t *testing.T) {
	mig := RenderEntityMigrationFromProto(EntityFromProtoSpec{
		Table:     "agent_runs",
		MessageFQ: entityProtoPkg + ".AgentRun",
		ProtoPkg:  entityProtoPkg,
		Fields: []codegen.SchemaFieldDef{
			{Name: "id", Kind: "string"},
			{Name: "tenant_id", Kind: "string", Owner: true, ReadOnly: true},
			{Name: "name", Kind: "string"},
			{Name: "blob", Kind: "message", TypeName: "google.protobuf.Any", Owner: true}, // TODO-carried: no column
		},
		Timestamps: true,
	})
	if !strings.Contains(mig.UpSQL, "COMMENT ON COLUMN agent_runs.tenant_id IS 'forge:owner';") {
		t.Errorf("the owner field's column must be declared in its birth migration:\n%s", mig.UpSQL)
	}
	if strings.Count(mig.UpSQL, "COMMENT ON COLUMN") != 1 {
		t.Errorf("only the born owner column is declared:\n%s", mig.UpSQL)
	}
	notes := strings.Join(mig.Notes, "\n")
	if !strings.Contains(notes, "field blob: marked forge:owner but born with no column") {
		t.Errorf("an owner marker on a field with no column must be reported; notes:\n%s", notes)
	}

	// A table owned by itself: the marker on the managed id field declares
	// the key column.
	self := RenderEntityMigrationFromProto(EntityFromProtoSpec{
		Table:     "tenants",
		MessageFQ: entityProtoPkg + ".Tenant",
		ProtoPkg:  entityProtoPkg,
		Fields: []codegen.SchemaFieldDef{
			{Name: "id", Kind: "string", Owner: true},
			{Name: "slug", Kind: "string"},
		},
	})
	if !strings.Contains(self.UpSQL, "COMMENT ON COLUMN tenants.id IS 'forge:owner';") {
		t.Errorf("an owner marker on the managed id field must declare the key column:\n%s", self.UpSQL)
	}

	// The applied schema reads the declaration back: this, not the proto,
	// is what the CRUD generator sees.
	if testing.Short() {
		return
	}
	migDir := t.TempDir()
	for i, up := range []string{mig.UpSQL, self.UpSQL} {
		if err := os.WriteFile(filepath.Join(migDir, string(rune('1'+i))+"_born.up.sql"), []byte(up), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tables, err := schemadef.ApplyAndIntrospect(migDir)
	if err != nil {
		t.Skipf("no reachable postgres: %v", err)
	}
	owners := map[string]string{}
	for _, tbl := range tables {
		for _, c := range tbl.Columns {
			if c.HasMarker(schemadef.ColumnMarkerOwner) {
				owners[tbl.Name] = c.Name
			}
		}
	}
	if owners["agent_runs"] != "tenant_id" || owners["tenants"] != "id" {
		t.Errorf("applied owner columns = %v, want agent_runs.tenant_id and tenants.id", owners)
	}
}
