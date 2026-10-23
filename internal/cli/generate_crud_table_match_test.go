package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
)

// writeDaemonProject is a project whose table is singular-named, the shape
// six of reliant's tables have (reliant P5b, F12.4).
func writeDaemonProject(t *testing.T, methods []codegen.Method, messages map[string][]codegen.MessageFieldDef) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"go.mod":                          "module example.com/app\n\ngo 1.22\n",
		"db/migrations/00001_init.up.sql": "CREATE TABLE daemon_attachment (id TEXT PRIMARY KEY, label TEXT NOT NULL);\n",
		"internal/handlers/daemon/service.go": `package daemon

type Deps struct{}

type Service struct{ deps Deps }
`,
	}
	for rel, body := range files {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	desc := codegen.ForgeDescriptor{Services: []codegen.ServiceDef{{
		Name: "DaemonService", Package: "app.v1", GoPackage: "example.com/app/gen/app/v1;appv1",
		Methods: methods, Messages: messages,
	}}}
	b, err := json.Marshal(desc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "gen"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gen", "forge_descriptor.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func daemonAttachmentWire() map[string][]codegen.MessageFieldDef {
	return map[string][]codegen.MessageFieldDef{
		"DaemonAttachment":            {{Name: "id", ProtoType: "string"}, {Name: "label", ProtoType: "string"}},
		"GetDaemonAttachmentRequest":  {{Name: "id", ProtoType: "string"}},
		"GetDaemonAttachmentResponse": {{Name: "daemon_attachment", ProtoType: "message", MessageType: "app.v1.DaemonAttachment"}},
	}
}

// A Get<E> over a singular-named table is CRUD: it used to look only for
// `daemon_attachments`, find nothing, and become a stub.
func TestStepCRUDHandlers_SingularTableIsWired(t *testing.T) {
	if testing.Short() {
		t.Skip("applies the fixture's migrations to a shadow postgres; runs in task test")
	}
	dir := writeDaemonProject(t,
		[]codegen.Method{{Name: "GetDaemonAttachment", InputType: "GetDaemonAttachmentRequest", OutputType: "GetDaemonAttachmentResponse"}},
		daemonAttachmentWire())
	_, out := ormThenCRUD(t, dir)
	ops, err := os.ReadFile(filepath.Join(dir, "internal", "handlers", "daemon", "handlers_crud_ops_gen.go"))
	if err != nil {
		t.Fatalf("GetDaemonAttachment got no CRUD wiring: %v\n%s", err, out)
	}
	if !strings.Contains(string(ops), "db.GetDaemonAttachmentByID") {
		t.Errorf("the ops do not read daemon_attachment through its store:\n%s", ops)
	}
}

// A Get<E> whose wire message exists and whose table does not is refused by
// name, with the table names it looked for.
func TestStepCRUDHandlers_RefusesACRUDRPCWithNoTable(t *testing.T) {
	if testing.Short() {
		t.Skip("applies the fixture's migrations to a shadow postgres; runs in task test")
	}
	wire := daemonAttachmentWire()
	wire["DaemonLease"] = []codegen.MessageFieldDef{{Name: "id", ProtoType: "string"}}
	dir := writeDaemonProject(t, []codegen.Method{
		{Name: "GetDaemonAttachment", InputType: "GetDaemonAttachmentRequest", OutputType: "GetDaemonAttachmentResponse"},
		{Name: "GetDaemonLease", InputType: "GetDaemonLeaseRequest", OutputType: "GetDaemonLeaseResponse"},
	}, wire)
	services, err := codegen.ParseServicesFromProtos("", dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := &pipelineContext{ProjectDir: dir, ModulePath: "example.com/app", Services: services}
	var stepErr error
	out := captureStdout(t, func() {
		if stepErr = stepInternalDBORM(ctx); stepErr == nil {
			stepErr = stepCRUDHandlers(ctx)
		}
	})
	if stepErr == nil {
		t.Fatalf("GetDaemonLease has a DaemonLease message and no table; want a refusal\n%s", out)
	}
	if !strings.Contains(stepErr.Error(), "DaemonService.GetDaemonLease: entity DaemonLease, no table daemon_leases or daemon_lease") {
		t.Errorf("the refusal must name the rpc and the tables it looked for:\n%v", stepErr)
	}
}

// A CRUD-named rpc over no table that the handler implements by hand is
// custom in fact (reliant: ListArchivedChats, GetRun): nothing is silent
// about it, so it is not refused — only one left as forge's stub is.
func TestStepCRUDHandlers_HandImplementedRPCWithNoTableIsFine(t *testing.T) {
	if testing.Short() {
		t.Skip("applies the fixture's migrations to a shadow postgres; runs in task test")
	}
	wire := daemonAttachmentWire()
	wire["DaemonLease"] = []codegen.MessageFieldDef{{Name: "id", ProtoType: "string"}}
	dir := writeDaemonProject(t, []codegen.Method{
		{Name: "GetDaemonLease", InputType: "GetDaemonLeaseRequest", OutputType: "GetDaemonLeaseResponse"},
	}, wire)
	if err := os.WriteFile(filepath.Join(dir, "internal", "handlers", "daemon", "rpc_get_daemon_lease.go"), []byte(`package daemon

// GetDaemonLease is computed from the scheduler, not a table.
func (s *Service) GetDaemonLease() {}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	services, err := codegen.ParseServicesFromProtos("", dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := &pipelineContext{ProjectDir: dir, ModulePath: "example.com/app", Services: services}
	var stepErr error
	out := captureStdout(t, func() {
		if stepErr = stepInternalDBORM(ctx); stepErr == nil {
			stepErr = stepCRUDHandlers(ctx)
		}
	})
	if stepErr != nil {
		t.Fatalf("a hand-implemented rpc over no table must not be refused: %v\n%s", stepErr, out)
	}
}
