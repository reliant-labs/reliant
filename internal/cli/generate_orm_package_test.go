package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
)

// database.orm.package end to end through the pipeline steps (reliant P5b,
// F12.2): the ORM lands in internal/store, beside a hand-written internal/db
// that declares the same names, and the CRUD wiring compiled against it
// imports it from there.
func TestStepInternalDBORM_HonoursTheORMPackage(t *testing.T) {
	if testing.Short() {
		t.Skip("applies the fixture's migrations to a shadow postgres; runs in task test")
	}
	dir := t.TempDir()
	files := map[string]string{
		"go.mod":                           "module example.com/app\n\ngo 1.22\n",
		"forge.yaml":                       "name: app\nmodule_path: example.com/app\ndatabase:\n  orm:\n    package: internal/store\n",
		"db/migrations/00001_tasks.up.sql": "CREATE TABLE tasks (id TEXT PRIMARY KEY, title TEXT NOT NULL);\n",
		// The project's own data layer: forge must not project into it.
		"internal/db/models.go": "package db\n\n// Task is the domain task.\ntype Task struct{ ID string }\n",
		"internal/handlers/task/service.go": `package task

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
		Name: "TaskService", Package: "app.v1", GoPackage: "example.com/app/gen/app/v1;appv1",
		Methods: []codegen.Method{{Name: "GetTask", InputType: "GetTaskRequest", OutputType: "GetTaskResponse"}},
		Messages: map[string][]codegen.MessageFieldDef{
			"Task":            {{Name: "id", ProtoType: "string"}, {Name: "title", ProtoType: "string"}},
			"GetTaskRequest":  {{Name: "id", ProtoType: "string"}},
			"GetTaskResponse": {{Name: "task", ProtoType: "message", MessageType: "app.v1.Task"}},
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
	cfg, err := config.LoadProjectDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	services, err := codegen.ParseServicesFromProtos("", dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := &pipelineContext{ProjectDir: dir, ModulePath: "example.com/app", Cfg: cfg, Services: services}
	var stepErr error
	out := captureStdout(t, func() {
		if stepErr = stepInternalDBORM(ctx); stepErr == nil {
			stepErr = stepCRUDHandlers(ctx)
		}
	})
	if stepErr != nil || ctx.ORMFailure != nil {
		t.Fatalf("steps: %v / %v\n%s", stepErr, ctx.ORMFailure, out)
	}
	if !strings.Contains(out, "Generated internal/store/") {
		t.Errorf("the ORM step does not report where it wrote:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "internal", "store", "task_orm_gen.go")); err != nil {
		t.Fatalf("internal/store/task_orm_gen.go: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "internal", "db", "task_orm_gen.go")); err == nil {
		t.Error("internal/db/task_orm_gen.go was written although the ORM package is internal/store")
	}
	ops, err := os.ReadFile(filepath.Join(dir, "internal", "handlers", "task", "handlers_crud_ops_gen.go"))
	if err != nil {
		t.Fatalf("CRUD ops: %v\n%s", err, out)
	}
	if !strings.Contains(string(ops), `db "example.com/app/internal/store"`) {
		t.Errorf("the CRUD ops must import the relocated ORM:\n%s", ops)
	}
	if strings.Contains(string(ops), `"example.com/app/internal/db"`) {
		t.Errorf("the CRUD ops import the project's own internal/db:\n%s", ops)
	}
	if on, err := deriveOrmEnabled(dir); err != nil || !on {
		t.Errorf("deriveOrmEnabled = %v, %v; the ORM output is in internal/store", on, err)
	}
}
