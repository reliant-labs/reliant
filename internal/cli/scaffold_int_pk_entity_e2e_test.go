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

// TestE2EIntegerPrimaryKey is the acceptance gate for an entity keyed on a
// BIGSERIAL column. The ORM projects its delegates with an int64 key
// (db.GetTaskByID(ctx, db, id int64)) while pkg/crud carries keys as text,
// and the ops file used to pass one for the other, so `forge generate` failed
// its own validate build for the whole CRUD quintet (obs 113d7531). Runs the
// real pipeline, requires build + vet green, and proves Create -> Get ->
// List (two pages, so the cursor crosses the text seam too) -> Update ->
// Delete against real postgres.
func TestE2EIntegerPrimaryKey(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	// --connect v1: the round-trip test below is connect-go v1 code.
	runCmd(t, dir, forgeBin, "project", "new", "intpkapp", "--mod", "example.com/intpkapp", "--service", "tasks", "--connect", "v1")
	projectDir := filepath.Join(dir, "intpkapp")
	addCorpusForgePkgReplace(t, projectDir)

	// The row still uses signed BIGSERIAL and an int64 entity field. The
	// request proto can use unsigned scalars without changing that storage.
	protoPath := filepath.Join(projectDir, "proto", "services", "tasks", "v1", "tasks.proto")
	proto := strings.Replace(readFileE2E(t, protoPath), "  // TODO: Add your RPC methods here.\n", `  rpc CreateTask(CreateTaskRequest) returns (CreateTaskResponse);
  rpc GetTask(GetTaskRequest) returns (GetTaskResponse);
  rpc ListTasks(ListTasksRequest) returns (ListTasksResponse);
  rpc UpdateTask(UpdateTaskRequest) returns (UpdateTaskResponse);
  rpc DeleteTask(DeleteTaskRequest) returns (DeleteTaskResponse);
`, 1) + `
message Task {
  int64 id = 1;
  string title = 2;
}

message CreateTaskRequest {
  string title = 1;
}

message CreateTaskResponse {
  Task task = 1;
}

message GetTaskRequest {
  uint32 id = 1;
}

message GetTaskResponse {
  Task task = 1;
}

message ListTasksRequest {
  int32 page_size = 1;
  string page_token = 2;
}

message ListTasksResponse {
  repeated Task tasks = 1;
  string next_page_token = 2;
}

message UpdateTaskRequest {
  Task task = 1;
}

message UpdateTaskResponse {
  Task task = 1;
}

message DeleteTaskRequest {
  uint64 id = 1;
}

message DeleteTaskResponse {}
`
	if !strings.Contains(proto, "rpc DeleteTask(") {
		t.Fatalf("the scaffolded proto has no RPC placeholder to replace:\n%s", proto)
	}
	if err := os.WriteFile(protoPath, []byte(proto), 0o644); err != nil {
		t.Fatal(err)
	}
	schema := `CREATE TABLE tasks (
    id BIGSERIAL PRIMARY KEY,
    title TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
`
	if err := os.WriteFile(filepath.Join(projectDir, "db", "migrations", "20260101000000_create_tasks.up.sql"), []byte(schema), 0o644); err != nil {
		t.Fatal(err)
	}
	runCmd(t, projectDir, forgeBin, "generate")

	ops := readFileE2E(t, filepath.Join(projectDir, "internal", "handlers", "tasks", "handlers_crud_ops_gen.go"))
	for _, want := range []string{
		"return strconv.FormatUint(uint64(req.Id), 10)",
		"return strconv.FormatUint(req.Id, 10)",
		"key, err := strconv.ParseInt(id, 10, 64)",
		"return strconv.FormatInt(entity.ID, 10)",
	} {
		if !strings.Contains(ops, want) {
			t.Errorf("ops file should contain %q; got:\n%s", want, ops)
		}
	}

	writeIntPKRoundTripTest(t, projectDir)
	runCmd(t, projectDir, "go", "build", "./...")
	runCmd(t, projectDir, "go", "vet", "./...")
	runCmd(t, projectDir, "go", "test", "./internal/handlers/tasks/")
}

// writeIntPKRoundTripTest drops the CRUD round trip into the generated app,
// over the born helpers_gen_test.go harness. (The born lifecycle test,
// handlers_crud_test.go, is written for string keys only.)
func writeIntPKRoundTripTest(t *testing.T, projectDir string) {
	t.Helper()
	helpers := readFileE2E(t, filepath.Join(projectDir, "internal", "handlers", "tasks", "helpers_gen_test.go"))
	m := regexp.MustCompile(`func NewTest(\w+)\(t \*testing\.T, opts \.\.\.TestOption\) \*Service`).FindStringSubmatch(helpers)
	if m == nil || !strings.Contains(helpers, "func NewMigratedTestDB(") {
		t.Fatalf("born helpers_gen_test.go carries no NewTest<X> / NewMigratedTestDB helper:\n%s", helpers)
	}
	test := fmt.Sprintf(`package tasks_test

// Written by forge's integer-key e2e (TestE2EIntegerPrimaryKey): an entity
// keyed on BIGSERIAL survives the whole CRUD quintet.

import (
	"context"
	"math"
	"testing"

	"connectrpc.com/connect"

	pb "example.com/intpkapp/gen/services/tasks/v1"
	"example.com/intpkapp/internal/handlers/tasks"
	"example.com/intpkapp/pkg/middleware"
)

func TestIntegerPrimaryKeyRoundTrip(t *testing.T) {
	db := tasks.NewMigratedTestDB(t)
	svc := tasks.NewTest%[1]s(t, tasks.WithDB(db))
	ctx := middleware.ContextWithClaims(context.Background(),
		&middleware.Claims{UserID: "test-user", Email: "test@example.com"})

	var ids []int64
	for _, title := range []string{"first", "second", "third"} {
		created, err := svc.CreateTask(ctx, connect.NewRequest(&pb.CreateTaskRequest{Title: title}))
		if err != nil {
			t.Fatalf("create %%s: %%v", title, err)
		}
		id := created.Msg.GetTask().GetId()
		if id == 0 {
			t.Fatalf("create %%s read back id 0; the BIGSERIAL default must allocate it", title)
		}
		ids = append(ids, id)
	}

	got, err := svc.GetTask(ctx, connect.NewRequest(&pb.GetTaskRequest{Id: uint32(ids[1])}))
	if err != nil {
		t.Fatalf("get: %%v", err)
	}
	if got.Msg.GetTask().GetTitle() != "second" {
		t.Fatalf("get %%d read back %%q, want second", ids[1], got.Msg.GetTask().GetTitle())
	}

	// Two pages: the cursor is the last row's key as text, and the next
	// page's predicate compares it as a number.
	page1, err := svc.ListTasks(ctx, connect.NewRequest(&pb.ListTasksRequest{PageSize: 2}))
	if err != nil {
		t.Fatalf("list page 1: %%v", err)
	}
	if n := len(page1.Msg.GetTasks()); n != 2 || page1.Msg.GetNextPageToken() == "" {
		t.Fatalf("page 1: %%d rows, token %%q; want 2 rows and a token", n, page1.Msg.GetNextPageToken())
	}
	page2, err := svc.ListTasks(ctx, connect.NewRequest(&pb.ListTasksRequest{PageSize: 2, PageToken: page1.Msg.GetNextPageToken()}))
	if err != nil {
		t.Fatalf("list page 2: %%v", err)
	}
	if rows := page2.Msg.GetTasks(); len(rows) != 1 || rows[0].GetId() != ids[2] {
		t.Fatalf("page 2: %%v, want the third task alone", rows)
	}

	if _, err := svc.UpdateTask(ctx, connect.NewRequest(&pb.UpdateTaskRequest{Task: &pb.Task{Id: ids[0], Title: "renamed"}})); err != nil {
		t.Fatalf("update: %%v", err)
	}
	got, err = svc.GetTask(ctx, connect.NewRequest(&pb.GetTaskRequest{Id: uint32(ids[0])}))
	if err != nil || got.Msg.GetTask().GetTitle() != "renamed" {
		t.Fatalf("get after update: %%v, %%v", got, err)
	}

	if _, err := svc.DeleteTask(ctx, connect.NewRequest(&pb.DeleteTaskRequest{Id: uint64(ids[0])})); err != nil {
		t.Fatalf("delete: %%v", err)
	}
	_, err = svc.GetTask(ctx, connect.NewRequest(&pb.GetTaskRequest{Id: uint32(ids[0])}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("get after delete: %%v, want NotFound", err)
	}

	// A uint32 request need not fit int32: the actual column is BIGINT.
	// A uint64 beyond MaxInt64, however, must not wrap to a signed row key.
	if _, err := db.Exec(ctx, "INSERT INTO tasks (id, title) VALUES ($1, 'wide'), (-1, 'negative'), ($2, 'largest')", int64(math.MaxUint32), int64(math.MaxInt64)); err != nil {
		t.Fatal(err)
	}
	wide, err := svc.GetTask(ctx, connect.NewRequest(&pb.GetTaskRequest{Id: math.MaxUint32}))
	if err != nil || wide.Msg.GetTask().GetId() != math.MaxUint32 {
		t.Fatalf("get MaxUint32 through signed BIGINT: %%v, %%v", wide, err)
	}
	if _, err := svc.DeleteTask(ctx, connect.NewRequest(&pb.DeleteTaskRequest{Id: math.MaxInt64})); err != nil {
		t.Fatalf("delete MaxInt64 through uint64 request: %%v", err)
	}
	if _, err := svc.DeleteTask(ctx, connect.NewRequest(&pb.DeleteTaskRequest{Id: math.MaxUint64})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("delete MaxUint64 must refuse signed overflow: %%v", err)
	}
	var negative int
	if err := db.QueryRow(ctx, "SELECT count(*) FROM tasks WHERE id = -1").Scan(&negative); err != nil || negative != 1 {
		t.Fatalf("overflow touched the negative row: count=%%d, err=%%v", negative, err)
	}
}
`, m[1])
	path := filepath.Join(projectDir, "internal", "handlers", "tasks", "int_pk_roundtrip_test.go")
	if err := os.WriteFile(path, []byte(test), 0o644); err != nil {
		t.Fatal(err)
	}
}
