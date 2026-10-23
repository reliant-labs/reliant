package codegen

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// renderListOps generates the ops file for a single-entity list service whose
// List response either carries a total_count field or not, and returns the
// rendered ops source.
func renderListOps(t *testing.T, withTotalCount bool) string {
	t.Helper()
	projectDir := t.TempDir()
	handlerDir := filepath.Join(projectDir, "internal", "handlers", "orders")
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(handlerDir, "service.go"), []byte(`package orders
import "github.com/reliant-labs/forge/pkg/orm"
type Deps struct { DB orm.Context }
type Service struct { deps Deps }
`), 0o644); err != nil {
		t.Fatal(err)
	}

	respFields := []MessageFieldDef{
		{Name: "orders", ProtoType: "message", MessageType: "Order"},
		{Name: "next_page_token", ProtoType: "string"},
	}
	if withTotalCount {
		respFields = append(respFields, MessageFieldDef{Name: "total_count", ProtoType: "int32"})
	}

	svc := ServiceDef{
		Name: "OrdersService", Package: "orders.v1",
		GoPackage: "example.com/test/gen/proto/services/orders/v1", PkgName: "ordersv1", ModulePath: "example.com/test",
		Methods: []Method{{Name: "ListOrders", InputType: "ListOrdersRequest", OutputType: "ListOrdersResponse"}},
		Messages: map[string][]MessageFieldDef{
			"ListOrdersRequest":  {{Name: "page_size", ProtoType: "int32"}, {Name: "page_token", ProtoType: "string"}},
			"ListOrdersResponse": respFields,
		},
		Schemas: map[string][]SchemaFieldDef{
			"orders.v1.Order": {{Name: "id", Kind: "string"}},
		},
	}
	entities := []EntityDef{{
		Name: "Order", TableName: "orders", PkField: "id", PkGoType: "string",
		Fields:  []EntityField{{Name: "id", GoName: "Id", ProtoType: "string", GoType: "string", Kind: FieldKindScalar}},
		Columns: []EntityColumn{{Name: "id", Type: "string", NotNull: true, IsPK: true, DeclType: "TEXT"}},
	}}
	crudMethods := MatchCRUDMethods(svc, entities)
	if _, err := GenerateCRUDHandlers(svc, crudMethods, "example.com/test", projectDir, nil); err != nil {
		t.Fatalf("GenerateCRUDHandlers: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(handlerDir, "handlers_crud_ops_gen.go"))
	if err != nil {
		t.Fatalf("read ops: %v", err)
	}
	ops := string(b)
	if _, perr := parser.ParseFile(token.NewFileSet(), "ops.go", ops, parser.SkipObjectResolution); perr != nil {
		t.Fatalf("ops not valid Go: %v\n%s", perr, ops)
	}
	return ops
}

// A List response that declares total_count gets a wired COUNT and packs the
// real total (the pre-fix generator left total_count at 0 forever).
func TestGenerateCRUDHandlers_TotalCountWired(t *testing.T) {
	ops := renderListOps(t, true)
	for _, want := range []string{
		"Count: func(ctx context.Context, opts []orm.QueryOption) (int64, error)",
		"db.CountOrder(ctx, s.deps.DB, opts...)",
		"totalCount int64",
		"int32(totalCount)", // gofmt aligns the field, so match spacing-insensitively
	} {
		if !strings.Contains(ops, want) {
			t.Errorf("total_count wiring missing %q; got:\n%s", want, ops)
		}
	}
	if !strings.Contains(ops, "TotalCount:") {
		t.Errorf("total_count wiring missing the TotalCount field; got:\n%s", ops)
	}
}

// A List response WITHOUT total_count gets no COUNT query (and no TotalCount
// packing), so no extra query is issued for entities that can't use it.
func TestGenerateCRUDHandlers_TotalCountAbsent(t *testing.T) {
	ops := renderListOps(t, false)
	if strings.Contains(ops, "Count: func") {
		t.Errorf("no total_count field → must not wire a Count closure; got:\n%s", ops)
	}
	if strings.Contains(ops, "TotalCount:") {
		t.Errorf("no total_count field → must not pack TotalCount; got:\n%s", ops)
	}
	// The list Pack still carries the (unused) totalCount param — the crud
	// library's Pack signature is uniform.
	if !strings.Contains(ops, "totalCount int64") {
		t.Errorf("list Pack should keep the uniform totalCount param; got:\n%s", ops)
	}
}
