package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/compiler/protogen"

	"github.com/reliant-labs/forge/internal/codegen"
)

// `// forge:custom` (codegen.ProtoMarkerCustom) declares one rpc custom
// whatever its name. These pin the descriptor half of the marker and the
// generate-side consumers that decide what an rpc gets: a stub, or nothing
// because the CRUD step implements it.

// The marker must survive the protogen round trip from both placements the
// raw scanner also reads — attached, and detached above — and touch only its
// own rpc. buildUnmanagedPlugin's BillingService.GetInvoice is CRUD-named,
// which is the case the marker exists for.
func TestExtractService_CustomMarker(t *testing.T) {
	getInvoice := []int32{6, 1, 2, 0}
	cases := []struct {
		name    string
		comment unmanagedComment
		want    bool
	}{
		{"leading", unmanagedComment{path: getInvoice, leading: " forge:custom\n"}, true},
		{"leading with prose", unmanagedComment{path: getInvoice, leading: " Reads an invoice with its payments.\n forge:custom — joins payments\n"}, true},
		{"detached block above", unmanagedComment{path: getInvoice, detached: []string{" forge:custom\n"}, leading: " Reads an invoice.\n"}, true},
		{"prose mention", unmanagedComment{path: getInvoice, leading: " Unlike forge:custom rpcs this one is CRUD.\n"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := buildUnmanagedPlugin(t, c.comment)
			if err := checkCustomMarkers(f); err != nil {
				t.Fatalf("checkCustomMarkers: %v", err)
			}
			billing := extractService(f, f.Services[1])
			if got := billing.Methods[0].Custom; got != c.want {
				t.Errorf("GetInvoice.Custom = %v, want %v", got, c.want)
			}
			if extractService(f, f.Services[0]).Methods[0].Custom {
				t.Error("ChatService.SendMessage carries no marker but came out custom")
			}
		})
	}
}

// A forge:custom that does nothing where it is written leaves forge wiring
// generic CRUD over the logic it was written to protect, so extraction
// refuses it and names the fix.
func TestCheckCustomMarkers_RefusesMarkersThatWouldDoNothing(t *testing.T) {
	cases := []struct {
		name    string
		comment unmanagedComment
		want    []string
	}{
		{
			name:    "near-miss spelling on an rpc",
			comment: unmanagedComment{path: []int32{6, 1, 2, 0}, leading: " forge:cusotm\n"},
			want:    []string{"chat/v1/chat.proto", "rpc BillingService.GetInvoice", `"forge:cusotm"`, `did you mean "forge:custom"`},
		},
		{
			// `rpc A(...); // forge:custom` and a marker on the line under
			// A followed by a blank line (written for the rpc below) both
			// arrive as A's trailing comment; neither is guessed at.
			name:    "in an rpc's trailing comment",
			comment: unmanagedComment{path: []int32{6, 1, 2, 0}, trailing: " forge:custom\n"},
			want:    []string{"rpc BillingService.GetInvoice", "trailing comment", "directly above the rpc"},
		},
		{
			name:    "on a service",
			comment: unmanagedComment{path: []int32{6, 1}, leading: " forge:custom\n"},
			want:    []string{"service BillingService", "ONE rpc", "forge:unmanaged"},
		},
		{
			name:    "on a message",
			comment: unmanagedComment{path: []int32{4, 0}, leading: " forge:custom\n"},
			want:    []string{"message Empty", "does nothing on a message", "CreateEmpty"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := checkCustomMarkers(buildUnmanagedPlugin(t, c.comment))
			if err == nil {
				t.Fatal("want an error, got nil")
			}
			for _, w := range c.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not mention %q", err, w)
				}
			}
		})
	}

	t.Run("generateDescriptor refuses before writing", func(t *testing.T) {
		f := buildUnmanagedPlugin(t, unmanagedComment{path: []int32{6, 1}, leading: " forge:custom\n"})
		out := filepath.Join(t.TempDir(), "gen")
		if err := generateDescriptor(&protogen.Plugin{Files: []*protogen.File{f}}, out); err == nil || !strings.Contains(err.Error(), "forge:custom") {
			t.Fatalf("generateDescriptor error = %v, want a forge:custom placement refusal", err)
		}
		if entries, _ := os.ReadDir(filepath.Join(out, descriptorStageDir)); len(entries) != 0 {
			t.Errorf("a refused extraction still wrote %d fragment(s)", len(entries))
		}
	})
}

// TestCollectCRUDMethodNames_NoORMMeansEveryRPCIsStubbed pins the silent gap
// `//forge:no-orm` left: the stub step skipped a CRUD-named rpc because it
// matched an entity, and the CRUD step — gated off with the ORM — never
// implemented it, so the rpc had neither and served Unimplemented with nothing
// in the tree saying so. Off the CRUD projection, nothing is skipped.
func TestCollectCRUDMethodNames_NoORMMeansEveryRPCIsStubbed(t *testing.T) {
	// No descriptor, no migrations: were the gate ignored this would still
	// return nil, so the assertion is on the gate short-circuiting first —
	// crudMethodsByService below pins the join itself.
	if got := collectCRUDMethodNames([]codegen.ServiceDef{{Name: "TaskService"}}, t.TempDir(), false); got != nil {
		t.Fatalf("CRUD projection off, yet skip set = %v", got)
	}
}

// TestCrudMethodsByService_IsPerServiceAndHonoursCustom: the skip set is keyed
// by service, so one service's CRUD CreateTask cannot suppress another
// service's custom CreateTask stub, and a forge:custom rpc is never in it.
func TestCrudMethodsByService_IsPerServiceAndHonoursCustom(t *testing.T) {
	task := codegen.EntityDef{Name: "Task"}
	crud := codegen.ServiceDef{Name: "TaskService", Methods: []codegen.Method{{Name: "CreateTask"}, {Name: "GetTask"}}}
	custom := codegen.ServiceDef{Name: "PlannerService", Methods: []codegen.Method{{Name: "CreateTask", Custom: true}, {Name: "GetTask"}}}
	got := crudMethodsByService([]codegen.ServiceDef{crud, custom}, []codegen.EntityDef{task})
	if !got["TaskService"]["CreateTask"] || !got["TaskService"]["GetTask"] {
		t.Errorf("TaskService skip set = %v, want CreateTask and GetTask", got["TaskService"])
	}
	if got["PlannerService"]["CreateTask"] {
		t.Error("PlannerService.CreateTask is forge:custom but is in the CRUD skip set — it would get no stub")
	}
	if !got["PlannerService"]["GetTask"] {
		t.Errorf("PlannerService skip set = %v, want GetTask (unmarked)", got["PlannerService"])
	}
}

// TestCheckCustomRPCsDelegate refuses a forge:custom rpc whose shim still
// calls the op forge stops generating, naming the file and line, before the
// validate build would fail and revert the whole generate.
func TestCheckCustomRPCsDelegate(t *testing.T) {
	dir := t.TempDir()
	hDir := filepath.Join(dir, "internal", "handlers", "task")
	if err := os.MkdirAll(hDir, 0o755); err != nil {
		t.Fatal(err)
	}
	shim := "package task\n\nimport \"context\"\n\nfunc (s *Service) CreateTask(ctx context.Context) error {\n\treturn s.crudCreateTaskOp().Run(ctx)\n}\n"
	if err := os.WriteFile(filepath.Join(hDir, "handlers_crud.go"), []byte(shim), 0o644); err != nil {
		t.Fatal(err)
	}
	// The forge-owned ops file declares the op; it is not demand for it.
	ops := "package task\n\nfunc (s *Service) crudGetTaskOp() {}\n"
	if err := os.WriteFile(filepath.Join(hDir, "handlers_crud_ops_gen.go"), []byte(ops), 0o644); err != nil {
		t.Fatal(err)
	}
	// The scaffolded lifecycle test calls the create-request factory, which
	// forge's own factories_gen_test.go declares (a declaration, not demand).
	lifecycle := "package task_test\n\nfunc TestLifecycle() {\n\t_ = task.NewCreateTaskRequest\n}\n"
	if err := os.WriteFile(filepath.Join(hDir, "handlers_crud_test.go"), []byte(lifecycle), 0o644); err != nil {
		t.Fatal(err)
	}
	factories := "package task\n\nfunc NewCreateTaskRequest() {}\nfunc NewUpdateTaskRequest() {}\n"
	if err := os.WriteFile(filepath.Join(hDir, "factories_gen_test.go"), []byte(factories), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := func(custom ...string) []codegen.ServiceDef {
		marked := map[string]bool{}
		for _, c := range custom {
			marked[c] = true
		}
		return []codegen.ServiceDef{{Name: "TaskService", Methods: []codegen.Method{
			{Name: "CreateTask", InputType: "CreateTaskRequest", Custom: marked["CreateTask"]},
			{Name: "GetTask", InputType: "GetTaskRequest", Custom: marked["GetTask"]},
		}}}
	}

	err := checkCustomRPCsDelegate(svc("CreateTask"), dir)
	if err == nil {
		t.Fatal("CreateTask is forge:custom and its shim calls s.crudCreateTaskOp(); want a refusal")
	}
	for _, w := range []string{"TaskService.CreateTask", "internal/handlers/task/handlers_crud.go:6 uses crudCreateTaskOp", "forge:custom"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error %q does not mention %q", err, w)
		}
	}

	// Once the shim is the author's own, the lifecycle test's use of the
	// create-request factory is the remaining dependency, and is named.
	own := "package task\n\nimport \"context\"\n\nfunc (s *Service) CreateTask(ctx context.Context) error {\n\treturn nil\n}\n"
	if err := os.WriteFile(filepath.Join(hDir, "handlers_crud.go"), []byte(own), 0o644); err != nil {
		t.Fatal(err)
	}
	err = checkCustomRPCsDelegate(svc("CreateTask"), dir)
	if err == nil || !strings.Contains(err.Error(), "handlers_crud_test.go:4 uses NewCreateTaskRequest") {
		t.Fatalf("want the lifecycle test's factory use named, got %v", err)
	}
	if err := checkCustomRPCsDelegate(svc("GetTask"), dir); err != nil {
		t.Errorf("only the ops file mentions crudGetTaskOp, which is not a reference; got %v", err)
	}
	if err := checkCustomRPCsDelegate(svc(), dir); err != nil {
		t.Errorf("no rpc is custom; got %v", err)
	}
}

// TestCheckCustomMarkerPlacement_RefusesAMarkerTheDescriptorLost: protobuf
// drops a comment block that sits under an rpc already carrying a same-line
// comment and is followed by a blank line, so the marker written for the rpc
// below never reaches the descriptor. Only the source shows it.
func TestCheckCustomMarkerPlacement_RefusesAMarkerTheDescriptorLost(t *testing.T) {
	dir := t.TempDir()
	protoDir := filepath.Join(dir, "proto", "services", "task", "v1")
	if err := os.MkdirAll(protoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := `syntax = "proto3";
package services.task.v1;
service TaskService {
  rpc GetTask(GetTaskRequest) returns (GetTaskResponse); // reads one task
  // forge:custom — lists with dependency edges

  rpc ListTasks(ListTasksRequest) returns (ListTasksResponse);
  // forge:custom
  rpc CreateTask(CreateTaskRequest) returns (CreateTaskResponse);
}
`
	if err := os.WriteFile(filepath.Join(protoDir, "task.proto"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := codegen.ServiceDef{Name: "TaskService", ProtoFile: "services/task/v1/task.proto", Methods: []codegen.Method{
		{Name: "GetTask"},
		{Name: "ListTasks"},                // what protobuf compiled: the marker was dropped
		{Name: "CreateTask", Custom: true}, // attached: reached the descriptor
	}}
	err := checkCustomMarkerPlacement([]codegen.ServiceDef{svc}, dir)
	if err == nil {
		t.Fatal("the marker above ListTasks did not reach the descriptor; want a refusal")
	}
	for _, w := range []string{"TaskService.ListTasks", "proto/services/task/v1/task.proto", "no blank line"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error %q does not mention %q", err, w)
		}
	}
	if strings.Contains(err.Error(), "CreateTask") || strings.Contains(err.Error(), "GetTask") {
		t.Errorf("error %q names an rpc whose marker reached the descriptor (or that has none)", err)
	}

	svc.Methods[1].Custom = true // the author moved the marker; protobuf now attaches it
	if err := checkCustomMarkerPlacement([]codegen.ServiceDef{svc}, dir); err != nil {
		t.Errorf("descriptor and source agree; got %v", err)
	}
	// A layout forge cannot map to disk is skipped, not guessed at.
	svc.ProtoFile = "elsewhere/task.proto"
	svc.Methods[1].Custom = false
	if err := checkCustomMarkerPlacement([]codegen.ServiceDef{svc}, dir); err != nil {
		t.Errorf("proto file not under proto/: want skip, got %v", err)
	}
}
