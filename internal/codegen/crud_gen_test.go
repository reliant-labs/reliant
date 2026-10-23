package codegen

import (
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
)

func TestMatchCRUDMethods_BasicMatching(t *testing.T) {
	entities := []EntityDef{
		{
			Name:      "Patient",
			TableName: "patients",
			PkField:   "id",
			PkGoType:  "int64",
			Fields: []EntityField{
				{Name: "id", GoName: "ID", GoType: "int64", Kind: FieldKindScalar},
				{Name: "name", GoName: "Name", GoType: "string", Kind: FieldKindScalar},
			},
		},
	}

	svc := ServiceDef{
		Name: "PatientsService",
		Methods: []Method{
			{Name: "CreatePatient", InputType: "CreatePatientRequest", OutputType: "CreatePatientResponse"},
			{Name: "GetPatient", InputType: "GetPatientRequest", OutputType: "GetPatientResponse"},
			{Name: "ListPatients", InputType: "ListPatientsRequest", OutputType: "ListPatientsResponse"},
			{Name: "UpdatePatient", InputType: "UpdatePatientRequest", OutputType: "UpdatePatientResponse"},
			{Name: "DeletePatient", InputType: "DeletePatientRequest", OutputType: "DeletePatientResponse"},
		},
	}

	matches := MatchCRUDMethods(svc, entities)

	if len(matches) != 5 {
		t.Fatalf("expected 5 matches, got %d", len(matches))
	}

	expected := []struct {
		name string
		op   string
	}{
		{"CreatePatient", "create"},
		{"GetPatient", "get"},
		{"ListPatients", "list"},
		{"UpdatePatient", "update"},
		{"DeletePatient", "delete"},
	}

	for i, exp := range expected {
		if matches[i].Method.Name != exp.name {
			t.Errorf("match[%d].Method.Name = %q, want %q", i, matches[i].Method.Name, exp.name)
		}
		if matches[i].Operation != exp.op {
			t.Errorf("match[%d].Operation = %q, want %q", i, matches[i].Operation, exp.op)
		}
		if matches[i].Entity.Name != "Patient" {
			t.Errorf("match[%d].Entity.Name = %q, want Patient", i, matches[i].Entity.Name)
		}
	}
}

func TestMatchCRUDMethods_NoEntityMatch(t *testing.T) {
	entities := []EntityDef{
		{Name: "Invoice", TableName: "invoices", PkField: "id", PkGoType: "int64"},
	}

	svc := ServiceDef{
		Name: "PatientsService",
		Methods: []Method{
			{Name: "CreatePatient", InputType: "CreatePatientRequest", OutputType: "CreatePatientResponse"},
		},
	}

	matches := MatchCRUDMethods(svc, entities)
	if len(matches) != 0 {
		t.Errorf("expected 0 matches for unrelated entity, got %d", len(matches))
	}
}

func TestMatchCRUDMethods_SkipsStreamingMethods(t *testing.T) {
	entities := []EntityDef{
		{Name: "Patient", TableName: "patients", PkField: "id", PkGoType: "int64"},
	}

	svc := ServiceDef{
		Name: "PatientsService",
		Methods: []Method{
			{Name: "CreatePatient", InputType: "CreatePatientRequest", OutputType: "CreatePatientResponse", ServerStreaming: true},
		},
	}

	matches := MatchCRUDMethods(svc, entities)
	if len(matches) != 0 {
		t.Errorf("expected 0 matches for streaming methods, got %d", len(matches))
	}
}

func TestMatchCRUDMethods_NonCRUDMethodsIgnored(t *testing.T) {
	entities := []EntityDef{
		{Name: "Patient", TableName: "patients", PkField: "id", PkGoType: "int64"},
	}

	svc := ServiceDef{
		Name: "PatientsService",
		Methods: []Method{
			{Name: "SearchPatients", InputType: "SearchPatientsRequest", OutputType: "SearchPatientsResponse"},
			{Name: "ArchivePatient", InputType: "ArchivePatientRequest", OutputType: "ArchivePatientResponse"},
		},
	}

	matches := MatchCRUDMethods(svc, entities)
	if len(matches) != 0 {
		t.Errorf("expected 0 matches for non-CRUD methods, got %d", len(matches))
	}
}

func TestParseCRUDOperation(t *testing.T) {
	tests := []struct {
		input    string
		wantOp   string
		wantName string
	}{
		{"CreatePatient", "create", "Patient"},
		{"GetPatient", "get", "Patient"},
		{"ListPatients", "list", "Patients"},
		{"UpdatePatient", "update", "Patient"},
		{"DeletePatient", "delete", "Patient"},
		{"SearchPatients", "", ""}, // not CRUD
		{"ArchivePatient", "", ""}, // not CRUD
		{"Create", "", ""},         // prefix only, no entity name
		{"Get", "", ""},
		{"ProcessPayment", "", ""},
	}

	for _, tt := range tests {
		op, name := parseCRUDOperation(tt.input)
		if op != tt.wantOp || name != tt.wantName {
			t.Errorf("parseCRUDOperation(%q) = (%q, %q), want (%q, %q)",
				tt.input, op, name, tt.wantOp, tt.wantName)
		}
	}
}

func TestGenerateCRUDHandlers_SkipsExistingMethods(t *testing.T) {
	projectDir := t.TempDir()
	handlerDir := filepath.Join(projectDir, "internal", "handlers", "patients")
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write a service.go with Deps struct containing DB field
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

	// Write a handlers.go that already implements CreatePatient
	handlersGo := `package patients

import (
	"context"
	"connectrpc.com/connect"
	pb "example.com/test/gen/proto/services/patients/v1"
)

func (s *Service) CreatePatient(ctx context.Context, req *connect.Request[pb.CreatePatientRequest]) (*connect.Response[pb.CreatePatientResponse], error) {
	return nil, nil
}
`
	if err := os.WriteFile(filepath.Join(handlerDir, "handlers.go"), []byte(handlersGo), 0o644); err != nil {
		t.Fatal(err)
	}

	svc := ServiceDef{
		Name:       "PatientsService",
		GoPackage:  "example.com/test/gen/proto/services/patients/v1",
		PkgName:    "patientsv1",
		ModulePath: "example.com/test",
		Methods: []Method{
			{Name: "CreatePatient", InputType: "CreatePatientRequest", OutputType: "CreatePatientResponse"},
			{Name: "GetPatient", InputType: "GetPatientRequest", OutputType: "GetPatientResponse"},
		},
	}

	entities := []EntityDef{
		{
			Name:      "Patient",
			TableName: "patients",
			PkField:   "id",
			PkGoType:  "int64",
			Fields: []EntityField{
				{Name: "id", GoName: "ID", GoType: "int64", Kind: FieldKindScalar},
				{Name: "name", GoName: "Name", GoType: "string", Kind: FieldKindScalar},
			},
		},
	}

	crudMethods := MatchCRUDMethods(svc, entities)

	_, err := GenerateCRUDHandlers(svc, crudMethods, "example.com/test", projectDir, nil)
	if err != nil {
		t.Fatalf("GenerateCRUDHandlers() error = %v", err)
	}

	// Tier-1 projection: per-RPC op constructors land in the ops file.
	opsPath := filepath.Join(handlerDir, "handlers_crud_ops_gen.go")
	opsData, err := os.ReadFile(opsPath)
	if err != nil {
		t.Fatalf("generated ops file not found: %v", err)
	}
	ops := string(opsData)

	// Should NOT carry an op for CreatePatient (already in user handlers.go)
	if contains(ops, "crudCreatePatientOp") {
		t.Error("ops file should not contain crudCreatePatientOp (already implemented)")
	}
	// Should carry the GetPatient op (not in user handlers)
	if !contains(ops, "func (s *Service) crudGetPatientOp()") {
		t.Error("ops file should contain crudGetPatientOp constructor")
	}

	// User-owned shim: only the un-implemented RPC gets a delegating method.
	shimPath := filepath.Join(handlerDir, "handlers_crud.go")
	shimData, err := os.ReadFile(shimPath)
	if err != nil {
		t.Fatalf("scaffolded handlers_crud.go not found: %v", err)
	}
	shim := string(shimData)

	if contains(shim, "func (s *Service) CreatePatient(") {
		t.Error("handlers_crud.go should not contain CreatePatient (already implemented)")
	}
	if !contains(shim, "func (s *Service) GetPatient(") {
		t.Error("handlers_crud.go should contain the GetPatient shim")
	}
	if !contains(shim, "crud.HandleGet(s.crudGetPatientOp())(ctx, req)") {
		t.Error("GetPatient shim should delegate to crud.HandleGet(s.crudGetPatientOp())")
	}
}

// TestEnsureDepsDBField_DoesNotMutateWhenHandlersGoExists pins the
// Tier-3 user-owned contract for handlers/<svc>/service.go: a user who
// owns the WHOLE CRUD surface for an entity (implements every matched RPC
// in handlers.go) never has service.go mutated.
//
// The protection is the DEDUP, not a handlers.go-existence gate. When the
// user has implemented ListPatients (the only CRUD-matched RPC) by hand,
// ScanExistingMethods filters it out, GenerateCRUDHandlers emits no op for
// it, and ensureDepsDBField is never reached — so nothing forge writes ever
// references s.deps.DB and service.go stays byte-for-byte identical.
//
// (An earlier version gated ensureDepsDBField on the ABSENCE of handlers.go,
// treating any handlers.go as "user manages Deps". That gate broke once
// `forge project new` began SHIPPING a handlers.go: it fired on every fresh
// project, so a newly-added CRUD entity's ops referenced an s.deps.DB field
// that was never injected and the build broke. See
// TestEnsureDepsDBField_InjectsWhenHandlersGoHasUnrelatedStubs for the
// counterpart: an UNIMPLEMENTED CRUD method alongside a shipped handlers.go
// DOES get the field injected, because forge is the one emitting the
// s.deps.DB reference.)
func TestEnsureDepsDBField_DoesNotMutateWhenHandlersGoExists(t *testing.T) {
	projectDir := t.TempDir()
	handlerDir := filepath.Join(projectDir, "internal", "handlers", "patients")
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// User-owned service.go: NO DB field, NO orm import. The user wants
	// to hand-write handlers without forge's CRUD codegen wiring.
	serviceGo := `package patients

import (
	"fmt"
	"log/slog"
)

type Deps struct {
	Logger *slog.Logger
}

func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("PatientsService: Deps.Logger is required")
	}
	return nil
}

type Service struct {
	deps Deps
}
`
	servicePath := filepath.Join(handlerDir, "service.go")
	if err := os.WriteFile(servicePath, []byte(serviceGo), 0o644); err != nil {
		t.Fatal(err)
	}

	// User-owned handlers.go: signals opt-out from forge CRUD codegen.
	// The user has implemented ListPatients themselves with whatever
	// shape they like — no DB dependency.
	handlersGo := `package patients

import (
	"context"
	"connectrpc.com/connect"
	pb "example.com/test/gen/proto/services/patients/v1"
)

func (s *Service) ListPatients(ctx context.Context, req *connect.Request[pb.ListPatientsRequest]) (*connect.Response[pb.ListPatientsResponse], error) {
	return nil, nil
}
`
	if err := os.WriteFile(filepath.Join(handlerDir, "handlers.go"), []byte(handlersGo), 0o644); err != nil {
		t.Fatal(err)
	}

	// Snapshot the exact bytes of service.go before generate.
	beforeBytes, err := os.ReadFile(servicePath)
	if err != nil {
		t.Fatal(err)
	}

	svc := ServiceDef{
		Name:       "PatientsService",
		GoPackage:  "example.com/test/gen/proto/services/patients/v1",
		PkgName:    "patientsv1",
		ModulePath: "example.com/test",
		Methods: []Method{
			{Name: "ListPatients", InputType: "ListPatientsRequest", OutputType: "ListPatientsResponse"},
		},
	}

	entities := []EntityDef{
		{
			Name:      "Patient",
			TableName: "patients",
			PkField:   "id",
			PkGoType:  "int64",
			Fields: []EntityField{
				{Name: "id", GoName: "ID", GoType: "int64", Kind: FieldKindScalar},
				{Name: "name", GoName: "Name", GoType: "string", Kind: FieldKindScalar},
			},
		},
	}

	crudMethods := MatchCRUDMethods(svc, entities)
	if len(crudMethods) != 1 {
		t.Fatalf("expected 1 CRUD match (ListPatients → Patient), got %d", len(crudMethods))
	}

	if _, err := GenerateCRUDHandlers(svc, crudMethods, "example.com/test", projectDir, nil); err != nil {
		t.Fatalf("GenerateCRUDHandlers() error = %v", err)
	}

	// service.go must be byte-for-byte identical — NO injected DB field,
	// NO injected orm import. This is the Tier-3-never-mutated guarantee.
	afterBytes, err := os.ReadFile(servicePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(beforeBytes) != string(afterBytes) {
		t.Errorf("service.go was mutated despite handlers.go existing.\n--- before ---\n%s\n--- after ---\n%s",
			string(beforeBytes), string(afterBytes))
	}

	// Sanity checks: defensive against the regression's exact symptoms.
	after := string(afterBytes)
	if strings.Contains(after, "orm.Context") {
		t.Error("service.go must not contain orm.Context after generate when handlers.go exists")
	}
	if strings.Contains(after, "github.com/reliant-labs/forge/pkg/orm") {
		t.Error("service.go must not contain pkg/orm import after generate when handlers.go exists")
	}
}

// TestEnsureDepsDBField_InjectsWhenHandlersGoAbsent pins the happy
// path: a fresh service with no handlers.go (user hasn't started
// writing handler code yet) DOES get the DB field auto-injected so the
// generated handlers_crud_gen.go compiles out of the box. This is the
// behavior that motivated ensureDepsDBField in the first place and we
// don't want to regress it while fixing the silent-mutation case above.
func TestEnsureDepsDBField_InjectsWhenHandlersGoAbsent(t *testing.T) {
	projectDir := t.TempDir()
	handlerDir := filepath.Join(projectDir, "internal", "handlers", "patients")
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Fresh service.go from the scaffold: no DB field, no orm import,
	// has the "// Add your dependencies here." marker.
	serviceGo := `package patients

import (
	"fmt"
	"log/slog"
)

type Deps struct {
	Logger *slog.Logger
	// Add your dependencies here.
}

func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("PatientsService: Deps.Logger is required")
	}
	return nil
}

type Service struct {
	deps Deps
}
`
	servicePath := filepath.Join(handlerDir, "service.go")
	if err := os.WriteFile(servicePath, []byte(serviceGo), 0o644); err != nil {
		t.Fatal(err)
	}
	// Intentionally NO handlers.go — fresh service, user hasn't started.

	svc := ServiceDef{
		Name:       "PatientsService",
		GoPackage:  "example.com/test/gen/proto/services/patients/v1",
		PkgName:    "patientsv1",
		ModulePath: "example.com/test",
		Methods: []Method{
			{Name: "ListPatients", InputType: "ListPatientsRequest", OutputType: "ListPatientsResponse"},
		},
	}

	entities := []EntityDef{
		{
			Name:      "Patient",
			TableName: "patients",
			PkField:   "id",
			PkGoType:  "int64",
			Fields: []EntityField{
				{Name: "id", GoName: "ID", GoType: "int64", Kind: FieldKindScalar},
				{Name: "name", GoName: "Name", GoType: "string", Kind: FieldKindScalar},
			},
		},
	}

	crudMethods := MatchCRUDMethods(svc, entities)

	if _, err := GenerateCRUDHandlers(svc, crudMethods, "example.com/test", projectDir, nil); err != nil {
		t.Fatalf("GenerateCRUDHandlers() error = %v", err)
	}

	after, err := os.ReadFile(servicePath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(after)

	if !strings.Contains(content, "DB") || !strings.Contains(content, "orm.Context") {
		t.Errorf("expected DB orm.Context to be injected into Deps when no handlers.go exists; got:\n%s", content)
	}
	if !strings.Contains(content, "github.com/reliant-labs/forge/pkg/orm") {
		t.Errorf("expected pkg/orm import to be injected when no handlers.go exists; got:\n%s", content)
	}
}

// TestEnsureDepsDBField_InjectsWhenHandlersGoHasUnrelatedStubs is the
// direct regression guard for the dogfood bug: `forge project new` ships a
// handlers.go carrying the example entity's stubs, and adding a REAL CRUD
// entity (its own table + Create/Get/List/Update/Delete RPCs not present in
// handlers.go) must still get `DB orm.Context` injected so the generated
// handlers_crud_ops_gen.go — which dereferences s.deps.DB — compiles.
//
// The old handlers.go-existence gate returned early here (handlers.go was on
// disk), left Deps without a DB field, and the build died on
// `s.deps.DB undefined`. This pins that the presence of an UNRELATED,
// unimplemented handlers.go no longer suppresses the injection.
func TestEnsureDepsDBField_InjectsWhenHandlersGoHasUnrelatedStubs(t *testing.T) {
	projectDir := t.TempDir()
	handlerDir := filepath.Join(projectDir, "internal", "handlers", "patients")
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Fresh scaffold service.go: Logger only, extension marker present.
	serviceGo := `package patients

import (
	"fmt"
	"log/slog"
)

type Deps struct {
	Logger *slog.Logger
	// Add your dependencies here (e.g. Repo Repository).
}

func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("PatientsService: Deps.Logger is required")
	}
	// Add checks for your required Deps fields here.
	return nil
}

type Service struct {
	deps Deps
}
`
	servicePath := filepath.Join(handlerDir, "service.go")
	if err := os.WriteFile(servicePath, []byte(serviceGo), 0o644); err != nil {
		t.Fatal(err)
	}

	// Shipped handlers.go with an UNRELATED entity's stubs (like the
	// example Item `forge project new` ships) — NOT the Patient CRUD forge
	// is about to wire.
	handlersGo := `package patients

import (
	"context"
	"connectrpc.com/connect"
	pb "example.com/test/gen/proto/services/patients/v1"
)

func (s *Service) CreateWidget(ctx context.Context, req *connect.Request[pb.CreateWidgetRequest]) (*connect.Response[pb.CreateWidgetResponse], error) {
	return nil, nil
}
`
	if err := os.WriteFile(filepath.Join(handlerDir, "handlers.go"), []byte(handlersGo), 0o644); err != nil {
		t.Fatal(err)
	}

	// Patient CRUD RPCs — NONE implemented in handlers.go, so forge wires
	// them and their ops reference s.deps.DB.
	svc := ServiceDef{
		Name:       "PatientsService",
		GoPackage:  "example.com/test/gen/proto/services/patients/v1",
		PkgName:    "patientsv1",
		ModulePath: "example.com/test",
		Methods: []Method{
			{Name: "CreatePatient", InputType: "CreatePatientRequest", OutputType: "CreatePatientResponse"},
			{Name: "GetPatient", InputType: "GetPatientRequest", OutputType: "GetPatientResponse"},
		},
	}
	entities := []EntityDef{
		{
			Name:      "Patient",
			TableName: "patients",
			PkField:   "id",
			PkGoType:  "string",
			Fields: []EntityField{
				{Name: "id", GoName: "Id", GoType: "string", Kind: FieldKindScalar},
				{Name: "name", GoName: "Name", GoType: "string", Kind: FieldKindScalar},
			},
		},
	}

	crudMethods := MatchCRUDMethods(svc, entities)
	if len(crudMethods) == 0 {
		t.Fatal("expected Patient CRUD methods to match")
	}

	if _, err := GenerateCRUDHandlers(svc, crudMethods, "example.com/test", projectDir, nil); err != nil {
		t.Fatalf("GenerateCRUDHandlers() error = %v", err)
	}

	after, err := os.ReadFile(servicePath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(after)
	if !strings.Contains(content, "orm.Context") {
		t.Errorf("expected DB orm.Context injected into Deps despite a shipped handlers.go with unrelated stubs; got:\n%s", content)
	}
	if !strings.Contains(content, "github.com/reliant-labs/forge/pkg/orm") {
		t.Errorf("expected pkg/orm import injected; got:\n%s", content)
	}

	// And the generated ops file references s.deps.DB — the field just
	// injected is exactly what it needs.
	opsPath := filepath.Join(handlerDir, "handlers_crud_ops_gen.go")
	ops, err := os.ReadFile(opsPath)
	if err != nil {
		t.Fatalf("expected handlers_crud_ops_gen.go to be written: %v", err)
	}
	if !strings.Contains(string(ops), "s.deps.DB") {
		t.Error("expected the generated ops to reference s.deps.DB (the injected field)")
	}
}

func TestGenerateCRUDHandlers_CleanupWhenNoMethods(t *testing.T) {
	projectDir := t.TempDir()
	handlerDir := filepath.Join(projectDir, "internal", "handlers", "patients")
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create stale legacy/current gen files self-identifying as forge
	// output (untracked files without the banner are never swept).
	genPath := filepath.Join(handlerDir, "handlers_crud_gen.go")
	if err := os.WriteFile(genPath, []byte("// Code generated by forge. DO NOT EDIT.\npackage patients\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	opsPath := filepath.Join(handlerDir, "handlers_crud_ops_gen.go")
	if err := os.WriteFile(opsPath, []byte("// Code generated by forge. DO NOT EDIT.\npackage patients\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Write handlers.go that already implements everything
	handlersGo := `package patients

import (
	"context"
	"connectrpc.com/connect"
	pb "example.com/test/gen/proto/services/patients/v1"
)

func (s *Service) CreatePatient(ctx context.Context, req *connect.Request[pb.CreatePatientRequest]) (*connect.Response[pb.CreatePatientResponse], error) {
	return nil, nil
}
`
	if err := os.WriteFile(filepath.Join(handlerDir, "handlers.go"), []byte(handlersGo), 0o644); err != nil {
		t.Fatal(err)
	}

	svc := ServiceDef{
		Name:       "PatientsService",
		GoPackage:  "example.com/test/gen/proto/services/patients/v1",
		PkgName:    "patientsv1",
		ModulePath: "example.com/test",
		Methods: []Method{
			{Name: "CreatePatient", InputType: "CreatePatientRequest", OutputType: "CreatePatientResponse"},
		},
	}

	crudMethods := []CRUDMethod{
		{
			Method:    MethodTemplateData{Name: "CreatePatient"},
			Entity:    EntityDef{Name: "Patient"},
			Operation: "create",
		},
	}

	_, err := GenerateCRUDHandlers(svc, crudMethods, "example.com/test", projectDir, nil)
	if err != nil {
		t.Fatalf("GenerateCRUDHandlers() error = %v", err)
	}

	// Stale files should be removed since CreatePatient is already implemented
	if _, err := os.Stat(genPath); !os.IsNotExist(err) {
		t.Error("stale legacy handlers_crud_gen.go should have been removed")
	}
	if _, err := os.Stat(opsPath); !os.IsNotExist(err) {
		t.Error("stale handlers_crud_ops_gen.go should have been removed")
	}
	// And nothing new is scaffolded: the user owns every CRUD method.
	if _, err := os.Stat(filepath.Join(handlerDir, "handlers_crud.go")); !os.IsNotExist(err) {
		t.Error("handlers_crud.go should not be scaffolded when no CRUD methods remain")
	}
}

// TestGenerateCRUDHandlers_KeepsUserOwnedLegacyGen pins the ownership guard
// on the legacy-file sweep: a handlers_crud_gen.go whose manifest entry is
// Tier-2 (disowned / user-owned) is the USER's file now — the retirement
// sweep must leave both the file and its manifest entry alone.
func TestGenerateCRUDHandlers_KeepsUserOwnedLegacyGen(t *testing.T) {
	projectDir := t.TempDir()
	handlerDir := filepath.Join(projectDir, "internal", "handlers", "patients")
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(handlerDir, "service.go"), []byte("package patients\n\ntype Deps struct{}\ntype Service struct{ deps Deps }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	legacyRel := filepath.Join("internal", "handlers", "patients", "handlers_crud_gen.go")
	legacyPath := filepath.Join(projectDir, legacyRel)
	legacyBody := "// Code generated by forge. DO NOT EDIT.\npackage patients\n\n// user took ownership of this file\n"
	if err := os.WriteFile(legacyPath, []byte(legacyBody), 0o644); err != nil {
		t.Fatal(err)
	}

	// Keyed as .forge/disowned.json records it: slash-separated on every OS.
	cs := &checksums.FileChecksums{Disowned: map[string]checksums.DisownedEntry{
		filepath.ToSlash(legacyRel): {Reason: "user took ownership"},
	}}

	svc := ServiceDef{
		Name:       "PatientsService",
		GoPackage:  "example.com/test/gen/proto/services/patients/v1",
		PkgName:    "patientsv1",
		ModulePath: "example.com/test",
		Methods: []Method{
			{Name: "GetPatient", InputType: "GetPatientRequest", OutputType: "GetPatientResponse"},
		},
	}
	entities := []EntityDef{{
		Name: "Patient", TableName: "patients", PkField: "id", PkGoType: "int64",
		Fields: []EntityField{{Name: "id", GoName: "ID", GoType: "int64"}},
	}}

	if _, err := GenerateCRUDHandlers(svc, MatchCRUDMethods(svc, entities), "example.com/test", projectDir, cs); err != nil {
		t.Fatalf("GenerateCRUDHandlers() error = %v", err)
	}

	after, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatalf("user-owned legacy handlers_crud_gen.go was deleted: %v", err)
	}
	if string(after) != legacyBody {
		t.Error("user-owned legacy handlers_crud_gen.go was modified")
	}
	if !cs.IsDisowned(legacyRel) {
		t.Error("ownership record for user-owned legacy file was dropped")
	}
}

func TestMatchCRUDMethods_CaseInsensitiveEntityMatch(t *testing.T) {
	entities := []EntityDef{
		{Name: "Patient", TableName: "patients", PkField: "id", PkGoType: "int64"},
	}

	svc := ServiceDef{
		Name: "PatientsService",
		Methods: []Method{
			{Name: "CreatePatient", InputType: "CreatePatientRequest", OutputType: "CreatePatientResponse"},
		},
	}

	matches := MatchCRUDMethods(svc, entities)
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matches))
	}
}

func TestMatchCRUDMethods_AuthRequired(t *testing.T) {
	entities := []EntityDef{
		{Name: "Patient", TableName: "patients", PkField: "id", PkGoType: "int64"},
	}

	svc := ServiceDef{
		Name: "PatientsService",
		Methods: []Method{
			{Name: "CreatePatient", InputType: "CreatePatientRequest", OutputType: "CreatePatientResponse", AuthRequired: true},
			{Name: "GetPatient", InputType: "GetPatientRequest", OutputType: "GetPatientResponse", AuthRequired: false},
		},
	}

	matches := MatchCRUDMethods(svc, entities)
	if len(matches) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(matches))
	}
	if !matches[0].Method.AuthRequired {
		t.Error("CreatePatient should have AuthRequired=true")
	}
	if matches[1].Method.AuthRequired {
		t.Error("GetPatient should have AuthRequired=false")
	}
}

func TestGenerateCRUDTests_BasicGeneration(t *testing.T) {
	projectDir := t.TempDir()
	handlerDir := filepath.Join(projectDir, "internal", "handlers", "patients")
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Real projects always have a service.go in the handler dir before CRUD
	// gen runs; the disk-first resolver needs at least one parseable .go
	// file to read the package clause from.
	if err := os.WriteFile(filepath.Join(handlerDir, "service.go"), []byte("package patients\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	svc := ServiceDef{
		Name:       "PatientsService",
		GoPackage:  "example.com/test/gen/proto/services/patients/v1",
		PkgName:    "patientsv1",
		ModulePath: "example.com/test",
		Methods: []Method{
			{Name: "CreatePatient", InputType: "CreatePatientRequest", OutputType: "CreatePatientResponse"},
			{Name: "GetPatient", InputType: "GetPatientRequest", OutputType: "GetPatientResponse"},
			{Name: "ListPatients", InputType: "ListPatientsRequest", OutputType: "ListPatientsResponse"},
			{Name: "UpdatePatient", InputType: "UpdatePatientRequest", OutputType: "UpdatePatientResponse"},
			{Name: "DeletePatient", InputType: "DeletePatientRequest", OutputType: "DeletePatientResponse"},
		},
	}

	entities := []EntityDef{
		{
			Name:       "Patient",
			TableName:  "patients",
			PkField:    "id",
			PkGoType:   "string",
			Timestamps: true,
			Fields: []EntityField{
				{Name: "id", GoName: "Id", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
				{Name: "name", GoName: "Name", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
				{Name: "active", GoName: "Active", ProtoType: "bool", GoType: "bool", Kind: FieldKindScalar},
				// A realistic timestamped entity EXPOSES created_at on the
				// wire — that is what makes the GetCreatedAt() assertion
				// compile (the wire assertion is gated on this field, not on
				// the table column alone).
				{Name: "created_at", GoName: "CreatedAt", ProtoType: "message", MessageType: "google.protobuf.Timestamp", GoType: "*timestamppb.Timestamp"},
			},
		},
	}

	crudMethods := MatchCRUDMethods(svc, entities)

	_, err := GenerateCRUDTests(svc, crudMethods, "example.com/test", projectDir, nil)
	if err != nil {
		t.Fatalf("GenerateCRUDTests() error = %v", err)
	}

	// The legacy marker-scaffold pair is no longer emitted.
	for _, retired := range []string{"handlers_crud_gen_test.go", "handlers_crud_integration_test.go"} {
		if _, err := os.Stat(filepath.Join(handlerDir, retired)); !os.IsNotExist(err) {
			t.Errorf("retired %s should not be generated", retired)
		}
	}

	// The replacement: ONE user-owned lifecycle test with real semantics.
	lifecyclePath := filepath.Join(handlerDir, "handlers_crud_test.go")
	lifecycleData, err := os.ReadFile(lifecyclePath)
	if err != nil {
		t.Fatalf("scaffolded handlers_crud_test.go not found: %v", err)
	}
	content := string(lifecycleData)

	if !contains(content, "package patients_test") {
		t.Error("expected package patients_test in lifecycle output")
	}
	if !contains(content, "TestCRUD_Patient_Lifecycle") {
		t.Error("expected TestCRUD_Patient_Lifecycle in lifecycle output")
	}

	// User-owned from line one: no Tier-1 banner, no scaffold markers.
	if contains(content, "DO NOT EDIT") {
		t.Error("lifecycle test is user-owned; must not carry a DO NOT EDIT banner")
	}
	if contains(content, "FORGE_SCAFFOLD:") {
		t.Error("lifecycle test is user-owned; must not carry FORGE_SCAFFOLD markers")
	}

	// Real-semantics assertions, not AnyOutcome frames.
	if !contains(content, "create must be an insert, never an upsert") {
		t.Error("expected distinct-id (insert-not-upsert) assertion in lifecycle output")
	}
	if !contains(content, "connect.CodeNotFound") {
		t.Error("expected clean NotFound assertion in lifecycle output")
	}
	// All 5 ops matched: list + update + delete sections render too.
	if !contains(content, "svc.ListPatients") {
		t.Error("expected list section in lifecycle output")
	}
	if !contains(content, "lifecycle-updated") {
		t.Error("expected update-mutates assertion (MutableStringField) in lifecycle output")
	}
	if !contains(content, "svc.DeletePatient") {
		t.Error("expected delete section in lifecycle output")
	}
	// timestamps: true on the entity → created_at asserted set.
	if !contains(content, "GetCreatedAt()") {
		t.Error("expected created_at assertion for timestamps:true entity")
	}

	// Must parse as valid Go.
	if _, err := parser.ParseFile(token.NewFileSet(), lifecyclePath, content, parser.SkipObjectResolution); err != nil {
		t.Errorf("lifecycle output is not valid Go: %v\n----\n%s", err, content)
	}

	// Scaffold-once: a later run must not touch the (now user-owned) file.
	custom := "package patients_test\n\n// customized by the user\n"
	if err := os.WriteFile(lifecyclePath, []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := GenerateCRUDTests(svc, crudMethods, "example.com/test", projectDir, nil); err != nil {
		t.Fatalf("GenerateCRUDTests() second run error = %v", err)
	}
	after, err := os.ReadFile(lifecyclePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != custom {
		t.Error("handlers_crud_test.go was rewritten on a later run; it must be scaffold-once")
	}
}

func TestGenerateCRUDTests_PartialCRUD(t *testing.T) {
	projectDir := t.TempDir()
	handlerDir := filepath.Join(projectDir, "internal", "handlers", "patients")
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Real projects always have a service.go in the handler dir before CRUD
	// gen runs; the disk-first resolver needs at least one parseable .go
	// file to read the package clause from.
	if err := os.WriteFile(filepath.Join(handlerDir, "service.go"), []byte("package patients\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	svc := ServiceDef{
		Name:       "PatientsService",
		GoPackage:  "example.com/test/gen/proto/services/patients/v1",
		PkgName:    "patientsv1",
		ModulePath: "example.com/test",
		Methods: []Method{
			{Name: "CreatePatient", InputType: "CreatePatientRequest", OutputType: "CreatePatientResponse"},
			{Name: "GetPatient", InputType: "GetPatientRequest", OutputType: "GetPatientResponse"},
		},
	}

	entities := []EntityDef{
		{
			Name:      "Patient",
			TableName: "patients",
			PkField:   "id",
			PkGoType:  "string",
			Fields: []EntityField{
				{Name: "id", GoName: "Id", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
				{Name: "name", GoName: "Name", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
			},
		},
	}

	crudMethods := MatchCRUDMethods(svc, entities)

	_, err := GenerateCRUDTests(svc, crudMethods, "example.com/test", projectDir, nil)
	if err != nil {
		t.Fatalf("GenerateCRUDTests() error = %v", err)
	}

	lifecyclePath := filepath.Join(handlerDir, "handlers_crud_test.go")
	lifecycleData, err := os.ReadFile(lifecyclePath)
	if err != nil {
		t.Fatalf("scaffolded handlers_crud_test.go not found: %v", err)
	}
	content := string(lifecycleData)

	// create+get with a string PK qualifies — the lifecycle test renders
	// covering exactly the ops that exist.
	if !contains(content, "TestCRUD_Patient_Lifecycle") {
		t.Error("expected TestCRUD_Patient_Lifecycle for create+get string-PK entity")
	}

	// Missing ops must not be referenced.
	for _, absent := range []string{"svc.ListPatients", "svc.UpdatePatient", "svc.DeletePatient"} {
		if contains(content, absent) {
			t.Errorf("lifecycle output should not reference %s (op not in proto)", absent)
		}
	}

	if _, err := parser.ParseFile(token.NewFileSet(), lifecyclePath, content, parser.SkipObjectResolution); err != nil {
		t.Errorf("lifecycle output is not valid Go: %v\n----\n%s", err, content)
	}
}

// TestGenerateCRUDTests_SkipsNonQualifyingEntities pins the qualification
// gate: the lifecycle test exercises forge's CRUD convention (string PK,
// create+get); an entity outside it gets no scaffold at all.
func TestGenerateCRUDTests_SkipsNonQualifyingEntities(t *testing.T) {
	projectDir := t.TempDir()
	handlerDir := filepath.Join(projectDir, "internal", "handlers", "patients")
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(handlerDir, "service.go"), []byte("package patients\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	svc := ServiceDef{
		Name:       "PatientsService",
		GoPackage:  "example.com/test/gen/proto/services/patients/v1",
		PkgName:    "patientsv1",
		ModulePath: "example.com/test",
		Methods: []Method{
			{Name: "CreatePatient", InputType: "CreatePatientRequest", OutputType: "CreatePatientResponse"},
			{Name: "GetPatient", InputType: "GetPatientRequest", OutputType: "GetPatientResponse"},
		},
	}

	// int64 PK — outside the string-PK CRUD convention.
	entities := []EntityDef{{
		Name: "Patient", TableName: "patients", PkField: "id", PkGoType: "int64",
		Fields: []EntityField{
			{Name: "id", GoName: "Id", ProtoType: "int64", GoType: "int64", Kind: FieldKindScalar},
			{Name: "name", GoName: "Name", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
		},
	}}

	if _, err := GenerateCRUDTests(svc, MatchCRUDMethods(svc, entities), "example.com/test", projectDir, nil); err != nil {
		t.Fatalf("GenerateCRUDTests() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(handlerDir, "handlers_crud_test.go")); !os.IsNotExist(err) {
		t.Error("expected no handlers_crud_test.go for a non-string-PK entity")
	}
}

func TestGenerateCRUDTests_CleanupWhenNoMethods(t *testing.T) {
	projectDir := t.TempDir()
	handlerDir := filepath.Join(projectDir, "internal", "handlers", "patients")
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Real projects always have a service.go in the handler dir before CRUD
	// gen runs; the disk-first resolver needs at least one parseable .go
	// file to read the package clause from.
	if err := os.WriteFile(filepath.Join(handlerDir, "service.go"), []byte("package patients\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create stale retired test gen files (both unit and integration
	// variants) still carrying FORGE_SCAFFOLD markers — i.e. forge-owned.
	unitPath := filepath.Join(handlerDir, "handlers_crud_gen_test.go")
	if err := os.WriteFile(unitPath, []byte("// FORGE_SCAFFOLD: stale\npackage patients_test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	integrationPath := filepath.Join(handlerDir, "handlers_crud_integration_test.go")
	if err := os.WriteFile(integrationPath, []byte("//go:build integration\n// FORGE_SCAFFOLD: stale\npackage patients_test\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	svc := ServiceDef{Name: "PatientsService"}

	_, err := GenerateCRUDTests(svc, nil, "example.com/test", projectDir, nil)
	if err != nil {
		t.Fatalf("GenerateCRUDTests() error = %v", err)
	}

	// Stale forge-owned files should be removed
	if _, err := os.Stat(unitPath); !os.IsNotExist(err) {
		t.Error("stale handlers_crud_gen_test.go should have been removed")
	}
	if _, err := os.Stat(integrationPath); !os.IsNotExist(err) {
		t.Error("stale handlers_crud_integration_test.go should have been removed")
	}

	// A legacy file whose markers the user cleared is user-owned: kept.
	customized := "package patients_test\n\n// user customized: markers cleared\n"
	if err := os.WriteFile(unitPath, []byte(customized), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := GenerateCRUDTests(svc, nil, "example.com/test", projectDir, nil); err != nil {
		t.Fatalf("GenerateCRUDTests() second run error = %v", err)
	}
	after, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("user-owned legacy test file was removed: %v", err)
	}
	if string(after) != customized {
		t.Error("user-owned legacy test file was modified")
	}
}

// TestGenerateCRUDTests_SkipsExistingMethods mirrors
// TestGenerateCRUDHandlers_SkipsExistingMethods: once the user has taken
// ownership of a CRUD method by writing a real handler, the test
// generator must stop treating it as forge-wired. With CreatePatient
// hand-implemented, the Patient entity no longer has a forge-owned
// create+get pair, so the lifecycle scaffold (which drives the GENERATED
// CRUD stack) must not be emitted at all — it would exercise the user's
// bespoke handler with conventions it never promised to follow.
func TestGenerateCRUDTests_SkipsExistingMethods(t *testing.T) {
	projectDir := t.TempDir()
	handlerDir := filepath.Join(projectDir, "internal", "handlers", "patients")
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write a service.go (required for buildCRUDTestTemplateData to
	// resolve the test-helper name) and a handlers.go that already
	// implements CreatePatient. The remaining methods should drive the
	// test scaffold.
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

	handlersGo := `package patients

import (
	"context"
	"connectrpc.com/connect"
	pb "example.com/test/gen/proto/services/patients/v1"
)

func (s *Service) CreatePatient(ctx context.Context, req *connect.Request[pb.CreatePatientRequest]) (*connect.Response[pb.CreatePatientResponse], error) {
	return nil, nil
}
`
	if err := os.WriteFile(filepath.Join(handlerDir, "handlers.go"), []byte(handlersGo), 0o644); err != nil {
		t.Fatal(err)
	}

	svc := ServiceDef{
		Name:       "PatientsService",
		GoPackage:  "example.com/test/gen/proto/services/patients/v1",
		PkgName:    "patientsv1",
		ModulePath: "example.com/test",
		Methods: []Method{
			{Name: "CreatePatient", InputType: "CreatePatientRequest", OutputType: "CreatePatientResponse"},
			{Name: "GetPatient", InputType: "GetPatientRequest", OutputType: "GetPatientResponse"},
		},
	}

	entities := []EntityDef{
		{
			Name:      "Patient",
			TableName: "patients",
			PkField:   "id",
			PkGoType:  "string",
			Fields: []EntityField{
				{Name: "id", GoName: "Id", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
				{Name: "name", GoName: "Name", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
			},
		},
	}

	crudMethods := MatchCRUDMethods(svc, entities)

	if _, err := GenerateCRUDTests(svc, crudMethods, "example.com/test", projectDir, nil); err != nil {
		t.Fatalf("GenerateCRUDTests() error = %v", err)
	}

	// CreatePatient is user-owned, so Patient has no forge-wired create:
	// the entity no longer qualifies and no lifecycle test is scaffolded.
	if _, err := os.Stat(filepath.Join(handlerDir, "handlers_crud_test.go")); !os.IsNotExist(err) {
		t.Error("expected no handlers_crud_test.go when the user owns the create method")
	}
}

// TestGenerateCRUDTests_RemovesScaffoldWhenAllUserOwned guarantees the
// stale-cleanup path runs after every CRUD method has been hand-implemented.
// Combined with the filter above, a user who hand-writes every CRUD method
// ends up with no `handlers_crud_gen_test.go` on disk — exactly the same
// shape `GenerateCRUDHandlers` produces for `handlers_crud_gen.go`.
func TestGenerateCRUDTests_RemovesScaffoldWhenAllUserOwned(t *testing.T) {
	projectDir := t.TempDir()
	handlerDir := filepath.Join(projectDir, "internal", "handlers", "patients")
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Seed a stale scaffold so we can verify the cleanup branch fires.
	unitPath := filepath.Join(handlerDir, "handlers_crud_gen_test.go")
	if err := os.WriteFile(unitPath, []byte("// FORGE_SCAFFOLD: stale\npackage patients_test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	integrationPath := filepath.Join(handlerDir, "handlers_crud_integration_test.go")
	if err := os.WriteFile(integrationPath, []byte("//go:build integration\n// FORGE_SCAFFOLD: stale\npackage patients_test\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	serviceGo := `package patients

type Deps struct{}
type Service struct{ deps Deps }
`
	if err := os.WriteFile(filepath.Join(handlerDir, "service.go"), []byte(serviceGo), 0o644); err != nil {
		t.Fatal(err)
	}

	handlersGo := `package patients

import (
	"context"
	"connectrpc.com/connect"
	pb "example.com/test/gen/proto/services/patients/v1"
)

func (s *Service) CreatePatient(ctx context.Context, req *connect.Request[pb.CreatePatientRequest]) (*connect.Response[pb.CreatePatientResponse], error) {
	return nil, nil
}
func (s *Service) GetPatient(ctx context.Context, req *connect.Request[pb.GetPatientRequest]) (*connect.Response[pb.GetPatientResponse], error) {
	return nil, nil
}
`
	if err := os.WriteFile(filepath.Join(handlerDir, "handlers.go"), []byte(handlersGo), 0o644); err != nil {
		t.Fatal(err)
	}

	svc := ServiceDef{
		Name:       "PatientsService",
		GoPackage:  "example.com/test/gen/proto/services/patients/v1",
		PkgName:    "patientsv1",
		ModulePath: "example.com/test",
		Methods: []Method{
			{Name: "CreatePatient", InputType: "CreatePatientRequest", OutputType: "CreatePatientResponse"},
			{Name: "GetPatient", InputType: "GetPatientRequest", OutputType: "GetPatientResponse"},
		},
	}
	entities := []EntityDef{{
		Name: "Patient", TableName: "patients", PkField: "id", PkGoType: "int64",
		Fields: []EntityField{{Name: "id", GoName: "ID", GoType: "int64"}},
	}}

	if _, err := GenerateCRUDTests(svc, MatchCRUDMethods(svc, entities), "example.com/test", projectDir, nil); err != nil {
		t.Fatalf("GenerateCRUDTests() error = %v", err)
	}

	if _, err := os.Stat(unitPath); !os.IsNotExist(err) {
		t.Error("expected handlers_crud_gen_test.go to be removed when all CRUD methods are user-owned")
	}
	if _, err := os.Stat(integrationPath); !os.IsNotExist(err) {
		t.Error("expected handlers_crud_integration_test.go to be removed when all CRUD methods are user-owned")
	}
}

func TestTestValueForType(t *testing.T) {
	tests := []struct {
		goType string
		want   string
	}{
		{"string", `"test-value"`},
		{"int32", "1"},
		{"int64", "1"},
		{"uint32", "1"},
		{"uint64", "1"},
		{"float32", "1.0"},
		{"float64", "1.0"},
		{"bool", "true"},
		{"[]byte", `[]byte("test")`},
		{"*timestamppb.Timestamp", "timestamppb.Now()"},
		// Wrapper types
		{"*string", `wrapperspb.String("test-value")`},
		{"*int32", "wrapperspb.Int32(42)"},
		{"*int64", "wrapperspb.Int64(42)"},
		{"*bool", "wrapperspb.Bool(true)"},
		{"*float64", "wrapperspb.Double(1.0)"},
		// Message/repeated/map types → nil
		{"*SomeMessage", "nil"},
		{"[]string", "nil"},
		{"[]*SomeMessage", "nil"},
		{"map[string]string", "nil"},
		// Enum-like types (bare identifier) → 0
		{"SomeCustomType", "0"},
	}

	for _, tt := range tests {
		got := testValueForType(tt.goType)
		if got != tt.want {
			t.Errorf("testValueForType(%q) = %q, want %q", tt.goType, got, tt.want)
		}
	}
}

func TestBuildCRUDTestTemplateData(t *testing.T) {
	svc := ServiceDef{
		Name:       "PatientsService",
		GoPackage:  "example.com/test/gen/proto/services/patients/v1",
		PkgName:    "patientsv1",
		ModulePath: "example.com/test",
	}

	crudMethods := []CRUDMethod{
		{
			Method:    MethodTemplateData{Name: "CreatePatient", InputType: "CreatePatientRequest", OutputType: "CreatePatientResponse"},
			Entity:    EntityDef{Name: "Patient", PkField: "id", PkGoType: "int64", Timestamps: true, Fields: []EntityField{{Name: "id", GoName: "ID", GoType: "int64"}, {Name: "name", GoName: "Name", GoType: "string"}, {Name: "created_at", GoName: "CreatedAt", ProtoType: "message", MessageType: "google.protobuf.Timestamp", GoType: "*timestamppb.Timestamp"}}},
			Operation: "create",
		},
		{
			Method:    MethodTemplateData{Name: "GetPatient", InputType: "GetPatientRequest", OutputType: "GetPatientResponse"},
			Entity:    EntityDef{Name: "Patient", PkField: "id", PkGoType: "int64"},
			Operation: "get",
		},
		{
			Method:    MethodTemplateData{Name: "ListPatients", InputType: "ListPatientsRequest", OutputType: "ListPatientsResponse"},
			Entity:    EntityDef{Name: "Patient", PkField: "id", PkGoType: "int64"},
			Operation: "list",
		},
		{
			Method:    MethodTemplateData{Name: "UpdatePatient", InputType: "UpdatePatientRequest", OutputType: "UpdatePatientResponse"},
			Entity:    EntityDef{Name: "Patient", PkField: "id", PkGoType: "int64"},
			Operation: "update",
		},
		{
			Method:    MethodTemplateData{Name: "DeletePatient", InputType: "DeletePatientRequest", OutputType: "DeletePatientResponse"},
			Entity:    EntityDef{Name: "Patient", PkField: "id", PkGoType: "int64"},
			Operation: "delete",
		},
	}

	data := buildCRUDTestTemplateData(svc, crudMethods, "example.com/test", "", nil)

	if data.Package != "patients" {
		t.Errorf("Package = %q, want patients", data.Package)
	}
	if data.ProtoPackage != "proto/services/patients" {
		t.Errorf("ProtoPackage = %q, want proto/services/patients", data.ProtoPackage)
	}
	if len(data.Entities) != 1 {
		t.Fatalf("expected 1 entity, got %d", len(data.Entities))
	}

	ent := data.Entities[0]
	if !ent.HasAllCRUD {
		t.Error("expected HasAllCRUD=true")
	}
	if !ent.HasCreate || !ent.HasGet || !ent.HasList || !ent.HasUpdate || !ent.HasDelete {
		t.Error("expected all individual CRUD flags to be true")
	}
	if ent.CreateMethod.MethodName != "CreatePatient" {
		t.Errorf("CreateMethod.MethodName = %q, want CreatePatient", ent.CreateMethod.MethodName)
	}
	if len(ent.Fields) != 2 {
		t.Errorf("expected 2 fields (id excluded; name + created_at), got %d", len(ent.Fields))
	}
	if !ent.HasTimestamps {
		t.Error("expected HasTimestamps=true (table timestamps:true AND proto exposes created_at)")
	}
	if ent.MutableStringField != "Name" {
		t.Errorf("MutableStringField = %q, want Name (first non-PK string field)", ent.MutableStringField)
	}
	if len(data.CRUDMethods) != 5 {
		t.Errorf("expected 5 CRUDMethods, got %d", len(data.CRUDMethods))
	}
}

// TestBuildCRUDTestTemplateData_TimestampsWithoutProtoField pins the
// born-from-proto case: the entity's TABLE has a created_at column (so
// entity.Timestamps is true off the schema), but its proto MESSAGE does
// not declare created_at. The lifecycle test asserts create sets the
// timestamp by reading the WIRE getter Get<Entity>().GetCreatedAt(), which
// pb.<Entity> only defines when the message carries the field — so the
// assertion must NOT be emitted here, or the generated test would fail to
// compile. This is the dogfood scenario: a marked entity's minimal birth
// migration adds created_at/updated_at columns the proto never declared.
func TestBuildCRUDTestTemplateData_TimestampsWithoutProtoField(t *testing.T) {
	svc := ServiceDef{
		Name:       "PeptidesService",
		GoPackage:  "example.com/test/gen/proto/services/peptides/v1",
		PkgName:    "peptidesv1",
		ModulePath: "example.com/test",
	}
	crudMethods := []CRUDMethod{
		{
			Method: MethodTemplateData{Name: "CreatePeptide", InputType: "CreatePeptideRequest", OutputType: "CreatePeptideResponse"},
			// Timestamps:true (the peptides table has created_at), but the
			// wire Fields carry NO created_at — the proto message never
			// declared one.
			Entity:    EntityDef{Name: "Peptide", PkField: "id", PkGoType: "string", Timestamps: true, Fields: []EntityField{{Name: "id", GoName: "Id", GoType: "string"}, {Name: "name", GoName: "Name", GoType: "string"}}},
			Operation: "create",
		},
	}

	data := buildCRUDTestTemplateData(svc, crudMethods, "example.com/test", "", nil)
	if len(data.Entities) != 1 {
		t.Fatalf("expected 1 entity, got %d", len(data.Entities))
	}
	if data.Entities[0].HasTimestamps {
		t.Error("HasTimestamps must be false when the proto message has no created_at field — the wire GetCreatedAt() assertion would not compile")
	}
}

func TestClassifyFilterField(t *testing.T) {
	tests := []struct {
		name       string
		field      MessageFieldDef
		wantType   string
		wantGoName string
	}{
		{"search field", MessageFieldDef{Name: "search", ProtoType: "string"}, "search", "Search"},
		{"query field", MessageFieldDef{Name: "query", ProtoType: "string"}, "search", "Query"},
		{"q field", MessageFieldDef{Name: "q", ProtoType: "string"}, "search", "Q"},
		{"bool field", MessageFieldDef{Name: "active", ProtoType: "bool"}, "exact", "Active"},
		{"string field", MessageFieldDef{Name: "status", ProtoType: "string"}, "exact", "Status"},
		{"int32 field", MessageFieldDef{Name: "age", ProtoType: "int32"}, "exact", "Age"},
		{"optional field", MessageFieldDef{Name: "active", ProtoType: "bool", IsOptional: true}, "exact", "Active"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ff := classifyFilterField(tt.field)
			if ff.FilterType != tt.wantType {
				t.Errorf("FilterType = %q, want %q", ff.FilterType, tt.wantType)
			}
			if ff.GoName != tt.wantGoName {
				t.Errorf("GoName = %q, want %q", ff.GoName, tt.wantGoName)
			}
			if ff.IsOptional != tt.field.IsOptional {
				t.Errorf("IsOptional = %v, want %v", ff.IsOptional, tt.field.IsOptional)
			}
		})
	}
}

func TestClassifySkipField(t *testing.T) {
	skipFields := []string{"page_size", "page_token", "descending", "desc", "sort_order"}
	for _, f := range skipFields {
		if !classifySkipField(f) {
			t.Errorf("expected %q to be skipped", f)
		}
	}

	noSkipFields := []string{"search", "active", "status", "order_by"}
	for _, f := range noSkipFields {
		if classifySkipField(f) {
			t.Errorf("expected %q to NOT be skipped", f)
		}
	}
}

func TestBuildCRUDTemplateData_WithFilters(t *testing.T) {
	svc := ServiceDef{
		Name:       "PatientsService",
		GoPackage:  "example.com/test/gen/proto/services/patients/v1",
		PkgName:    "patientsv1",
		ModulePath: "example.com/test",
		Messages: map[string][]MessageFieldDef{
			"ListPatientsRequest": {
				{Name: "page_size", ProtoType: "int32"},
				{Name: "page_token", ProtoType: "string"},
				{Name: "active", ProtoType: "bool", IsOptional: true},
				{Name: "search", ProtoType: "string", IsOptional: true},
				{Name: "status", ProtoType: "string"},
				{Name: "order_by", ProtoType: "string"},
				{Name: "descending", ProtoType: "bool"},
			},
		},
	}

	crudMethods := []CRUDMethod{
		{
			Method: MethodTemplateData{Name: "ListPatients", InputType: "ListPatientsRequest", OutputType: "ListPatientsResponse"},
			Entity: EntityDef{Name: "Patient", PkField: "id", PkGoType: "int64",
				Fields: []EntityField{
					{Name: "id", GoName: "ID", ProtoType: "int64", GoType: "int64", Kind: FieldKindScalar},
					{Name: "name", GoName: "Name", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
					{Name: "status", GoName: "Status", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
					{Name: "active", GoName: "Active", ProtoType: "bool", GoType: "bool", Kind: FieldKindScalar},
				},
				// Filter validation runs against the introspected applied
				// schema (Columns), and search filters span SearchColumns.
				Columns: []EntityColumn{
					{Name: "id", Type: "int64", NotNull: true, IsPK: true},
					{Name: "name", Type: "string", NotNull: true},
					{Name: "status", Type: "string", NotNull: true},
					{Name: "active", Type: "bool", NotNull: true},
				},
				SearchColumns: []string{"name", "status"},
			},
			Operation: "list",
		},
	}

	data, err := buildCRUDTemplateData(svc, crudMethods, nil, "example.com/test")
	if err != nil {
		t.Fatalf("buildCRUDTemplateData() error = %v", err)
	}

	if !data.NeedsORM {
		t.Error("expected NeedsORM=true")
	}
	if !data.HasFilters {
		t.Error("expected HasFilters=true")
	}
	if !data.HasOrderBy {
		t.Error("expected HasOrderBy=true")
	}
	if len(data.CRUDMethods) != 1 {
		t.Fatalf("expected 1 method, got %d", len(data.CRUDMethods))
	}

	m := data.CRUDMethods[0]
	if !m.HasFilters {
		t.Error("expected method HasFilters=true")
	}
	if !m.HasOrderBy {
		t.Error("expected method HasOrderBy=true")
	}
	if len(m.FilterFields) != 3 {
		t.Fatalf("expected 3 filter fields, got %d", len(m.FilterFields))
	}

	// Check filter field classification
	expected := []struct {
		goName     string
		filterType string
		isOptional bool
	}{
		{"Active", "exact", true},
		{"Search", "search", true},
		{"Status", "exact", false},
	}
	for i, exp := range expected {
		ff := m.FilterFields[i]
		if ff.GoName != exp.goName {
			t.Errorf("filter[%d].GoName = %q, want %q", i, ff.GoName, exp.goName)
		}
		if ff.FilterType != exp.filterType {
			t.Errorf("filter[%d].FilterType = %q, want %q", i, ff.FilterType, exp.filterType)
		}
		if ff.IsOptional != exp.isOptional {
			t.Errorf("filter[%d].IsOptional = %v, want %v", i, ff.IsOptional, exp.isOptional)
		}
	}

	// The search filter spans the entity's declared SearchColumns; it never
	// maps to a column of its own.
	search := m.FilterFields[1]
	if len(search.SearchColumns) != 2 || search.SearchColumns[0] != "name" || search.SearchColumns[1] != "status" {
		t.Errorf("search.SearchColumns = %v, want [name status]", search.SearchColumns)
	}
}

// TestBuildCRUDTemplateData_FilterMappingErrors pins the loud-failure
// contract for filter mapping: an exact filter must name a DECLARED entity
// column, and a search filter needs at least one non-PK string column to
// span. Shipping either as a phantom-column query was the
// review-confirmed silence bug.
func TestBuildCRUDTemplateData_FilterMappingErrors(t *testing.T) {
	listMethod := func(entity EntityDef) []CRUDMethod {
		return []CRUDMethod{{
			Method:    MethodTemplateData{Name: "ListPatients", InputType: "ListPatientsRequest", OutputType: "ListPatientsResponse"},
			Entity:    entity,
			Operation: "list",
		}}
	}

	t.Run("exact_filter_with_no_matching_column_errors", func(t *testing.T) {
		svc := ServiceDef{
			Name: "PatientsService",
			Messages: map[string][]MessageFieldDef{
				"ListPatientsRequest": {
					{Name: "page_size", ProtoType: "int32"},
					{Name: "page_token", ProtoType: "string"},
					{Name: "favorite_color", ProtoType: "string"},
				},
			},
		}
		// The applied schema HAS columns — just not favorite_color. The
		// error must be about that specific filter field.
		entity := EntityDef{Name: "Patient", PkField: "id", PkGoType: "int64",
			Fields: []EntityField{
				{Name: "id", GoName: "ID", ProtoType: "int64", GoType: "int64", Kind: FieldKindScalar},
				{Name: "name", GoName: "Name", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
			},
			Columns: []EntityColumn{
				{Name: "id", Type: "int64", NotNull: true, IsPK: true},
				{Name: "name", Type: "string", NotNull: true},
			},
			SearchColumns: []string{"name"},
		}
		_, err := buildCRUDTemplateData(svc, listMethod(entity), nil, "example.com/test")
		if err == nil {
			t.Fatal("expected error for exact filter naming no declared column")
		}
		if !strings.Contains(err.Error(), "favorite_color") {
			t.Errorf("error should name the unmappable filter field; got: %v", err)
		}
	})

	t.Run("search_filter_with_no_string_columns_errors", func(t *testing.T) {
		svc := ServiceDef{
			Name: "PatientsService",
			Messages: map[string][]MessageFieldDef{
				"ListPatientsRequest": {
					{Name: "page_size", ProtoType: "int32"},
					{Name: "page_token", ProtoType: "string"},
					{Name: "search", ProtoType: "string"},
				},
			},
		}
		// No text columns in the applied schema → empty SearchColumns → a
		// search filter has nothing to span and must fail the generate.
		entity := EntityDef{Name: "Patient", PkField: "id", PkGoType: "int64",
			Fields: []EntityField{
				{Name: "id", GoName: "ID", ProtoType: "int64", GoType: "int64", Kind: FieldKindScalar},
				{Name: "age", GoName: "Age", ProtoType: "int32", GoType: "int32", Kind: FieldKindScalar},
			},
			Columns: []EntityColumn{
				{Name: "id", Type: "int64", NotNull: true, IsPK: true},
				{Name: "age", Type: "int64", NotNull: true},
			},
		}
		_, err := buildCRUDTemplateData(svc, listMethod(entity), nil, "example.com/test")
		if err == nil {
			t.Fatal("expected error for search filter on an entity with no string columns")
		}
		if !strings.Contains(err.Error(), "search") {
			t.Errorf("error should name the search filter; got: %v", err)
		}
	})
}

func TestGenerateCRUDHandlers_WithFilters(t *testing.T) {
	projectDir := t.TempDir()
	handlerDir := filepath.Join(projectDir, "internal", "handlers", "patients")
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write a service.go with Deps struct containing DB field
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

	svc := ServiceDef{
		Name:       "PatientsService",
		GoPackage:  "example.com/test/gen/proto/services/patients/v1",
		PkgName:    "patientsv1",
		ModulePath: "example.com/test",
		Methods: []Method{
			{Name: "ListPatients", InputType: "ListPatientsRequest", OutputType: "ListPatientsResponse"},
		},
		Messages: map[string][]MessageFieldDef{
			"ListPatientsRequest": {
				{Name: "page_size", ProtoType: "int32"},
				{Name: "page_token", ProtoType: "string"},
				{Name: "active", ProtoType: "bool", IsOptional: true},
				{Name: "search", ProtoType: "string", IsOptional: true},
				{Name: "order_by", ProtoType: "string"},
				{Name: "descending", ProtoType: "bool"},
			},
		},
	}

	entities := []EntityDef{{
		Name: "Patient", TableName: "patients", PkField: "id", PkGoType: "int64",
		Fields: []EntityField{
			{Name: "id", GoName: "ID", ProtoType: "int64", GoType: "int64", Kind: FieldKindScalar},
			{Name: "name", GoName: "Name", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
			{Name: "active", GoName: "Active", ProtoType: "bool", GoType: "bool", Kind: FieldKindScalar},
		},
		// Filter validation runs against the introspected applied schema.
		Columns: []EntityColumn{
			{Name: "id", Type: "int64", NotNull: true, IsPK: true},
			{Name: "name", Type: "string", NotNull: true},
			{Name: "active", Type: "bool", NotNull: true},
		},
		SearchColumns: []string{"name"},
	}}

	crudMethods := MatchCRUDMethods(svc, entities)

	_, err := GenerateCRUDHandlers(svc, crudMethods, "example.com/test", projectDir, nil)
	if err != nil {
		t.Fatalf("GenerateCRUDHandlers() error = %v", err)
	}

	opsPath := filepath.Join(handlerDir, "handlers_crud_ops_gen.go")
	data, err := os.ReadFile(opsPath)
	if err != nil {
		t.Fatalf("generated ops file not found: %v", err)
	}

	content := string(data)

	// Tier-1 file: must carry the generated banner.
	if !contains(content, "Code generated by forge. DO NOT EDIT.") {
		t.Error("expected DO NOT EDIT banner in ops file")
	}

	// Should contain the unexported list-op constructor with the entity's
	// declared-column allowlist.
	if !contains(content, "func (s *Service) crudListPatientsOp() crud.ListOp[") {
		t.Error("expected crudListPatientsOp constructor in ops file")
	}
	if !contains(content, "Columns:       db.PatientColumns") {
		t.Error("expected Columns: db.PatientColumns allowlist in ListOp literal")
	}

	// Per-entity conversion pair: the ops file carries patientToProto /
	// patientFromProto built from the wire-field x column intersection, and
	// the Pack seam calls it — no type-alias passthrough.
	// ToProto returns (*pb.Patient, error) so a corrupt-enum read surfaces
	// as CodeInternal instead of a silent UNSPECIFIED.
	if !contains(content, "func patientToProto(e *db.Patient) (*pb.Patient, error) {") {
		t.Error("expected patientToProto conversion function (error-returning) in ops file")
	}
	if !contains(content, "func patientFromProto(m *pb.Patient) (*db.Patient, error) {") {
		t.Error("expected patientFromProto conversion function in ops file")
	}
	if !contains(content, "m, err := patientToProto(it)") || !contains(content, "out = append(out, m)") {
		t.Error("expected list Pack to convert rows via patientToProto and propagate its error")
	}
	// No timestamp columns on this entity → no timestamppb import.
	if contains(content, "timestamppb") {
		t.Error("ops file must not import timestamppb when no conversion needs it")
	}

	// Should contain orm import
	if !contains(content, "pkg/orm") {
		t.Error("expected orm import in ops file")
	}

	// Search spans the entity's string columns via WhereILikeAny — never
	// a phantom "search" column.
	if !contains(content, `orm.WhereILikeAny([]string{"name"`) || !contains(content, `"%"+*req.Search+"%"`) {
		t.Error("expected WhereILikeAny over entity string columns for search filter")
	}
	if contains(content, `WhereILike("search"`) {
		t.Error("search filter must not hit a phantom search column via WhereILike")
	}
	if !contains(content, "WhereEq") {
		t.Error("expected WhereEq for exact filter")
	}

	// Should hand off ordering to the crud library (lifecycle lives in
	// pkg/crud; the op only wires request accessors).
	if !contains(content, "HasOrderBy:    true") {
		t.Error("expected HasOrderBy: true in generated ListOp literal")
	}
	if !contains(content, "req.OrderBy") {
		t.Error("expected req.OrderBy accessor closure in ops file")
	}
	if !contains(content, "req.Descending") {
		t.Error("expected req.Descending accessor closure in ops file")
	}

	// Should contain filter nil check for optional fields (inside the
	// per-RPC Filters closure).
	if !contains(content, "req.Active != nil") {
		t.Error("expected nil check for optional Active filter")
	}
	if !contains(content, "req.Search != nil") {
		t.Error("expected nil check for optional Search filter")
	}

	// Ops file must parse as valid Go.
	if _, err := parser.ParseFile(token.NewFileSet(), opsPath, content, parser.SkipObjectResolution); err != nil {
		t.Errorf("ops file is not valid Go: %v\n----\n%s", err, content)
	}

	// The RPC method itself lives in the user-owned shim and delegates.
	shimData, err := os.ReadFile(filepath.Join(handlerDir, "handlers_crud.go"))
	if err != nil {
		t.Fatalf("scaffolded handlers_crud.go not found: %v", err)
	}
	shim := string(shimData)
	if !contains(shim, "func (s *Service) ListPatients(") {
		t.Error("expected ListPatients method in handlers_crud.go")
	}
	if !contains(shim, "crud.HandleList(s.crudListPatientsOp())(ctx, req)") {
		t.Error("expected ListPatients to delegate to crud.HandleList(s.crudListPatientsOp())")
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "handlers_crud.go", shim, parser.SkipObjectResolution); err != nil {
		t.Errorf("handlers_crud.go is not valid Go: %v\n----\n%s", err, shim)
	}
}

// TestGenerateCRUDHandlers_NonPaginatedList pins Phase-4 #3: an AIP-132
// list RPC that carries filter + order_by fields but NO cursor pagination
// (no page_size/page_token) is still a Tier-1 generated list handler — it
// must NOT fall to the custom path. The generated ListOp wires the filter
// and order-by closures, HasPagination is false (no req.PageSize/PageToken
// dereferences, no NextPageToken in the response Pack), and the shim still
// delegates to crud.HandleList.
func TestGenerateCRUDHandlers_NonPaginatedList(t *testing.T) {
	projectDir := t.TempDir()
	handlerDir := filepath.Join(projectDir, "internal", "handlers", "widgets")
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		t.Fatal(err)
	}

	serviceGo := `package widgets

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

	svc := ServiceDef{
		Name:       "WidgetsService",
		GoPackage:  "example.com/test/gen/proto/services/widgets/v1",
		PkgName:    "widgetsv1",
		ModulePath: "example.com/test",
		Methods: []Method{
			{Name: "ListWidgets", InputType: "ListWidgetsRequest", OutputType: "ListWidgetsResponse"},
		},
		Messages: map[string][]MessageFieldDef{
			// No page_size/page_token — a plain filtered/ordered list.
			"ListWidgetsRequest": {
				{Name: "active", ProtoType: "bool"},
				{Name: "order_by", ProtoType: "string"},
				{Name: "descending", ProtoType: "bool"},
			},
			"ListWidgetsResponse": {
				{Name: "widgets", ProtoType: "message"},
			},
		},
	}

	entities := []EntityDef{{
		Name: "Widget", TableName: "widgets", PkField: "id", PkGoType: "int64",
		Fields: []EntityField{
			{Name: "id", GoName: "ID", ProtoType: "int64", GoType: "int64", Kind: FieldKindScalar},
			{Name: "active", GoName: "Active", ProtoType: "bool", GoType: "bool", Kind: FieldKindScalar},
		},
		Columns: []EntityColumn{
			{Name: "id", Type: "int64", NotNull: true, IsPK: true},
			{Name: "active", Type: "bool", NotNull: true},
		},
	}}

	crudMethods := MatchCRUDMethods(svc, entities)
	if _, err := GenerateCRUDHandlers(svc, crudMethods, "example.com/test", projectDir, nil); err != nil {
		t.Fatalf("GenerateCRUDHandlers() error = %v", err)
	}

	opsPath := filepath.Join(handlerDir, "handlers_crud_ops_gen.go")
	opsBytes, err := os.ReadFile(opsPath)
	if err != nil {
		t.Fatalf("non-paginated list must still produce a Tier-1 ops file: %v", err)
	}
	ops := string(opsBytes)

	// It's a real ListOp, not a custom stub.
	if !contains(ops, "func (s *Service) crudListWidgetsOp() crud.ListOp[") {
		t.Errorf("expected a generated ListOp for the non-paginated list; got:\n%s", ops)
	}
	if !contains(ops, "HasPagination: false") {
		t.Errorf("non-paginated list must set HasPagination: false; got:\n%s", ops)
	}
	if !contains(ops, "HasOrderBy:    true") {
		t.Errorf("expected HasOrderBy: true (order_by + descending present); got:\n%s", ops)
	}
	// No cursor dereferences and no NextPageToken packing.
	for _, bad := range []string{"req.PageSize", "req.PageToken", "NextPageToken:"} {
		if contains(ops, bad) {
			t.Errorf("non-paginated list ops must not reference %q; got:\n%s", bad, ops)
		}
	}
	// The exact filter still wires.
	if !contains(ops, `orm.WhereEq("active"`) {
		t.Errorf("expected the active filter wired via WhereEq; got:\n%s", ops)
	}
	if _, perr := parser.ParseFile(token.NewFileSet(), opsPath, ops, parser.SkipObjectResolution); perr != nil {
		t.Errorf("ops file is not valid Go: %v\n----\n%s", perr, ops)
	}

	// The shim still delegates to the crud library (not a custom body).
	shimBytes, err := os.ReadFile(filepath.Join(handlerDir, "handlers_crud.go"))
	if err != nil {
		t.Fatalf("read shim: %v", err)
	}
	shim := string(shimBytes)
	if !contains(shim, "crud.HandleList(s.crudListWidgetsOp())(ctx, req)") {
		t.Errorf("non-paginated list shim must delegate to crud.HandleList; got:\n%s", shim)
	}
	if contains(shim, "forge:custom-read-shape") {
		t.Errorf("non-paginated list must NOT be a custom-read-shape stub; got:\n%s", shim)
	}
}

// TestGenerateCRUDHandlers_OrderByWithoutDescending pins the review bug:
// a List request that declares `string order_by = N;` but NO companion
// `bool descending` field must NOT generate an OrderBy closure that
// dereferences req.Descending (which the proto type doesn't have) — that
// makes `go build` of the generated app fail. HasOrderBy is still true
// (order_by IS wired), but the closure returns a constant false
// (ascending) and never names req.Descending.
func TestGenerateCRUDHandlers_OrderByWithoutDescending(t *testing.T) {
	projectDir := t.TempDir()
	handlerDir := filepath.Join(projectDir, "internal", "handlers", "widgets")
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		t.Fatal(err)
	}

	serviceGo := `package widgets

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

	svc := ServiceDef{
		Name:       "WidgetsService",
		GoPackage:  "example.com/test/gen/proto/services/widgets/v1",
		PkgName:    "widgetsv1",
		ModulePath: "example.com/test",
		Methods: []Method{
			{Name: "ListWidgets", InputType: "ListWidgetsRequest", OutputType: "ListWidgetsResponse"},
		},
		Messages: map[string][]MessageFieldDef{
			// order_by present, descending ABSENT — the bug repro.
			"ListWidgetsRequest": {
				{Name: "order_by", ProtoType: "string"},
			},
			"ListWidgetsResponse": {
				{Name: "widgets", ProtoType: "message"},
			},
		},
	}

	entities := []EntityDef{{
		Name: "Widget", TableName: "widgets", PkField: "id", PkGoType: "int64",
		Fields: []EntityField{
			{Name: "id", GoName: "ID", ProtoType: "int64", GoType: "int64", Kind: FieldKindScalar},
		},
		Columns: []EntityColumn{
			{Name: "id", Type: "int64", NotNull: true, IsPK: true},
		},
	}}

	crudMethods := MatchCRUDMethods(svc, entities)
	if _, err := GenerateCRUDHandlers(svc, crudMethods, "example.com/test", projectDir, nil); err != nil {
		t.Fatalf("GenerateCRUDHandlers() error = %v", err)
	}

	opsPath := filepath.Join(handlerDir, "handlers_crud_ops_gen.go")
	opsBytes, err := os.ReadFile(opsPath)
	if err != nil {
		t.Fatalf("order_by-only list must still produce a Tier-1 ops file: %v", err)
	}
	ops := string(opsBytes)

	if !contains(ops, "HasOrderBy:    true") {
		t.Errorf("expected HasOrderBy: true (order_by present); got:\n%s", ops)
	}
	// The whole point of the fix: no dereference of the nonexistent field.
	if contains(ops, "req.Descending") {
		t.Errorf("order_by WITHOUT a descending field must NOT reference req.Descending; got:\n%s", ops)
	}
	if !contains(ops, "return req.OrderBy, false") {
		t.Errorf("expected the ascending-default OrderBy closure; got:\n%s", ops)
	}
	if _, perr := parser.ParseFile(token.NewFileSet(), opsPath, ops, parser.SkipObjectResolution); perr != nil {
		t.Errorf("ops file is not valid Go: %v\n----\n%s", perr, ops)
	}
}

func TestBuildCRUDTemplateData_NoFilters(t *testing.T) {
	svc := ServiceDef{
		Name:       "PatientsService",
		GoPackage:  "example.com/test/gen/proto/services/patients/v1",
		PkgName:    "patientsv1",
		ModulePath: "example.com/test",
		// No Messages — simulates no proto message field parsing
	}

	crudMethods := []CRUDMethod{
		{
			Method:    MethodTemplateData{Name: "ListPatients", InputType: "ListPatientsRequest", OutputType: "ListPatientsResponse"},
			Entity:    EntityDef{Name: "Patient", PkField: "id", PkGoType: "int64", Fields: []EntityField{{Name: "id", GoName: "ID", GoType: "int64"}}},
			Operation: "list",
		},
	}

	data, err := buildCRUDTemplateData(svc, crudMethods, nil, "example.com/test")
	if err != nil {
		t.Fatalf("buildCRUDTemplateData() error = %v", err)
	}

	m := data.CRUDMethods[0]
	if m.HasFilters {
		t.Error("expected HasFilters=false when no Messages")
	}
	if m.HasOrderBy {
		t.Error("expected HasOrderBy=false when no Messages")
	}
	if !m.HasPagination {
		t.Error("expected HasPagination=true (AIP-158 naming)")
	}
}

func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 &&
		len(s) >= len(substr) &&
		// Simple containment check
		findSubstring(s, substr)
}

func findSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestValidateCRUDShape_BespokeShape covers the gating that landed for
// crud-body-generator-shape-mismatch: when proto messages don't match the
// AIP-158 conventions the CRUD body template assumes, validateCRUDShape
// reports the mismatch with a human-readable reason so buildCRUDTemplateData
// can emit a CodeUnimplemented stub instead of non-compiling body code.
//
// The kalshi-trader port hit each of these shapes:
//   - ListMarketsRequest carries `Limit` (int32) and `Cursor` (string)
//     instead of AIP-158 `page_size` / `page_token`
//   - GetMarketRequest's key is `ticker` (string), not the entity's
//     default `id` PK
//   - CreateMarketResponse has `repeated Market markets = 1` (a fan-out)
//     instead of the AIP-shaped `Market market = 1`
//
// Each row asserts the rule that catches the specific divergence; the
// final "matches" row pins the happy path so a future refactor that
// accidentally tightens the rules trips a test rather than starving
// real CRUD generation.
func TestValidateCRUDShape_BespokeShape(t *testing.T) {
	type tc struct {
		name      string
		svc       ServiceDef
		cm        CRUDMethod
		wantOK    bool
		reasonHas string // substring expected in reason when !wantOK
	}

	patient := EntityDef{Name: "Patient", TableName: "patients", PkField: "id", PkGoType: "int64"}
	market := EntityDef{Name: "Market", TableName: "markets", PkField: "id", PkGoType: "int64"}

	cases := []tc{
		{
			// A list with NEITHER page_size nor page_token but a valid
			// repeated-entity response is a plain AIP-132 filtered list —
			// Tier-1 (non-paginated), NOT custom. (`limit`/`cursor` are
			// just extra scalar fields the body ignores.)
			name: "list_no_pagination_fields_is_tier1",
			svc: ServiceDef{
				Messages: map[string][]MessageFieldDef{
					"ListMarketsRequest": {
						{Name: "active", ProtoType: "bool"},
						{Name: "order_by", ProtoType: "string"},
					},
					"ListMarketsResponse": {
						{Name: "markets", ProtoType: "message"},
					},
				},
			},
			cm: CRUDMethod{
				Method:    MethodTemplateData{Name: "ListMarkets", InputType: "ListMarketsRequest", OutputType: "ListMarketsResponse"},
				Entity:    market,
				Operation: "list",
			},
			wantOK: true,
		},
		{
			// Exactly one half of cursor pagination is a bespoke
			// contract the cursor template can't honor → custom path.
			name: "list_half_pagination_is_custom",
			svc: ServiceDef{
				Messages: map[string][]MessageFieldDef{
					"ListMarketsRequest": {
						{Name: "page_size", ProtoType: "int32"},
						{Name: "cursor", ProtoType: "string"},
					},
					"ListMarketsResponse": {
						{Name: "markets", ProtoType: "message"},
					},
				},
			},
			cm: CRUDMethod{
				Method:    MethodTemplateData{Name: "ListMarkets", InputType: "ListMarketsRequest", OutputType: "ListMarketsResponse"},
				Entity:    market,
				Operation: "list",
			},
			wantOK:    false,
			reasonHas: "page_token",
		},
		{
			name: "list_missing_response_plural_field",
			svc: ServiceDef{
				Messages: map[string][]MessageFieldDef{
					"ListPatientsRequest": {
						{Name: "page_size", ProtoType: "int32"},
						{Name: "page_token", ProtoType: "string"},
					},
					"ListPatientsResponse": {
						{Name: "items", ProtoType: "message"},
					},
				},
			},
			cm: CRUDMethod{
				Method:    MethodTemplateData{Name: "ListPatients", InputType: "ListPatientsRequest", OutputType: "ListPatientsResponse"},
				Entity:    patient,
				Operation: "list",
			},
			wantOK:    false,
			reasonHas: "patients",
		},
		{
			name: "get_request_missing_pk",
			svc: ServiceDef{
				Messages: map[string][]MessageFieldDef{
					"GetMarketRequest": {
						{Name: "ticker", ProtoType: "string"},
					},
				},
			},
			cm: CRUDMethod{
				Method:    MethodTemplateData{Name: "GetMarket", InputType: "GetMarketRequest", OutputType: "GetMarketResponse"},
				Entity:    market,
				Operation: "get",
			},
			wantOK:    false,
			reasonHas: "id",
		},
		{
			name: "create_response_missing_entity_field",
			svc: ServiceDef{
				Messages: map[string][]MessageFieldDef{
					"CreateMarketRequest": {
						{Name: "ticker", ProtoType: "string"},
					},
					"CreateMarketResponse": {
						{Name: "markets", ProtoType: "message"},
					},
				},
			},
			cm: CRUDMethod{
				Method:    MethodTemplateData{Name: "CreateMarket", InputType: "CreateMarketRequest", OutputType: "CreateMarketResponse"},
				Entity:    market,
				Operation: "create",
			},
			wantOK:    false,
			reasonHas: "market",
		},
		{
			name: "update_request_missing_entity_message",
			svc: ServiceDef{
				Messages: map[string][]MessageFieldDef{
					"UpdateMarketRequest": {
						{Name: "ticker", ProtoType: "string"},
						{Name: "title", ProtoType: "string"},
					},
				},
			},
			cm: CRUDMethod{
				Method:    MethodTemplateData{Name: "UpdateMarket", InputType: "UpdateMarketRequest", OutputType: "UpdateMarketResponse"},
				Entity:    market,
				Operation: "update",
			},
			wantOK:    false,
			reasonHas: "Market message",
		},
		{
			// A message-typed update field carrying the entity's name in
			// MessageType matches: ProtoType is the unmatchable literal
			// "message" for every message field, and relying on it alone
			// produced a false mismatch on forge's own scaffold (F2 bug).
			name: "update_request_messagetype_matches",
			svc: ServiceDef{
				Messages: map[string][]MessageFieldDef{
					"UpdateMarketRequest": {
						{Name: "market", ProtoType: "message", MessageType: "Market"},
					},
				},
			},
			cm: CRUDMethod{
				Method:    MethodTemplateData{Name: "UpdateMarket", InputType: "UpdateMarketRequest", OutputType: "UpdateMarketResponse"},
				Entity:    market,
				Operation: "update",
			},
			wantOK: true,
		},
		{
			name: "update_request_qualified_messagetype_matches",
			svc: ServiceDef{
				Messages: map[string][]MessageFieldDef{
					"UpdateMarketRequest": {
						{Name: "market", ProtoType: "message", MessageType: "db.v1.Market"},
					},
				},
			},
			cm: CRUDMethod{
				Method:    MethodTemplateData{Name: "UpdateMarket", InputType: "UpdateMarketRequest", OutputType: "UpdateMarketResponse"},
				Entity:    market,
				Operation: "update",
			},
			wantOK: true,
		},
		{
			// The observed-fields diagnostic must show the real message
			// type (MessageType), not the useless literal "message" — a
			// wrong matcher self-incriminates.
			name: "mismatch_reason_shows_observed_message_types",
			svc: ServiceDef{
				Messages: map[string][]MessageFieldDef{
					"UpdateMarketRequest": {
						{Name: "update_mask", ProtoType: "message", MessageType: "google.protobuf.FieldMask"},
					},
				},
			},
			cm: CRUDMethod{
				Method:    MethodTemplateData{Name: "UpdateMarket", InputType: "UpdateMarketRequest", OutputType: "UpdateMarketResponse"},
				Entity:    market,
				Operation: "update",
			},
			wantOK:    false,
			reasonHas: "observed fields: update_mask google.protobuf.FieldMask",
		},
		{
			name: "delete_request_missing_pk",
			svc: ServiceDef{
				Messages: map[string][]MessageFieldDef{
					"DeleteMarketRequest": {
						{Name: "ticker", ProtoType: "string"},
					},
				},
			},
			cm: CRUDMethod{
				Method:    MethodTemplateData{Name: "DeleteMarket", InputType: "DeleteMarketRequest", OutputType: "DeleteMarketResponse"},
				Entity:    market,
				Operation: "delete",
			},
			wantOK:    false,
			reasonHas: "id",
		},
		{
			name: "no_messages_map_returns_legacy_ok",
			svc:  ServiceDef{}, // Messages is nil
			cm: CRUDMethod{
				Method:    MethodTemplateData{Name: "ListPatients", InputType: "ListPatientsRequest", OutputType: "ListPatientsResponse"},
				Entity:    patient,
				Operation: "list",
			},
			wantOK: true,
		},
		{
			name: "matches_aip158_shape",
			svc: ServiceDef{
				Messages: map[string][]MessageFieldDef{
					"ListPatientsRequest": {
						{Name: "page_size", ProtoType: "int32"},
						{Name: "page_token", ProtoType: "string"},
					},
					"ListPatientsResponse": {
						{Name: "patients", ProtoType: "message"},
						{Name: "next_page_token", ProtoType: "string"},
					},
				},
			},
			cm: CRUDMethod{
				Method:    MethodTemplateData{Name: "ListPatients", InputType: "ListPatientsRequest", OutputType: "ListPatientsResponse"},
				Entity:    patient,
				Operation: "list",
			},
			wantOK: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ok, reason := validateCRUDShape(c.svc, c.cm)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v (reason=%q)", ok, c.wantOK, reason)
			}
			if !c.wantOK && c.reasonHas != "" && !strings.Contains(reason, c.reasonHas) {
				t.Errorf("reason = %q, want substring %q", reason, c.reasonHas)
			}
		})
	}
}

// TestValidateCRUDShape_MultiWordEntity is the F1 regression: a multi-word
// entity (module_config) whose proto carries the entity-field names the
// scaffolder actually emits — snake_case "module_config" (create/get/update)
// and pluralized snake "module_configs" (list) — MUST pass validateCRUDShape.
//
// The historical bug: the detector compared the observed response fields
// against a concatenated-lowercase form ("moduleconfig" / "moduleconfigs"),
// which matches NEITHER the scaffolder's snake_case emit NOR the ops emitter's
// PascalCase Go field. So every multi-word entity failed the shape check and
// fell to the empty custom-read-shape stub. Routing all three sites through
// naming.EntityFieldName / EntityListFieldName makes the correct snake_case
// field the one the detector expects.
func TestValidateCRUDShape_MultiWordEntity(t *testing.T) {
	mc := EntityDef{Name: "ModuleConfig", TableName: "module_configs", PkField: "id", PkGoType: "string"}

	svc := ServiceDef{
		Messages: map[string][]MessageFieldDef{
			// create response: `ModuleConfig module_config = 1;`
			"CreateModuleConfigResponse": {
				{Name: "module_config", ProtoType: "message", MessageType: "ModuleConfig"},
			},
			// list request: AIP-158 cursor pair; response: repeated entity
			// as `module_configs`.
			"ListModuleConfigsRequest": {
				{Name: "page_size", ProtoType: "int32"},
				{Name: "page_token", ProtoType: "string"},
			},
			"ListModuleConfigsResponse": {
				{Name: "module_configs", ProtoType: "message", MessageType: "ModuleConfig"},
				{Name: "next_page_token", ProtoType: "string"},
			},
		},
	}

	cases := []struct {
		op   string
		in   string
		out  string
		name string
	}{
		{"create", "CreateModuleConfigRequest", "CreateModuleConfigResponse", "CreateModuleConfig"},
		{"list", "ListModuleConfigsRequest", "ListModuleConfigsResponse", "ListModuleConfigs"},
	}
	for _, c := range cases {
		t.Run(c.op, func(t *testing.T) {
			cm := CRUDMethod{
				Method:    MethodTemplateData{Name: c.name, InputType: c.in, OutputType: c.out},
				Entity:    mc,
				Operation: c.op,
			}
			ok, reason := validateCRUDShape(svc, cm)
			if !ok {
				t.Fatalf("multi-word %s entity must pass shape check, got mismatch: %s", c.op, reason)
			}
		})
	}

	// Guard the exact former defect: a response carrying ONLY the
	// concatenated-lowercase field ("moduleconfig", no underscore) is NOT the
	// shape the scaffolder emits, so it must now FAIL — proving the detector
	// keys off the snake_case field, not the concatenated form.
	badSvc := ServiceDef{
		Messages: map[string][]MessageFieldDef{
			"CreateModuleConfigResponse": {
				{Name: "moduleconfig", ProtoType: "message", MessageType: "ModuleConfig"},
			},
		},
	}
	ok, reason := validateCRUDShape(badSvc, CRUDMethod{
		Method:    MethodTemplateData{Name: "CreateModuleConfig", InputType: "CreateModuleConfigRequest", OutputType: "CreateModuleConfigResponse"},
		Entity:    mc,
		Operation: "create",
	})
	if ok {
		t.Fatalf("a concatenated-lowercase response field must NOT satisfy the detector (expected mismatch)")
	}
	if !strings.Contains(reason, "module_config") {
		t.Errorf("mismatch reason should name the expected snake_case field module_config; got %q", reason)
	}
}

// TestGenerateCRUDHandlers_StubsOnShapeMismatch is the full integration
// regression for crud-body-generator-shape-mismatch: when the input/output
// messages don't fit the CRUD conventions, the package must still compile
// (and satisfy the proto service interface). In the projection/implementation
// split that means: no op constructor is rendered into the Tier-1 ops file,
// and the user-owned handlers_crud.go shim carries a tagged
// CodeUnimplemented stub for each mismatched RPC rather than a delegation
// that would dereference fields the request type doesn't have.
//
// Each method pins one of the kalshi-trader shapes:
//   - ListMarkets: `limit/cursor` instead of `page_size/page_token`
//   - GetMarket:   `ticker` instead of `id`
//   - CreateMarket: response has `repeated Market markets` not `Market market`
//
// Together they prove buildCRUDTemplateData + the shim template fall back
// to a stub for every operation where validateCRUDShape returns false.
func TestGenerateCRUDHandlers_StubsOnShapeMismatch(t *testing.T) {
	projectDir := t.TempDir()
	handlerDir := filepath.Join(projectDir, "internal", "handlers", "markets")
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		t.Fatal(err)
	}

	serviceGo := `package markets

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

	svc := ServiceDef{
		Name:       "MarketsService",
		GoPackage:  "example.com/test/gen/proto/services/markets/v1",
		PkgName:    "marketsv1",
		ModulePath: "example.com/test",
		Methods: []Method{
			{Name: "ListMarkets", InputType: "ListMarketsRequest", OutputType: "ListMarketsResponse"},
			{Name: "GetMarket", InputType: "GetMarketRequest", OutputType: "GetMarketResponse"},
			{Name: "CreateMarket", InputType: "CreateMarketRequest", OutputType: "CreateMarketResponse"},
		},
		Messages: map[string][]MessageFieldDef{
			// bespoke kalshi-style pagination: page_size WITHOUT
			// page_token (a custom offset/cursor contract the cursor
			// template can't honor) → custom path, not a plain list.
			"ListMarketsRequest": {
				{Name: "page_size", ProtoType: "int32"},
				{Name: "cursor", ProtoType: "string"},
			},
			"ListMarketsResponse": {
				{Name: "markets", ProtoType: "message"},
			},
			// string Ticker key, not int64 Id
			"GetMarketRequest": {
				{Name: "ticker", ProtoType: "string"},
			},
			"GetMarketResponse": {
				{Name: "market", ProtoType: "message"},
			},
			// response holds repeated entity (fan-out create), not single
			"CreateMarketRequest": {
				{Name: "ticker", ProtoType: "string"},
			},
			"CreateMarketResponse": {
				{Name: "markets", ProtoType: "message"},
			},
		},
	}

	entities := []EntityDef{{
		Name: "Market", TableName: "markets", PkField: "id", PkGoType: "int64",
		Fields: []EntityField{
			{Name: "id", GoName: "ID", GoType: "int64", Kind: FieldKindScalar},
			{Name: "ticker", GoName: "Ticker", GoType: "string", Kind: FieldKindScalar},
		},
	}}

	crudMethods := MatchCRUDMethods(svc, entities)
	if _, err := GenerateCRUDHandlers(svc, crudMethods, "example.com/test", projectDir, nil); err != nil {
		t.Fatalf("GenerateCRUDHandlers() error = %v", err)
	}

	// 1. Even when every method is custom-shaped, the Tier-1 ops file is
	//    still written — it carries the <entity>ToProto/FromProto
	//    conversion pair the WIRED custom bodies project rows through. It
	//    must NOT carry any op constructor (no crud.*Op) or pull in the
	//    crud/middleware imports (which would be unused → won't compile).
	opsPath := filepath.Join(handlerDir, "handlers_crud_ops_gen.go")
	opsBytes, err := os.ReadFile(opsPath)
	if err != nil {
		t.Fatalf("ops file must exist to carry the conversion helpers the wired custom bodies use: %v", err)
	}
	ops := string(opsBytes)
	if !strings.Contains(ops, "func marketToProto(") {
		t.Errorf("ops file must carry marketToProto for the wired custom body; got:\n%s", ops)
	}
	if strings.Contains(ops, "crud.ListOp[") || strings.Contains(ops, "/pkg/middleware") || strings.Contains(ops, "/pkg/crud") {
		t.Errorf("all-custom ops file must carry only conversions (no op constructors / crud / middleware imports); got:\n%s", ops)
	}
	if _, perr := parser.ParseFile(token.NewFileSet(), "handlers_crud_ops_gen.go", ops, parser.SkipObjectResolution); perr != nil {
		t.Errorf("ops file is not valid Go: %v\n----\n%s", perr, ops)
	}

	// 2. The wired custom bodies land in the user-owned shim.
	data, err := os.ReadFile(filepath.Join(handlerDir, "handlers_crud.go"))
	if err != nil {
		t.Fatalf("read handlers_crud.go: %v", err)
	}
	content := string(data)

	// 3. Every method should be emitted (so the proto interface is satisfied).
	for _, want := range []string{"func (s *Service) ListMarkets(", "func (s *Service) GetMarket(", "func (s *Service) CreateMarket("} {
		if !strings.Contains(content, want) {
			t.Errorf("missing handler declaration %q in:\n%s", want, content)
		}
	}

	// 4. Each method must carry the forge:custom-read-shape marker (so
	//    the user can grep for it), and the reason must self-incriminate
	//    by listing the fields the matcher actually observed. The old
	//    FORGE_CRUD_SHAPE_MISMATCH spelling must NOT be emitted anymore
	//    (audit still recognizes it in existing files for one release).
	if mismatches := strings.Count(content, "forge:custom-read-shape"); mismatches < 3 {
		t.Errorf("expected >=3 forge:custom-read-shape markers, got %d in:\n%s", mismatches, content)
	}
	if strings.Contains(content, "FORGE_CRUD_SHAPE_MISMATCH") {
		t.Errorf("freshly generated output must use the forge:custom-read-shape marker, not the legacy spelling:\n%s", content)
	}
	if !strings.Contains(content, "observed fields:") {
		t.Errorf("expected mismatch reasons to carry the observed-fields diagnostic:\n%s", content)
	}

	// 5. None of the AIP-158 dereferences (or crud delegations) that would
	//    fail to compile against this proto should appear in the output.
	for _, bad := range []string{"req.PageSize", "req.PageToken", "crud.HandleList(", "crud.HandleGet(", "crud.HandleCreate("} {
		if strings.Contains(content, bad) {
			t.Errorf("unexpected %q in custom-shape output:\n%s", bad, content)
		}
	}

	// 6. The custom body is WIRED, not a blank stub: it must run a real
	//    query (db.ListMarket), project rows via the conversion helper,
	//    and carry the refine TODO — and must NOT 501 with CodeUnimplemented.
	if c := strings.Count(content, "db.ListMarket("); c != 3 {
		t.Errorf("expected 3 wired db.ListMarket calls (one per custom RPC), got %d in:\n%s", c, content)
	}
	if !strings.Contains(content, "marketToProto(row)") {
		t.Errorf("wired body must project rows via marketToProto; got:\n%s", content)
	}
	if !strings.Contains(content, "// TODO: refine this query") {
		t.Errorf("wired body must carry the refine TODO; got:\n%s", content)
	}
	if strings.Contains(content, "connect.CodeUnimplemented") {
		t.Errorf("wired custom body must not return CodeUnimplemented; got:\n%s", content)
	}

	// 7. The scaffolded file must parse as valid Go — the whole point of
	//    the wired fallback is that the package keeps compiling against
	//    the real proto shape.
	file, err := parser.ParseFile(token.NewFileSet(), "handlers_crud.go", content, parser.SkipObjectResolution)
	if err != nil {
		t.Errorf("generated file is not valid Go: %v\n----\n%s", err, content)
	}

	// 8. The wired body needs pkg/orm and internal/db imported (it builds
	//    orm.QueryOption filters and calls db.ListMarket), and must not
	//    import middleware, which it never calls — an unused import does
	//    not compile.
	//
	//    Read the IMPORT BLOCK, not the file text: the scaffolded header
	//    names the middleware import path in prose so a handler author
	//    knows what to add when they write the auth check, and a raw
	//    substring scan cannot tell that apart from a real import.
	imported := map[string]bool{}
	if file != nil {
		for _, spec := range file.Imports {
			path, uerr := strconv.Unquote(spec.Path.Value)
			if uerr != nil {
				t.Fatalf("unquote import %s: %v", spec.Path.Value, uerr)
			}
			imported[path] = true
		}
	}
	for _, want := range []string{"github.com/reliant-labs/forge/pkg/orm", "example.com/test/internal/db"} {
		if !imported[want] {
			t.Errorf("expected import of %q for the wired custom body; got imports %v\n%s", want, imported, content)
		}
	}
	if imported["example.com/test/pkg/middleware"] {
		t.Errorf("custom body must not import middleware (unused); got:\n%s", content)
	}
}

// TestGenerateCRUDHandlers_MultiWordEntity is the F1 end-to-end guard: a
// multi-word entity (module_config), with the exact proto shape `forge scaffold
// entity module_config ...` emits, must generate REAL CRUD — a wired ops file
// with crud.*Op constructors and a delegating shim — NOT the empty
// custom-read-shape stub that the concatenated-lowercase detector bug produced.
//
// It asserts the generated Go (a) parses, (b) references the PascalCase Go
// response fields protoc-gen-go derives from the snake proto fields
// (ModuleConfig / ModuleConfigs), and (c) actually runs a query and packs rows
// (db.ListModuleConfig + the ModuleConfigs: response field) — i.e. "returns
// rows", never CodeUnimplemented.
func TestGenerateCRUDHandlers_MultiWordEntity(t *testing.T) {
	projectDir := t.TempDir()
	handlerDir := filepath.Join(projectDir, "internal", "handlers", "settings")
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		t.Fatal(err)
	}

	serviceGo := `package settings

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

	// Proto shape mirrors what `forge scaffold` births for a
	// `// forge:entity message ModuleConfig { string name; bool enabled; }`
	// exactly: snake_case entity fields, AIP-158 list.
	svc := ServiceDef{
		Name:       "SettingsService",
		GoPackage:  "example.com/test/gen/proto/services/settings/v1",
		PkgName:    "settingsv1",
		ModulePath: "example.com/test",
		Methods: []Method{
			{Name: "CreateModuleConfig", InputType: "CreateModuleConfigRequest", OutputType: "CreateModuleConfigResponse"},
			{Name: "GetModuleConfig", InputType: "GetModuleConfigRequest", OutputType: "GetModuleConfigResponse"},
			{Name: "ListModuleConfigs", InputType: "ListModuleConfigsRequest", OutputType: "ListModuleConfigsResponse"},
			{Name: "UpdateModuleConfig", InputType: "UpdateModuleConfigRequest", OutputType: "UpdateModuleConfigResponse"},
			{Name: "DeleteModuleConfig", InputType: "DeleteModuleConfigRequest", OutputType: "DeleteModuleConfigResponse"},
		},
		Messages: map[string][]MessageFieldDef{
			"CreateModuleConfigRequest": {
				{Name: "name", ProtoType: "string"},
				{Name: "enabled", ProtoType: "bool"},
			},
			"CreateModuleConfigResponse": {
				{Name: "module_config", ProtoType: "message", MessageType: "ModuleConfig"},
			},
			"GetModuleConfigRequest": {
				{Name: "id", ProtoType: "string"},
			},
			"GetModuleConfigResponse": {
				{Name: "module_config", ProtoType: "message", MessageType: "ModuleConfig"},
			},
			"ListModuleConfigsRequest": {
				{Name: "page_size", ProtoType: "int32"},
				{Name: "page_token", ProtoType: "string"},
			},
			"ListModuleConfigsResponse": {
				{Name: "module_configs", ProtoType: "message", MessageType: "ModuleConfig"},
				{Name: "next_page_token", ProtoType: "string"},
			},
			"UpdateModuleConfigRequest": {
				{Name: "module_config", ProtoType: "message", MessageType: "ModuleConfig"},
				{Name: "update_mask", ProtoType: "message", MessageType: "google.protobuf.FieldMask"},
			},
			"UpdateModuleConfigResponse": {
				{Name: "module_config", ProtoType: "message", MessageType: "ModuleConfig"},
			},
			"DeleteModuleConfigRequest": {
				{Name: "id", ProtoType: "string"},
			},
			"DeleteModuleConfigResponse": {
				{Name: "id", ProtoType: "string"},
			},
		},
	}

	entities := []EntityDef{{
		Name: "ModuleConfig", TableName: "module_configs", PkField: "id", PkGoType: "string",
		Fields: []EntityField{
			{Name: "id", GoName: "Id", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
			{Name: "name", GoName: "Name", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
			{Name: "enabled", GoName: "Enabled", ProtoType: "bool", GoType: "bool", Kind: FieldKindScalar},
		},
		Columns: []EntityColumn{
			{Name: "id", Type: "string", NotNull: true, IsPK: true},
			{Name: "name", Type: "string", NotNull: true},
			{Name: "enabled", Type: "bool", NotNull: true},
		},
		SearchColumns: []string{"name"},
	}}

	crudMethods := MatchCRUDMethods(svc, entities)
	if len(crudMethods) != 5 {
		t.Fatalf("expected 5 matched CRUD methods, got %d", len(crudMethods))
	}
	if _, err := GenerateCRUDHandlers(svc, crudMethods, "example.com/test", projectDir, nil); err != nil {
		t.Fatalf("GenerateCRUDHandlers() error = %v", err)
	}

	// The ops file must carry REAL op constructors (not just conversions),
	// and reference the PascalCase Go response fields protoc-gen-go derives
	// from module_config / module_configs.
	opsBytes, err := os.ReadFile(filepath.Join(handlerDir, "handlers_crud_ops_gen.go"))
	if err != nil {
		t.Fatalf("read ops file: %v", err)
	}
	ops := string(opsBytes)
	if _, perr := parser.ParseFile(token.NewFileSet(), "handlers_crud_ops_gen.go", ops, parser.SkipObjectResolution); perr != nil {
		t.Fatalf("ops file is not valid Go: %v\n----\n%s", perr, ops)
	}
	for _, want := range []string{
		"crud.CreateOp[",
		"crud.ListOp[",
		"crud.GetOp[",
		"crud.UpdateOp[",
		"crud.DeleteOp[",
		"m, err := moduleconfigToProto(entity)", // create/get/update Pack: error-returning conversion
		"ModuleConfig: m",                       // create/get/update Pack: Go field name assigned from converted value
		"ModuleConfigs: out",                    // list Pack: pluralized Go field name
	} {
		if !strings.Contains(ops, want) {
			t.Errorf("ops file missing %q — multi-word CRUD did not wire real ops; got:\n%s", want, ops)
		}
	}

	// The shim delegates to the crud lifecycle for every op (no stub markers,
	// no CodeUnimplemented) — the whole point of "returns rows".
	shimBytes, err := os.ReadFile(filepath.Join(handlerDir, "handlers_crud.go"))
	if err != nil {
		t.Fatalf("read shim: %v", err)
	}
	shim := string(shimBytes)
	if _, perr := parser.ParseFile(token.NewFileSet(), "handlers_crud.go", shim, parser.SkipObjectResolution); perr != nil {
		t.Fatalf("shim is not valid Go: %v\n----\n%s", perr, shim)
	}
	for _, want := range []string{"crud.HandleCreate(", "crud.HandleList(", "crud.HandleGet(", "crud.HandleUpdate(", "crud.HandleDelete("} {
		if !strings.Contains(shim, want) {
			t.Errorf("shim missing delegation %q — a multi-word entity fell back to a stub; got:\n%s", want, shim)
		}
	}
	if strings.Contains(shim, "forge:custom-read-shape") {
		t.Errorf("multi-word entity must NOT produce custom-read-shape stubs; got:\n%s", shim)
	}
	if strings.Contains(shim, "connect.CodeUnimplemented") {
		t.Errorf("multi-word entity CRUD must run real ops, not 501; got:\n%s", shim)
	}
}

// TestGenerateCRUDHandlers_ShimScaffoldOnceAndAppend pins the ownership
// semantics of the implementation half of the split: handlers_crud.go is
// scaffolded exactly once (tracked Tier-2), the user's edits are never
// rewritten, and CRUD RPCs added later get their shim APPENDED without
// touching existing content.
func TestGenerateCRUDHandlers_ShimScaffoldOnceAndAppend(t *testing.T) {
	projectDir := t.TempDir()
	handlerDir := filepath.Join(projectDir, "internal", "handlers", "patients")
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		t.Fatal(err)
	}

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

	entities := []EntityDef{{
		Name: "Patient", TableName: "patients", PkField: "id", PkGoType: "string",
		Fields: []EntityField{
			{Name: "id", GoName: "Id", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
			{Name: "name", GoName: "Name", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
		},
	}}

	svcV1 := ServiceDef{
		Name:       "PatientsService",
		GoPackage:  "example.com/test/gen/proto/services/patients/v1",
		PkgName:    "patientsv1",
		ModulePath: "example.com/test",
		Methods: []Method{
			{Name: "CreatePatient", InputType: "CreatePatientRequest", OutputType: "CreatePatientResponse"},
		},
	}

	checksums.ResetSkipWrite()
	cs := &checksums.FileChecksums{}
	if _, err := GenerateCRUDHandlers(svcV1, MatchCRUDMethods(svcV1, entities), "example.com/test", projectDir, cs); err != nil {
		t.Fatalf("GenerateCRUDHandlers() first run error = %v", err)
	}

	shimRel := filepath.Join("internal", "handlers", "patients", "handlers_crud.go")
	shimPath := filepath.Join(projectDir, shimRel)
	first, err := os.ReadFile(shimPath)
	if err != nil {
		t.Fatalf("handlers_crud.go not scaffolded: %v", err)
	}
	if !contains(string(first), "func (s *Service) CreatePatient(") {
		t.Fatalf("expected CreatePatient shim in scaffold; got:\n%s", first)
	}
	// User-owned Tier-2 from birth: NO certification marker — that is
	// what keeps the cleanup sweeps and the Tier-1 stomp guard away —
	// and the path is marked written-this-run so the stale sweep never
	// targets it.
	if checksums.Verify(first) != checksums.NoMarker {
		t.Error("handlers_crud.go must not carry a forge:hash marker (user-owned Tier-2)")
	}
	if !checksums.WasWrittenThisRun(shimRel) {
		t.Error("handlers_crud.go not marked written-this-run")
	}

	// The user customizes the file: replaces the delegation body.
	customized := strings.Replace(string(first),
		"resp, err := crud.HandleCreate(s.crudCreatePatientOp())(ctx, req)",
		"// user customization marker\n\tresp, err := crud.HandleCreate(s.crudCreatePatientOp())(ctx, req)", 1)
	if customized == string(first) {
		t.Fatalf("expected CreatePatient shim to delegate via crud.HandleCreate; got:\n%s", first)
	}
	if err := os.WriteFile(shimPath, []byte(customized), 0o644); err != nil {
		t.Fatal(err)
	}

	// A new CRUD RPC appears in the proto: GetPatient.
	svcV2 := svcV1
	svcV2.Methods = []Method{
		{Name: "CreatePatient", InputType: "CreatePatientRequest", OutputType: "CreatePatientResponse"},
		{Name: "GetPatient", InputType: "GetPatientRequest", OutputType: "GetPatientResponse"},
	}
	if _, err := GenerateCRUDHandlers(svcV2, MatchCRUDMethods(svcV2, entities), "example.com/test", projectDir, cs); err != nil {
		t.Fatalf("GenerateCRUDHandlers() second run error = %v", err)
	}

	second, err := os.ReadFile(shimPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(second)

	// Existing content (including the user's edit) is preserved verbatim
	// as a prefix-or-better: never modified, never duplicated.
	if !contains(got, "// user customization marker") {
		t.Errorf("user customization was lost on regen:\n%s", got)
	}
	if c := strings.Count(got, "func (s *Service) CreatePatient("); c != 1 {
		t.Errorf("CreatePatient declared %d times after append, want exactly 1", c)
	}
	// The new RPC's shim was appended.
	if !contains(got, "func (s *Service) GetPatient(") {
		t.Errorf("expected GetPatient shim appended on second run:\n%s", got)
	}
	if !contains(got, "crud.HandleGet(s.crudGetPatientOp())(ctx, req)") {
		t.Errorf("appended GetPatient shim must delegate to crud.HandleGet:\n%s", got)
	}
	// And the result still parses as Go (import insertion + append must
	// not corrupt the file).
	if _, err := parser.ParseFile(token.NewFileSet(), "handlers_crud.go", got, parser.SkipObjectResolution); err != nil {
		t.Errorf("handlers_crud.go is not valid Go after append: %v\n----\n%s", err, got)
	}

	// A third run with no new methods is a no-op.
	if _, err := GenerateCRUDHandlers(svcV2, MatchCRUDMethods(svcV2, entities), "example.com/test", projectDir, cs); err != nil {
		t.Fatalf("GenerateCRUDHandlers() third run error = %v", err)
	}
	third, err := os.ReadFile(shimPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(third) != got {
		t.Error("handlers_crud.go changed on a run with no new CRUD methods")
	}

	// The Tier-1 ops file regenerates every run and carries both ops.
	ops, err := os.ReadFile(filepath.Join(handlerDir, "handlers_crud_ops_gen.go"))
	if err != nil {
		t.Fatalf("handlers_crud_ops_gen.go not written: %v", err)
	}
	for _, want := range []string{"crudCreatePatientOp", "crudGetPatientOp"} {
		if !contains(string(ops), want) {
			t.Errorf("expected %s in ops file", want)
		}
	}
}

// TestEnsureDepsDBField_InjectsValidateDepsCheck pins the J-round boot
// gate: when CRUD codegen injects the DB dep, it must ALSO gate it in
// validateDeps — otherwise a missing DATABASE_URL flows a nil
// orm.Context into the handlers and the first RPC panics instead of the
// boot failing with an actionable error.
func TestEnsureDepsDBField_InjectsValidateDepsCheck(t *testing.T) {
	projectDir := t.TempDir()
	handlerDir := filepath.Join(projectDir, "internal", "handlers", "patients")
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Scaffold-shaped service.go: bare-Deps trio + both extension markers.
	serviceGo := `package patients

import (
	"fmt"
	"log/slog"
)

type Deps struct {
	Logger *slog.Logger
	// Add your dependencies here.
}

func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("PatientsService: Deps.Logger is required")
	}
	// Add checks for your required Deps fields here. Example:
	//   if d.Repo == nil { return fmt.Errorf("PatientsService: Deps.Repo is required") }
	return nil
}

type Service struct {
	deps Deps
}
`
	servicePath := filepath.Join(handlerDir, "service.go")
	if err := os.WriteFile(servicePath, []byte(serviceGo), 0o644); err != nil {
		t.Fatal(err)
	}

	svc := ServiceDef{
		Name:       "PatientsService",
		GoPackage:  "example.com/test/gen/proto/services/patients/v1",
		PkgName:    "patientsv1",
		ModulePath: "example.com/test",
		Methods: []Method{
			{Name: "ListPatients", InputType: "ListPatientsRequest", OutputType: "ListPatientsResponse"},
		},
	}
	entities := []EntityDef{
		{
			Name: "Patient", TableName: "patients", PkField: "id", PkGoType: "int64",
			Fields: []EntityField{
				{Name: "id", GoName: "ID", GoType: "int64", Kind: FieldKindScalar},
				{Name: "name", GoName: "Name", GoType: "string", Kind: FieldKindScalar},
			},
		},
	}

	if _, err := GenerateCRUDHandlers(svc, MatchCRUDMethods(svc, entities), "example.com/test", projectDir, nil); err != nil {
		t.Fatalf("GenerateCRUDHandlers() error = %v", err)
	}

	content, err := os.ReadFile(servicePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "d.DB == nil") {
		t.Errorf("validateDeps should gate the injected DB dep at boot; got:\n%s", content)
	}

	// Idempotent: a second pass must not duplicate the check.
	if _, err := GenerateCRUDHandlers(svc, MatchCRUDMethods(svc, entities), "example.com/test", projectDir, nil); err != nil {
		t.Fatalf("second GenerateCRUDHandlers() error = %v", err)
	}
	content, err = os.ReadFile(servicePath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(content), "d.DB == nil"); got != 1 {
		t.Errorf("d.DB == nil appears %d times after regen, want exactly 1:\n%s", got, content)
	}
}

// TestImplementedMethodsIn distinguishes real generated bodies from
// shape-mismatch stubs in a retired handlers_crud_gen.go — the input to
// the LOUD "replacing a previously generated implementation" warning.
// Both marker spellings (current + pre-rename) must mark a method as a
// stub, not an implementation.
func TestImplementedMethodsIn(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	legacy := `// Code generated by forge. DO NOT EDIT.
package api

// ListTrades implements the ListTrades RPC.
//
// FORGE_CRUD_SHAPE_MISMATCH: request shaped by ticker+limit
//
// Replace this stub.
func (s *Service) ListTrades(ctx context.Context, req *connect.Request[pb.ListTradesRequest]) (*connect.Response[pb.ListTradesResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

// GetTrade implements the GetTrade RPC.
//
// forge:custom-read-shape: bespoke key
func (s *Service) GetTrade(ctx context.Context, req *connect.Request[pb.GetTradeRequest]) (*connect.Response[pb.GetTradeResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

// CreateTrade handles the CreateTrade RPC (CRUD create).
func (s *Service) CreateTrade(ctx context.Context, req *connect.Request[pb.CreateTradeRequest]) (*connect.Response[pb.CreateTradeResponse], error) {
	return realBody(ctx, req)
}
`
	path := filepath.Join(dir, "handlers_crud_gen.go")
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	got := implementedMethodsIn(path)
	if got["ListTrades"] {
		t.Error("ListTrades carried the legacy mismatch marker — must not count as implemented")
	}
	if got["GetTrade"] {
		t.Error("GetTrade carried the current mismatch marker — must not count as implemented")
	}
	if !got["CreateTrade"] {
		t.Error("CreateTrade had a real generated body — must count as implemented")
	}

	// Missing file (the common post-migration case) → nil, no error.
	if m := implementedMethodsIn(filepath.Join(dir, "absent.go")); m != nil {
		t.Errorf("missing file must return nil, got %v", m)
	}
}

// TestWarnCustomReadShapeStubs_IsLoud captures stdout and asserts one
// warning line per stubbed RPC, with the stronger REPLACING phrasing
// when the RPC previously had a real generated implementation — the
// exact near-miss a downstream agent filed after live traffic almost
// hit a silently re-stubbed procedure.
func TestWarnCustomReadShapeStubs_IsLoud(t *testing.T) {
	methods := []CRUDMethodTemplateData{
		{MethodName: "ListTrades", ShapeMismatch: true, MismatchReason: "ticker+limit"},
		{MethodName: "GetTrade", ShapeMismatch: true, MismatchReason: "bespoke key"},
		{MethodName: "CreateTrade"}, // wired — must stay silent
	}

	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	warnCustomReadShapeStubs(methods, "handlers/api/handlers_crud.go", map[string]bool{"ListTrades": true})
	os.Stdout = orig
	_ = w.Close()
	out, _ := io.ReadAll(r)

	s := string(out)
	if !strings.Contains(s, "ListTrades: REPLACING a previously generated implementation") {
		t.Errorf("previously implemented RPC must get the REPLACING warning, got:\n%s", s)
	}
	if !strings.Contains(s, "GetTrade: custom read shape (bespoke key)") {
		t.Errorf("fresh stub must get the one-line custom-read-shape notice naming the RPC, got:\n%s", s)
	}
	if strings.Contains(s, "CreateTrade") {
		t.Errorf("wired RPCs must not be warned about, got:\n%s", s)
	}
}

// TestBuildEntityConv pins the per-entity proto<->struct conversion pair:
// entity structs are projections of the APPLIED schema (time.Time,
// pointers for nullable columns, native slices), wire messages are the
// proto truth (timestamppb, repeated fields), and BuildEntityConv maps
// the intersection of wire fields and columns by name. Wire-only fields
// never reach the database; column-only fields never leak onto the wire.
func TestBuildEntityConv(t *testing.T) {
	entity := EntityDef{
		Name: "Patient",
		Fields: []EntityField{
			{Name: "id", GoName: "Id", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
			{Name: "nickname", GoName: "Nickname", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
			{Name: "created_at", GoName: "CreatedAt", ProtoType: "message", MessageType: "google.protobuf.Timestamp", GoType: "*timestamppb.Timestamp", Kind: FieldKindTimestamp},
			{Name: "tags", GoName: "Tags", ProtoType: "string", GoType: "[]string", Kind: FieldKindRepeatedScalar},
			{Name: "display", GoName: "Display", ProtoType: "string", GoType: "string", Kind: FieldKindScalar}, // wire-only
		},
		Columns: []EntityColumn{
			{Name: "id", Type: "string", NotNull: true, IsPK: true},
			{Name: "nickname", Type: "string"}, // nullable → *string struct field
			{Name: "created_at", Type: "time", NotNull: true},
			{Name: "tags", Type: "string", IsArray: true, NotNull: true},
			{Name: "internal_notes", Type: "string"}, // column-only: never on the wire
		},
	}

	conv, _ := BuildEntityConv(ServiceDef{}, entity)
	if conv.EntityName != "Patient" || conv.EntityLower != "patient" {
		t.Errorf("conv names = %q/%q, want Patient/patient", conv.EntityName, conv.EntityLower)
	}

	toProto := strings.Join(conv.ToProtoAssigns, "\n")
	fromProto := strings.Join(conv.FromProtoAssigns, "\n")

	// Plain NOT NULL scalar: direct assignment both ways.
	if !strings.Contains(toProto, "m.Id = e.ID") {
		t.Error("missing direct toProto assignment for id")
	}
	if !strings.Contains(fromProto, "e.ID = m.Id") {
		t.Error("missing direct fromProto assignment for id")
	}

	// Nullable column is a pointer struct field: deref on the way out,
	// pointer-wrap on the way in.
	if !strings.Contains(toProto, "if e.Nickname != nil {") || !strings.Contains(toProto, "m.Nickname = *e.Nickname") {
		t.Errorf("nullable column should deref the pointer field toProto; got:\n%s", toProto)
	}
	if !strings.Contains(fromProto, "v := m.Nickname") || !strings.Contains(fromProto, "e.Nickname = &v") {
		t.Errorf("nullable column should pointer-wrap fromProto; got:\n%s", fromProto)
	}

	// time column <-> timestamppb wire field: the conversion is the ONLY
	// place timestamppb appears (the struct field is time.Time).
	if !strings.Contains(toProto, "if !e.CreatedAt.IsZero() {") || !strings.Contains(toProto, "m.CreatedAt = timestamppb.New(e.CreatedAt)") {
		t.Errorf("NOT NULL time column should convert via timestamppb.New under an IsZero guard; got:\n%s", toProto)
	}
	if !strings.Contains(fromProto, "if m.CreatedAt != nil {") || !strings.Contains(fromProto, "e.CreatedAt = m.CreatedAt.AsTime()") {
		t.Errorf("timestamp wire field should convert via AsTime under a nil guard; got:\n%s", fromProto)
	}

	// Array column: copied as a native slice both ways.
	if !strings.Contains(toProto, "m.Tags = append([]string(nil), e.Tags...)") {
		t.Errorf("array column should copy as a native slice toProto; got:\n%s", toProto)
	}
	if !strings.Contains(fromProto, "e.Tags = append([]string(nil), m.Tags...)") {
		t.Errorf("array column should copy as a native slice fromProto; got:\n%s", fromProto)
	}

	// Wire-only field: documented and dropped, never assigned.
	if !strings.Contains(toProto, `// Display: wire-only field (no "display" column in the applied schema)`) {
		t.Errorf("wire-only field should be dropped with an explanatory comment; got:\n%s", toProto)
	}
	if strings.Contains(fromProto, "e.Display") {
		t.Error("wire-only field must never be assigned onto the entity struct")
	}

	// Column-only field: never appears on the wire.
	if strings.Contains(toProto, "InternalNotes") || strings.Contains(fromProto, "InternalNotes") {
		t.Error("column-only field must never appear in conversions")
	}

	// Timestamp conversions gate the timestamppb import.
	if !ConvNeedsTimestamppb([]EntityConvTemplateData{conv}) {
		t.Error("ConvNeedsTimestamppb should be true when a conversion uses timestamppb")
	}
	plain, _ := BuildEntityConv(ServiceDef{}, EntityDef{
		Name:    "Tag",
		Fields:  []EntityField{{Name: "id", GoName: "Id", ProtoType: "string", GoType: "string", Kind: FieldKindScalar}},
		Columns: []EntityColumn{{Name: "id", Type: "string", NotNull: true, IsPK: true}},
	})
	if ConvNeedsTimestamppb([]EntityConvTemplateData{plain}) {
		t.Error("ConvNeedsTimestamppb should be false without timestamp conversions")
	}
}

// TestBuildEntityConv_OptionalScalarPairing pins the explicit-presence
// pairing: a proto3 `optional` scalar is a POINTER field on the
// protobuf-go struct (*string), and a nullable column is a POINTER
// field on the entity struct — so the conversion must copy pointer to
// pointer nil-safely in both directions. Treating the wire side as a
// plain scalar emitted `e.X = &v` of an already-pointer (**string) and
// `m.X = *e.X` (string into a *string wire field) — the generated ops
// file did not compile.
func TestBuildEntityConv_OptionalScalarPairing(t *testing.T) {
	entity := EntityDef{
		Name: "Order",
		Fields: []EntityField{
			{Name: "failure_reason", GoName: "FailureReason", ProtoType: "string", GoType: "string", Kind: FieldKindScalar, Optional: true},
			{Name: "priority", GoName: "Priority", ProtoType: "int32", GoType: "int32", Kind: FieldKindScalar, Optional: true},
			{Name: "note", GoName: "Note", ProtoType: "string", GoType: "string", Kind: FieldKindScalar, Optional: true},
			{Name: "completed_at", GoName: "CompletedAt", ProtoType: "message", MessageType: "google.protobuf.Timestamp", GoType: "*timestamppb.Timestamp", Kind: FieldKindTimestamp, Optional: true},
			{Name: "name", GoName: "Name", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
		},
		Columns: []EntityColumn{
			{Name: "failure_reason", Type: "string"},      // nullable → *string entity field
			{Name: "priority", Type: "int64"},             // nullable numeric → *int64 entity field
			{Name: "note", Type: "string", NotNull: true}, // optional wire field over a NOT NULL column
			{Name: "completed_at", Type: "time"},          // nullable → *time.Time entity field
			{Name: "name", Type: "string"},                // PLAIN wire scalar over a nullable column
		},
	}

	conv, _ := BuildEntityConv(ServiceDef{}, entity)
	toProto := strings.Join(conv.ToProtoAssigns, "\n")
	fromProto := strings.Join(conv.FromProtoAssigns, "\n")

	// optional string <-> nullable TEXT: nil-safe pointer copy BOTH ways
	// (never &v of an already-pointer, never a bare deref into a *string
	// wire field).
	if !strings.Contains(fromProto, "if m.FailureReason != nil {\n\t\tv := *m.FailureReason\n\t\te.FailureReason = &v\n\t}") {
		t.Errorf("optional scalar fromProto should nil-safe copy the pointer; got:\n%s", fromProto)
	}
	if !strings.Contains(toProto, "if e.FailureReason != nil {\n\t\tv := *e.FailureReason\n\t\tm.FailureReason = &v\n\t}") {
		t.Errorf("optional scalar toProto should nil-safe copy the pointer; got:\n%s", toProto)
	}

	// optional int32 <-> nullable BIGINT: numeric cast inside the nil
	// guard. int32 widens to a BIGINT column losslessly, so the WRITE is a
	// bare cast; the READ narrows, so it carries a range check — a BIGINT
	// holding 3000000000 is not an int32, and the cast alone would hand
	// the client -1294967296.
	if !strings.Contains(fromProto, "if m.Priority != nil {\n\t\tv := int64(*m.Priority)\n\t\te.Priority = &v\n\t}") {
		t.Errorf("optional numeric fromProto should cast inside the nil guard; got:\n%s", fromProto)
	}
	if !strings.Contains(toProto, "v := int32(*e.Priority)\n\t\tm.Priority = &v") {
		t.Errorf("optional numeric toProto should cast inside the nil guard; got:\n%s", toProto)
	}
	if !strings.Contains(toProto, "if *e.Priority < -2147483648 || *e.Priority > 2147483647 {") {
		t.Errorf("the narrowing read should be range-checked, not a bare cast; got:\n%s", toProto)
	}

	// optional wire scalar over a NOT NULL column: deref in under a nil
	// guard; pointer-wrap out.
	if !strings.Contains(fromProto, "if m.Note != nil {\n\t\te.Note = *m.Note\n\t}") {
		t.Errorf("optional scalar over NOT NULL column fromProto should deref under a nil guard; got:\n%s", fromProto)
	}
	if !strings.Contains(toProto, "v := e.Note\n\t\tm.Note = &v") {
		t.Errorf("optional scalar over NOT NULL column toProto should pointer-wrap; got:\n%s", toProto)
	}

	// optional google.protobuf.Timestamp is NOT a promoted scalar — the
	// timestamp path already handles presence via the message pointer
	// (regression pin: promotion must not touch non-scalar kinds).
	if !strings.Contains(fromProto, "if m.CompletedAt != nil {\n\t\tt := m.CompletedAt.AsTime()\n\t\te.CompletedAt = &t\n\t}") {
		t.Errorf("optional timestamp fromProto must keep the AsTime pairing; got:\n%s", fromProto)
	}
	if !strings.Contains(toProto, "if e.CompletedAt != nil {\n\t\tm.CompletedAt = timestamppb.New(*e.CompletedAt)\n\t}") {
		t.Errorf("optional timestamp toProto must keep the timestamppb pairing; got:\n%s", toProto)
	}

	// PLAIN wire scalar over a nullable column: the empty-value mapping
	// stays (regression pin — promotion fires only on Optional fields).
	if !strings.Contains(fromProto, "v := m.Name\n\t\te.Name = &v") {
		t.Errorf("plain scalar over nullable column fromProto must stay pointer-wrap of the value; got:\n%s", fromProto)
	}
	if !strings.Contains(toProto, "if e.Name != nil {\n\t\tm.Name = *e.Name\n\t}") {
		t.Errorf("plain scalar over nullable column toProto must stay deref under nil guard; got:\n%s", toProto)
	}

	// The Optional bit itself must survive both entity-field constructors
	// — dropping it there is exactly how the bug hid: the deep-schema
	// path built the field, BuildEntityConv saw a plain scalar.
	if f := schemaFieldToEntityField(SchemaFieldDef{Name: "x", Kind: "string", Optional: true}); !f.Optional {
		t.Error("schemaFieldToEntityField must carry SchemaFieldDef.Optional onto the EntityField")
	}
	if f := messageFieldToEntityField(MessageFieldDef{Name: "x", ProtoType: "string", IsOptional: true}); !f.Optional {
		t.Error("messageFieldToEntityField must carry MessageFieldDef.IsOptional onto the EntityField")
	}
}

// TestBuildCRUDTemplateData_CreateAssigns pins the replacement for the
// retired CreateFieldData/CreateFields shape: create-request fields are
// precomputed into CreateAssigns statements that map request fields onto
// the entity struct by column name, with request-only fields dropped
// loudly (a comment in the generated code) rather than silently.
func TestBuildCRUDTemplateData_CreateAssigns(t *testing.T) {
	svc := ServiceDef{
		Name: "PatientsService",
		Messages: map[string][]MessageFieldDef{
			"CreatePatientRequest": {
				{Name: "name", ProtoType: "string"},
				{Name: "nickname", ProtoType: "string", IsOptional: true},
				{Name: "favorite_color", ProtoType: "string"}, // request-only
			},
			"CreatePatientResponse": {
				{Name: "patient", ProtoType: "message", MessageType: "Patient"},
			},
		},
	}
	crudMethods := []CRUDMethod{{
		Method: MethodTemplateData{Name: "CreatePatient", InputType: "CreatePatientRequest", OutputType: "CreatePatientResponse"},
		Entity: EntityDef{Name: "Patient", PkField: "id", PkGoType: "string",
			Fields: []EntityField{
				{Name: "id", GoName: "Id", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
				{Name: "name", GoName: "Name", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
				{Name: "nickname", GoName: "Nickname", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
			},
			Columns: []EntityColumn{
				{Name: "id", Type: "string", NotNull: true, IsPK: true},
				{Name: "name", Type: "string", NotNull: true},
				{Name: "nickname", Type: "string"}, // nullable
			},
		},
		Operation: "create",
	}}

	data, err := buildCRUDTemplateData(svc, crudMethods, nil, "example.com/test")
	if err != nil {
		t.Fatalf("buildCRUDTemplateData() error = %v", err)
	}
	if len(data.CRUDMethods) != 1 {
		t.Fatalf("expected 1 method, got %d", len(data.CRUDMethods))
	}
	assigns := strings.Join(data.CRUDMethods[0].CreateAssigns, "\n")

	// NOT NULL string column ← plain request field.
	if !strings.Contains(assigns, "e.Name = req.Name") {
		t.Errorf("missing direct create assign for name; got:\n%s", assigns)
	}
	// proto3 optional scalar surfaces as a pointer on the request and the
	// nullable column is a pointer struct field: nil-guarded copy.
	if !strings.Contains(assigns, "if req.Nickname != nil {") {
		t.Errorf("optional request field should be nil-guarded; got:\n%s", assigns)
	}
	// Request-only field: dropped with an explanatory comment.
	if !strings.Contains(assigns, `// FavoriteColor: request-only field (no "favorite_color" column in the applied schema)`) {
		t.Errorf("request-only field should be dropped with a comment; got:\n%s", assigns)
	}
	if strings.Contains(assigns, "e.FavoriteColor =") {
		t.Error("request-only field must never be assigned onto the entity struct")
	}

	// The conversion pair rides along for the entity, and nothing here
	// needs timestamppb.
	if len(data.Entities) != 1 || data.Entities[0].EntityName != "Patient" {
		t.Fatalf("expected the Patient conversion pair in Entities, got %+v", data.Entities)
	}
	if data.NeedsTimestamppb {
		t.Error("NeedsTimestamppb should be false without timestamp conversions")
	}
}
