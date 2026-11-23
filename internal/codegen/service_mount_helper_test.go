package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/connectv2test"
)

// A service may declare an RPC named like its mount helper — Register in
// connect-go v1, RegisterV2 in v2. One type cannot carry both, so the helper
// is renamed — v1 to Mount, v2 to the first of MountV2, Mount2V2, … no RPC
// uses — and every generated caller has to follow.
func TestMountMethodFor(t *testing.T) {
	plain := ServiceDef{Methods: []Method{{Name: "Ping"}}}
	register := ServiceDef{Methods: []Method{{Name: "Register"}, {Name: "Ping"}}}
	registerV2 := ServiceDef{Methods: []Method{{Name: "RegisterV2"}, {Name: "Ping"}}}
	for _, tc := range []struct {
		name string
		svc  ServiceDef
		v2   bool
		want string
	}{
		{"v1 plain", plain, false, "Register"},
		{"v1 Register rpc", register, false, "Mount"},
		{"v1 RegisterV2 rpc does not collide", registerV2, false, "Register"},
		{"v2 plain", plain, true, "RegisterV2"},
		{"v2 Register rpc does not collide", register, true, "RegisterV2"},
		{"v2 RegisterV2 rpc", registerV2, true, "MountV2"},
		{"v2 MountV2 rpc alone does not collide", ServiceDef{Methods: []Method{{Name: "MountV2"}}}, true, "RegisterV2"},
		{"v2 RegisterV2 and MountV2 rpcs", ServiceDef{Methods: []Method{{Name: "RegisterV2"}, {Name: "MountV2"}}}, true, "Mount2V2"},
		{"v2 RegisterV2, MountV2 and Mount2V2 rpcs", ServiceDef{Methods: []Method{{Name: "RegisterV2"}, {Name: "MountV2"}, {Name: "Mount2V2"}}}, true, "Mount3V2"},
		{"v2 gaps are filled first", ServiceDef{Methods: []Method{{Name: "RegisterV2"}, {Name: "Mount2V2"}}}, true, "MountV2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := mountMethodFor(tc.svc, tc.v2); got != tc.want {
				t.Errorf("mountMethodFor = %q, want %q", got, tc.want)
			}
		})
	}
}

// registerV2Service is the v2 ItemsService fixture declaring an RPC named
// RegisterV2 — the collision with the v2 mount helper — beside a Ping.
func registerV2Service() ServiceDef {
	svc := v2ItemsService()
	svc.Methods = []Method{
		{Name: "RegisterV2", InputType: "RegisterV2Request", OutputType: "RegisterV2Response", AuthRequired: true},
		{Name: "Ping", InputType: "PingRequest", OutputType: "PingResponse", AuthRequired: true},
	}
	return svc
}

const registerV2Proto = `syntax = "proto3";
package items.v1;
option go_package = "example.com/v2app/gen/services/items/v1;itemsv1";
message RegisterV2Request {}
message RegisterV2Response {}
message PingRequest {}
message PingResponse {}
service ItemsService {
  rpc RegisterV2(RegisterV2Request) returns (RegisterV2Response);
  rpc Ping(PingRequest) returns (PingResponse);
}
`

const (
	v2HelperRegister = "func (s *Service) RegisterV2(server *connect.Server)"
	v2HelperMount    = "func (s *Service) MountV2(server *connect.Server)"
)

// A fresh v2 service with a RegisterV2 RPC: service.go's helper is MountV2,
// the RPC gets its stub, and the mock picks the same helper name.
func TestServiceStubConnectV2RegisterV2RPC(t *testing.T) {
	project := t.TempDir()
	writeConnectV2GoMod(t, project)
	handlerDir := filepath.Join(project, "internal", "handlers", "items")
	if err := GenerateServiceStub(registerV2Service(), handlerDir); err != nil {
		t.Fatalf("GenerateServiceStub: %v", err)
	}
	service := readFile(t, filepath.Join(handlerDir, "service.go"))
	if !strings.Contains(service, v2HelperMount) || strings.Contains(service, v2HelperRegister) {
		t.Errorf("service.go must declare the MountV2 helper, not RegisterV2:\n%s", service)
	}
	stub := readFile(t, filepath.Join(handlerDir, RPCHandlerFileName("RegisterV2")))
	if !strings.Contains(stub, "func (s *Service) RegisterV2(") {
		t.Errorf("%s does not stub the RegisterV2 rpc:\n%s", RPCHandlerFileName("RegisterV2"), stub)
	}

	mocks := filepath.Join(project, "internal", "handlers", "mocks")
	if _, err := GenerateMock(registerV2Service(), project, mocks, nil); err != nil {
		t.Fatalf("GenerateMock: %v", err)
	}
	entries, err := os.ReadDir(mocks)
	if err != nil || len(entries) != 1 {
		t.Fatalf("want one mock file, got %v (%v)", entries, err)
	}
	mock := readFile(t, filepath.Join(mocks, entries[0].Name()))
	if !strings.Contains(mock, ") MountV2(server *connect.Server)") {
		t.Errorf("the mock's helper must be MountV2, as the service's is:\n%s", mock)
	}

	// The component test helper mounts through the same name.
	data := goldenHelperData(true)
	data.MountMethod = mountMethodFor(registerV2Service(), true)
	helpers := string(renderProjectTemplate(t, "component_test_helpers.go.tmpl", data))
	if !strings.Contains(helpers, "testkit.NewConnectClientV2(t, svc.MountV2,") {
		t.Errorf("the test helper must mount through MountV2:\n%s", helpers)
	}
}

// The usual order: the service is scaffolded with no RPCs (helper
// RegisterV2), and a RegisterV2 RPC is added to the proto later. The stub
// pass must rename the owned helper to MountV2 — not count it as the RPC's
// implementation — and stub the RPC; removing the RPC renames it back.
func TestGenerateMissingHandlerStubs_ConnectV2RegisterV2RPCAddedLater(t *testing.T) {
	project := t.TempDir()
	writeConnectV2GoMod(t, project)
	handlerDir := filepath.Join(project, "internal", "handlers", "items")
	pingOnly := registerV2Service()
	pingOnly.Methods = pingOnly.Methods[1:]
	if err := GenerateServiceStub(pingOnly, handlerDir); err != nil {
		t.Fatalf("GenerateServiceStub: %v", err)
	}
	servicePath := filepath.Join(handlerDir, "service.go")
	if !strings.Contains(readFile(t, servicePath), v2HelperRegister) {
		t.Fatalf("precondition: service.go is born with the RegisterV2 helper")
	}
	// An author's own edit elsewhere in the file must survive the rename.
	if err := os.WriteFile(servicePath, []byte(readFile(t, servicePath)+"\n// kept: the author's note\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := GenerateMissingHandlerStubs(registerV2Service(), project, handlerDir, nil, nil)
	if err != nil {
		t.Fatalf("GenerateMissingHandlerStubs: %v", err)
	}
	if strings.Join(res.NewMethods, ",") != "RegisterV2" {
		t.Errorf("NewMethods = %v, want [RegisterV2]: the helper was counted as the RPC", res.NewMethods)
	}
	service := readFile(t, servicePath)
	if !strings.Contains(service, "// MountV2 registers the service's Connect handlers") ||
		!strings.Contains(service, v2HelperMount) || strings.Contains(service, v2HelperRegister) {
		t.Errorf("service.go's helper (and its doc comment) must be renamed to MountV2:\n%s", service)
	}
	if !strings.Contains(service, "// kept: the author's note") {
		t.Errorf("the rename rewrote more than the helper's name:\n%s", service)
	}
	if !strings.Contains(readFile(t, filepath.Join(handlerDir, RPCHandlerFileName("RegisterV2"))), "func (s *Service) RegisterV2(") {
		t.Errorf("the RegisterV2 rpc was not stubbed")
	}

	// Idempotent: a second pass changes nothing.
	if _, err := GenerateMissingHandlerStubs(registerV2Service(), project, handlerDir, nil, nil); err != nil {
		t.Fatalf("GenerateMissingHandlerStubs (again): %v", err)
	}
	if again := readFile(t, servicePath); again != service {
		t.Errorf("a second pass rewrote service.go:\n%s", again)
	}

	// The RPC is removed (proto and handler file): the helper is RegisterV2 again.
	if err := os.Remove(filepath.Join(handlerDir, RPCHandlerFileName("RegisterV2"))); err != nil {
		t.Fatal(err)
	}
	if _, err := GenerateMissingHandlerStubs(pingOnly, project, handlerDir, nil, nil); err != nil {
		t.Fatalf("GenerateMissingHandlerStubs (rpc removed): %v", err)
	}
	if back := readFile(t, servicePath); !strings.Contains(back, v2HelperRegister) || strings.Contains(back, v2HelperMount) {
		t.Errorf("with the RPC gone the helper must be RegisterV2 again:\n%s", back)
	}
}

// The reconciler and generated callers pick the same first unoccupied name,
// including methods whose RPC has disappeared from the proto.
func TestReconcileMountHelper_ConnectV2(t *testing.T) {
	rpc := func(names ...string) ServiceDef {
		svc := v2ItemsService()
		svc.Methods = nil
		for _, n := range names {
			svc.Methods = append(svc.Methods, Method{Name: n, InputType: n + "Request", OutputType: n + "Response"})
		}
		return svc
	}
	method := func(name string) string {
		return "func (s *Service) " + name + "(ctx context.Context, req *pb." + name + "Request) (*pb." + name + "Response, error) {\n\treturn nil, nil\n}\n"
	}
	for _, tc := range []struct {
		name   string
		svc    ServiceDef
		helper string            // the helper's name in service.go before the pass
		files  map[string]string // other declarations in the package
		want   string            // the helper's name after
	}{
		{
			name: "MountV2 rpc added beside RegisterV2: MountV2 helper moves on",
			svc:  rpc("RegisterV2", "MountV2", "Ping"), helper: "MountV2",
			files: map[string]string{"rpc_register_v2.go": method("RegisterV2")},
			want:  "Mount2V2",
		},
		{
			name: "colliding rpcs removed: back to RegisterV2",
			svc:  rpc("Ping"), helper: "Mount2V2",
			want: "RegisterV2",
		},
		{
			name: "one of two colliding rpcs removed: back to the earliest free candidate",
			svc:  rpc("RegisterV2", "Ping"), helper: "Mount2V2",
			files: map[string]string{"rpc_register_v2.go": method("RegisterV2")},
			want:  "MountV2",
		},
		{
			name: "another method reserves a candidate even with a value receiver",
			svc:  rpc("RegisterV2", "Ping"), helper: "RegisterV2",
			files: map[string]string{"extra.go": "func (s Service) MountV2() string { return \"mine\" }\n"},
			want:  "Mount2V2",
		},
		{
			name: "a retained implementation reserves the removed RPC's name",
			svc:  rpc("MountV2", "Ping"), helper: "Mount2V2",
			files: map[string]string{"rpc_register_v2.go": method("RegisterV2")},
			want:  "Mount2V2",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			project := t.TempDir()
			writeConnectV2GoMod(t, project)
			dir := filepath.Join(project, "internal", "handlers", "items")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			write := func(name, body string) {
				t.Helper()
				src := "package items\n\nimport (\n\t\"context\"\n\n\tconnect \"connectrpc.com/connect/v2\"\n\tpb \"example.com/v2app/gen/services/items/v1\"\n)\n\nvar _ = context.Background\nvar _ *connect.Server\nvar _ = pb.File_services_items_v1_items_proto\n\n" + body
				if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			write("service.go", "type Service struct{}\n\n// "+tc.helper+" registers the service's Connect handlers on server.\nfunc (s *Service) "+tc.helper+"(server *connect.Server) {}\n")
			for name, body := range tc.files {
				write(name, body)
			}
			for range 2 {
				rows, _, err := inventoryRows(InventoryGenInput{GenContext: GenContext{ProjectDir: project, ModulePath: v2ItemsModule}, Services: []ServiceDef{tc.svc}})
				if err != nil || len(rows) != 1 || rows[0].MountMethod != tc.want {
					t.Fatalf("inventory rows = %v, %v; want mount %s before and after reconciliation", rows, err, tc.want)
				}
				if err := reconcileMountHelper(tc.svc, project, dir, nil); err != nil {
					t.Fatalf("reconcileMountHelper: %v", err)
				}
			}
			got := readFile(t, filepath.Join(dir, "service.go"))
			if !strings.Contains(got, "// "+tc.want+" registers") || !strings.Contains(got, "func (s *Service) "+tc.want+"(server *connect.Server)") {
				t.Errorf("want the helper (and its doc comment) named %s:\n%s", tc.want, got)
			}
		})
	}
}

// connect-go v1 is untouched: the stub pass never renames an owned
// service.go's Register helper.
func TestGenerateMissingHandlerStubs_ConnectV1HelperNotRenamed(t *testing.T) {
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "go.mod"), []byte("module "+v2ItemsModule+"\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	handlerDir := filepath.Join(project, "internal", "handlers", "items")
	svc := registerV2Service()
	svc.Methods = svc.Methods[1:]
	if err := GenerateServiceStub(svc, handlerDir); err != nil {
		t.Fatalf("GenerateServiceStub: %v", err)
	}
	before := readFile(t, filepath.Join(handlerDir, "service.go"))
	svc.Methods = append(svc.Methods, Method{Name: "Register", InputType: "RegisterRequest", OutputType: "RegisterResponse"})
	if _, err := GenerateMissingHandlerStubs(svc, project, handlerDir, nil, nil); err != nil {
		t.Fatalf("GenerateMissingHandlerStubs: %v", err)
	}
	if after := readFile(t, filepath.Join(handlerDir, "service.go")); after != before {
		t.Errorf("a v1 service.go was rewritten:\n%s", after)
	}
}

// mountV2Service adds an RPC named MountV2 to registerV2Service, so both of
// the first two helper candidates are taken.
func mountV2Service() ServiceDef {
	svc := registerV2Service()
	svc.Methods = append(svc.Methods, Method{Name: "MountV2", InputType: "MountV2Request", OutputType: "MountV2Response", AuthRequired: true})
	return svc
}

const mountV2Proto = `syntax = "proto3";
package items.v1;
option go_package = "example.com/v2app/gen/services/items/v1;itemsv1";
message RegisterV2Request {}
message RegisterV2Response {}
message MountV2Request {}
message MountV2Response {}
message PingRequest {}
message PingResponse {}
service ItemsService {
  rpc RegisterV2(RegisterV2Request) returns (RegisterV2Response);
  rpc Ping(PingRequest) returns (PingResponse);
  rpc MountV2(MountV2Request) returns (MountV2Response);
}
`

// The proof: against real connect-go v2 stubs, a service declaring RPCs
// named like the mount helper builds — fresh, and when the RPCs arrive one
// pass at a time after the service was scaffolded — with the service and its
// mock both satisfying the generated Handler and both mounting through the
// resolved helper on a mountkit.MuxV2.
func TestServiceConnectV2RegisterV2RPCCompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("runs buf and compiles a module against real connect-go v2 stubs; runs in task test")
	}
	for _, tc := range []struct {
		name   string
		proto  string
		svc    ServiceDef
		helper string
	}{
		{"RegisterV2 rpc", registerV2Proto, registerV2Service(), "MountV2"},
		{"RegisterV2 and MountV2 rpcs", mountV2Proto, mountV2Service(), "Mount2V2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := mountMethodFor(tc.svc, true); got != tc.helper {
				t.Fatalf("mountMethodFor = %q, want %q", got, tc.helper)
			}
			mod := connectv2test.NewModule(t, v2ItemsModule, map[string]string{"services/items/v1/items.proto": tc.proto})

			// Fresh: the RPCs exist when the service is scaffolded.
			if err := GenerateServiceStub(tc.svc, filepath.Join(mod.Dir, "internal", "handlers", "items")); err != nil {
				t.Fatalf("GenerateServiceStub: %v", err)
			}
			// Later: scaffolded with Ping only, then each stub pass sees one
			// more of the service's RPCs, as a proto grows.
			laterDir := filepath.Join(mod.Dir, "internal", "handlers", "later")
			grown := tc.svc
			grown.Methods = []Method{tc.svc.Methods[1]} // Ping
			if err := GenerateServiceStub(grown, laterDir); err != nil {
				t.Fatalf("GenerateServiceStub (later): %v", err)
			}
			for _, m := range append([]Method{tc.svc.Methods[0]}, tc.svc.Methods[2:]...) {
				grown.Methods = append(grown.Methods, m)
				if _, err := GenerateMissingHandlerStubs(grown, mod.Dir, laterDir, nil, nil); err != nil {
					t.Fatalf("GenerateMissingHandlerStubs (+%s): %v", m.Name, err)
				}
			}
			if _, err := GenerateMock(tc.svc, mod.Dir, filepath.Join(mod.Dir, "internal", "handlers", "mocks"), nil); err != nil {
				t.Fatalf("GenerateMock: %v", err)
			}
			mod.Write(t, "internal/check/check.go", `// Package check pins the rendered types to what the generated mounts call.
package check

import (
	"github.com/reliant-labs/forge/pkg/mountkit"

	"example.com/v2app/gen/services/items/v1/itemsv1connect"
	"example.com/v2app/internal/handlers/items"
	later "example.com/v2app/internal/handlers/later"
	"example.com/v2app/internal/handlers/mocks"
)

var (
	_ itemsv1connect.ItemsServiceHandler = (*items.Service)(nil)
	_ itemsv1connect.ItemsServiceHandler = (*later.Service)(nil)
	_ itemsv1connect.ItemsServiceHandler = (*mocks.ItemsServiceMock)(nil)
)

// Mount is what mounts_services_gen.go and routes_gen.go emit for the
// service's resolved helper.
func Mount(m *mountkit.MuxV2, a *items.Service, b *later.Service, c *mocks.ItemsServiceMock) {
	m.Register(a.`+tc.helper+`)
	m.Register(b.`+tc.helper+`)
	m.Register(c.`+tc.helper+`)
}
`)
			mod.Go(t, "build", "./internal/...")
			mod.Go(t, "vet", "./internal/check")
		})
	}
}

func TestConnectV2GeneratedMountsCompileWithOwnedCollisions(t *testing.T) {
	if testing.Short() {
		t.Skip("generates real Connect v2 stubs and compiles the generated callers; runs in task test")
	}
	for _, tc := range []struct {
		name, proto, owned, initial, want string
		svc                               ServiceDef
	}{
		{
			name: "removed RPC method stays owned", svc: mountV2Service(), initial: "Mount2V2", want: "Mount2V2",
			proto: strings.Replace(mountV2Proto, "  rpc RegisterV2(RegisterV2Request) returns (RegisterV2Response);\n", "", 1),
			owned: "func (s *Service) RegisterV2(ctx context.Context, req *pb.RegisterV2Request) (*pb.RegisterV2Response, error) { return nil, nil }",
		},
		{
			name: "non-RPC value method stays owned", svc: registerV2Service(), initial: "RegisterV2", want: "Mount2V2", proto: registerV2Proto,
			owned: "func (s Service) MountV2() string { return fmt.Sprint(\"owned\") }",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mod := connectv2test.NewModule(t, v2ItemsModule, map[string]string{"services/items/v1/items.proto": tc.proto})
			svc := tc.svc
			if tc.initial == "Mount2V2" {
				svc.Methods = svc.Methods[1:] // only the removed implementation remains on disk
			}
			dir := filepath.Join(mod.Dir, "internal", "handlers", "items")
			if err := GenerateServiceStub(tc.svc, dir); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "service.go")
			if tc.initial == "RegisterV2" {
				src := strings.ReplaceAll(readFile(t, path), "MountV2", "RegisterV2")
				if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(filepath.Join(dir, RPCHandlerFileName("RegisterV2"))); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Remove(filepath.Join(dir, RPCHandlerFileName("RegisterV2"))); err != nil {
				t.Fatal(err)
			}
			mod.Write(t, "internal/handlers/items/owned.go", `package items
import("context"; "fmt"; pb "example.com/v2app/gen/services/items/v1")
var _ = context.Background
var _ = fmt.Sprint
var _ = (*pb.RegisterV2Request)(nil)
`+tc.owned+"\n")
			in := InventoryGenInput{GenContext: GenContext{ProjectDir: mod.Dir, ModulePath: v2ItemsModule}, Services: []ServiceDef{svc}}
			if err := GenerateInventory(in); err != nil {
				t.Fatal(err)
			}
			if _, err := GenerateMissingHandlerStubs(svc, mod.Dir, dir, nil, nil); err != nil {
				t.Fatal(err)
			}
			if _, err := GenerateMock(svc, mod.Dir, filepath.Join(mod.Dir, "internal", "handlers", "mocks"), nil); err != nil {
				t.Fatal(err)
			}
			mod.Write(t, "internal/app/components.go", `package app
import "example.com/v2app/internal/handlers/items"
type Components struct { Items *items.Service }
`)
			mod.Write(t, "pkg/config/config.go", "package config\ntype Config struct{}\n")
			mod.Write(t, "pkg/middleware/middleware.go", `package middleware
import "github.com/reliant-labs/forge/pkg/authn"
var ClaimsFromContext = authn.ClaimsFromContext
`)
			mod.Write(t, "internal/check/mount.go", `package check
import (
    "github.com/reliant-labs/forge/pkg/mountkit"
    "example.com/v2app/internal/handlers/mocks"
)
func MountMock(m *mountkit.MuxV2, c *mocks.ItemsServiceMock) { m.Register(c.`+tc.want+`) }
`)
			mod.Go(t, "build", "./internal/...")
		})
	}
}

// The `<…>Name` constant the mounts and route sets reference is spelled from
// the raw proto name by v1's plugin and from the GoName by v2's, which differ
// for a service name with a lowercase letter after a digit.
func TestInventoryServiceNameConstPerMajor(t *testing.T) {
	row := InventoryServiceData{Name: "alpha", FieldName: "Alpha", ProtoServiceName: "Alphav1connectService", ConnectPkg: "alphav1connectv1connect", Audience: "user"}
	if got := row.ServiceNameConst(false); got != "Alphav1connectServiceName" {
		t.Errorf("v1 = %q", got)
	}
	if got := row.ServiceNameConst(true); got != "Alphav1ConnectServiceName" {
		t.Errorf("v2 = %q", got)
	}
	for _, v2 := range []bool{false, true} {
		want := row.ServiceNameConst(v2)
		mounts := string(renderProjectTemplate(t, "mounts_services_gen.go.tmpl", InventoryGenData{Module: "example.com/x", Services: []InventoryServiceData{row}, ConnectV2: v2}))
		view := routesGenView{Module: "example.com/x", Services: []routesServiceView{{InventoryServiceData: row, Type: "*alpha.Service"}},
			Audiences: []routesAudienceView{{Name: "UserRoutes", Audience: "user", Default: true}}, ConnectV2: v2}
		routes, err := renderGoTemplate("routes_gen.go.tmpl", view)
		if err != nil {
			t.Fatal(err)
		}
		for name, src := range map[string]string{"mounts_services_gen.go": mounts, "routes_gen.go": string(routes)} {
			if strings.Count(src, "alphav1connectv1connect."+want) < 2 {
				t.Errorf("v2=%v %s must reference alphav1connectv1connect.%s in its mount and its inventory row:\n%s", v2, name, want, src)
			}
		}
	}
}
