package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
)

// The store is a projection of the APPLIED schema, not of the wire
// (reliant P5b spike, F12.1). reliant marks every CRUD-named rpc
// `// forge:custom` and most of its tables have no rpc at all, so `forge
// generate` projected zero entities: no internal/db/*_orm_gen.go, no
// store, nothing for the data layer to port onto.
func TestStepInternalDBORM_ProjectsEveryTableWhateverTheRPCs(t *testing.T) {
	if testing.Short() {
		t.Skip("applies the fixture's migrations to a shadow postgres; runs in task test")
	}
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.22\n",
		"db/migrations/00001_init.up.sql": `CREATE TABLE tasks (id TEXT PRIMARY KEY, title TEXT NOT NULL);
CREATE TABLE message_content_blocks (id TEXT PRIMARY KEY, message_id TEXT NOT NULL, body TEXT);
CREATE TABLE legacy_audit (id BIGSERIAL PRIMARY KEY, line TEXT NOT NULL);
COMMENT ON TABLE legacy_audit IS 'written by the old pipeline — forge:no-entity';
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
	// Every CRUD-named rpc over tasks is custom: CRUD wiring has nothing to
	// do, and the store must be projected anyway.
	desc := codegen.ForgeDescriptor{Services: []codegen.ServiceDef{{
		Name: "TaskService", Package: "app.v1",
		Methods: []codegen.Method{
			{Name: "GetTask", InputType: "GetTaskRequest", OutputType: "GetTaskResponse", Custom: true},
			{Name: "ListTasks", InputType: "ListTasksRequest", OutputType: "ListTasksResponse", Custom: true},
		},
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

	ctx := &pipelineContext{ProjectDir: dir, ModulePath: "example.com/app"}
	var stepErr error
	out := captureStdout(t, func() { stepErr = stepInternalDBORM(ctx) })
	if stepErr != nil || ctx.ORMFailure != nil {
		t.Fatalf("stepInternalDBORM: %v / %v\n%s", stepErr, ctx.ORMFailure, out)
	}
	for _, rel := range []string{"internal/db/task_orm_gen.go", "internal/db/message_content_block_orm_gen.go"} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Errorf("%s was not projected: %v\n%s", rel, err, out)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "internal/db/legacy_audit_orm_gen.go")); err == nil {
		t.Error("a forge:no-entity table was projected")
	}
	store, err := os.ReadFile(filepath.Join(dir, "internal", "db", "store_gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"type TaskStore interface", "type MessageContentBlockStore interface"} {
		if !strings.Contains(string(store), want) {
			t.Errorf("store_gen.go lacks %q", want)
		}
	}
	if strings.Contains(string(store), "LegacyAudit") {
		t.Error("store_gen.go names the forge:no-entity table")
	}
}

// The ORM gate no longer asks for a service: a worker-only project with a
// schema has a data layer too.
func TestGateORM_NeedsAnEntrypointNotAService(t *testing.T) {
	if !gateORMHasServices(&pipelineContext{HasWorkers: true}) {
		t.Error("a worker-only project with a database must get the ORM projection")
	}
	if gateORMHasServices(&pipelineContext{}) {
		t.Error("with no entrypoint there is no go mod tidy to resolve the ORM's imports; the gate must stay shut")
	}
}
