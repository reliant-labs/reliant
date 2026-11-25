package codegen

import (
	"strings"
	"testing"
)

// forge report 443bd9d8: a field marked `// forge:computed` after birth stays
// on the born Create request, and the generated Create copied the client's
// value into the server-owned column while Update refused it. Create skips
// it, says why, and still maps every other field.
func TestBuildCreateAssigns_SkipsServerOwnedFields(t *testing.T) {
	svc := ServiceDef{
		Package: "orders.v1",
		Schemas: map[string][]SchemaFieldDef{
			"orders.v1.CreateOrderRequest": {
				{Name: "name", Kind: "string"},
				{Name: "state", Kind: "enum", TypeName: "orders.v1.OrderState"},
			},
		},
	}
	entity := EntityDef{
		Name: "Order",
		Fields: []EntityField{
			{Name: "name"},
			{Name: "state", ReadOnly: true},
		},
		Columns: []EntityColumn{
			{Name: "name", Type: "string", NotNull: true, DeclType: "TEXT"},
			{Name: "state", Type: "string", NotNull: true, DeclType: "TEXT", Default: "'draft'"},
		},
	}
	m := Method{Name: "CreateOrder", InputType: "CreateOrderRequest", InputTypeFQ: "orders.v1.CreateOrderRequest"}

	createAssigns, unmapped := buildCreateAssigns(svc, m, entity)
	if len(unmapped) != 0 {
		t.Fatalf("unmapped: %+v", unmapped)
	}
	assigns := strings.Join(createAssigns, "\n")
	if strings.Contains(assigns, "e.State =") {
		t.Errorf("Create must not copy a server-owned field from the request; got:\n%s", assigns)
	}
	if !strings.Contains(assigns, "// State: server-owned") {
		t.Errorf("Create should say why the field is not stored; got:\n%s", assigns)
	}
	if !strings.Contains(assigns, "e.Name = req.Name") {
		t.Errorf("Create must still map the client-writable fields; got:\n%s", assigns)
	}
}
