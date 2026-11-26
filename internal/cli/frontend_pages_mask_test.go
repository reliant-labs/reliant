package cli

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
)

// aip134PageDataForTest builds PageTemplateData for the CANONICAL
// generated update shape: the request wraps the entity and carries a
// google.protobuf.FieldMask (`Task task = 1; FieldMask update_mask = 2;`)
// — the shape `forge scaffold entity` and the scaffold proto emit.
func aip134PageDataForTest(t *testing.T) codegen.PageTemplateData {
	t.Helper()
	svc := codegen.ServiceDef{
		Name:      "TaskService",
		Package:   "demo.v1",
		ProtoFile: "proto/services/tasks/v1/tasks.proto",
		Methods: []codegen.Method{
			{Name: "ListTasks", InputType: "ListTasksRequest", OutputType: "ListTasksResponse"},
			{Name: "GetTask", InputType: "GetTaskRequest", OutputType: "GetTaskResponse"},
			{Name: "CreateTask", InputType: "CreateTaskRequest", OutputType: "CreateTaskResponse"},
			{Name: "UpdateTask", InputType: "UpdateTaskRequest", OutputType: "UpdateTaskResponse"},
		},
		Messages: map[string][]codegen.MessageFieldDef{
			"CreateTaskRequest": {
				{Name: "title", ProtoType: "string"},
				{Name: "status", ProtoType: "string"},
			},
			"UpdateTaskRequest": {
				{Name: "task", ProtoType: "message", MessageType: "Task"},
				{Name: "update_mask", ProtoType: "message", MessageType: "google.protobuf.FieldMask"},
			},
			"Task": {
				{Name: "id", ProtoType: "string"},
				{Name: "title", ProtoType: "string"},
				{Name: "status", ProtoType: "string"},
				{Name: "created_at", ProtoType: "message", MessageType: "google.protobuf.Timestamp"},
			},
		},
	}
	pages := codegen.ExtractCRUDEntities(svc)
	if len(pages) != 1 {
		t.Fatalf("expected 1 CRUD entity, got %d", len(pages))
	}
	page := pages[0]
	entity := codegen.EntityDef{
		Name:      "Task",
		PkField:   "id",
		ProtoFile: "proto/db/v1/tasks.proto",
		Fields: []codegen.EntityField{
			{Name: "id", ProtoType: "string", Kind: codegen.FieldKindScalar},
			{Name: "title", ProtoType: "string", Kind: codegen.FieldKindScalar},
			{Name: "status", ProtoType: "string", Kind: codegen.FieldKindScalar},
		},
	}
	codegen.AttachEntityMeta(&page, entity, svc)
	return page
}

// TestExtractCRUDEntities_AIP134UpdateShape pins the extraction: form
// fields come from the ENTITY message (not the request's wrapper/mask
// fields), and the wrapper + mask field names are recorded for the
// template.
func TestExtractCRUDEntities_AIP134UpdateShape(t *testing.T) {
	page := aip134PageDataForTest(t)

	if page.UpdateEntityFieldCamel != "task" {
		t.Errorf("UpdateEntityFieldCamel = %q, want %q", page.UpdateEntityFieldCamel, "task")
	}
	if page.UpdateMaskFieldCamel != "updateMask" {
		t.Errorf("UpdateMaskFieldCamel = %q, want %q", page.UpdateMaskFieldCamel, "updateMask")
	}

	var names []string
	for _, f := range page.UpdateFields {
		names = append(names, f.Name)
	}
	got := strings.Join(names, ",")
	// id comes from the URL param, created_at is machinery, and the
	// wrapper/mask are request plumbing — only title and status are form
	// fields.
	if got != "title,status" {
		t.Errorf("UpdateFields = %s, want title,status", got)
	}
	for _, f := range page.UpdateFields {
		if f.ProtoName == "" {
			t.Errorf("UpdateFields[%s].ProtoName empty — the mask path needs the snake_case proto name", f.Name)
		}
	}
}

// TestEditPage_SendsAIP134Mask pins the rendered edit page for both
// frontend kinds: the mutation nests the form values under the entity
// wrapper (with the PK inside) and sends an update_mask naming exactly
// the fields the form edits — without it the server's update clobbers
// every column the form doesn't carry.
func TestEditPage_SendsAIP134Mask(t *testing.T) {
	page := aip134PageDataForTest(t)

	for _, kind := range []string{"pages", "vite-spa-pages"} {
		t.Run(kind, func(t *testing.T) {
			tmpl, err := loadPageTemplate(kind, "edit-page.tsx.tmpl")
			if err != nil {
				t.Fatalf("load edit-page: %v", err)
			}
			var b strings.Builder
			if err := tmpl.Execute(&b, page); err != nil {
				t.Fatalf("render edit-page: %v", err)
			}
			edit := b.String()

			for _, want := range []string{
				// Entity nested under the wrapper, PK from the route param.
				"task: {",
				"id: id,",
				// The mask names exactly the form's fields, snake_case.
				`updateMask: { paths: ["title", "status"] },`,
			} {
				if !strings.Contains(edit, want) {
					t.Errorf("edit page missing %q:\n%s", want, edit)
				}
			}
			// The legacy flat spread must be gone — `{ id, ...values }` at
			// the top level doesn't match the wrapped request type.
			if strings.Contains(edit, "mutation.mutate({\n      id,") {
				t.Errorf("edit page still spreads values at the request top level:\n%s", edit)
			}
			// The mask is request plumbing, never a form input.
			if strings.Contains(edit, `register("updateMask")`) {
				t.Errorf("edit page renders the update_mask as a form field:\n%s", edit)
			}
		})
	}
}
