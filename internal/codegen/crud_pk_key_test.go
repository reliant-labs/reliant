package codegen

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/templates"
)

// renderTaskOps generates the ops file for a Task entity keyed on a column
// of pkGoType, served by the full CRUD quintet whose get/delete requests
// carry `<reqProtoType> id`.
func renderTaskOps(t *testing.T, pkGoType, reqProtoType string) (string, CRUDTemplateData) {
	t.Helper()
	key := []MessageFieldDef{{Name: "id", ProtoType: reqProtoType}}
	svc := ServiceDef{
		Name:       "TasksService",
		GoPackage:  "example.com/test/gen/proto/services/tasks/v1",
		PkgName:    "tasksv1",
		ModulePath: "example.com/test",
		Methods: []Method{
			{Name: "CreateTask", InputType: "CreateTaskRequest", OutputType: "CreateTaskResponse"},
			{Name: "GetTask", InputType: "GetTaskRequest", OutputType: "GetTaskResponse"},
			{Name: "ListTasks", InputType: "ListTasksRequest", OutputType: "ListTasksResponse"},
			{Name: "UpdateTask", InputType: "UpdateTaskRequest", OutputType: "UpdateTaskResponse"},
			{Name: "DeleteTask", InputType: "DeleteTaskRequest", OutputType: "DeleteTaskResponse"},
		},
		Messages: map[string][]MessageFieldDef{
			"GetTaskRequest":     key,
			"DeleteTaskRequest":  key,
			"CreateTaskResponse": {{Name: "task", ProtoType: "message", MessageType: "Task"}},
			"GetTaskResponse":    {{Name: "task", ProtoType: "message", MessageType: "Task"}},
			"UpdateTaskRequest":  {{Name: "task", ProtoType: "message", MessageType: "Task"}},
			"UpdateTaskResponse": {{Name: "task", ProtoType: "message", MessageType: "Task"}},
			"ListTasksRequest":   {{Name: "page_size", ProtoType: "int32"}, {Name: "page_token", ProtoType: "string"}},
			"ListTasksResponse":  {{Name: "tasks", ProtoType: "message", MessageType: "Task"}, {Name: "next_page_token", ProtoType: "string"}},
		},
	}
	entities := []EntityDef{{
		Name: "Task", TableName: "tasks", PkField: "id", PkGoType: pkGoType,
		Fields: []EntityField{
			{Name: "id", GoName: "ID", GoType: pkGoType, Kind: FieldKindScalar},
			{Name: "title", GoName: "Title", GoType: "string", Kind: FieldKindScalar},
		},
	}}
	crudMethods := MatchCRUDMethods(svc, entities)
	if len(crudMethods) != 5 {
		t.Fatalf("MatchCRUDMethods matched %d rpcs, want the quintet", len(crudMethods))
	}
	data, err := buildCRUDTemplateData(svc, crudMethods, nil, "example.com/test")
	if err != nil {
		t.Fatalf("buildCRUDTemplateData: %v", err)
	}
	ops, err := templates.ServiceTemplates().Render("handlers_crud_ops_gen.go.tmpl", data)
	if err != nil {
		t.Fatalf("render ops: %v", err)
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "handlers_crud_ops_gen.go", ops, 0); err != nil {
		t.Fatalf("ops file does not parse: %v\n%s", err, ops)
	}
	return string(ops), data
}

// An integer primary key (BIGSERIAL, INTEGER, an IDENTITY) projects ORM
// delegates that take the column's Go type, while pkg/crud carries the key as
// text. The ops used to pass the text straight through — `ID: func(req)
// string { return req.Id }` over an int64 field — so `forge generate` failed
// its own validate build on every integer-keyed table (obs 113d7531). The ops
// now format the key into the text and parse it back for the delegate.
func TestCRUDOps_IntegerPrimaryKeyCrossesTheTextSeam(t *testing.T) {
	cases := []struct {
		pkGoType, reqProtoType string
		want                   []string
	}{
		{"int64", "int64", []string{
			`"strconv"`,
			"ID: func(req *pb.GetTaskRequest) string { return strconv.FormatInt(req.Id, 10) },",
			"ID: func(req *pb.DeleteTaskRequest) string { return strconv.FormatInt(req.Id, 10) },",
			"key, err := strconv.ParseInt(id, 10, 64)",
			"return db.GetTaskByID(ctx, s.deps.DB, key)",
			"return db.DeleteTask(ctx, s.deps.DB, key)",
			`crud.ScopedUpdate(ctx, "id", strconv.FormatInt(entity.ID, 10), entity, opts,`,
			"EntityID: func(entity *db.Task) string { return strconv.FormatInt(entity.ID, 10) },",
		}},
		{"int32", "int32", []string{
			"return strconv.FormatInt(int64(req.Id), 10)",
			"key, err := strconv.ParseInt(id, 10, 32)",
			"return db.GetTaskByID(ctx, s.deps.DB, int32(key))",
			"return db.DeleteTask(ctx, s.deps.DB, int32(key))",
			"return strconv.FormatInt(int64(entity.ID), 10)",
		}},
		// A request that kept the scaffold's `string id` over an integer
		// column: the text is parsed, and text that is no integer names
		// no row.
		{"int64", "string", []string{
			"ID: func(req *pb.GetTaskRequest) string { return req.Id },",
			"key, err := strconv.ParseInt(id, 10, 64)",
			"return nil, orm.ErrNoRows",
		}},
		{"int32", "uint32", []string{
			"return strconv.FormatUint(uint64(req.Id), 10)",
			"key, err := strconv.ParseInt(id, 10, 32)",
			"return db.GetTaskByID(ctx, s.deps.DB, int32(key))",
			"return db.DeleteTask(ctx, s.deps.DB, int32(key))",
		}},
		{"int64", "uint64", []string{
			"return strconv.FormatUint(req.Id, 10)",
			"key, err := strconv.ParseInt(id, 10, 64)",
			"return db.GetTaskByID(ctx, s.deps.DB, key)",
			"return db.DeleteTask(ctx, s.deps.DB, key)",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.pkGoType+"_over_"+tc.reqProtoType, func(t *testing.T) {
			ops, data := renderTaskOps(t, tc.pkGoType, tc.reqProtoType)
			if !data.NeedsStrconv {
				t.Error("NeedsStrconv = false for an integer key")
			}
			for _, m := range data.CRUDMethods {
				if m.ShapeMismatch {
					t.Errorf("%s fell to the custom-read-shape stub: %s", m.MethodName, m.MismatchReason)
				}
			}
			for _, want := range tc.want {
				if !strings.Contains(ops, want) {
					t.Errorf("ops file lacks %q:\n%s", want, ops)
				}
			}
		})
	}
}

// A string key renders exactly as before: no strconv, the text passed
// straight to the delegate.
func TestCRUDOps_StringPrimaryKeyIsUnchanged(t *testing.T) {
	ops, data := renderTaskOps(t, "string", "string")
	if data.NeedsStrconv || strings.Contains(ops, "strconv") {
		t.Errorf("a string key must not reach strconv:\n%s", ops)
	}
	for _, want := range []string{
		"ID: func(req *pb.GetTaskRequest) string { return req.Id },",
		"func(ctx context.Context, id string) (*db.Task, error) {",
		"return db.GetTaskByID(ctx, s.deps.DB, id)",
		"return db.DeleteTask(ctx, s.deps.DB, id)",
		"EntityID: func(entity *db.Task) string { return entity.ID },",
	} {
		if !strings.Contains(ops, want) {
			t.Errorf("ops file lacks %q:\n%s", want, ops)
		}
	}
}

// A key the text seam cannot carry gets the Unimplemented stub, which the
// author implements, never an op that does not compile.
func TestCRUDOps_UncarriableKeyIsACustomShape(t *testing.T) {
	_, data := renderTaskOps(t, "time.Time", "string")
	for _, m := range data.CRUDMethods {
		if m.Operation == "create" {
			continue
		}
		if !m.ShapeMismatch || !strings.Contains(m.MismatchReason, "projects to Go type time.Time") {
			t.Errorf("%s: ShapeMismatch=%v reason=%q, want the key-type refusal", m.MethodName, m.ShapeMismatch, m.MismatchReason)
		}
	}

	for _, storage := range []string{"uint32", "uint64"} {
		ops, unsigned := renderTaskOps(t, storage, storage)
		for _, m := range unsigned.CRUDMethods {
			if m.Operation != "create" && (!m.ShapeMismatch || !strings.Contains(m.MismatchReason, "projects to Go type "+storage)) {
				t.Errorf("%s over %s: want unsigned storage refusal, got %q", m.MethodName, storage, m.MismatchReason)
			}
		}
		if strings.Contains(ops, "strconv.ParseInt(") || strings.Contains(ops, "strconv.ParseUint(") {
			t.Errorf("unsupported %s storage must not generate a key parser:\n%s", storage, ops)
		}
	}

	_, data = renderTaskOps(t, "int64", "bytes")
	for _, m := range data.CRUDMethods {
		want := m.Operation == "get" || m.Operation == "delete"
		if m.ShapeMismatch != want {
			t.Errorf("%s: ShapeMismatch=%v, want %v (reason %q)", m.MethodName, m.ShapeMismatch, want, m.MismatchReason)
		}
	}
}
