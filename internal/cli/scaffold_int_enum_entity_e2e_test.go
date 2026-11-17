//go:build e2e

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestE2EIntegerEnumColumns is the acceptance gate for a proto enum stored in
// an INTEGER column — the wire number, reliant's choice for Task.status.
// forge births enum columns as TEXT holding value names, but SQL is the
// schema: a column the author declared integer used to fail generate with
// "no conversion between wire kind enum and column type INT4".
//
// The entity carries the full mix — a plain enum (NOT NULL DEFAULT 1, so an
// unset enum stores the declared default), an `optional` enum over a nullable
// column, and a `repeated` enum over INTEGER[] — and an `optional` enum list
// filter. Runs the real pipeline, requires build + vet green, and proves the
// runtime round-trip (Create → Get → List(filter)) against real postgres.
func TestE2EIntegerEnumColumns(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "intenumapp", "--mod", "example.com/intenumapp", "--service", "tasks")
	projectDir := filepath.Join(dir, "intenumapp")
	addCorpusForgePkgReplace(t, projectDir)

	protoPath := filepath.Join(projectDir, "proto", "services", "tasks", "v1", "tasks.proto")
	proto := readFileE2E(t, protoPath) + `
enum TaskStatus {
  TASK_STATUS_UNSPECIFIED = 0;
  TASK_STATUS_PENDING = 1;
  TASK_STATUS_RUNNING = 2;
  TASK_STATUS_DONE = 3;
}

// forge:entity
message Task {
  string id = 1;
  string title = 2;
  TaskStatus status = 3;
  optional TaskStatus fallback_status = 4;
  repeated TaskStatus history = 5;
}

message ListTasksRequest {
  int32 page_size = 1;
  string page_token = 2;
  optional TaskStatus status = 3;
}
`
	if err := os.WriteFile(protoPath, []byte(proto), 0o644); err != nil {
		t.Fatal(err)
	}
	// Birth completes the quintet; then the author's schema replaces the
	// born TEXT columns with integers, as a project that already stores
	// its enums that way declares them.
	runCmd(t, projectDir, forgeBin, "scaffold")
	born, _ := filepath.Glob(filepath.Join(projectDir, "db", "migrations", "*_create_tasks.up.sql"))
	if len(born) != 1 {
		t.Fatalf("want one born tasks migration, got %v", born)
	}
	intSchema := `CREATE TABLE tasks (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    status INTEGER NOT NULL DEFAULT 1,
    fallback_status INTEGER,
    history INTEGER[] NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
`
	if err := os.Remove(born[0]); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "db", "migrations", "20260101000000_create_tasks.up.sql"), []byte(intSchema), 0o644); err != nil {
		t.Fatal(err)
	}
	runCmd(t, projectDir, forgeBin, "generate")

	ops := readFileE2E(t, filepath.Join(projectDir, "internal", "handlers", "tasks", "handlers_crud_ops_gen.go"))
	for _, want := range []string{
		// fromProto: a sent value stores its NUMBER. INTEGER is int32 (#991)
		// and, DEFAULT 1 not being its zero, a pointer whose nil is unset —
		// so an unset enum is omitted and the database stores its DEFAULT
		// (F12.5, #990).
		"v := int32(m.Status)",
		"e.Status = &v",
		"if m.FallbackStatus != nil && *m.FallbackStatus != 0 {", // optional → nullable: unset is NULL
		"e.History = append(e.History, int32(v))",
		"m.Status = pb.TaskStatus(*e.Status)", // toProto: the number IS the wire value
		"m.History = append(m.History, pb.TaskStatus(v))",
		`orm.WhereEq("status", int32(*req.Status))`, // the filter binds the number
	} {
		if !strings.Contains(ops, want) {
			t.Errorf("ops file should contain %q; got:\n%s", want, ops)
		}
	}
	if strings.Contains(ops, "e.Status = 1") {
		t.Error("the create op must leave an unset enum to the column DEFAULT, not paste its literal")
	}
	if strings.Contains(ops, `orm.WhereEq("status", (*req.Status).String())`) {
		t.Error("an integer enum column's filter must not bind the value name (Postgres integer = text)")
	}

	writeIntEnumRoundTripTest(t, projectDir)
	runCmd(t, projectDir, "go", "build", "./...")
	runCmd(t, projectDir, "go", "vet", "./...")
	runCmd(t, projectDir, "go", "test", "./internal/handlers/tasks/")
}

// writeIntEnumRoundTripTest drops a Create → Get → List(filter) test into the
// generated app, reusing the born harness helpers, as the TEXT-enum e2e does.
func writeIntEnumRoundTripTest(t *testing.T, projectDir string) {
	t.Helper()
	born := readFileE2E(t, filepath.Join(projectDir, "internal", "handlers", "tasks", "handlers_crud_test.go"))
	m := regexp.MustCompile(`(\w+)\.NewTest(\w+)\(`).FindStringSubmatch(born)
	if m == nil {
		t.Fatalf("born handlers_crud_test.go carries no <pkg>.NewTest<X> helper call:\n%s", born)
	}
	pkgAlias, helper := m[1], m[2]
	pbImport := regexp.MustCompile(`pb "([^"]+)"`).FindStringSubmatch(born)
	svcImport := regexp.MustCompile(pkgAlias + ` "([^"]+)"`).FindStringSubmatch(born)
	if pbImport == nil || svcImport == nil {
		t.Fatalf("born handlers_crud_test.go lacks the pb or handler-package import:\n%s", born)
	}
	test := fmt.Sprintf(`package tasks_test

// Written by forge's integer-enum e2e (TestE2EIntegerEnumColumns): enum
// fields survive Create -> Get -> List(filter) stored as wire NUMBERS.

import (
	"testing"

	"connectrpc.com/connect"

	pb "%[1]s"
	%[3]s "%[4]s"
)

func TestIntegerEnumRoundTrip(t *testing.T) {
	db := crudTestDB(t)
	svc := %[3]s.NewTest%[2]s(t, %[3]s.WithDB(db))
	ctx := crudTestCtx()

	fallback := pb.TaskStatus_TASK_STATUS_DONE
	created, err := svc.CreateTask(ctx, connect.NewRequest(&pb.CreateTaskRequest{
		Title:          "int-enum-roundtrip",
		Status:         pb.TaskStatus_TASK_STATUS_RUNNING,
		FallbackStatus: &fallback,
		History:        []pb.TaskStatus{pb.TaskStatus_TASK_STATUS_PENDING, pb.TaskStatus_TASK_STATUS_RUNNING},
	}))
	if err != nil {
		t.Fatalf("create: %%v", err)
	}
	got, err := svc.GetTask(ctx, connect.NewRequest(&pb.GetTaskRequest{Id: created.Msg.GetTask().GetId()}))
	if err != nil {
		t.Fatalf("get: %%v", err)
	}
	task := got.Msg.GetTask()
	if task.GetStatus() != pb.TaskStatus_TASK_STATUS_RUNNING || task.GetFallbackStatus() != pb.TaskStatus_TASK_STATUS_DONE {
		t.Fatalf("enums did not survive create->get: status %%v, fallback %%v", task.GetStatus(), task.GetFallbackStatus())
	}
	if h := task.GetHistory(); len(h) != 2 || h[0] != pb.TaskStatus_TASK_STATUS_PENDING || h[1] != pb.TaskStatus_TASK_STATUS_RUNNING {
		t.Fatalf("repeated enum did not survive create->get: %%v", h)
	}

	// Unset status stores the column DEFAULT (1 = PENDING), not 0.
	unset, err := svc.CreateTask(ctx, connect.NewRequest(&pb.CreateTaskRequest{Title: "defaulted"}))
	if err != nil {
		t.Fatalf("create unset: %%v", err)
	}
	if s := unset.Msg.GetTask().GetStatus(); s != pb.TaskStatus_TASK_STATUS_PENDING {
		t.Fatalf("unset status read back %%v, want the column DEFAULT (PENDING)", s)
	}

	match := pb.TaskStatus_TASK_STATUS_RUNNING
	listed, err := svc.ListTasks(ctx, connect.NewRequest(&pb.ListTasksRequest{PageSize: 10, Status: &match}))
	if err != nil {
		t.Fatalf("list with enum filter: %%v", err)
	}
	if n := len(listed.Msg.GetTasks()); n != 1 {
		t.Fatalf("enum filter matched %%d rows, want 1", n)
	}
}
`, pbImport[1], helper, pkgAlias, svcImport[1])
	path := filepath.Join(projectDir, "internal", "handlers", "tasks", "int_enum_roundtrip_test.go")
	if err := os.WriteFile(path, []byte(test), 0o644); err != nil {
		t.Fatal(err)
	}
}
