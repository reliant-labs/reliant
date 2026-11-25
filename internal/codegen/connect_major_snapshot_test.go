package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/connectmajor"
)

// `forge generate` reads the connect-go major once per run and stamps it on
// the services it parsed (WithConnectMajor). Every per-service emitter must
// render THAT major, not re-read go.mod: a tidy or a user hook can rewrite
// go.mod mid-run, and the project wiring already follows the snapshot. So
// against a go.mod that says the opposite, every output follows the stamp.
func TestConnectMajorSnapshotWinsOverGoMod(t *testing.T) {
	t.Parallel()
	const v1GoMod = "module example.com/test\n\ngo 1.24\n\nrequire connectrpc.com/connect v1.19.0\n"
	const v2GoMod = "module example.com/test\n\ngo 1.24\n\nrequire connectrpc.com/connect/v2 v2.0.2\n"
	for _, tc := range []struct {
		name     string
		gomod    string
		snapshot connectmajor.Major
	}{
		{name: "v1 go.mod, v2 snapshot", gomod: v1GoMod, snapshot: connectmajor.V2},
		{name: "v2 go.mod, v1 snapshot", gomod: v2GoMod, snapshot: connectmajor.V1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			projectDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(projectDir, "go.mod"), []byte(tc.gomod), 0o644); err != nil {
				t.Fatal(err)
			}
			v2 := tc.snapshot.IsV2()
			if got := connectmajor.DetectOrDefault(projectDir).IsV2(); got == v2 {
				t.Fatalf("fixture go.mod already selects the snapshot's major")
			}

			parsed := snapshotPatientsService()
			stamped := WithConnectMajor([]ServiceDef{parsed}, tc.snapshot)
			if parsed.ConnectMajor != 0 {
				t.Fatal("WithConnectMajor modified its input")
			}
			svc := stamped[0]
			handlerDir := filepath.Join(projectDir, "internal", "handlers", "patients")
			crud := map[string]bool{"GetPatient": true, "CreatePatient": true}

			// service.go + rpc_ping.go (handler stubs, scaffold tests).
			if err := GenerateServiceStub(svc, handlerDir, crud); err != nil {
				t.Fatalf("GenerateServiceStub: %v", err)
			}
			// A method added later goes through the missing-stub path.
			grown := svc
			grown.Methods = append(append([]Method(nil), svc.Methods...),
				Method{Name: "Echo", InputType: "EchoRequest", OutputType: "EchoResponse"})
			if _, err := GenerateMissingHandlerStubs(grown, projectDir, handlerDir, crud, nil); err != nil {
				t.Fatalf("GenerateMissingHandlerStubs: %v", err)
			}
			entities := []EntityDef{{
				Name: "Patient", TableName: "patients", PkField: "id", PkGoType: "string",
				Fields: []EntityField{
					{Name: "id", GoName: "Id", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
					{Name: "name", GoName: "Name", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
				},
			}}
			if _, err := GenerateCRUDHandlers(svc, MatchCRUDMethods(svc, entities), "example.com/test", projectDir, nil); err != nil {
				t.Fatalf("GenerateCRUDHandlers: %v", err)
			}
			if _, err := GenerateCRUDTests(svc, MatchCRUDMethods(svc, entities), "example.com/test", projectDir, nil); err != nil {
				t.Fatalf("GenerateCRUDTests: %v", err)
			}
			mockDir := filepath.Join(projectDir, "internal", "handlers", "mocks")
			if _, err := GenerateMock(svc, projectDir, mockDir, nil); err != nil {
				t.Fatalf("GenerateMock: %v", err)
			}
			// The command tree is born beside its Tier-1 root.go anchor; a
			// legacy-composition project (NewComponents) gets the serve.go
			// whose wiring names the connect-go major.
			for rel, body := range map[string]string{
				"cmd/test/cmd/root.go":       "package cmd\n",
				"internal/app/components.go": "package app\n\nfunc NewComponents() {}\n",
			} {
				path := filepath.Join(projectDir, filepath.FromSlash(rel))
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := GenerateCmdServerForConfigs(DefaultConfigFieldNames(), nil, tc.snapshot, projectDir, nil); err != nil {
				t.Fatalf("GenerateCmdServerForConfigs: %v", err)
			}

			// Each output's major-specific shape, and the other major's
			// absence.
			read := func(rel string) string {
				t.Helper()
				b, err := os.ReadFile(filepath.Join(projectDir, rel))
				if err != nil {
					t.Fatal(err)
				}
				return string(b)
			}
			for _, f := range []struct {
				rel    string
				v1, v2 string
			}{
				{"internal/handlers/patients/service.go", "Register(mux *http.ServeMux", "RegisterV2(server *connect.Server)"},
				{"internal/handlers/patients/rpc_ping.go", "connect.Request[pb.PingRequest]", "req *pb.PingRequest,"},
				{"internal/handlers/patients/rpc_echo.go", "connect.Request[pb.EchoRequest]", "req *pb.EchoRequest,"},
				{"internal/handlers/patients/handlers_crud.go", "connect.Request[pb.GetPatientRequest]", "req *pb.GetPatientRequest,"},
				{"internal/handlers/patients/handlers_crud_test.go", "connect.NewRequest(", `connect "connectrpc.com/connect/v2"`},
				{filepath.ToSlash(MockRelPath(svc)), `"connectrpc.com/connect"`, `connect "connectrpc.com/connect/v2"`},
				{primaryCmdServeRel(t, projectDir), "middleware.NewAuthInterceptor(", "middleware.NewAuthInterceptorV2("},
			} {
				body := read(f.rel)
				want, not := f.v1, f.v2
				if v2 {
					want, not = f.v2, f.v1
				}
				if !strings.Contains(body, want) {
					t.Errorf("%s: snapshot %s, but the file lacks %q:\n%s", f.rel, tc.snapshot, want, body)
				}
				if strings.Contains(body, not) {
					t.Errorf("%s: snapshot %s, but the file renders the other major's %q (go.mod's):\n%s", f.rel, tc.snapshot, not, body)
				}
			}
		})
	}
}

// primaryCmdServeRel is the serve.go GenerateCmdServerForConfigs wrote, relative
// to projectDir.
func primaryCmdServeRel(t *testing.T, projectDir string) string {
	t.Helper()
	var found string
	_ = filepath.WalkDir(filepath.Join(projectDir, "cmd"), func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() == "serve.go" {
			found, _ = filepath.Rel(projectDir, path)
		}
		return nil
	})
	if found == "" {
		t.Fatal("GenerateCmdServerForConfigs wrote no serve.go under cmd/")
	}
	return filepath.ToSlash(found)
}

func snapshotPatientsService() ServiceDef {
	return ServiceDef{
		Name:       "PatientsService",
		Package:    "patients.v1",
		GoPackage:  "example.com/test/gen/services/patients/v1",
		PkgName:    "patientsv1",
		ProtoFile:  "proto/services/patients/v1/patients.proto",
		ModulePath: "example.com/test",
		Methods: []Method{
			{Name: "GetPatient", InputType: "GetPatientRequest", OutputType: "GetPatientResponse", AuthRequired: true},
			{Name: "CreatePatient", InputType: "CreatePatientRequest", OutputType: "CreatePatientResponse", AuthRequired: true},
			{Name: "Ping", InputType: "PingRequest", OutputType: "PingResponse", AuthRequired: true},
		},
	}
}
