package scaffold

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/connectv2test"
)

// `forge scaffold rpc` in a connect-go v2 project writes the signatures
// protoc-gen-connect-go v2 declares: bare messages, and the per-rpc
// <Svc><Method>ServerStream type for every streaming shape.

var billingV2Ref = v2ConnectRef{
	Alias:         "billingv1connect",
	ImportPath:    "example.com/app/gen/services/billing/v1/billingv1connect",
	ServiceGoName: "BillingService",
}

func TestBuildRPCHandlerStubV2_SignaturesPerMode(t *testing.T) {
	cases := []struct {
		name string
		mode rpcStreamMode
		want string
	}{
		{"unary", rpcUnary, "func (s *Service) Foo(ctx context.Context, req *pb.FooRequest) (*pb.FooResponse, error)"},
		{"server-stream", rpcServerStream, "func (s *Service) Foo(ctx context.Context, req *pb.FooRequest, stream billingv1connect.BillingServiceFooServerStream) error"},
		{"client-stream", rpcClientStream, "func (s *Service) Foo(ctx context.Context, stream billingv1connect.BillingServiceFooServerStream) (*pb.FooResponse, error)"},
		{"bidi-stream", rpcBidiStream, "func (s *Service) Foo(ctx context.Context, stream billingv1connect.BillingServiceFooServerStream) error"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := buildRPCHandlerStubV2("billing", "Foo", "example.com/app/gen/services/billing/v1", billingV2Ref, c.mode)
			if !strings.Contains(got, c.want) {
				t.Errorf("stub missing the v2 signature\nwant substring:\n%s\nfull stub:\n%s", c.want, got)
			}
			if strings.Contains(got, `"connectrpc.com/connect"`) || strings.Contains(got, "connect.Request[") {
				t.Errorf("v2 stub renders a connect-go v1 shape:\n%s", got)
			}
			// The generated <pkg>connect package is imported only for a
			// stream type; a unary stub naming it would not compile.
			if hasImport := strings.Contains(got, billingV2Ref.ImportPath); hasImport != (c.mode != rpcUnary) {
				t.Errorf("connect package imported=%v for mode %s:\n%s", hasImport, c.name, got)
			}
			if !strings.Contains(got, "svcerr.ScaffoldStub(") {
				t.Errorf("stub must return ScaffoldStub:\n%s", got)
			}
			file, err := parser.ParseFile(token.NewFileSet(), "rpc_foo.go", got, parser.ParseComments)
			if err != nil {
				t.Fatalf("stub does not parse: %v\n%s", err, got)
			}
			var fn *ast.FuncDecl
			for _, d := range file.Decls {
				if f, ok := d.(*ast.FuncDecl); ok && f.Name.Name == "Foo" {
					fn = f
				}
			}
			if fn == nil || fn.Doc == nil {
				t.Fatalf("no documented Foo method:\n%s", got)
			}
			if !strings.HasPrefix(fn.Doc.List[0].Text, "// Foo ") {
				t.Errorf("doc must start with the method name, got %q", fn.Doc.List[0].Text)
			}
			if last := fn.Doc.List[len(fn.Doc.List)-1].Text; !codegen.UnwiredStubMarkerRE.MatchString(last) {
				t.Errorf("the marker must be the LAST doc line, got %q", last)
			}
		})
	}
}

// An aliased <pkg>connect import keeps its alias in the stub.
func TestBuildRPCHandlerStubV2_AliasedConnectImport(t *testing.T) {
	ref := billingV2Ref
	ref.Alias = "billingconnect"
	got := buildRPCHandlerStubV2("billing", "Foo", "example.com/app/gen/services/billing/v1", ref, rpcServerStream)
	if want := "\tbillingconnect \"" + ref.ImportPath + "\"\n"; !strings.Contains(got, want) {
		t.Errorf("want aliased import %q:\n%s", want, got)
	}
}

const v2ServiceGoFixture = `package tasks

import (
	connect "connectrpc.com/connect/v2"

	pb "example.com/testproj/gen/services/tasks/v1"
	taskconn "example.com/testproj/gen/services/tasks/v1/tasksv1connect"
)

type Service struct {
	taskconn.UnimplementedTaskTrackerHandler
}

func (s *Service) RegisterV2(server *connect.Server) {
	taskconn.RegisterTaskTrackerHandler(server, s)
}

var _ = pb.File_services_tasks_v1_tasks_proto
`

func writeConnectV2Mod(t *testing.T, dir string) {
	t.Helper()
	gomod := "module example.com/testproj\n\ngo 1.24\n\nrequire connectrpc.com/connect/v2 v2.0.2\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(gomod), 0o644); err != nil {
		t.Fatal(err)
	}
}

// runRPC in a v2 project reads the stream type's package alias and service
// name off the handler's own service.go.
func TestRunAddRPC_ConnectV2ReadsServiceFile(t *testing.T) {
	dir := withTempProject(t, minimalServiceForgeYAML)
	markServiceProject(t, dir)
	writeConnectV2Mod(t, dir)
	handlerDir := filepath.Join(dir, "internal", "handlers", "tasks")
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(handlerDir, "service.go"), []byte(v2ServiceGoFixture), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := runRPC(testFactory(), "tasks", "TailEvents", rpcServerStream); err != nil {
		t.Fatalf("runRPC: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(handlerDir, "rpc_tail_events.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"req *pb.TailEventsRequest, stream taskconn.TaskTrackerTailEventsServerStream) error",
		"\ttaskconn \"example.com/testproj/gen/services/tasks/v1/tasksv1connect\"\n",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("stub missing %q:\n%s", want, body)
		}
	}
}

// With no service.go to read, forge's scaffolding convention names the
// package and the service.
func TestRunAddRPC_ConnectV2Fallback(t *testing.T) {
	dir := withTempProject(t, minimalServiceForgeYAML)
	markServiceProject(t, dir)
	writeConnectV2Mod(t, dir)
	handlerDir := filepath.Join(dir, "internal", "handlers", "tasks")
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := runRPC(testFactory(), "tasks", "Sync", rpcBidiStream); err != nil {
		t.Fatalf("runRPC: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(handlerDir, "rpc_sync.go"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "stream tasksv1connect.TasksServiceSyncServerStream) error"; !strings.Contains(string(body), want) {
		t.Errorf("stub missing %q:\n%s", want, body)
	}
}

const v2TasksProto = `syntax = "proto3";

package tasks.v1;

option go_package = "example.com/testproj/gen/services/tasks/v1;tasksv1";

message GetRequest {}
message GetResponse {}
message WatchRequest {}
message WatchResponse {}
message PushRequest {}
message PushResponse {}
message SyncRequest {}
message SyncResponse {}

service TasksService {
  rpc Get(GetRequest) returns (GetResponse);
  rpc Watch(WatchRequest) returns (stream WatchResponse);
  rpc Push(stream PushRequest) returns (PushResponse);
  rpc Sync(stream SyncRequest) returns (stream SyncResponse);
}
`

// TestBuildRPCHandlerStubV2_CompilesAgainstGeneratedStubs builds the four
// stubs beside forge's v2 service.go against stubs protoc-gen-connect-go v2
// generated, and asserts *Service then implements every rpc of the
// generated Handler interface itself (not through the embedded
// Unimplemented handler — the check type-asserts each method's shape).
func TestBuildRPCHandlerStubV2_CompilesAgainstGeneratedStubs(t *testing.T) {
	mod := connectv2test.NewModule(t, "example.com/testproj", map[string]string{"services/tasks/v1/tasks.proto": v2TasksProto})
	handlerDir := filepath.Join(mod.Dir, "internal", "handlers", "tasks")
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	svc := codegen.ServiceDef{
		Name: "TasksService", Package: "tasks.v1", PkgName: "tasksv1",
		GoPackage:  "example.com/testproj/gen/services/tasks/v1",
		ProtoFile:  "proto/services/tasks/v1/tasks.proto",
		ModulePath: "example.com/testproj",
	}
	if err := codegen.RegenerateServiceFile(svc, handlerDir); err != nil {
		t.Fatalf("RegenerateServiceFile: %v", err)
	}
	const pbPath = "example.com/testproj/gen/services/tasks/v1"
	ref := rpcStubConnect(mod.Dir, handlerDir, "tasks", pbPath)
	if ref == nil {
		t.Fatal("a module requiring connect/v2 directly was not read as v2")
	}
	for name, mode := range map[string]rpcStreamMode{"Get": rpcUnary, "Watch": rpcServerStream, "Push": rpcClientStream, "Sync": rpcBidiStream} {
		mod.Write(t, filepath.Join("internal", "handlers", "tasks", codegen.RPCHandlerFileName(name)),
			buildRPCHandlerStubV2("tasks", name, pbPath, *ref, mode))
	}
	mod.Write(t, "internal/check/check.go", `package check

import (
	"context"

	pb "example.com/testproj/gen/services/tasks/v1"
	"example.com/testproj/gen/services/tasks/v1/tasksv1connect"
	"example.com/testproj/internal/handlers/tasks"
)

var (
	_ tasksv1connect.TasksServiceHandler = (*tasks.Service)(nil)
	_ func(context.Context, *pb.GetRequest) (*pb.GetResponse, error) = (*tasks.Service)(nil).Get
	_ func(context.Context, *pb.WatchRequest, tasksv1connect.TasksServiceWatchServerStream) error = (*tasks.Service)(nil).Watch
	_ func(context.Context, tasksv1connect.TasksServicePushServerStream) (*pb.PushResponse, error) = (*tasks.Service)(nil).Push
	_ func(context.Context, tasksv1connect.TasksServiceSyncServerStream) error = (*tasks.Service)(nil).Sync
)
`)
	mod.Go(t, "build", "./...")
	mod.Go(t, "vet", "./...")
}
