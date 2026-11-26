//go:build e2e

package cli

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestE2EORMPackageBesideTheProjectsOwnDBBuilds is reliant's transition
// through the real pipeline: the ORM relocated to internal/store
// (database.orm.package) beside the project's own internal/db, and a
// handlers_crud.go that already imports that internal/db as db when forge
// appends scoped CRUD shims to it. The shims name the ORM's *Task, so forge
// must import the store under a name the file does not already bind — it
// used to add `db "…/internal/store"`, and the package stopped compiling
// with `db redeclared in this block`.
func TestE2EORMPackageBesideTheProjectsOwnDBBuilds(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "storeapp", "--mod", "example.com/storeapp", "--service", "widget")
	projectDir := filepath.Join(dir, "storeapp")
	addCorpusForgePkgReplace(t, projectDir)

	forgeYAML := filepath.Join(projectDir, "forge.yaml")
	cfg := readFileE2E(t, forgeYAML)
	if strings.Contains(cfg, "\n  orm:") {
		t.Fatalf("forge.yaml already declares an orm block — fixture is stale:\n%s", cfg)
	}
	const orm = "\n  orm:\n    package: internal/store"
	if loc := regexp.MustCompile(`(?m)^database:[ \t]*$`).FindStringIndex(cfg); loc != nil {
		cfg = cfg[:loc[1]] + orm + cfg[loc[1]:]
	} else {
		cfg = strings.TrimRight(cfg, "\n") + "\ndatabase:" + orm + "\n"
	}
	writeCollideFile(t, forgeYAML, cfg)

	// The project's own data layer binds db. A second package, whose
	// declared name differs from its directory, already binds store.
	writeCollideFile(t, filepath.Join(projectDir, "internal", "db", "facade.go"),
		"package db\n\n// Ping is the hand-written data layer.\nfunc Ping() error { return nil }\n")
	writeCollideFile(t, filepath.Join(projectDir, "internal", "adapter", "adapter.go"),
		"package store\n\n// Query is unrelated to the generated ORM.\nfunc Query() {}\n")

	// CRUD rpcs over a table that already exists (forge:entity would be
	// inert): the migration declares the owner, so both shims are scoped
	// and name the ORM's *Task.
	protoPath := filepath.Join(projectDir, "proto", "services", "widget", "v1", "widget.proto")
	src := readFileE2E(t, protoPath)
	const anchor = "  // TODO: Add your RPC methods here.\n"
	if !strings.Contains(src, anchor) {
		t.Fatalf("widget.proto has no %q anchor — fixture is stale:\n%s", anchor, src)
	}
	src = strings.Replace(src, anchor, "  rpc CreateTask(CreateTaskRequest) returns (CreateTaskResponse);\n  rpc GetTask(GetTaskRequest) returns (GetTaskResponse);\n", 1) + `
message Task {
  string id = 1;
  string owner_id = 2;
  string title = 3;
}
message CreateTaskRequest { string title = 1; }
message CreateTaskResponse { Task task = 1; }
message GetTaskRequest { string id = 1; }
message GetTaskResponse { Task task = 1; }
`
	writeCollideFile(t, protoPath, src)
	writeCollideFile(t, filepath.Join(projectDir, "db", "migrations", "20260101000000_create_tasks.up.sql"),
		"CREATE TABLE tasks (\n    id TEXT PRIMARY KEY,\n    owner_id TEXT NOT NULL,\n    title TEXT NOT NULL\n);\n"+
			"COMMENT ON COLUMN tasks.owner_id IS 'forge:owner';\n")

	// A shim file that exists before any CRUD shim does and holds the
	// facade's db, so both scoped shims are APPENDED to it.
	shimPath := filepath.Join(projectDir, "internal", "handlers", "widget", "handlers_crud.go")
	const facadeImport = "\t\"example.com/storeapp/internal/db\"\n"
	const adapterImport = "\t\"example.com/storeapp/internal/adapter\"\n"
	writeCollideFile(t, shimPath, "package widget\n\nimport (\n"+facadeImport+adapterImport+")\n\nvar _ = db.Ping\nvar _ = store.Query\n")

	runCmd(t, projectDir, forgeBin, "generate")

	shim := readFileE2E(t, shimPath)
	for _, want := range []string{
		facadeImport,
		"\"example.com/storeapp/internal/adapter\"",
		"var _ = store.Query",
		"\tormstore \"example.com/storeapp/internal/store\"\n",
		"*ormstore.Task",
		"func (s *Service) GetTask(",
	} {
		if !strings.Contains(shim, want) {
			t.Errorf("handlers_crud.go missing %q:\n%s", want, shim)
		}
	}
	if strings.Contains(shim, `db "example.com/storeapp/internal/store"`) {
		t.Errorf("the store is imported as db beside the project's own db:\n%s", shim)
	}
	runCmd(t, projectDir, "go", "build", "./...")
	// vet compiles the handler package's test files too: the generated
	// factories and the scaffolded lifecycle test.
	runCmd(t, projectDir, "go", "vet", "./internal/handlers/widget/")
}
