package codegen

import (
	"testing"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

// TestBuildSchemaEntities_RequiresWireMessage pins the half of the entity
// join that used to be assumed rather than checked.
//
// An entity exists when BOTH halves exist: a table in the applied schema AND
// a wire message the CRUD projection can name. The table half was verified;
// the wire half was inferred from the RPC NAME alone. So a service with
// `ListSecrets` + `SetSecret` + `DeleteSecret`, whose actual messages are
// `SecretSummary` and `SecretVersion` and which declares no `Secret` at all,
// produced an entity called Secret the moment an unrelated migration
// happened to create a `secrets` table.
//
// What that cost: forge scaffolded a CRUD page and mock fixtures importing
// `Secret` and `SecretSchema` from a generated `_pb` module that exports
// neither, and the frontend stopped compiling. The scaffold is "yours"-owned,
// so forge never took it back when the table was dropped again.
func TestBuildSchemaEntities_RequiresWireMessage(t *testing.T) {
	const pkg = "controlplane.v1"
	const protoFile = "services/secret_store/v1/secret_store.proto"

	table := schemadef.Table{
		Name:   "secrets",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			{Name: "id", Type: schemadef.CanonicalType("string"), IsPK: true, NotNull: true},
			{Name: "name", Type: schemadef.CanonicalType("string")},
		},
	}

	// The real shape of control-plane's SecretStoreService: CRUD-prefixed
	// RPC names, but no `Secret` message anywhere in the descriptor.
	svc := ServiceDef{
		Name:      "SecretStoreService",
		Package:   pkg,
		ProtoFile: protoFile,
		Methods: []Method{
			{Name: "ListSecrets"},
			{Name: "DeleteSecret"},
		},
		SchemaFiles: map[string]string{
			pkg + ".SecretSummary":        protoFile,
			pkg + ".SecretVersion":        protoFile,
			pkg + ".ListSecretsRequest":   protoFile,
			pkg + ".ListSecretsResponse":  protoFile,
			pkg + ".DeleteSecretRequest":  protoFile,
			pkg + ".DeleteSecretResponse": protoFile,
		},
	}

	got := entitiesForTables([]schemadef.Table{table}, []ServiceDef{svc})
	for _, e := range got {
		if e.Name == "Secret" {
			t.Fatalf("built entity %q from a service that declares no %s.Secret message — "+
				"the CRUD projection would import a symbol the generated _pb module does not export",
				e.Name, pkg)
		}
	}
}

// TestBuildSchemaEntities_KeepsEntityWithWireMessage is the other side of the
// same gate: the ordinary case must keep working. A service whose descriptor
// DOES declare the entity message still projects an entity.
func TestBuildSchemaEntities_KeepsEntityWithWireMessage(t *testing.T) {
	const pkg = "controlplane.v1"
	const protoFile = "services/daemon/v1/daemon.proto"

	table := schemadef.Table{
		Name:   "daemons",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			{Name: "id", Type: schemadef.CanonicalType("string"), IsPK: true, NotNull: true},
			{Name: "name", Type: schemadef.CanonicalType("string")},
		},
	}

	svc := ServiceDef{
		Name:      "DaemonService",
		Package:   pkg,
		ProtoFile: protoFile,
		Methods: []Method{
			{Name: "ListDaemons"},
			{Name: "GetDaemon"},
		},
		SchemaFiles: map[string]string{
			pkg + ".Daemon":              protoFile,
			pkg + ".ListDaemonsRequest":  protoFile,
			pkg + ".ListDaemonsResponse": protoFile,
		},
	}

	got := entitiesForTables([]schemadef.Table{table}, []ServiceDef{svc})
	var found bool
	for _, e := range got {
		if e.Name == "Daemon" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Daemon entity was dropped despite %s.Daemon being declared; got %+v", pkg, got)
	}
}

// TestBuildSchemaEntities_OlderDescriptorWithoutSchemaFiles guards the
// compatibility edge. A descriptor generated before SchemaFiles existed
// carries no message inventory at all, so the gate has nothing to check
// against and must not silently drop every entity in the project.
func TestBuildSchemaEntities_OlderDescriptorWithoutSchemaFiles(t *testing.T) {
	table := schemadef.Table{
		Name:   "widgets",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			{Name: "id", Type: schemadef.CanonicalType("string"), IsPK: true, NotNull: true},
		},
	}

	svc := ServiceDef{
		Name:      "WidgetService",
		Package:   "shop.v1",
		ProtoFile: "services/widget/v1/widget.proto",
		Methods:   []Method{{Name: "ListWidgets"}},
		// No SchemaFiles and no Schemas — a pre-SchemaFiles descriptor.
	}

	got := entitiesForTables([]schemadef.Table{table}, []ServiceDef{svc})
	if len(got) != 1 || got[0].Name != "Widget" {
		t.Fatalf("pre-SchemaFiles descriptor should still project entities, got %+v", got)
	}
}
