package codegen

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/reliant-labs/forge/pkg/servicecatalog"
)

func TestServiceCatalogClientTypedMarkerFixture(t *testing.T) {
	dir := t.TempDir()
	fset := token.NewFileSet()
	parse := func(name, src string) *ast.File {
		t.Helper()
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), src, 0)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	marker, err := new(types.Config).Check("github.com/reliant-labs/forge/pkg/servicecatalog", fset, []*ast.File{parse("marker.go", `package servicecatalog
 type ClientBinding struct{ Service,ConfigField string }
 func NewClient[T any](b ClientBinding, ctor func()T)T{return ctor()}
 `)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := new(types.Config).Check("example.com/shop/gen/ordersconnect", fset, []*ast.File{parse("wire.go", `package ordersconnect
 const OrdersServiceName="shop.v1.OrdersService"
 type OrdersServiceClient interface{ Get() }
 func NewOrdersServiceClient() OrdersServiceClient{return nil}
 `)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	file := parse("internal/app/providers.go", `package app
 import (
 c "github.com/reliant-labs/forge/pkg/servicecatalog"
 rpc "example.com/shop/gen/ordersconnect"
 )
 type OrdersDeps struct{}
 func NewOrders(OrdersDeps) rpc.OrdersServiceClient {
  return c.NewClient(c.ClientBinding{Service:rpc.OrdersServiceName,ConfigField:"shop.config.v1.API.orders_url"},rpc.NewOrdersServiceClient)
 }
 type DynamicDeps struct{}
 func NewDynamic(DynamicDeps) rpc.OrdersServiceClient {
  b:=c.ClientBinding{Service:rpc.OrdersServiceName,ConfigField:"shop.config.v1.API.orders_url"}
  return c.NewClient(b,rpc.NewOrdersServiceClient)
 }
 type SecretDeps struct{}
 func NewSecret(SecretDeps) rpc.OrdersServiceClient {
  return c.NewClient(c.ClientBinding{Service:rpc.OrdersServiceName,ConfigField:"shop.config.v1.API.token"},rpc.NewOrdersServiceClient)
 }
 `)
	info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}, Uses: map[*ast.Ident]types.Object{}}
	pkg, err := (&types.Config{Importer: importerFunc(func(path string) (*types.Package, error) {
		if path == marker.Path() {
			return marker, nil
		}
		return wire, nil
	})}).Check("example.com/shop/internal/app", fset, []*ast.File{file}, info)
	if err != nil {
		t.Fatal(err)
	}
	p := &packages.Package{Types: pkg, Fset: fset, Syntax: []*ast.File{file}, TypesInfo: info}
	desc := ForgeDescriptor{Configs: []ConfigMessage{{FullName: "shop.config.v1.API", Name: "API", Fields: []ConfigField{{Name: "orders_url", ProtoType: "string"}, {Name: "token", ProtoType: "string", Sensitive: true}}}}}
	b, _ := json.Marshal(desc)
	if err := os.MkdirAll(filepath.Join(dir, "gen"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "gen/forge_descriptor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gen/forge_descriptor/fixture.json"), b, 0644); err != nil {
		t.Fatal(err)
	}
	m := &graphModel{u: &graphUniverse{app: p}, byName: map[string]*graphNode{"Orders": {}, "Dynamic": {}, "Secret": {}}}
	var diagnostics []servicecatalog.Diagnostic
	out := graphCatalogClients(GraphGenInput{GenContext: GenContext{ProjectDir: dir}}, m, &diagnostics)
	if len(out) != 3 || len(diagnostics) != 2 {
		t.Fatalf("clients=%+v diagnostics=%+v", out, diagnostics)
	}
	// The same registered provider can be reachable from several process roots;
	// a third root must not acquire its client capability by proto discovery.
	m.nodes = []*graphNode{{Name: "Orders", Kind: nodeProvider}, {Name: "Dynamic", Kind: nodeProvider}, {Name: "Secret", Kind: nodeProvider}}
	m.u.produced = map[string]types.Type{}
	m.u.deps = map[string]map[string]types.Type{}
	m.rootViews = []graphRootView{{Name: "api", Command: []string{"server", "api"}, Reach: []string{"Orders"}}, {Name: "worker", Command: []string{"server", "worker"}, Reach: []string{"Orders"}}, {Name: "gateway", Command: []string{"server", "gateway"}}}
	catalog := graphCatalog(GraphGenInput{GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/shop"}}, m)
	for _, process := range catalog.Processes {
		if process.ID == "example.com/shop#gateway" {
			if len(process.Clients) != 0 {
				t.Fatal("client leaked to gateway")
			}
		} else if len(process.Clients) != 1 {
			t.Fatalf("shared provider missing from root: %+v", process)
		}
	}
	for _, cl := range out {
		if cl.Provider == "Orders" {
			if cl.Resolution != "declared" || cl.Service != "shop.v1.OrdersService" || cl.ConfigField != "shop.config.v1.API.orders_url" || cl.Constructor != wire.Path()+".NewOrdersServiceClient" || cl.Source.Path != "internal/app/providers.go" {
				t.Fatalf("marker lost: %+v", cl)
			}
		} else if cl.Resolution != "unresolved" || cl.Service != "" || cl.ConfigField != "" {
			t.Fatalf("dynamic/secret exposed: %+v", cl)
		}
	}
}
