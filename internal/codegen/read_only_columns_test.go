package codegen

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// readOnlyTestEntity is a Shipment whose lifecycle a custom RPC owns: a
// read-only status, a computed total (EntityField.ReadOnly covers both
// markers), and the shapes that must NOT be reported — a read-only PK, a
// read-only wire field with no column, and a column with no wire field.
func readOnlyTestEntity() EntityDef {
	return EntityDef{
		Name:      "Shipment",
		TableName: "shipments",
		PkField:   "id",
		PkGoType:  "string",
		Fields: []EntityField{
			{Name: "id", GoName: "Id", ProtoType: "string", GoType: "string", Kind: FieldKindScalar, ReadOnly: true},
			{Name: "title", GoName: "Title", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
			{Name: "status", GoName: "Status", ProtoType: "string", GoType: "string", Kind: FieldKindScalar, ReadOnly: true},
			{Name: "total", GoName: "Total", ProtoType: "int64", GoType: "int64", Kind: FieldKindScalar, ReadOnly: true},
			{Name: "display", GoName: "Display", ProtoType: "string", GoType: "string", Kind: FieldKindScalar, ReadOnly: true},
		},
		Columns: []EntityColumn{
			{Name: "id", Type: "string", NotNull: true, IsPK: true},
			{Name: "title", Type: "string", NotNull: true},
			{Name: "status", Type: "string", NotNull: true, Default: "'pending'"},
			{Name: "total", Type: "int64", NotNull: true, Default: "0"},
			{Name: "internal_notes", Type: "string", NotNull: true, Default: "''"},
		},
	}
}

func TestReadOnlyColumns(t *testing.T) {
	got := ReadOnlyColumns(readOnlyTestEntity())
	want := []string{"status", "total"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ReadOnlyColumns = %v, want %v (column order; PK, wire-only and column-only excluded)", got, want)
	}

	plain := readOnlyTestEntity()
	for i := range plain.Fields {
		plain.Fields[i].ReadOnly = false
	}
	if got := ReadOnlyColumns(plain); got != nil {
		t.Errorf("an entity with no read-only fields reports %v, want nil", got)
	}
}

func generateReadOnlyOps(t *testing.T, entity EntityDef) string {
	t.Helper()
	projectDir := t.TempDir()
	handlerDir := filepath.Join(projectDir, "internal", "handlers", "patients")
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeMaskTestServiceGo(t, handlerDir)

	svc := ServiceDef{
		Name:       "PatientsService",
		GoPackage:  "example.com/test/gen/proto/services/patients/v1",
		PkgName:    "patientsv1",
		ModulePath: "example.com/test",
		Methods: []Method{
			{Name: "UpdateShipment", InputType: "UpdateShipmentRequest", OutputType: "UpdateShipmentResponse"},
		},
		Messages: map[string][]MessageFieldDef{
			"UpdateShipmentRequest": {
				{Name: "shipment", ProtoType: "message", MessageType: "Shipment"},
				{Name: "update_mask", ProtoType: "message", MessageType: "google.protobuf.FieldMask"},
			},
		},
	}
	if _, err := GenerateCRUDHandlers(svc, MatchCRUDMethods(svc, []EntityDef{entity}), "example.com/test", projectDir, nil); err != nil {
		t.Fatalf("GenerateCRUDHandlers() error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(handlerDir, "handlers_crud_ops_gen.go"))
	if err != nil {
		t.Fatalf("generated ops file not found: %v", err)
	}
	ops := string(data)
	if _, err := parser.ParseFile(token.NewFileSet(), "ops.go", ops, parser.SkipObjectResolution); err != nil {
		t.Fatalf("ops output is not valid Go: %v\n----\n%s", err, ops)
	}
	return ops
}

// The generated Update op carries the read-only columns on BOTH client
// paths: op.ReadOnly (HandleUpdate refuses a mask naming one) and the full
// replace's crud.Preserve (a request cannot reset one by omitting it).
// Without either, the AIP-134 Update — which wraps the whole entity — wrote
// every read-only field back.
func TestGenerateCRUDHandlers_UpdateOpRefusesReadOnlyColumns(t *testing.T) {
	ops := generateReadOnlyOps(t, readOnlyTestEntity())

	if !strings.Contains(ops, `ReadOnly: []string{"status", "total"},`) {
		t.Errorf("update op must declare its read-only columns for HandleUpdate's mask check:\n%s", ops)
	}
	if !strings.Contains(ops, `db.UpdateShipment(ctx, s.deps.DB, entity,`) ||
		!strings.Contains(ops, `crud.Preserve("status", "total"))`) {
		t.Errorf("update op's full replace must preserve the read-only columns:\n%s", ops)
	}
	// The masked write still goes straight to the repository: refusing the
	// client's paths is HandleUpdate's job, and the repository's masked path
	// is how the owning RPC writes these columns.
	if !strings.Contains(ops, "db.UpdateShipmentMasked(ctx, s.deps.DB, entity, fields)") {
		t.Errorf("PersistMasked must still delegate to db.UpdateShipmentMasked unchanged:\n%s", ops)
	}
}

// An entity with no read-only fields generates exactly the op it always
// did: no ReadOnly declaration and a plain full replace.
func TestGenerateCRUDHandlers_UpdateOpWithoutReadOnlyColumnsUnchanged(t *testing.T) {
	entity := readOnlyTestEntity()
	for i := range entity.Fields {
		entity.Fields[i].ReadOnly = false
	}
	ops := generateReadOnlyOps(t, entity)
	for _, frag := range []string{"ReadOnly:", "crud.Preserve"} {
		if strings.Contains(ops, frag) {
			t.Errorf("no read-only fields: ops file must not emit %s:\n%s", frag, ops)
		}
	}
	if !strings.Contains(ops, "return db.UpdateShipment(ctx, s.deps.DB, entity)") {
		t.Errorf("full replace should stay the plain delegate:\n%s", ops)
	}
}
