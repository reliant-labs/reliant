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
	for _, m := range []EntityFromProtoMigration{mig, self} {
		if strings.Contains(m.UpSQL, "Ownership: not declared") || strings.Contains(strings.Join(m.Notes, "\n"), "whose rows it holds is not declared") {
			t.Errorf("a birth that declared its owner must not ask for one:\n%s\nnotes: %v", m.UpSQL, m.Notes)
		}
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

// A birth with no owner field declares NOTHING about whose rows the table
// holds: no owner, and no forge:public / forge:unscoped exemption either.
// An exemption forge wrote would answer the crud-table-unowned lint on the
// author's behalf, so the gate would never ask them. The birth asks instead:
// a note at scaffold time and a SQL comment beside the table, neither of
// which spells a marker.
func TestRenderEntityMigrationFromProto_OwnerlessBirthDeclaresNoExemption(t *testing.T) {
	plain := hardeningSpec()
	plain.AppendOnly = false
	for name, spec := range map[string]EntityFromProtoSpec{"plain": plain, "append-only": hardeningSpec()} {
		t.Run(name, func(t *testing.T) {
			mig := RenderEntityMigrationFromProto(spec)
			for _, marker := range []string{schemadef.TableMarkerUnscoped, schemadef.TableMarkerPublic, schemadef.TableMarkerOwner} {
				if strings.Contains(mig.UpSQL, marker) {
					t.Errorf("an ownerless birth must not declare %s for its author:\n%s", marker, mig.UpSQL)
				}
			}
			if !strings.Contains(mig.UpSQL, "-- Ownership: not declared.") {
				t.Errorf("the birth must say, beside the table, that ownership is undeclared:\n%s", mig.UpSQL)
			}
			notes := strings.Join(mig.Notes, "\n")
			for _, want := range []string{
				"table audit_logs: whose rows it holds is not declared",
				"COMMENT ON TABLE audit_logs IS 'forge:owner=<column>'",
				"'forge:unscoped: <why>'",
				"'forge:public: <why>'",
				"forge lint --crud-ownership",
			} {
				if !strings.Contains(notes, want) {
					t.Errorf("the scaffold note must name %q; notes:\n%s", want, notes)
				}
			}
			if strings.Contains(notes, "TODO") {
				t.Errorf("the ownership note is a decision, not a TODO field (it would count as one):\n%s", notes)
			}
		})
	}
}
