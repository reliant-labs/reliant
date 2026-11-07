package codegen

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
)

// Which CRUD ops forge emits must not depend on WHICH FILE of the handler
// package declares the RPC method.
//
// Dogfood evidence (roofers, invoices): an agent moved CreatePayment —
// still the scaffolded delegation `crud.HandleCreate(s.crudCreatePaymentOp())`
// — from handlers_crud.go into a sibling invoice_ops.go. The next generate
// read "declared outside handlers_crud.go" as "implemented by hand", dropped
// crudCreatePaymentOp from handlers_crud_ops_gen.go, failed its own build
// validation (`s.crudCreatePaymentOp undefined`) and reverted 70 files.

// opDemandFixture lays out a handler package with service.go wired for
// CRUD and returns the service + entity the tests generate against.
func opDemandFixture(t *testing.T) (projectDir, handlerDir string, svc ServiceDef, entities []EntityDef) {
	t.Helper()
	projectDir = t.TempDir()
	handlerDir = filepath.Join(projectDir, "internal", "handlers", "patients")
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
	svc = ServiceDef{
		Name:       "PatientsService",
		GoPackage:  "example.com/test/gen/proto/services/patients/v1",
		PkgName:    "patientsv1",
		ModulePath: "example.com/test",
		Methods: []Method{
			{Name: "CreatePatient", InputType: "CreatePatientRequest", OutputType: "CreatePatientResponse"},
			{Name: "GetPatient", InputType: "GetPatientRequest", OutputType: "GetPatientResponse"},
		},
	}
	entities = []EntityDef{{
		Name: "Patient", TableName: "patients", PkField: "id", PkGoType: "string",
		Fields: []EntityField{
			{Name: "id", GoName: "Id", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
			{Name: "name", GoName: "Name", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
		},
	}}
	return projectDir, handlerDir, svc, entities
}

// cutMethod removes `func (s *Service) <name>(` and its body (plus its doc
// comment) from src, returning the remaining file and the cut declaration.
func cutMethod(t *testing.T, src, name string) (rest, method string) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "handlers_crud.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse handlers_crud.go: %v\n%s", err, src)
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || fn.Name.Name != name {
			continue
		}
		start := fn.Pos()
		if fn.Doc != nil {
			start = fn.Doc.Pos()
		}
		from, to := fset.Position(start).Offset, fset.Position(fn.End()).Offset
		return src[:from] + src[to:], src[from:to]
	}
	t.Fatalf("method %s not found in:\n%s", name, src)
	return "", ""
}

// countMethodDecls counts `func (s *Service) <name>(` across every
// non-test .go file in dir — the duplicate-method compile error, measured.
func countMethodDecls(t *testing.T, dir, name string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		n += strings.Count(string(b), "func (s *Service) "+name+"(")
	}
	return n
}

func readOps(t *testing.T, handlerDir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(handlerDir, "handlers_crud_ops_gen.go"))
	if err != nil {
		t.Fatalf("handlers_crud_ops_gen.go not written: %v", err)
	}
	return string(b)
}

// TestGenerateCRUDHandlers_ShimMovedToSiblingFileKeepsItsOp is the roofers
// reproduction: a delegating shim moved verbatim out of handlers_crud.go
// still calls its op, so the op must still be emitted — and the shim must
// not be re-appended to handlers_crud.go (a duplicate method).
func TestGenerateCRUDHandlers_ShimMovedToSiblingFileKeepsItsOp(t *testing.T) {
	projectDir, handlerDir, svc, entities := opDemandFixture(t)
	checksums.ResetSkipWrite()
	cs := &checksums.FileChecksums{}

	if _, err := GenerateCRUDHandlers(svc, MatchCRUDMethods(svc, entities), "example.com/test", projectDir, cs); err != nil {
		t.Fatalf("first GenerateCRUDHandlers() error = %v", err)
	}
	shimPath := filepath.Join(handlerDir, "handlers_crud.go")
	shim, err := os.ReadFile(shimPath)
	if err != nil {
		t.Fatalf("handlers_crud.go not scaffolded: %v", err)
	}

	// The agent's edit: move CreatePatient, delegation and all, into a
	// sibling file. The package is unchanged as far as Go is concerned.
	rest, moved := cutMethod(t, string(shim), "CreatePatient")
	if !strings.Contains(moved, "s.crudCreatePatientOp()") {
		t.Fatalf("scaffolded CreatePatient does not delegate to its op:\n%s", moved)
	}
	if err := os.WriteFile(shimPath, []byte(rest), 0o644); err != nil {
		t.Fatal(err)
	}
	sibling := "package patients\n\nimport (\n\t\"context\"\n\n\t\"connectrpc.com/connect\"\n\t\"github.com/reliant-labs/forge/pkg/crud\"\n\n" +
		"\tpb \"example.com/test/gen/proto/services/patients/v1\"\n)\n\n" + moved + "\n"
	if err := os.WriteFile(filepath.Join(handlerDir, "patient_ops.go"), []byte(sibling), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := GenerateCRUDHandlers(svc, MatchCRUDMethods(svc, entities), "example.com/test", projectDir, cs); err != nil {
		t.Fatalf("second GenerateCRUDHandlers() error = %v", err)
	}

	ops := readOps(t, handlerDir)
	for _, want := range []string{"func (s *Service) crudCreatePatientOp()", "func (s *Service) crudGetPatientOp()"} {
		if !strings.Contains(ops, want) {
			t.Errorf("ops file lost %q after the shim moved to a sibling file — patient_ops.go would not compile:\n%s", want, ops)
		}
	}
	if n := countMethodDecls(t, handlerDir, "CreatePatient"); n != 1 {
		t.Errorf("CreatePatient declared %d times across the package after regenerate, want exactly 1", n)
	}
}

// TestGenerateCRUDHandlers_AllShimsMovedNoShimFileRebirth: the shims all
// live in a sibling file and handlers_crud.go is gone. Every op is still
// called, so every op is emitted — and handlers_crud.go is not re-scaffolded
// with a second declaration of each method.
func TestGenerateCRUDHandlers_AllShimsMovedNoShimFileRebirth(t *testing.T) {
	projectDir, handlerDir, svc, entities := opDemandFixture(t)
	checksums.ResetSkipWrite()
	cs := &checksums.FileChecksums{}
	if _, err := GenerateCRUDHandlers(svc, MatchCRUDMethods(svc, entities), "example.com/test", projectDir, cs); err != nil {
		t.Fatalf("first GenerateCRUDHandlers() error = %v", err)
	}
	shimPath := filepath.Join(handlerDir, "handlers_crud.go")
	shim, err := os.ReadFile(shimPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(handlerDir, "patient_ops.go"), shim, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(shimPath); err != nil {
		t.Fatal(err)
	}

	if _, err := GenerateCRUDHandlers(svc, MatchCRUDMethods(svc, entities), "example.com/test", projectDir, cs); err != nil {
		t.Fatalf("second GenerateCRUDHandlers() error = %v", err)
	}
	ops := readOps(t, handlerDir)
	for _, want := range []string{"func (s *Service) crudCreatePatientOp()", "func (s *Service) crudGetPatientOp()"} {
		if !strings.Contains(ops, want) {
			t.Errorf("ops file lost %q although patient_ops.go calls it:\n%s", want, ops)
		}
	}
	if _, err := os.Stat(shimPath); !os.IsNotExist(err) {
		t.Errorf("handlers_crud.go was re-scaffolded although every RPC already has a method (stat err = %v)", err)
	}
}

// TestGenerateCRUDHandlers_HandImplementationInShimFileIsHandWritten is the
// other half of file independence: a method that does NOT call its op is
// implemented by hand wherever it lives — handlers_crud.go included. So it
// gets no op, and the op's own validation (here: a list filter with no
// column, whose error tells the author to "implement the RPC by hand")
// does not fail the generate the author already followed.
func TestGenerateCRUDHandlers_HandImplementationInShimFileIsHandWritten(t *testing.T) {
	projectDir, handlerDir, svc, entities := opDemandFixture(t)
	svc.Methods = append(svc.Methods, Method{Name: "ListPatients", InputType: "ListPatientsRequest", OutputType: "ListPatientsResponse"})
	svc.Messages = map[string][]MessageFieldDef{
		// `overdue` names no column: the generated list op cannot exist.
		"ListPatientsRequest":  {{Name: "overdue", ProtoType: "bool"}},
		"ListPatientsResponse": {{Name: "patients", ProtoType: "message"}},
	}
	handWritten := `package patients

import (
	"context"

	"connectrpc.com/connect"

	pb "example.com/test/gen/proto/services/patients/v1"
)

func (s *Service) ListPatients(ctx context.Context, req *connect.Request[pb.ListPatientsRequest]) (*connect.Response[pb.ListPatientsResponse], error) {
	return connect.NewResponse(&pb.ListPatientsResponse{}), nil
}
`
	if err := os.WriteFile(filepath.Join(handlerDir, "handlers_crud.go"), []byte(handWritten), 0o644); err != nil {
		t.Fatal(err)
	}

	checksums.ResetSkipWrite()
	if _, err := GenerateCRUDHandlers(svc, MatchCRUDMethods(svc, entities), "example.com/test", projectDir, &checksums.FileChecksums{}); err != nil {
		t.Fatalf("GenerateCRUDHandlers() error = %v — ListPatients is implemented by hand in handlers_crud.go, so its op's filter validation must not apply", err)
	}
	ops := readOps(t, handlerDir)
	if strings.Contains(ops, "crudListPatientsOp") {
		t.Errorf("hand-written ListPatients (no op call) must get no op:\n%s", ops)
	}
	for _, want := range []string{"func (s *Service) crudCreatePatientOp()", "func (s *Service) crudGetPatientOp()"} {
		if !strings.Contains(ops, want) {
			t.Errorf("ops file missing %q for the RPCs forge shims:\n%s", want, ops)
		}
	}
	if n := countMethodDecls(t, handlerDir, "ListPatients"); n != 1 {
		t.Errorf("ListPatients declared %d times, want exactly 1", n)
	}
}

// TestGenerateCRUDHandlers_KeepsConversionHelpersHandWrittenCodeCalls: every
// CRUD RPC of the entity is implemented by hand (no op call), but a custom
// RPC projects rows through the generated patientToProto. No op is
// demanded; the helper is, so the ops file must keep carrying it.
func TestGenerateCRUDHandlers_KeepsConversionHelpersHandWrittenCodeCalls(t *testing.T) {
	projectDir, handlerDir, svc, entities := opDemandFixture(t)
	handWritten := `package patients

import (
	"context"

	"connectrpc.com/connect"

	pb "example.com/test/gen/proto/services/patients/v1"
	"example.com/test/internal/db"
)

func (s *Service) CreatePatient(ctx context.Context, req *connect.Request[pb.CreatePatientRequest]) (*connect.Response[pb.CreatePatientResponse], error) {
	return connect.NewResponse(&pb.CreatePatientResponse{}), nil
}

func (s *Service) GetPatient(ctx context.Context, req *connect.Request[pb.GetPatientRequest]) (*connect.Response[pb.GetPatientResponse], error) {
	m, err := patientToProto(&db.Patient{})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.GetPatientResponse{Patient: m}), nil
}
`
	if err := os.WriteFile(filepath.Join(handlerDir, "patient_handlers.go"), []byte(handWritten), 0o644); err != nil {
		t.Fatal(err)
	}

	checksums.ResetSkipWrite()
	if _, err := GenerateCRUDHandlers(svc, MatchCRUDMethods(svc, entities), "example.com/test", projectDir, &checksums.FileChecksums{}); err != nil {
		t.Fatalf("GenerateCRUDHandlers() error = %v", err)
	}
	ops := readOps(t, handlerDir)
	if !strings.Contains(ops, "func patientToProto(") {
		t.Errorf("ops file must keep patientToProto: patient_handlers.go calls it:\n%s", ops)
	}
	if strings.Contains(ops, "crudCreatePatientOp") || strings.Contains(ops, "crudGetPatientOp") {
		t.Errorf("no op is called, so none may be emitted:\n%s", ops)
	}
	if _, perr := parser.ParseFile(token.NewFileSet(), "handlers_crud_ops_gen.go", ops, parser.SkipObjectResolution); perr != nil {
		t.Errorf("helpers-only ops file is not valid Go: %v\n%s", perr, ops)
	}
	if _, err := os.Stat(filepath.Join(handlerDir, "handlers_crud.go")); !os.IsNotExist(err) {
		t.Errorf("handlers_crud.go scaffolded although every RPC is implemented (stat err = %v)", err)
	}
}

// TestGenerateCRUDHandlers_UserDeclaredHelperIsNotRedeclared: a package that
// declares its own patientToProto (and calls it) is not asking forge for
// one — emitting forge's would redeclare theirs.
func TestGenerateCRUDHandlers_UserDeclaredHelperIsNotRedeclared(t *testing.T) {
	projectDir, handlerDir, svc, entities := opDemandFixture(t)
	handWritten := `package patients

import (
	"context"

	"connectrpc.com/connect"

	pb "example.com/test/gen/proto/services/patients/v1"
)

func patientToProto() *pb.Patient { return &pb.Patient{} }

func (s *Service) CreatePatient(ctx context.Context, req *connect.Request[pb.CreatePatientRequest]) (*connect.Response[pb.CreatePatientResponse], error) {
	return connect.NewResponse(&pb.CreatePatientResponse{Patient: patientToProto()}), nil
}

func (s *Service) GetPatient(ctx context.Context, req *connect.Request[pb.GetPatientRequest]) (*connect.Response[pb.GetPatientResponse], error) {
	return connect.NewResponse(&pb.GetPatientResponse{Patient: patientToProto()}), nil
}
`
	if err := os.WriteFile(filepath.Join(handlerDir, "patient_handlers.go"), []byte(handWritten), 0o644); err != nil {
		t.Fatal(err)
	}
	opsPath := filepath.Join(handlerDir, "handlers_crud_ops_gen.go")

	checksums.ResetSkipWrite()
	if _, err := GenerateCRUDHandlers(svc, MatchCRUDMethods(svc, entities), "example.com/test", projectDir, &checksums.FileChecksums{}); err != nil {
		t.Fatalf("GenerateCRUDHandlers() error = %v", err)
	}
	if ops, err := os.ReadFile(opsPath); err == nil && strings.Contains(string(ops), "func patientToProto(") {
		t.Errorf("forge emitted patientToProto although the package declares its own:\n%s", ops)
	}
}

// TestGenerateCRUDHandlers_CustomReadShapeKeepsHelperOnRegenerate: a
// custom-read-shape RPC has no op — its scaffolded body in handlers_crud.go
// queries and projects rows through <entity>ToProto itself. On the SECOND
// run that body is a declared method calling no op, so the helper survives
// only because the body calls it.
func TestGenerateCRUDHandlers_CustomReadShapeKeepsHelperOnRegenerate(t *testing.T) {
	projectDir, handlerDir, svc, entities := opDemandFixture(t)
	svc.Methods = []Method{{Name: "ListPatients", InputType: "ListPatientsRequest", OutputType: "ListPatientsResponse"}}
	svc.Messages = map[string][]MessageFieldDef{
		// page_size without page_token: a bespoke contract → custom read shape.
		"ListPatientsRequest":  {{Name: "page_size", ProtoType: "int32"}, {Name: "cursor", ProtoType: "string"}},
		"ListPatientsResponse": {{Name: "patients", ProtoType: "message"}},
	}
	checksums.ResetSkipWrite()
	cs := &checksums.FileChecksums{}
	for run := 1; run <= 2; run++ {
		if _, err := GenerateCRUDHandlers(svc, MatchCRUDMethods(svc, entities), "example.com/test", projectDir, cs); err != nil {
			t.Fatalf("run %d: GenerateCRUDHandlers() error = %v", run, err)
		}
		shim, err := os.ReadFile(filepath.Join(handlerDir, "handlers_crud.go"))
		if err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		if !strings.Contains(string(shim), "patientToProto(row)") {
			t.Fatalf("run %d: the custom-read-shape body no longer projects through patientToProto — fixture is stale:\n%s", run, shim)
		}
		if ops := readOps(t, handlerDir); !strings.Contains(ops, "func patientToProto(") {
			t.Errorf("run %d: ops file dropped patientToProto, which the custom-read-shape body in handlers_crud.go calls:\n%s", run, ops)
		}
	}
}
