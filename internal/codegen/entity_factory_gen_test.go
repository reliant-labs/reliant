package codegen

import (
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// TestRenderEntityFactoryFile_ValidGo renders the factory helper from hand-built
// specs (no DB) and asserts the output is valid, gofmt-clean Go carrying the
// shared helper, per-entity override type, baked SQL, and the New<Entity> body.
func TestRenderEntityFactoryFile_ValidGo(t *testing.T) {
	specs := []entityFactorySpec{
		{
			goName:    "Order",
			lower:     "order",
			parentSQL: "INSERT INTO \"customers\" (id, email) VALUES ('c0', 'a@b.co')\nON CONFLICT (id) DO NOTHING;",
			rootSQL:   "INSERT INTO \"orders\" (id, customer_id, status) VALUES ($1, 'c0', 'active');",
		},
		{
			// Multi-word entity → camelCase const stem; no parents (root has no FKs).
			goName:    "OrderItem",
			lower:     "orderItem",
			parentSQL: "",
			rootSQL:   "INSERT INTO \"order_items\" (id, label) VALUES ($1, 'x');",
		},
	}

	out := renderEntityFactoryFile("github.com/acme/shop/internal/db", factoryGroup{pkg: "order", specs: specs})
	if _, err := format.Source(out); err != nil {
		t.Fatalf("rendered factories_gen_test.go is not valid Go: %v\n---\n%s", err, out)
	}
	s := string(out)
	for _, want := range []string{
		// The clause is the HANDLER package's own, not a factory-only
		// package: that is what makes these visible to both the in-package
		// and the external `order_test` files in the same directory.
		"package order",
		`"github.com/oklog/ulid/v2"`,
		`db "github.com/acme/shop/internal/db"`,
		"func seedFactoryParents(t testing.TB, database orm.Context, sql string)",
		"type OrderOverride func(*db.Order)",
		"func NewOrder(t testing.TB, database orm.Context, overrides ...OrderOverride) *db.Order",
		"seedFactoryParents(t, database, orderFactoryParentSQL)",
		"id := ulid.Make().String()",
		"database.Exec(context.Background(), orderFactoryRootSQL, id)",
		"db.GetOrderByID(context.Background(), database, id)",
		"db.UpdateOrder(context.Background(), database, row)",
		"VALUES ($1,",
		"type OrderItemOverride func(*db.OrderItem)",
		"const orderItemFactoryRootSQL",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("rendered factory_gen.go missing %q", want)
		}
	}
	// The parent-less entity must NOT reference a parent-seed const.
	if strings.Contains(s, "orderItemFactoryParentSQL") {
		t.Errorf("parent-less entity should not emit or reference a parent SQL const")
	}
}

func structFromSrc(t *testing.T, src string) (string, *ast.StructType) {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "x.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts := spec.(*ast.TypeSpec)
			if st, ok := ts.Type.(*ast.StructType); ok {
				return ts.Name.Name, st
			}
		}
	}
	t.Fatal("no struct found")
	return "", nil
}

func TestEntityFromStruct(t *testing.T) {
	q := "`"
	name, st := structFromSrc(t, "package db\n"+
		"type Order struct {\n"+
		"\tbun.BaseModel "+q+`bun:"table:orders,alias:orders"`+q+"\n"+
		"\tId string "+q+`bun:"id,pk"`+q+"\n"+
		"\tCustomerId string "+q+`bun:"customer_id,notnull"`+q+"\n"+
		"}")
	ent, ok := entityFromStruct(name, st)
	if !ok {
		t.Fatal("expected Order to be recognized as an entity")
	}
	if ent.goName != "Order" || ent.table != "orders" || ent.pkColumn != "id" || !ent.pkString {
		t.Errorf("got %+v, want {Order orders id true}", ent)
	}
}

func TestEntityFromStruct_NonStringPKSkipped(t *testing.T) {
	q := "`"
	name, st := structFromSrc(t, "package db\n"+
		"type Event struct {\n"+
		"\tbun.BaseModel "+q+`bun:"table:events"`+q+"\n"+
		"\tId int64 "+q+`bun:"id,pk,autoincrement"`+q+"\n"+
		"}")
	ent, ok := entityFromStruct(name, st)
	if !ok {
		t.Fatal("expected entity recognition")
	}
	if ent.pkString {
		t.Errorf("int64 PK must not be flagged pkString: %+v", ent)
	}
}

func TestEntityFromStruct_NotAnEntity(t *testing.T) {
	q := "`"
	name, st := structFromSrc(t, "package db\n"+
		"type Config struct {\n"+
		"\tName string "+q+`json:"name"`+q+"\n"+
		"}")
	if _, ok := entityFromStruct(name, st); ok {
		t.Error("a struct with no bun table tag must not be treated as an entity")
	}
}
