package codegen

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/reliant-labs/forge/pkg/servicecatalog"
)

func TestServiceCatalogTypedMultiRoleFixture(t *testing.T) {
	dir := t.TempDir()
	fset := token.NewFileSet()
	src := `package app
 type Port interface { Get(string) (string,error) }
 type Impl struct{}
 func (*Impl) Get(s string) (string,error) { return s,nil }
 type ConsumerDeps struct { API Port; Dynamic Port }
 type Consumer struct{}
 type RemoteDeps struct{}
 func NewRemote(RemoteDeps) Port {return &Impl{}}
 type API struct{}
 type Worker struct{}
 `
	f, err := parser.ParseFile(fset, filepath.Join(dir, "internal/app/providers.go"), src, 0)
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := new(types.Config).Check("example.com/shop/internal/app", fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatal(err)
	}
	p := &packages.Package{Types: pkg, Fset: fset}
	iface := pkg.Scope().Lookup("Port").Type()
	u := &graphUniverse{app: p, lookup: func(string) *packages.Package { return p }, compDir: func(BuildComponent) string { return "" }, produced: map[string]types.Type{"Consumer": pkg.Scope().Lookup("Consumer").Type(), "Remote": iface, "Alternative": types.NewPointer(pkg.Scope().Lookup("Impl").Type())}, deps: map[string]map[string]types.Type{"Consumer": {"API": iface, "Dynamic": iface}}}
	consumer := &graphNode{Name: "Consumer", Kind: nodeComponent, Comp: &BuildComponent{compConstructor: "NewRemote"}, DepsType: "ConsumerDeps", Deps: []DepsField{{Name: "API"}, {Name: "Dynamic"}}}
	remote := &graphNode{Name: "Remote", Kind: nodeProvider, File: "internal/app/providers.go", Line: 8}
	alt := &graphNode{Name: "Alternative", Kind: nodeProvider}
	routes := &graphNode{Name: "UserRoutes", Kind: nodeRoutes, Audience: "user", Services: []*graphNode{consumer}}
	nodes := []*graphNode{consumer, remote, alt, routes}
	m := &graphModel{nodes: nodes, byName: map[string]*graphNode{}, u: u, roots: []graphRoot{{Name: "api", TypeName: "API", File: "internal/app/roots.go", Line: 3}, {Name: "worker", TypeName: "Worker", File: "internal/app/roots.go", Line: 6}}, rootViews: []graphRootView{{Name: "api", TypeName: "API", Command: []string{"server", "api"}, Reach: []string{"Consumer", "Remote", "UserRoutes"}}, {Name: "worker", TypeName: "Worker", Command: []string{"server", "worker"}, Reach: []string{"Remote"}}}, compose: `package app
 func(g *Graph) newConsumer() { _=ConsumerDeps{API:g.Remote(),Dynamic:chooseAtRuntime()} }
 `, routesView: routesGenView{Services: []routesServiceView{{InventoryServiceData: InventoryServiceData{FieldName: "Consumer", ProtoServiceName: "OrdersService"}}}}}
	for _, n := range nodes {
		m.byName[n.Name] = n
	}
	in := GraphGenInput{GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/shop"}, Services: []ServiceDef{{Name: "OrdersService", Package: "shop.v1", ProtoFile: "shop/v1/orders.proto", Methods: []Method{{Name: "Get", InputTypeFQ: "shop.v1.Request", OutputTypeFQ: "shop.v1.Response", NoSideEffects: true, ServerStreaming: true, AuthRequired: true}}, Schemas: map[string][]SchemaFieldDef{"shop.v1.Request": {{Name: "id", Kind: "string", ValidateOptions: "[(buf.validate.field).string.min_len=1]"}}}}}}
	c := graphCatalog(in, m)
	if len(c.Processes) != 2 || len(c.Processes[0].Serves) != 1 || len(c.Processes[1].Serves) != 0 {
		t.Fatalf("roles: %+v", c.Processes)
	}
	if got := c.Processes[0].Serves[0].Service; got != "shop.v1.OrdersService" {
		t.Fatal(got)
	}
	var bound, dynamic servicecatalog.Binding
	for _, b := range c.Bindings {
		switch b.Field {
		case "API":
			bound = b
		case "Dynamic":
			dynamic = b
		}
	}
	if bound.Resolution != "composition_expression" || strings.Join(bound.Providers, ",") != "Remote" || strings.Join(bound.Candidates, ",") != "Alternative,Remote" {
		t.Fatalf("binding: %+v", bound)
	}
	if dynamic.Resolution != "unresolved" || len(dynamic.Providers) != 0 || len(dynamic.Candidates) != 2 {
		t.Fatalf("dynamic must not guess: %+v", dynamic)
	}
	if bound.Interface == "" || bound.Methods == nil || len(bound.Methods) != 0 {
		t.Fatalf("a binding names its interface and records no code method set: %+v", bound)
	}
	meth := c.Services[0].Methods[0]
	if !meth.ServerStreaming || meth.Idempotency != "NO_SIDE_EFFECTS" || meth.Input != "shop.v1.Request" {
		t.Fatalf("wire: %+v", meth)
	}
	if err := emitGraphCatalog(in, m); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(filepath.Join(dir, servicecatalog.GeneratedPath))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(first, []byte(dir)) || !bytes.Contains(first, []byte("validate_options")) {
		t.Fatal("absolute source leak or schema lost")
	}
	if _, err := servicecatalog.Decode(first); err != nil {
		t.Fatal(err)
	}
	if err := emitGraphCatalog(in, m); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(filepath.Join(dir, servicecatalog.GeneratedPath))
	if !bytes.Equal(first, second) {
		t.Fatal("catalog is nondeterministic")
	}
	raw, _ := json.Marshal(c)
	if len(raw) == 0 {
		t.Fatal("no output")
	}
}

func TestServiceCatalogRejectsEndpointValues(t *testing.T) {
	for _, s := range []string{"https://secret.example/path?token=abc", "user:password@host", "config..url", "/tmp/config"} {
		if catalogProtoRef(s) {
			t.Fatalf("accepted %q", s)
		}
	}
}
