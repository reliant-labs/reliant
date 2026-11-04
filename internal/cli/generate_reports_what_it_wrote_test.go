package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
)

// A generate that continues past a failed step used to report the step's
// dependents as if they had run. reliant main, pinned forge 8d812662:
// "⚠️ Warning: ORM generation failed", then "✅ Generated internal/db/ (2
// entity ORM files)", "✅ Generated handlers/message CRUD wiring …" and
// "✅ Generated …/handlers_crud_test.go" — and none of those files existed.
// The ORM line printed after the failure, and the CRUD lines printed after
// calls that returned early having written nothing (every CRUD rpc was
// hand-written).

// orderlessTaskProject is a project with one entity, tasks, behind
// TaskService.GetTask, whose handler package implements GetTask by hand.
func orderlessTaskProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"go.mod":                           "module example.com/app\n\ngo 1.22\n",
		"db/migrations/00001_tasks.up.sql": "CREATE TABLE tasks (id TEXT PRIMARY KEY, title TEXT NOT NULL);\n",
		"internal/handlers/task/service.go": `package task

import (
	"context"

	"connectrpc.com/connect"
)

type GetTaskRequest struct{ Id string }
type GetTaskResponse struct{}

type Deps struct{}

type Service struct{ deps Deps }

// GetTask is implemented by hand: there is nothing for CRUD to wire.
func (s *Service) GetTask(ctx context.Context, req *connect.Request[GetTaskRequest]) (*connect.Response[GetTaskResponse], error) {
	return connect.NewResponse(&GetTaskResponse{}), nil
}
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
		Name: "TaskService", Package: "app.v1",
		Methods: []codegen.Method{{Name: "GetTask", InputType: "GetTaskRequest", OutputType: "GetTaskResponse"}},
	}}}
	b, err := json.Marshal(desc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "gen"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "gen", "forge_descriptor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gen", "forge_descriptor", "fixture.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func ormThenCRUD(t *testing.T, dir string) (*pipelineContext, string) {
	t.Helper()
	services, err := codegen.ParseServicesFromProtos("", dir)
	if err != nil {
		t.Fatalf("parse services: %v", err)
	}
	ctx := &pipelineContext{ProjectDir: dir, ModulePath: "example.com/app", Services: services}
	var stepErr error
	out := captureStdout(t, func() {
		if stepErr = stepInternalDBORM(ctx); stepErr == nil {
			stepErr = stepCRUDHandlers(ctx)
		}
	})
	if stepErr != nil {
		t.Fatalf("steps: %v\n%s", stepErr, out)
	}
	return ctx, out
}

func TestGenerate_ADependentOfAFailedORMStepSaysItWasSkipped(t *testing.T) {
	if testing.Short() {
		t.Skip("applies the fixture's migrations to a shadow postgres; runs in task test")
	}
	dir := orderlessTaskProject(t)
	// The project's own data layer already declares Task: the ORM refuses.
	if err := os.MkdirAll(filepath.Join(dir, "internal", "db"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "internal", "db", "models.go"),
		[]byte("package db\n\ntype Task struct{ ID string }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, out := ormThenCRUD(t, dir)
	if ctx.ORMFailure == nil {
		t.Fatalf("the ORM step did not fail on a name internal/db already declares; the premise is gone:\n%s", out)
	}
	if strings.Contains(out, "✅ Generated internal/db/") {
		t.Errorf("a failed ORM step reported its output as generated:\n%s", out)
	}
	if len(ctx.SoftFailures) != 1 || ctx.SoftFailures[0] != "ORM generation" {
		t.Errorf("the run's closing summary must name the failed step; SoftFailures = %v", ctx.SoftFailures)
	}
	if strings.Contains(out, "✅ Generated handlers/") {
		t.Errorf("CRUD wiring reported as generated after the ORM it is written against failed:\n%s", out)
	}
	if !strings.Contains(out, "Skipped: the ORM step above failed") {
		t.Errorf("the CRUD step must say it was skipped, and why:\n%s", out)
	}
	for _, rel := range []string{"internal/db/task_orm_gen.go", "internal/handlers/task/handlers_crud_ops_gen.go", "internal/handlers/task/handlers_crud.go"} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err == nil {
			t.Errorf("%s was written", rel)
		}
	}
}

func TestGenerate_CRUDLinesReportWhatWasWritten(t *testing.T) {
	if testing.Short() {
		t.Skip("applies the fixture's migrations to a shadow postgres; runs in task test")
	}
	dir := orderlessTaskProject(t)

	_, out := ormThenCRUD(t, dir)
	if !strings.Contains(out, "✅ Generated internal/db/") {
		t.Fatalf("the ORM step did not run; the premise is gone:\n%s", out)
	}
	if strings.Contains(out, "✅ Generated handlers/task CRUD wiring") ||
		strings.Contains(out, "✅ Generated handlers/task/handlers_crud_test.go") {
		t.Errorf("a CRUD line claimed a write when every CRUD rpc is hand-written:\n%s", out)
	}
	for _, want := range []string{
		"handlers/task: no CRUD wiring written — every CRUD rpc (GetTask) is implemented by hand",
		"handlers/task/handlers_crud_test.go not written",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not say %q:\n%s", want, out)
		}
	}
	for _, rel := range []string{"internal/handlers/task/handlers_crud_ops_gen.go", "internal/handlers/task/handlers_crud_test.go"} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err == nil {
			t.Errorf("%s exists, but the run said it wrote nothing", rel)
		}
	}
}

// The completion line of a run that continued past a failed step names it.
func TestPrintSoftFailures(t *testing.T) {
	var b strings.Builder
	printSoftFailures(&b, nil)
	if b.Len() != 0 {
		t.Errorf("a clean run printed %q", b.String())
	}
	printSoftFailures(&b, []string{"ORM generation"})
	if !strings.Contains(b.String(), "1 step(s) FAILED") || !strings.Contains(b.String(), "ORM generation") {
		t.Errorf("summary = %q", b.String())
	}
}
