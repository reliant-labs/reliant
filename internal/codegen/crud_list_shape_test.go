package codegen

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/templates"
)

// A List RPC whose response does not carry the repeated <Entity> field is,
// by definition, the custom read shape: that absence IS the classification
// the shape matcher makes, and the shim it emits runs the query but leaves
// the projection as an explicit TODO, returning an EMPTY response.
//
// The scaffolded lifecycle test must therefore emit NO list assertion for
// it. Asserting `Get<Entity>s()` names a method the renamed wire type does
// not have (it does not compile); asserting the real accessor would compile
// and then fail at run time on a response forge deliberately declined to
// populate — inside a scaffold-once file the author was told not to rewrite.
//
// Regression for the Fixture Corpus break: fixture step 7c renames
// ListItemsResponse's `repeated Item items` to `results`, and the scaffold
// kept emitting `listed.Msg.GetItems()`.
func customReadShapeListSvc() (ServiceDef, []CRUDMethod) {
	entity := EntityDef{
		Name: "Item", TableName: "items", PkField: "id", PkGoType: "string",
		Fields: []EntityField{
			{Name: "id", GoName: "Id", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
			{Name: "name", GoName: "Name", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
		},
	}
	svc := ServiceDef{
		Name: "ItemService", Package: "item.v1", PkgName: "itemv1", ModulePath: "example.com/test",
		Messages: map[string][]MessageFieldDef{
			"CreateItemRequest":  {{Name: "name", ProtoType: "string"}},
			"CreateItemResponse": {{Name: "item", ProtoType: "message", MessageType: "Item"}},
			"GetItemRequest":     {{Name: "id", ProtoType: "string"}},
			"ListItemsRequest":   {},
			// The 7c rename: `results`, not `items`.
			"ListItemsResponse": {{Name: "results", ProtoType: "message", MessageType: "Item"}},
		},
	}
	methods := []CRUDMethod{
		{Method: MethodTemplateData{Name: "CreateItem", InputType: "CreateItemRequest", OutputType: "CreateItemResponse"}, Entity: entity, Operation: "create"},
		{Method: MethodTemplateData{Name: "GetItem", InputType: "GetItemRequest", OutputType: "GetItemResponse"}, Entity: entity, Operation: "get"},
		{Method: MethodTemplateData{Name: "ListItems", InputType: "ListItemsRequest", OutputType: "ListItemsResponse"}, Entity: entity, Operation: "list"},
	}
	return svc, methods
}

func TestCRUDTestScaffold_CustomReadShapeListEmitsNoAssertion(t *testing.T) {
	svc, methods := customReadShapeListSvc()

	// Precondition: the shape matcher really does route this to the custom
	// read path. Without this the test could pass for the wrong reason.
	var listMethod CRUDMethod
	for _, cm := range methods {
		if cm.Operation == "list" {
			listMethod = cm
		}
	}
	if ok, _ := validateCRUDShape(svc, listMethod); ok {
		t.Fatal("precondition failed: a ListItemsResponse carrying `results` instead of " +
			"`items` must classify as the custom read shape")
	}

	data := buildCRUDTestTemplateData(svc, methods, "example.com/test", "", nil)
	if len(data.Entities) != 1 {
		t.Fatalf("expected 1 entity, got %d", len(data.Entities))
	}
	if data.Entities[0].HasList {
		t.Error("a custom-read-shape List must not produce a lifecycle list assertion: " +
			"forge cannot assert a row count against a response its own scaffold leaves unpopulated")
	}

	rendered, err := templates.ServiceTemplates().Render("handlers_crud_test.go.tmpl", data)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	content := string(rendered)

	// The concrete compile failure from the corpus, pinned literally.
	if strings.Contains(content, "GetItems()") {
		t.Error("scaffold emits GetItems(), which does not exist on a ListItemsResponse " +
			"whose repeated field is named `results` — this is the Fixture Corpus break")
	}
	if strings.Contains(content, "svc.ListItems") {
		t.Error("scaffold must emit no ListItems call at all for a custom read shape")
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "handlers_crud_test.go", content, parser.SkipObjectResolution); err != nil {
		t.Errorf("scaffold is not valid Go: %v\n----\n%s", err, content)
	}
}

// The converse, so the fix cannot be "never emit a list assertion": a
// Tier-1 list shape must still get its full assertion.
func TestCRUDTestScaffold_Tier1ListStillAsserts(t *testing.T) {
	svc, methods := customReadShapeListSvc()
	// Put the conventional field name back.
	svc.Messages["ListItemsResponse"] = []MessageFieldDef{
		{Name: "items", ProtoType: "message", MessageType: "Item"},
	}

	data := buildCRUDTestTemplateData(svc, methods, "example.com/test", "", nil)
	if !data.Entities[0].HasList {
		t.Fatal("a conventional ListItemsResponse carrying `items` must keep its list assertion")
	}
	rendered, err := templates.ServiceTemplates().Render("handlers_crud_test.go.tmpl", data)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	content := string(rendered)
	if !strings.Contains(content, "GetItems()") {
		t.Error("the Tier-1 list shape must still assert against GetItems()")
	}
	if !strings.Contains(content, "svc.ListItems") {
		t.Error("the Tier-1 list shape must still call ListItems")
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "handlers_crud_test.go", content, parser.SkipObjectResolution); err != nil {
		t.Errorf("scaffold is not valid Go: %v\n----\n%s", err, content)
	}
}
