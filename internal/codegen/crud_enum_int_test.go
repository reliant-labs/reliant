package codegen

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These pin a proto enum stored in an INTEGER column: the wire number, the
// storage choice reliant made for Task.status. forge births enum columns as
// TEXT holding value names, but SQL is the schema truth, and an author who
// declared the column integer used to get "no conversion between wire kind
// enum and column type INT4" — the whole generate refused. Every integer
// width introspects to the canonical int64.

func intEnumEntity() (ServiceDef, EntityDef) {
	svc := ServiceDef{
		Package: "tasks.v1",
		Schemas: map[string][]SchemaFieldDef{
			"tasks.v1.Task": {
				{Name: "status", Kind: "enum", TypeName: "tasks.v1.TaskStatus"},
				{Name: "fallback_status", Kind: "enum", TypeName: "tasks.v1.TaskStatus", Optional: true},
				{Name: "history", Kind: "enum", TypeName: "tasks.v1.TaskStatus", Repeated: true},
				{Name: "prior", Kind: "enum", TypeName: "tasks.v1.TaskStatus"},
			},
		},
	}
	entity := EntityDef{
		Name: "Task",
		Fields: []EntityField{
			{Name: "status", GoName: "Status", ProtoType: "enum", GoType: "TaskStatus", Kind: FieldKindEnum, MessageType: "tasks.v1.TaskStatus"},
			{Name: "fallback_status", GoName: "FallbackStatus", ProtoType: "enum", GoType: "TaskStatus", Kind: FieldKindEnum, MessageType: "tasks.v1.TaskStatus", Optional: true},
			{Name: "history", GoName: "History", ProtoType: "enum", GoType: "TaskStatus", Kind: FieldKindEnum, MessageType: "tasks.v1.TaskStatus"},
			{Name: "prior", GoName: "Prior", ProtoType: "enum", GoType: "TaskStatus", Kind: FieldKindEnum, MessageType: "tasks.v1.TaskStatus"},
		},
		Columns: []EntityColumn{
			{Name: "status", Type: "int64", NotNull: true, DeclType: "INT4", Default: "1"},
			{Name: "fallback_status", Type: "int64", DeclType: "INT4"},
			{Name: "history", Type: "int64", IsArray: true, NotNull: true, DeclType: "INT4[]"},
			{Name: "prior", Type: "int64", NotNull: true, DeclType: "INT2"}, // no DEFAULT
		},
	}
	return svc, entity
}

func TestBuildEntityConv_IntegerEnumColumns(t *testing.T) {
	svc, entity := intEnumEntity()
	conv, unmapped := BuildEntityConv(svc, entity)
	if len(unmapped) != 0 {
		t.Fatalf("every enum field over an integer column must map; unmapped: %+v", unmapped)
	}
	toProto := strings.Join(conv.ToProtoAssigns, "\n")
	fromProto := strings.Join(conv.FromProtoAssigns, "\n")

	for _, want := range []string{
		// Unset (the proto zero) leaves the defaulted column unset — a
		// pointer, so the INSERT omits it and the DEFAULT fires; a sent
		// value stores its wire NUMBER at the column's width (INT4 →
		// int32, codegen.GoIntType).
		"if m.Status != 0 {\n\t\tv := int32(m.Status)\n\t\te.Status = &v\n\t}",
		// No DEFAULT: the value goes as sent (no CHECK to refuse the zero).
		"e.Prior = int16(m.Prior)",
		// Optional over nullable: unset is NULL.
		"if m.FallbackStatus != nil && *m.FallbackStatus != 0 {\n\t\tv := int32(*m.FallbackStatus)\n\t\te.FallbackStatus = &v\n\t}",
		"for _, v := range m.History {\n\t\te.History = append(e.History, int32(v))\n\t}",
	} {
		if !strings.Contains(fromProto, want) {
			t.Errorf("fromProto missing %q; got:\n%s", want, fromProto)
		}
	}
	for _, want := range []string{
		// The stored number IS the wire value: a cast, with no guard —
		// an INT4 column holds nothing an int32 cannot.
		"m.Status = pb.TaskStatus(*e.Status)",
		"if e.FallbackStatus != nil {",
		"ev := pb.TaskStatus(*e.FallbackStatus)",
		"m.FallbackStatus = &ev",
		"m.History = append(m.History, pb.TaskStatus(v))",
	} {
		if !strings.Contains(toProto, want) {
			t.Errorf("toProto missing %q; got:\n%s", want, toProto)
		}
	}
	// Names never touch an integer column.
	if strings.Contains(toProto+fromProto, ".String()") || strings.Contains(toProto, "_value[") {
		t.Errorf("an integer column must not be converted through value names; got:\n%s\n%s", fromProto, toProto)
	}
}

// TestGenerateCRUDHandlers_IntegerEnumFilter: a list filter over an integer
// enum column binds the wire number — binding the name hits Postgres with
// `integer = text` on the first filtered call.
func TestGenerateCRUDHandlers_IntegerEnumFilter(t *testing.T) {
	projectDir := t.TempDir()
	handlerDir := filepath.Join(projectDir, "internal", "handlers", "tasks")
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	serviceGo := "package tasks\n\nimport \"github.com/reliant-labs/forge/pkg/orm\"\n\ntype Deps struct {\n\tDB orm.Context\n}\n\ntype Service struct {\n\tdeps Deps\n}\n"
	if err := os.WriteFile(filepath.Join(handlerDir, "service.go"), []byte(serviceGo), 0o644); err != nil {
		t.Fatal(err)
	}
	svc, entity := intEnumEntity()
	svc.Name, svc.GoPackage, svc.PkgName, svc.ModulePath = "TasksService", "example.com/test/gen/services/tasks/v1", "tasksv1", "example.com/test"
	svc.Methods = []Method{{Name: "ListTasks", InputType: "ListTasksRequest", OutputType: "ListTasksResponse"}}
	svc.Messages = map[string][]MessageFieldDef{
		"ListTasksRequest": {
			{Name: "page_size", ProtoType: "int32"},
			{Name: "page_token", ProtoType: "string"},
			{Name: "status", ProtoType: "enum", IsOptional: true},
			{Name: "prior", ProtoType: "enum"}, // plain: always applied
		},
	}
	entity.TableName, entity.PkField, entity.PkGoType = "tasks", "id", "string"
	entity.Fields = append([]EntityField{{Name: "id", GoName: "Id", ProtoType: "string", GoType: "string", Kind: FieldKindScalar}}, entity.Fields...)
	entity.Columns = append([]EntityColumn{{Name: "id", Type: "string", NotNull: true, IsPK: true, DeclType: "TEXT"}}, entity.Columns...)

	if _, err := GenerateCRUDHandlers(svc, MatchCRUDMethods(svc, []EntityDef{entity}), "example.com/test", projectDir, nil); err != nil {
		t.Fatalf("GenerateCRUDHandlers() error = %v", err)
	}
	opsPath := filepath.Join(handlerDir, "handlers_crud_ops_gen.go")
	data, err := os.ReadFile(opsPath)
	if err != nil {
		t.Fatal(err)
	}
	ops := string(data)
	for _, want := range []string{
		`orm.WhereEq("status", int32(*req.Status))`,
		`orm.WhereEq("prior", int32(req.Prior))`,
	} {
		if !strings.Contains(ops, want) {
			t.Errorf("ops missing %q; got:\n%s", want, ops)
		}
	}
	for _, bad := range []string{`(*req.Status).String()`, `req.Prior.String()`} {
		if strings.Contains(ops, bad) {
			t.Errorf("an integer enum column's filter must not bind the value name (%s); got:\n%s", bad, ops)
		}
	}
	if _, perr := parser.ParseFile(token.NewFileSet(), opsPath, ops, parser.SkipObjectResolution); perr != nil {
		t.Errorf("ops file is not valid Go: %v", perr)
	}
}
