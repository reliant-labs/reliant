package codegen

import (
	"bytes"
	"flag"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/connectv2test"
	"github.com/reliant-labs/forge/internal/templates"
)

// The service-level artefacts forge renders for a project that builds
// against connect-go v2 (connectmajor reads a direct require of
// connectrpc.com/connect/v2 in go.mod): the handler stubs, service.go, the
// CRUD shims, the scaffolded tests and the proto mock.
//
// TestServiceArtefactsConnectV2Golden pins every one byte for byte, and
// TestServiceArtefactsConnectV2Compile builds them in a real module against
// stubs protoc-gen-connect-go v2 generated, then runs the scaffolded tests —
// the only proof a v2 signature is the one the generated Handler interface
// declares. The v1 output of the same renderers is pinned by the existing
// goldens and tests, untouched.

var updateServiceV2Golden = flag.Bool("update-service-v2-golden", false, "rewrite testdata/connect_v2_service/*.golden")

const v2ItemsModule = "example.com/v2app"

const v2ItemsProto = `syntax = "proto3";

package items.v1;

option go_package = "example.com/v2app/gen/services/items/v1;itemsv1";

message Item {
  string id = 1;
  string name = 2;
}

message GetItemRequest { string id = 1; }
message GetItemResponse { Item item = 1; }
message CreateItemRequest { Item item = 1; }
message CreateItemResponse { Item item = 1; }
message SearchItemsRequest {
  optional string name = 1;
  string q = 2;
}
message SearchItemsResponse { repeated Item items = 1; }
message PingRequest {}
message PingResponse {}
message TailRequest {}
message TailResponse {}
message UploadRequest {}
message UploadResponse {}
message ChatRequest {}
message ChatResponse {}

service ItemsService {
  rpc GetItem(GetItemRequest) returns (GetItemResponse);
  rpc CreateItem(CreateItemRequest) returns (CreateItemResponse);
  rpc SearchItems(SearchItemsRequest) returns (SearchItemsResponse);
  rpc Ping(PingRequest) returns (PingResponse);
  rpc Tail(TailRequest) returns (stream TailResponse);
  rpc Upload(stream UploadRequest) returns (UploadResponse);
  rpc Chat(stream ChatRequest) returns (stream ChatResponse);
}
`

func v2ItemsService() ServiceDef {
	return ServiceDef{
		Name:       "ItemsService",
		Package:    "items.v1",
		GoPackage:  v2ItemsModule + "/gen/services/items/v1",
		PkgName:    "itemsv1",
		ProtoFile:  "proto/services/items/v1/items.proto",
		ModulePath: v2ItemsModule,
		Methods: []Method{
			{Name: "GetItem", InputType: "GetItemRequest", OutputType: "GetItemResponse", AuthRequired: true},
			{Name: "CreateItem", InputType: "CreateItemRequest", OutputType: "CreateItemResponse", AuthRequired: true},
			{Name: "SearchItems", InputType: "SearchItemsRequest", OutputType: "SearchItemsResponse", AuthRequired: true},
			{Name: "Ping", InputType: "PingRequest", OutputType: "PingResponse", AuthRequired: true},
			{Name: "Tail", InputType: "TailRequest", OutputType: "TailResponse", ServerStreaming: true, AuthRequired: true},
			{Name: "Upload", InputType: "UploadRequest", OutputType: "UploadResponse", ClientStreaming: true, AuthRequired: true},
			{Name: "Chat", InputType: "ChatRequest", OutputType: "ChatResponse", ClientStreaming: true, ServerStreaming: true, AuthRequired: true},
		},
	}
}

// writeConnectV2GoMod marks dir as a connect-go v2 project for the
// renderers (connectmajor.Detect needs only the direct require).
func writeConnectV2GoMod(t *testing.T, dir string) {
	t.Helper()
	gomod := "module " + v2ItemsModule + "\n\ngo 1.24\n\nrequire connectrpc.com/connect/v2 v2.0.2\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(gomod), 0o644); err != nil {
		t.Fatal(err)
	}
}

// v2CRUDShimData is the CRUD template data generate builds for ItemsService
// with an Item entity: a delegating Get and Create, and a custom-read-shape
// SearchItems (its request does not follow AIP-158), whose wired body reads
// request fields.
func v2CRUDShimData() CRUDTemplateData {
	base := CRUDMethodTemplateData{
		EntityName: "Item", EntityPbType: "Item", EntityLower: "item", EntitySnake: "item",
		AuthRequired: true, AuthSeam: CRUDAuthSeam(), PkField: "Id", PkDBField: "Id",
		PkColumnName: "id", PkGoType: "string", ResponseField: "Item", ConnectV2: true,
	}
	get, create, search := base, base, base
	get.MethodName, get.InputType, get.OutputType, get.Operation = "GetItem", "GetItemRequest", "GetItemResponse", "get"
	create.MethodName, create.InputType, create.OutputType, create.Operation = "CreateItem", "CreateItemRequest", "CreateItemResponse", "create"
	search.MethodName, search.InputType, search.OutputType, search.Operation = "SearchItems", "SearchItemsRequest", "SearchItemsResponse", "list"
	search.ResponseField = "Items"
	search.ShapeMismatch = true
	search.MismatchReason = "request has no page_size"
	search.CustomFilters = []FilterFieldData{
		{ProtoName: "name", GoName: "Name", ColumnName: "name", FieldType: "string", IsOptional: true},
		{ProtoName: "q", GoName: "Q", FieldType: "string", FilterType: "search", SearchColumns: []string{"name"}},
	}
	return CRUDTemplateData{
		Package:      "items",
		Module:       v2ItemsModule,
		ProtoPackage: "services/items",
		DBImport:     v2ItemsModule + "/internal/db",
		ConnectV2:    true,
		CRUDMethods:  []CRUDMethodTemplateData{get, create, search},
	}
}

// renderV2ItemsService runs forge's service-level renderers for
// ItemsService into projectDir (which must already hold a v2 go.mod) and
// returns the files they wrote, keyed by golden name.
func renderV2ItemsService(t *testing.T, projectDir string) map[string][]byte {
	t.Helper()
	svc := v2ItemsService()
	relDir := filepath.Join("internal", "handlers", "items")
	handlerDir := filepath.Join(projectDir, relDir)
	crud := map[string]bool{"GetItem": true, "CreateItem": true, "SearchItems": true}

	// service.go, rpc_<name>.go and the per-rpc scaffold tests.
	if err := GenerateServiceStub(svc, handlerDir, crud); err != nil {
		t.Fatalf("GenerateServiceStub: %v", err)
	}
	// The CRUD shims (handlers_crud.go) and the Deps.DB they need.
	if err := ensureDepsDBField(handlerDir); err != nil {
		t.Fatalf("ensureDepsDBField: %v", err)
	}
	data := v2CRUDShimData()
	if err := ensureCRUDShimFile(projectDir, relDir, data, data.CRUDMethods, nil, nil); err != nil {
		t.Fatalf("ensureCRUDShimFile: %v", err)
	}
	// The lifecycle test (handlers_crud_test.go).
	get, create := data.CRUDMethods[0], data.CRUDMethods[1]
	crudTest, err := templates.ServiceTemplates().Render("handlers_crud_test.go.tmpl", CRUDTestTemplateData{
		Package: "items", Module: v2ItemsModule, ProtoPackage: "services/items",
		TestHelperName: "Items", ServiceImportPath: "items",
		MigrationsDir: "db/migrations", MigrationsDirArgs: `"db", "migrations"`,
		ConnectV2: true,
		Entities: []CRUDTestEntityData{{
			EntityName: "Item", EntityLower: "item", PkField: "Id", PkGoType: "string",
			HasCreate: true, HasGet: true, CreateMethod: create, GetMethod: get,
		}},
	})
	if err != nil {
		t.Fatalf("render handlers_crud_test.go.tmpl: %v", err)
	}
	if err := os.WriteFile(filepath.Join(handlerDir, "handlers_crud_test.go"), crudTest, 0o644); err != nil {
		t.Fatal(err)
	}
	// The proto mock.
	if _, err := GenerateMock(svc, projectDir, filepath.Join(projectDir, "internal", "handlers", "mocks"), nil); err != nil {
		t.Fatalf("GenerateMock: %v", err)
	}

	out := map[string][]byte{}
	for _, dir := range []string{handlerDir, filepath.Join(projectDir, "internal", "handlers", "mocks")} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			body, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			out[e.Name()] = body
		}
	}

	// The append path: an rpc file that exists but no longer declares its
	// method gets the handlers_methods.go.tmpl fragment and the imports a
	// v2 streaming stub needs.
	appendDir := t.TempDir()
	appendPath := filepath.Join(appendDir, "rpc_tail.go")
	if err := os.WriteFile(appendPath, []byte("package items\n\n// Tail moved elsewhere.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	one := mapServiceDefToTemplateData(svc, projectDir)
	one.Methods = []MethodTemplateData{one.Methods[4]} // Tail
	if err := scaffoldHandlerStubs(appendPath, one); err != nil {
		t.Fatalf("scaffoldHandlerStubs (append): %v", err)
	}
	appended, err := os.ReadFile(appendPath)
	if err != nil {
		t.Fatal(err)
	}
	out["rpc_tail_appended.go"] = appended
	return out
}

func TestServiceArtefactsConnectV2Golden(t *testing.T) {
	projectDir := t.TempDir()
	writeConnectV2GoMod(t, projectDir)
	got := renderV2ItemsService(t, projectDir)

	goldenDir := filepath.Join("testdata", "connect_v2_service")
	names := make([]string, 0, len(got))
	for name := range got {
		names = append(names, name)
	}
	sort.Strings(names)
	if *updateServiceV2Golden {
		_ = os.RemoveAll(goldenDir)
		if err := os.MkdirAll(goldenDir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range names {
		body := got[name]
		// Every artefact parses as Go and names no connect-go v1 shape.
		if _, err := parser.ParseFile(token.NewFileSet(), name, body, parser.ParseComments); err != nil {
			t.Errorf("%s does not parse: %v\n%s", name, err, body)
		}
		for _, v1 := range []string{`"connectrpc.com/connect"`, "connect.Request[", "connect.Response[", "connect.NewRequest(", "connect.HandlerOption", "crud.Handle", ".Msg."} {
			if bytes.Contains(body, []byte(v1)) {
				t.Errorf("%s still renders the connect-go v1 shape %q:\n%s", name, v1, body)
			}
		}
		golden := filepath.Join(goldenDir, name+".golden")
		if *updateServiceV2Golden {
			if err := os.WriteFile(golden, body, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Errorf("%s: no golden (%v); rerun with -update-service-v2-golden", name, err)
			continue
		}
		if !bytes.Equal(body, want) {
			t.Errorf("%s differs from %s; rerun with -update-service-v2-golden if the change is intended\n--- got ---\n%s", name, golden, body)
		}
	}
	if !*updateServiceV2Golden {
		entries, err := os.ReadDir(goldenDir)
		if err != nil {
			t.Fatalf("read %s: %v", goldenDir, err)
		}
		if len(entries) == 0 {
			t.Fatalf("%s holds no goldens, so the stale-golden check below asserts nothing", goldenDir)
		}
		for _, e := range entries {
			if _, ok := got[strings.TrimSuffix(e.Name(), ".golden")]; !ok {
				t.Errorf("stale golden %s: nothing renders it any more", e.Name())
			}
		}
	}
}

// The v1 renders of the same service are untouched by the v2 branch: a
// project without a direct connect/v2 require still gets connect.Request
// signatures and Register(mux, ...connect.HandlerOption).
func TestServiceArtefactsConnectV1Unchanged(t *testing.T) {
	data := mapServiceDefToTemplateData(v2ItemsService(), t.TempDir()) // no go.mod: v1
	if data.ConnectV2 {
		t.Fatal("a project with no go.mod rendered as connect-go v2")
	}
	svcFile, err := templates.ServiceTemplates().Render("service.go.tmpl", data)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"connectrpc.com/connect"`, "func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {"} {
		if !bytes.Contains(svcFile, []byte(want)) {
			t.Errorf("v1 service.go lost %q:\n%s", want, svcFile)
		}
	}
	if bytes.Contains(svcFile, []byte("RegisterV2")) {
		t.Errorf("v1 service.go renders RegisterV2:\n%s", svcFile)
	}
}

// A pristine v2 stub is still recognised by the stub→CRUD excision pass:
// its body is the same svcerr.ScaffoldStub return as v1's.
func TestExciseUnwiredStubs_ConnectV2Shape(t *testing.T) {
	projectDir := t.TempDir()
	writeConnectV2GoMod(t, projectDir)
	targetDir := filepath.Join(projectDir, "internal", "handlers", "items")
	if err := GenerateServiceStub(v2ItemsService(), targetDir); err != nil {
		t.Fatalf("GenerateServiceStub: %v", err)
	}
	removed, err := ExciseUnwiredStubs(projectDir, targetDir, map[string]bool{"GetItem": true, "Tail": true})
	if err != nil {
		t.Fatalf("ExciseUnwiredStubs: %v", err)
	}
	sort.Strings(removed)
	if strings.Join(removed, ",") != "GetItem,Tail" {
		t.Fatalf("excision removed %v, want [GetItem Tail]", removed)
	}
	left := ScanUnwiredStubMethods(targetDir)
	if left["GetItem"] || left["Tail"] || !left["Ping"] || !left["Chat"] {
		t.Errorf("unexpected stubs left after excision: %v", keysOf(left))
	}
}

// TestServiceArtefactsConnectV2Compile renders ItemsService's artefacts into
// a module with real connect-go v2 stubs, vets everything, asserts the
// handler and the mock satisfy the generated Handler interface and
// mountkit.RegistrarV2, and runs the scaffolded tests — the unary row
// in-process, the server- and client-stream rows over a v2 client.
func TestServiceArtefactsConnectV2Compile(t *testing.T) {
	mod := connectv2test.NewModule(t, v2ItemsModule, map[string]string{"services/items/v1/items.proto": v2ItemsProto})
	renderV2ItemsService(t, mod.Dir)

	// What the rest of a forge project supplies around the handler
	// package: the generated CRUD ops, the ORM, the example metric, the
	// middleware claims and the test harness. Minimal stand-ins, typed
	// as the real ones are.
	mod.Write(t, "internal/db/db.go", `package db

import (
	"context"

	"github.com/reliant-labs/forge/pkg/orm"
)

type Item struct{ Id, Name string }

func ListItem(ctx context.Context, db orm.Context, opts ...orm.QueryOption) ([]*Item, error) { return nil, nil }
`)
	mod.Write(t, "internal/metrics/metrics.go", `package metrics

import "context"

func RecordCreated(ctx context.Context, entity string, err error) {}
`)
	mod.Write(t, "pkg/middleware/middleware.go", `package middleware

import "context"

type Claims struct{ UserID, Email string }

type claimsKey struct{}

func ContextWithClaims(ctx context.Context, c *Claims) context.Context {
	return context.WithValue(ctx, claimsKey{}, c)
}
`)
	mod.Write(t, "internal/handlers/items/handlers_crud_ops_gen.go", `package items

import (
	"github.com/reliant-labs/forge/pkg/crud"

	pb "example.com/v2app/gen/services/items/v1"
	"example.com/v2app/internal/db"
)

func (s *Service) crudGetItemOp() crud.GetOp[pb.GetItemRequest, pb.GetItemResponse, db.Item] {
	return crud.GetOp[pb.GetItemRequest, pb.GetItemResponse, db.Item]{}
}

func (s *Service) crudCreateItemOp() crud.CreateOp[pb.CreateItemRequest, pb.CreateItemResponse, db.Item] {
	return crud.CreateOp[pb.CreateItemRequest, pb.CreateItemResponse, db.Item]{}
}

func itemToProto(e *db.Item) (*pb.Item, error) { return &pb.Item{Id: e.Id, Name: e.Name}, nil }
`)
	mod.Write(t, "internal/handlers/items/helpers_gen_test.go", `package items

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"testing"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/testkit"

	pb "example.com/v2app/gen/services/items/v1"
	"example.com/v2app/gen/services/items/v1/itemsv1connect"
)

type TestOption func(*Deps)

// placeholderDB stands in for the ORM the scaffolded rows never reach,
// as the real harness's WithPlaceholderDeps does.
type placeholderDB struct{ orm.Context }

func WithDB(db orm.Context) TestOption   { return func(d *Deps) { d.DB = db } }
func WithPlaceholderDeps() TestOption    { return func(*Deps) {} }
func AuthedContext(t *testing.T) context.Context { return context.Background() }

func NewTestItems(t *testing.T, opts ...TestOption) *Service {
	deps := Deps{Logger: slog.Default(), DB: placeholderDB{}}
	for _, o := range opts {
		o(&deps)
	}
	svc, err := New(deps)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func NewTestItemsServer(t *testing.T, opts ...TestOption) (*httptest.Server, itemsv1connect.ItemsServiceClient) {
	return testkit.NewConnectClientV2(t, NewTestItems(t, opts...).RegisterV2, itemsv1connect.NewItemsServiceClient)
}

func NewCreateItemRequest(t testing.TB, db orm.Context, variant int) *pb.CreateItemRequest {
	return &pb.CreateItemRequest{Item: &pb.Item{Name: "item"}}
}
`)
	mod.Write(t, "internal/check/check.go", `// Package check pins the rendered v2 types to the interfaces they serve.
package check

import (
	"github.com/reliant-labs/forge/pkg/mountkit"

	"example.com/v2app/gen/services/items/v1/itemsv1connect"
	"example.com/v2app/internal/handlers/items"
	"example.com/v2app/internal/handlers/mocks"
)

var (
	_ itemsv1connect.ItemsServiceHandler = (*items.Service)(nil)
	_ mountkit.RegistrarV2               = (*items.Service)(nil)
	_ itemsv1connect.ItemsServiceHandler = (*mocks.ItemsServiceMock)(nil)
	_ mountkit.RegistrarV2               = (*mocks.ItemsServiceMock)(nil)
)
`)

	mod.Go(t, "build", "./...")
	mod.Go(t, "vet", "./...")
	// The lifecycle test needs a database; every scaffolded rpc test runs.
	out := mod.Go(t, "test", "-count=1", "-run", "_Generated", "-v", "./internal/handlers/items/")
	for _, ran := range []string{"TestPing_Generated", "TestTail_Generated", "TestUpload_Generated"} {
		if !strings.Contains(out, "--- PASS: "+ran) {
			t.Errorf("%s did not pass:\n%s", ran, out)
		}
	}
}
