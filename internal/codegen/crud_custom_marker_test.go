package codegen

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

// These tests pin `// forge:custom` (ProtoMarkerCustom): the declared,
// per-rpc way to say "this rpc is not forge CRUD", which the framework guide
// otherwise only lets an author express by renaming the rpc — the move it
// forbids.

func TestCRUDShape_ForgeCustomAndStreamingAreCustom(t *testing.T) {
	cases := []struct {
		m          Method
		op, entity string
	}{
		{Method{Name: "CreateTask"}, "create", "Task"},
		{Method{Name: "ListTasks"}, "list", "Tasks"}, // as written; callers singularize
		{Method{Name: "CreateTask", Custom: true}, "", ""},
		{Method{Name: "ListTasks", Custom: true}, "", ""},
		{Method{Name: "GetTask", ServerStreaming: true}, "", ""},
		{Method{Name: "UpdateTask", ClientStreaming: true}, "", ""},
		{Method{Name: "RecalculateEstimate"}, "", ""},
	}
	for _, c := range cases {
		op, entity := CRUDShape(c.m)
		if op != c.op || entity != c.entity {
			t.Errorf("CRUDShape(%+v) = (%q, %q), want (%q, %q)", c.m, op, entity, c.op, c.entity)
		}
	}
}

func taskTable() schemadef.Table {
	return schemadef.Table{
		Name:   "tasks",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			{Name: "id", Type: schemadef.CanonicalType("string"), IsPK: true, NotNull: true},
			{Name: "title", Type: schemadef.CanonicalType("string")},
		},
	}
}

// taskService is a TaskService whose CRUD quintet is CRUD by name, with the
// rpcs named in custom marked `// forge:custom`.
func taskService(custom ...string) ServiceDef {
	marked := map[string]bool{}
	for _, c := range custom {
		marked[c] = true
	}
	var methods []Method
	for _, name := range []string{"CreateTask", "GetTask", "UpdateTask", "DeleteTask", "ListTasks"} {
		methods = append(methods, Method{Name: name, InputType: name + "Request", OutputType: name + "Response", Custom: marked[name]})
	}
	return ServiceDef{Name: "TaskService", Package: "services.task.v1", Methods: methods}
}

// TestEntities_EveryRPCCustomMeansNoEntity is the per-entity opt-out: marked
// on all five CRUD-named rpcs, the table is plain schema — no entity, so no
// ORM struct in internal/db (the `Task redeclared` collision a project with
// its own Task type hit), no CRUD wiring, no page.
func TestEntities_EveryRPCCustomMeansNoEntity(t *testing.T) {
	all := taskService("CreateTask", "GetTask", "UpdateTask", "DeleteTask", "ListTasks")
	if got := entitiesForTables([]schemadef.Table{taskTable()}, []ServiceDef{all}); len(got) != 0 {
		t.Fatalf("every Task rpc is forge:custom, yet entities = %v", got)
	}
}

// TestEntities_PartlyCustomKeepsTheEntity: one custom rpc leaves the rest of
// the entity CRUD — the marker removes that rpc, not the entity.
func TestEntities_PartlyCustomKeepsTheEntity(t *testing.T) {
	svc := taskService("CreateTask")
	entities := entitiesForTables([]schemadef.Table{taskTable()}, []ServiceDef{svc})
	if len(entities) != 1 || entities[0].Name != "Task" {
		t.Fatalf("entities = %v, want [Task]", entities)
	}
	var got []string
	for _, cm := range MatchCRUDMethods(svc, entities) {
		got = append(got, cm.Method.Name)
	}
	want := []string{"GetTask", "UpdateTask", "DeleteTask", "ListTasks"}
	if len(got) != len(want) {
		t.Fatalf("MatchCRUDMethods = %v, want %v (CreateTask is forge:custom)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("MatchCRUDMethods = %v, want %v", got, want)
		}
	}
}

func TestCustomMarker_Recognition(t *testing.T) {
	cases := []struct {
		comment string
		marks   bool
		near    string
	}{
		{" forge:custom\n", true, ""},
		{" Creates a task.\n forge:custom — also wires dependencies\n", true, ""},
		{" forge:custom: wires dependencies\n", true, ""},
		{" see forge:custom in the docs\n", false, ""},                  // prose, not a marker line
		{" forge:custom-read-shape: x\n", false, ""},                    // a Go marker, not this one
		{" forge:customs\n", false, "forge:customs"},                    // near miss
		{" forge:cusotm\n", false, "forge:cusotm"},                      // near miss
		{" forge:custom-rpc\n", false, "forge:custom-rpc"},              // near miss
		{" forge:mutation\n", false, ""},                                // another known marker
		{" forge:unmanaged — not here\n", false, ""},                    // known, not near
		{" forge:custom\n forge:mutation\n", true, ""},                  // stacked markers
		{" plain doc comment with no marker at all\n", false, ""},       // nothing
		{" forge:customize the form later\n", false, "forge:customize"}, // near miss, refused
	}
	for _, c := range cases {
		if got := CommentMarksCustom(c.comment); got != c.marks {
			t.Errorf("CommentMarksCustom(%q) = %v, want %v", c.comment, got, c.marks)
		}
		if got := CustomMarkerNearMiss(c.comment); got != c.near {
			t.Errorf("CustomMarkerNearMiss(%q) = %q, want %q", c.comment, got, c.near)
		}
	}
	if !IsKnownProtoMarker(ProtoMarkerCustom) {
		t.Errorf("%s is not in KnownProtoMarkers: the proto-markers lint would call it unknown", ProtoMarkerCustom)
	}
}

// TestRawScan_RPCCustomPlacement pins the raw scanner's reading of the
// marker — where the author WROTE it, which the list-filter lint and
// generate's descriptor cross-check consume: every comment line above the rpc
// up to the previous declaration (a blank line does not detach it), never a
// trailing comment (the descriptor path refuses those), and never a marker
// above the service a one-line body opens.
func TestRawScan_RPCCustomPlacement(t *testing.T) {
	src := `syntax = "proto3";
package services.task.v1;

service TaskService {
  // Creates a task.
  // forge:custom — also wires dependencies
  rpc CreateTask(CreateTaskRequest) returns (CreateTaskResponse);

  // forge:custom

  rpc GetTask(GetTaskRequest) returns (GetTaskResponse);
  rpc UpdateTask(UpdateTaskRequest) returns (UpdateTaskResponse); // forge:custom
  rpc DeleteTask(DeleteTaskRequest) returns (DeleteTaskResponse);
  // an ordinary comment
  rpc ListTasks(ListTasksRequest) returns (ListTasksResponse);
}

// forge:custom
service OneLine { rpc CreateWidget(CreateWidgetRequest) returns (CreateWidgetResponse); }
`
	path := filepath.Join(t.TempDir(), "task.proto")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	scan, err := ScanRawProtoFiles([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"CreateTask":   true,
		"GetTask":      true,  // a blank line under the marker does not turn it off
		"UpdateTask":   false, // trailing on the rpc line: refused by generate, never custom
		"DeleteTask":   false,
		"ListTasks":    false,
		"CreateWidget": false, // the marker above `service OneLine {` is the service's
	}
	for _, rpc := range scan.RPCs {
		w, ok := want[rpc.Name]
		if !ok {
			continue
		}
		if rpc.Custom != w {
			t.Errorf("%s: Custom = %v, want %v", rpc.Name, rpc.Custom, w)
		}
		delete(want, rpc.Name)
	}
	for name := range want {
		t.Errorf("raw scan did not capture rpc %s", name)
	}
}
