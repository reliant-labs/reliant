package codegen

import (
	"os"
	"path/filepath"
	"testing"
)

// crud_fill_test.go pins the generate-time unsatisfiable-column gate and
// the forge:fill=handler shim scaffold end to end through
// GenerateCRUDHandlers — the same chokepoint TestGenerateCRUDHandlers_WithFilters
// exercises for list filters.

func fillTestHandlerDir(t *testing.T) (projectDir, handlerDir string) {
	t.Helper()
	projectDir = t.TempDir()
	handlerDir = filepath.Join(projectDir, "internal", "handlers", "customers")
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	serviceGo := `package customers

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
	return projectDir, handlerDir
}

func fillTestServiceAndEntity(col EntityColumn) (ServiceDef, []EntityDef) {
	svc := ServiceDef{
		Name:       "CustomersService",
		Package:    "customers.v1",
		GoPackage:  "example.com/test/gen/proto/services/customers/v1",
		PkgName:    "customersv1",
		ModulePath: "example.com/test",
		Methods: []Method{
			{Name: "CreateCustomer", InputType: "CreateCustomerRequest", OutputType: "CreateCustomerResponse"},
			{Name: "GetCustomer", InputType: "GetCustomerRequest", OutputType: "GetCustomerResponse"},
		},
		Schemas: map[string][]SchemaFieldDef{
			// company_id carries NO ReadOnly here — it is deliberately
			// absent from the create request wire fields (requestWireFields
			// resolves from Schemas["...CreateCustomerRequest"], which never
			// lists it), matching how forge:read-only actually omits a
			// field from the born Create request message.
			"customers.v1.CreateCustomerRequest": {
				{Name: "name", Kind: "string"},
			},
		},
	}
	entities := []EntityDef{{
		Name: "Customer", TableName: "customers", PkField: "id", PkGoType: "string",
		Fields: []EntityField{
			{Name: "id", GoName: "Id", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
			{Name: "name", GoName: "Name", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
			{Name: col.Name, GoName: "CompanyId", ProtoType: "string", GoType: "string", Kind: FieldKindScalar, ReadOnly: true},
		},
		Columns: []EntityColumn{
			{Name: "id", Type: "string", NotNull: true, IsPK: true},
			{Name: "name", Type: "string", NotNull: true},
			col,
		},
	}}
	return svc, entities
}

// TestGenerateCRUDHandlers_FailsOnUnsatisfiableColumn is the generate-time
// half of the derived lint: an entity carrying a NOT NULL, no-DEFAULT,
// forge:read-only column with no forge:fill declaration must fail the
// generate rather than ship a Create that always violates a constraint.
func TestGenerateCRUDHandlers_FailsOnUnsatisfiableColumn(t *testing.T) {
	projectDir, _ := fillTestHandlerDir(t)
	svc, entities := fillTestServiceAndEntity(EntityColumn{
		Name: "company_id", Type: "string", NotNull: true, DeclType: "TEXT",
	})
	crudMethods := MatchCRUDMethods(svc, entities)

	_, err := GenerateCRUDHandlers(svc, crudMethods, "example.com/test", projectDir, nil)
	if err == nil {
		t.Fatal("expected GenerateCRUDHandlers to fail on an unsatisfiable column, got nil error")
	}
	if !contains(err.Error(), "company_id") {
		t.Errorf("error must name the unsatisfiable column; got: %v", err)
	}
	if !contains(err.Error(), "forge:fill") {
		t.Errorf("error must point at the forge:fill remediation; got: %v", err)
	}
}

// TestGenerateCRUDHandlers_FillULIDSuppressesTheGate proves the
// suppression is read from the real schema fact, not a broken check: the
// SAME shape as the failing test above, differing only in FillStrategy.
func TestGenerateCRUDHandlers_FillULIDSuppressesTheGate(t *testing.T) {
	projectDir, handlerDir := fillTestHandlerDir(t)
	svc, entities := fillTestServiceAndEntity(EntityColumn{
		Name: "company_id", Type: "string", NotNull: true, DeclType: "TEXT",
		FillStrategy: "ulid",
	})
	crudMethods := MatchCRUDMethods(svc, entities)

	if _, err := GenerateCRUDHandlers(svc, crudMethods, "example.com/test", projectDir, nil); err != nil {
		t.Fatalf("forge:fill=ulid must suppress the unsatisfiable-column gate: %v", err)
	}
	// fill=ulid is forge's own job (pkg/crud.Repo.fillULIDColumns) — it must
	// NOT scaffold a handler acknowledgement, that would be a false nag.
	shimPath := filepath.Join(handlerDir, "handlers_crud.go")
	data, err := os.ReadFile(shimPath)
	if err != nil {
		t.Fatalf("read shim: %v", err)
	}
	if contains(string(data), "FORGE_SCAFFOLD") {
		t.Errorf("forge:fill=ulid must not scaffold a FORGE_SCAFFOLD reminder (forge fills it itself):\n%s", data)
	}
}

// TestGenerateCRUDHandlers_FillHandlerScaffoldsOpEntityWrapper is the
// forge:fill=handler half: the gate is suppressed AND the create shim
// scaffolds an op.Entity wrapper carrying a FORGE_SCAFFOLD reminder naming
// the column, so `forge lint --scaffolds` fails the build until the author
// fills it in.
func TestGenerateCRUDHandlers_FillHandlerScaffoldsOpEntityWrapper(t *testing.T) {
	projectDir, handlerDir := fillTestHandlerDir(t)
	svc, entities := fillTestServiceAndEntity(EntityColumn{
		Name: "company_id", Type: "string", NotNull: true, DeclType: "TEXT",
		FillStrategy: "handler",
	})
	crudMethods := MatchCRUDMethods(svc, entities)

	if _, err := GenerateCRUDHandlers(svc, crudMethods, "example.com/test", projectDir, nil); err != nil {
		t.Fatalf("forge:fill=handler must suppress the unsatisfiable-column gate: %v", err)
	}

	shimPath := filepath.Join(handlerDir, "handlers_crud.go")
	data, err := os.ReadFile(shimPath)
	if err != nil {
		t.Fatalf("read shim: %v", err)
	}
	content := string(data)
	if !contains(content, "FORGE_SCAFFOLD") {
		t.Errorf("forge:fill=handler must scaffold a FORGE_SCAFFOLD reminder:\n%s", content)
	}
	if !contains(content, "company_id") {
		t.Errorf("the scaffold must name the column:\n%s", content)
	}
	if !contains(content, "op := s.crudCreateCustomerOp()") || !contains(content, "op.Entity = func(") {
		t.Errorf("the scaffold must wrap op.Entity around the generated build closure:\n%s", content)
	}
}
