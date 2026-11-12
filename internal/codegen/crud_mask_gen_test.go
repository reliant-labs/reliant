// Tests for the AIP-134 update_mask wiring in the generated CRUD split:
// a google.protobuf.FieldMask field on the update request makes the ops
// file wire crud.UpdateOp.Mask + PersistMasked (→ db.Update<Entity>Masked)
// and makes the scaffolded lifecycle test exercise masked AND unmasked
// updates. Requests without a mask keep the legacy full-replace shape
// byte-for-byte.
package codegen

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// maskTestService builds the canonical scaffold shape: an update request
// wrapping the entity plus an update_mask FieldMask (withMask=true), or
// the legacy entity-only request (withMask=false).
func maskTestService(withMask bool) ServiceDef {
	updateFields := []MessageFieldDef{
		{Name: "patient", ProtoType: "message", MessageType: "Patient"},
	}
	if withMask {
		updateFields = append(updateFields, MessageFieldDef{
			Name: "update_mask", ProtoType: "message", MessageType: "google.protobuf.FieldMask",
		})
	}
	return ServiceDef{
		Name:       "PatientsService",
		GoPackage:  "example.com/test/gen/proto/services/patients/v1",
		PkgName:    "patientsv1",
		ModulePath: "example.com/test",
		Methods: []Method{
			{Name: "CreatePatient", InputType: "CreatePatientRequest", OutputType: "CreatePatientResponse"},
			{Name: "GetPatient", InputType: "GetPatientRequest", OutputType: "GetPatientResponse"},
			{Name: "UpdatePatient", InputType: "UpdatePatientRequest", OutputType: "UpdatePatientResponse"},
		},
		Messages: map[string][]MessageFieldDef{
			"UpdatePatientRequest": updateFields,
		},
	}
}

func maskTestEntities() []EntityDef {
	return []EntityDef{
		{
			Name:      "Patient",
			TableName: "patients",
			PkField:   "id",
			PkGoType:  "string",
			Fields: []EntityField{
				{Name: "id", GoName: "Id", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
				{Name: "name", GoName: "Name", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
				{Name: "email", GoName: "Email", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
			},
		},
	}
}

func writeMaskTestServiceGo(t *testing.T, handlerDir string) {
	t.Helper()
	serviceGo := `package patients

import "github.com/reliant-labs/forge/pkg/orm"

type Deps struct {
	DB orm.Context
}

type Service struct {
	deps Deps
}
`
	if err := os.WriteFile(filepath.Join(handlerDir, "service.go"), []byte(serviceGo), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGenerateCRUDHandlers_UpdateMaskWired(t *testing.T) {
	projectDir := t.TempDir()
	handlerDir := filepath.Join(projectDir, "internal", "handlers", "patients")
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeMaskTestServiceGo(t, handlerDir)

	svc := maskTestService(true)
	entities := maskTestEntities()
	if _, err := GenerateCRUDHandlers(svc, MatchCRUDMethods(svc, entities), "example.com/test", projectDir, nil); err != nil {
		t.Fatalf("GenerateCRUDHandlers() error = %v", err)
	}

	opsData, err := os.ReadFile(filepath.Join(handlerDir, "handlers_crud_ops_gen.go"))
	if err != nil {
		t.Fatalf("generated ops file not found: %v", err)
	}
	ops := string(opsData)

	// The UpdateOp wires BOTH mask hooks — Mask without PersistMasked is
	// the CodeInternal wiring bug pkg/crud guards against.
	if !strings.Contains(ops, "Mask: func(req *pb.UpdatePatientRequest) []string { return req.GetUpdateMask().GetPaths() },") {
		t.Error("ops file should wire UpdateOp.Mask from req.GetUpdateMask().GetPaths()")
	}
	// The trailing opts are the scoping seam: an override attaches an
	// ownership predicate there, and a masked write must honor it or the
	// mask becomes a way around the scope.
	if !strings.Contains(ops, "PersistMasked: func(ctx context.Context, entity *db.Patient, fields []string, opts ...orm.QueryOption) error {") {
		t.Error("ops file should wire UpdateOp.PersistMasked with the variadic scoping options")
	}
	if !strings.Contains(ops, "db.UpdatePatientMasked(ctx, s.deps.DB, entity, fields)") {
		t.Error("PersistMasked should delegate to db.UpdatePatientMasked")
	}

	// The user-owned shim documents that the mask is honored, not the old
	// whole-row-write disclaimer.
	shimData, err := os.ReadFile(filepath.Join(handlerDir, "handlers_crud.go"))
	if err != nil {
		t.Fatalf("scaffolded handlers_crud.go not found: %v", err)
	}
	shim := string(shimData)
	if !strings.Contains(shim, "req.Msg.UpdateMask is honored") {
		t.Error("update shim doc should state the mask is honored")
	}
	if strings.Contains(shim, "not yet applied") {
		t.Error("update shim doc still carries the stale 'not yet applied' disclaimer")
	}

	if _, err := parser.ParseFile(token.NewFileSet(), "ops.go", ops, parser.SkipObjectResolution); err != nil {
		t.Errorf("ops output is not valid Go: %v\n----\n%s", err, ops)
	}
}

func TestGenerateCRUDHandlers_NoMaskField_LegacyFullReplace(t *testing.T) {
	projectDir := t.TempDir()
	handlerDir := filepath.Join(projectDir, "internal", "handlers", "patients")
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeMaskTestServiceGo(t, handlerDir)

	svc := maskTestService(false)
	entities := maskTestEntities()
	if _, err := GenerateCRUDHandlers(svc, MatchCRUDMethods(svc, entities), "example.com/test", projectDir, nil); err != nil {
		t.Fatalf("GenerateCRUDHandlers() error = %v", err)
	}

	opsData, err := os.ReadFile(filepath.Join(handlerDir, "handlers_crud_ops_gen.go"))
	if err != nil {
		t.Fatalf("generated ops file not found: %v", err)
	}
	ops := string(opsData)
	for _, frag := range []string{"Mask:", "PersistMasked:", "UpdatePatientMasked"} {
		if strings.Contains(ops, frag) {
			t.Errorf("no update_mask on the request: ops file must not emit %s", frag)
		}
	}

	// The shim keeps the whole-row disclaimer and points at AIP-134.
	shimData, err := os.ReadFile(filepath.Join(handlerDir, "handlers_crud.go"))
	if err != nil {
		t.Fatalf("scaffolded handlers_crud.go not found: %v", err)
	}
	if !strings.Contains(string(shimData), "whole-row write") {
		t.Error("maskless update shim should keep the whole-row-write disclaimer")
	}
}

func TestGenerateCRUDTests_MaskedAndUnmaskedExercised(t *testing.T) {
	projectDir := t.TempDir()
	handlerDir := filepath.Join(projectDir, "internal", "handlers", "patients")
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(handlerDir, "service.go"), []byte("package patients\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	svc := maskTestService(true)
	entities := maskTestEntities()
	if _, err := GenerateCRUDTests(svc, MatchCRUDMethods(svc, entities), "example.com/test", projectDir, nil); err != nil {
		t.Fatalf("GenerateCRUDTests() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(handlerDir, "handlers_crud_test.go"))
	if err != nil {
		t.Fatalf("scaffolded handlers_crud_test.go not found: %v", err)
	}
	content := string(data)

	// Unmasked full replace stays exercised…
	if !strings.Contains(content, "lifecycle-updated") {
		t.Error("lifecycle test should keep the unmasked (full replace) update exercise")
	}
	// …and the masked path is exercised alongside it.
	if !strings.Contains(content, `"google.golang.org/protobuf/types/known/fieldmaskpb"`) {
		t.Error("lifecycle test should import fieldmaskpb for the masked update")
	}
	if !strings.Contains(content, `fieldmaskpb.FieldMask{Paths: []string{"name"}}`) {
		t.Error("masked update should send a mask naming only the mutable field (name)")
	}
	// The non-clobber proof: the second string field (email) carries a
	// value the mask does not name, and is asserted unchanged.
	if !strings.Contains(content, "MUST-NOT-PERSIST") {
		t.Error("masked update should load an unmasked field with a clobber value")
	}
	if !strings.Contains(content, "keptUnmaskedValue") {
		t.Error("masked update should assert the unmasked field (email) survived via keptUnmaskedValue")
	}
	// Unknown mask path → clean InvalidArgument.
	if !strings.Contains(content, "this_field_does_not_exist") ||
		!strings.Contains(content, "connect.CodeInvalidArgument") {
		t.Error("masked update should assert unknown paths map to InvalidArgument")
	}

	if _, err := parser.ParseFile(token.NewFileSet(), "lifecycle.go", content, parser.SkipObjectResolution); err != nil {
		t.Errorf("lifecycle output is not valid Go: %v\n----\n%s", err, content)
	}
}

func TestGenerateCRUDTests_NoMask_NoFieldMaskImport(t *testing.T) {
	projectDir := t.TempDir()
	handlerDir := filepath.Join(projectDir, "internal", "handlers", "patients")
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(handlerDir, "service.go"), []byte("package patients\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	svc := maskTestService(false)
	entities := maskTestEntities()
	if _, err := GenerateCRUDTests(svc, MatchCRUDMethods(svc, entities), "example.com/test", projectDir, nil); err != nil {
		t.Fatalf("GenerateCRUDTests() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(handlerDir, "handlers_crud_test.go"))
	if err != nil {
		t.Fatalf("scaffolded handlers_crud_test.go not found: %v", err)
	}
	content := string(data)
	for _, frag := range []string{"fieldmaskpb", "masked-updated", "MUST-NOT-PERSIST"} {
		if strings.Contains(content, frag) {
			t.Errorf("maskless update request: lifecycle test must not emit %s (unused import / dead block)", frag)
		}
	}
	if !strings.Contains(content, "lifecycle-updated") {
		t.Error("maskless lifecycle test should still exercise the full-replace update")
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "lifecycle.go", content, parser.SkipObjectResolution); err != nil {
		t.Errorf("lifecycle output is not valid Go: %v\n----\n%s", err, content)
	}
}
