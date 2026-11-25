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

// A connect-go v2 provider registers its client with
// servicecatalog.NewClientV2. The typed inventory must read it exactly as it
// reads a v1 NewClient: the declared service, its config field and the
// generated constructor, resolved as "declared" with no diagnostic.
func TestServiceCatalogClientInventoryKeepsNewClientV2Binding(t *testing.T) {
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
	// The marker package mirrors pkg/servicecatalog's two signatures: the
	// binding is argument 0 and the constructor argument 1 in both.
	marker, err := new(types.Config).Check("github.com/reliant-labs/forge/pkg/servicecatalog", fset, []*ast.File{parse("marker.go", `package servicecatalog
type ClientBinding struct{ Service, ConfigField string }
type HTTPClient interface{}
type V2Client struct{}
func NewClient[T any](b ClientBinding, ctor func() T) T { return ctor() }
func NewClientV2[T any](b ClientBinding, ctor func(*V2Client) T, httpClient HTTPClient, baseURL string) T { return ctor(nil) }
`)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// v2 generated code: the same <Service>Name constant and New<Service>Client
	// constructor, which takes a *connect.Client.
	wire, err := (&types.Config{Importer: importerFunc(func(string) (*types.Package, error) { return marker, nil })}).Check("example.com/shop/gen/ordersconnect", fset, []*ast.File{parse("wire.go", `package ordersconnect
import c "github.com/reliant-labs/forge/pkg/servicecatalog"
const OrdersServiceName = "shop.v1.OrdersService"
type OrdersServiceClient interface{ Get() }
func NewOrdersServiceClient(*c.V2Client) OrdersServiceClient { return nil }
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
	return c.NewClientV2(c.ClientBinding{Service: rpc.OrdersServiceName, ConfigField: "shop.config.v1.API.orders_url"}, rpc.NewOrdersServiceClient, nil, "http://orders")
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
	desc := ForgeDescriptor{Configs: []ConfigMessage{{FullName: "shop.config.v1.API", Name: "API", Fields: []ConfigField{{Name: "orders_url", ProtoType: "string"}}}}}
	b, err := json.Marshal(desc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "gen/forge_descriptor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gen/forge_descriptor/fixture.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}

	m := &graphModel{u: &graphUniverse{app: &packages.Package{Types: pkg, Fset: fset, Syntax: []*ast.File{file}, TypesInfo: info}}, byName: map[string]*graphNode{"Orders": {}}}
	var diagnostics []servicecatalog.Diagnostic
	out := graphCatalogClients(GraphGenInput{GenContext: GenContext{ProjectDir: dir}}, m, &diagnostics)
	if len(out) != 1 || len(diagnostics) != 0 {
		t.Fatalf("clients=%+v diagnostics=%+v, want one declared client and no diagnostic", out, diagnostics)
	}
	cl := out[0]
	if cl.Resolution != "declared" || cl.Provider != "Orders" || cl.Service != "shop.v1.OrdersService" ||
		cl.ConfigField != "shop.config.v1.API.orders_url" || cl.Constructor != wire.Path()+".NewOrdersServiceClient" ||
		cl.Source.Path != "internal/app/providers.go" {
		t.Fatalf("v2 binding lost: %+v", cl)
	}
}
