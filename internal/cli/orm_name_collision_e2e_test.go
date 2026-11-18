//go:build e2e

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestE2EORMRefusesANameTheProjectAlreadyDeclares reproduces reliant's P3b
// collision through the real pipeline: a project adopting forge already has
// an internal/db of its own, declaring the entity's name (reliant: `type Task
// = core.Task`) next to a hand-written types.go.
//
// Pinned:
//  1. generate refuses BEFORE the validate build (non-fatal to the other steps by default, fatal
//     under --strict), naming the project's
//     declaration (file:line), the entity and table behind the clash, and the
//     ways out — where it used to fail on `Task redeclared` and revert;
//  2. the hand-written internal/db/types.go survives every run (the
//     projection used to delete any types.go there);
//  3. handing the name to forge (renaming the project's type) generates and
//     builds.
func TestE2EORMRefusesANameTheProjectAlreadyDeclares(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "collideapp", "--mod", "example.com/collideapp", "--service", "widget")
	projectDir := filepath.Join(dir, "collideapp")
	addCorpusForgePkgReplace(t, projectDir)

	protoPath := filepath.Join(projectDir, "proto", "services", "widget", "v1", "widget.proto")
	src := readFileE2E(t, protoPath)
	const anchor = "  // TODO: Add your RPC methods here.\n"
	if !strings.Contains(src, anchor) {
		t.Fatalf("widget.proto has no %q anchor — fixture is stale:\n%s", anchor, src)
	}
	src = strings.Replace(src, anchor, "  rpc GetTask(GetTaskRequest) returns (GetTaskResponse);\n", 1) + `
message Task {
  string id = 1;
  string title = 2;
}
message GetTaskRequest { string id = 1; }
message GetTaskResponse { Task task = 1; }
`
	writeCollideFile(t, protoPath, src)
	writeCollideFile(t, filepath.Join(projectDir, "db", "migrations", "20260101000000_create_tasks.up.sql"),
		"CREATE TABLE tasks (\n    id TEXT PRIMARY KEY,\n    title TEXT NOT NULL\n);\n")
	dbDir := filepath.Join(projectDir, "internal", "db")
	types := "package db\n\n// Cursor pages the hand-written queries.\ntype Cursor string\n"
	writeCollideFile(t, filepath.Join(dbDir, "types.go"), types)
	models := "package db\n\n// Task is the hand-written data layer's task row.\ntype Task struct {\n\tID    string\n\tTitle string\n}\n"
	writeCollideFile(t, filepath.Join(dbDir, "models.go"), models)

	// ── (1) refused, with the declaration and the way out ───────────────
	// A default generate runs the steps that do not need the refused ORM,
	// says which were skipped, and exits non-zero; --strict stops at the
	// refusal.
	out, err := runCmdAllowFail(t, projectDir, forgeBin, "generate")
	if err == nil {
		t.Fatalf("generate must exit non-zero when its ORM step is refused:\n%s", out)
	}
	for _, w := range []string{
		"ORM generation failed",
		"the other steps ran and their output stands",
		"Task, declared at internal/db/models.go:4",
		"for entity Task (table tasks)",
		"//forge:no-orm",
		"Skipped: the ORM step above failed",
		"1 step(s) FAILED",
	} {
		if !strings.Contains(out, w) {
			t.Errorf("refusal does not mention %q:\n%s", w, out)
		}
	}
	if strings.Contains(out, "redeclared in this block") {
		t.Errorf("the collision reached the validate build instead of being refused up front:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(dbDir, "task_orm_gen.go")); err == nil {
		t.Errorf("task_orm_gen.go was written despite the refusal")
	}
	strictOut, strictErr := runCmdAllowFail(t, projectDir, forgeBin, "generate", "--strict")
	if strictErr == nil {
		t.Fatalf("generate --strict must fail on an ORM that redeclares internal/db/models.go's Task:\n%s", strictOut)
	}
	if !strings.Contains(strictOut, "Task, declared at internal/db/models.go:4") {
		t.Errorf("--strict failure does not name the declaration:\n%s", strictOut)
	}

	// ── (2) + (3) hand the name to forge ─────────────────────────────────
	writeCollideFile(t, filepath.Join(dbDir, "models.go"), strings.Replace(models, "type Task struct", "type TaskRow struct", 1))
	runCmd(t, projectDir, forgeBin, "generate")
	if _, err := os.Stat(filepath.Join(dbDir, "task_orm_gen.go")); err != nil {
		t.Errorf("the Task entity was not projected once the name was free: %v", err)
	}
	if got := readFileE2E(t, filepath.Join(dbDir, "types.go")); got != types {
		t.Errorf("the hand-written internal/db/types.go was changed or deleted; now:\n%s", got)
	}
	runCmd(t, projectDir, "go", "build", "./...")
}

func writeCollideFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
