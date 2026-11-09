// Regression guard for the scaffold's own lint-cleanliness: a second mutable
// string field whose Go name ends in an initialism (…Id) must not render a
// local variable that trips revive's var-naming rule (Id→ID). The scaffold
// used to emit `keep<Field>` (e.g. keepCustomerId), which failed `forge lint`
// out of the box for every CRUD service with such a field; it now emits a
// fixed, field-independent local name.
package codegen

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestGenerateCRUDTests_SecondStringFieldIsLintCleanVarName(t *testing.T) {
	projectDir := t.TempDir()
	handlerDir := filepath.Join(projectDir, "internal", "handlers", "orders")
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(handlerDir, "service.go"), []byte("package orders\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	svc := ServiceDef{
		Name:       "OrdersService",
		GoPackage:  "example.com/test/gen/proto/services/orders/v1",
		PkgName:    "ordersv1",
		ModulePath: "example.com/test",
		Methods: []Method{
			{Name: "CreateOrder", InputType: "CreateOrderRequest", OutputType: "CreateOrderResponse"},
			{Name: "GetOrder", InputType: "GetOrderRequest", OutputType: "GetOrderResponse"},
			{Name: "UpdateOrder", InputType: "UpdateOrderRequest", OutputType: "UpdateOrderResponse"},
		},
		Messages: map[string][]MessageFieldDef{
			"UpdateOrderRequest": {
				{Name: "order", ProtoType: "message", MessageType: "Order"},
				{Name: "update_mask", ProtoType: "message", MessageType: "google.protobuf.FieldMask"},
			},
		},
	}
	// label = mutable string field, customer_id = the SECOND string field
	// whose Go name ends in "Id" — the exact shape that used to render
	// `keepCustomerId`.
	entities := []EntityDef{{
		Name:      "Order",
		TableName: "orders",
		PkField:   "id",
		PkGoType:  "string",
		Fields: []EntityField{
			{Name: "id", GoName: "Id", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
			{Name: "label", GoName: "Label", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
			{Name: "customer_id", GoName: "CustomerId", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
		},
	}}

	if _, err := GenerateCRUDTests(svc, MatchCRUDMethods(svc, entities), "example.com/test", projectDir, nil); err != nil {
		t.Fatalf("GenerateCRUDTests: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(handlerDir, "handlers_crud_test.go"))
	if err != nil {
		t.Fatalf("read rendered scaffold: %v", err)
	}
	content := string(data)

	// The masked-update non-clobber assertion must be present, keyed off the
	// fixed local name.
	if !strings.Contains(content, "keptUnmaskedValue := maskedRow.GetCustomerId()") {
		t.Errorf("expected fixed local name binding; got:\n%s", content)
	}
	// No owned local identifier may carry a trailing initialism. Match a
	// declaration/assignment/use of a keep*Id identifier (guards against a
	// regression to the `keep<Field>` shape) — comments no longer interpolate
	// the field name, so a plain scan is sufficient.
	if regexp.MustCompile(`\bkeep[A-Za-z]*Id\b`).MatchString(content) {
		t.Errorf("rendered scaffold reintroduced an Id-suffixed keep* identifier (revive var-naming would gate):\n%s", content)
	}

	if _, err := parser.ParseFile(token.NewFileSet(), "lifecycle.go", content, parser.SkipObjectResolution); err != nil {
		t.Errorf("rendered scaffold is not valid Go: %v", err)
	}
}
