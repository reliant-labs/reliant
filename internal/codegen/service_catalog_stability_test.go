package codegen

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/reliant-labs/forge/pkg/servicecatalog"
)

// The committed catalog is merged by a queue: any pull request ahead of yours
// that moves a line in a handler file must not make yours stale. So moving
// declarations around, editing unrelated code above them, or adding a Go
// method leaves the bytes identical; adding an rpc changes them.

const stableCatalogBase = `package app

import (
	c "github.com/reliant-labs/forge/pkg/servicecatalog"
	rpc "example.com/shop/gen/ordersconnect"
)

type Port interface {
	Get(id string) (string, error)
}

type Impl struct{}

func (*Impl) Get(id string) (string, error) { return id, nil }

type ConsumerDeps struct{ API Port }

func NewConsumer(ConsumerDeps) Port { return &Impl{} }

type OrdersDeps struct{}

func NewOrders(OrdersDeps) rpc.OrdersServiceClient {
	return c.NewClient(c.ClientBinding{Service: rpc.OrdersServiceName, ConfigField: "shop.config.v1.API.orders_url"}, rpc.NewOrdersServiceClient)
}

type API struct{}
`

// The same declarations, reordered and pushed down by unrelated code.
const stableCatalogMoved = `package app

import (
	c "github.com/reliant-labs/forge/pkg/servicecatalog"
	rpc "example.com/shop/gen/ordersconnect"
)

// An unrelated helper, merged ahead of us, pushes everything down.
func unrelated() int {
	x := 1
	x++
	return x
}

type API struct{}

type OrdersDeps struct{}

func NewOrders(OrdersDeps) rpc.OrdersServiceClient {

	return c.NewClient(c.ClientBinding{Service: rpc.OrdersServiceName, ConfigField: "shop.config.v1.API.orders_url"}, rpc.NewOrdersServiceClient)
}

type ConsumerDeps struct{ API Port }

func NewConsumer(ConsumerDeps) Port { return &Impl{} }

type Impl struct{}

// Port is what the consumer reads.
type Port interface {
	// Get reads one.
	Get(id string) (string, error)
}

func (*Impl) Get(id string) (string, error) { return id, nil }
`

var stableCatalogServices = []ServiceDef{{Name: "OrdersService", Package: "shop.v1", ProtoFile: "shop/v1/orders.proto",
	Methods: []Method{{Name: "GetOrder", InputTypeFQ: "shop.v1.GetOrderRequest", OutputTypeFQ: "shop.v1.GetOrderResponse", AuthRequired: true}}}}

// stableCatalog runs the real projection over one internal/app source and
// returns the bytes forge generate commits, and the project dir.
func stableCatalog(t *testing.T, src string, services []ServiceDef) ([]byte, string) {
	t.Helper()
	dir := t.TempDir()
	fset := token.NewFileSet()
	parse := func(name, src string) *ast.File {
		t.Helper()
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), src, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	marker, err := new(types.Config).Check("github.com/reliant-labs/forge/pkg/servicecatalog", fset, []*ast.File{parse("marker.go", `package servicecatalog
type ClientBinding struct{ Service, ConfigField string }
func NewClient[T any](b ClientBinding, ctor func() T) T { return ctor() }
`)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := new(types.Config).Check("example.com/shop/gen/ordersconnect", fset, []*ast.File{parse("wire.go", `package ordersconnect
const OrdersServiceName = "shop.v1.OrdersService"
type OrdersServiceClient interface{ GetOrder() }
func NewOrdersServiceClient() OrdersServiceClient { return nil }
`)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	const rel = "internal/app/providers.go"
	if err := os.MkdirAll(filepath.Join(dir, "internal/app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, rel), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	file := parse(rel, src)
	info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}, Uses: map[*ast.Ident]types.Object{}, Defs: map[*ast.Ident]types.Object{}}
	pkg, err := (&types.Config{Importer: importerFunc(func(path string) (*types.Package, error) {
		if path == marker.Path() {
			return marker, nil
		}
		return wire, nil
	})}).Check("example.com/shop/internal/app", fset, []*ast.File{file}, info)
	if err != nil {
		t.Fatal(err)
	}
	// Discovery records these positions (graphProvider, graphRoot): the
	// catalog must not carry them.
	line := func(name string) int {
		for _, d := range file.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if d.Name.Name == name {
					return fset.Position(d.Pos()).Line
				}
			case *ast.GenDecl:
				for _, s := range d.Specs {
					if ts, ok := s.(*ast.TypeSpec); ok && ts.Name.Name == name {
						return fset.Position(ts.Pos()).Line
					}
				}
			}
		}
		t.Fatalf("no %s in fixture", name)
		return 0
	}
	p := &packages.Package{Types: pkg, Fset: fset, Syntax: []*ast.File{file}, TypesInfo: info}
	port := pkg.Scope().Lookup("Port").Type()
	consumer := &graphNode{Name: "Consumer", Kind: nodeComponent, Comp: &BuildComponent{compConstructor: "NewConsumer"}, DepsType: "ConsumerDeps", Deps: []DepsField{{Name: "API"}}}
	orders := &graphNode{Name: "Orders", Kind: nodeProvider, Ctor: "NewOrders", File: rel, Line: line("NewOrders")}
	nodes := []*graphNode{consumer, orders}
	m := &graphModel{nodes: nodes, byName: map[string]*graphNode{},
		u: &graphUniverse{app: p, lookup: func(string) *packages.Package { return p }, compDir: func(BuildComponent) string { return "" },
			produced: map[string]types.Type{"Consumer": port, "Orders": wire.Scope().Lookup("OrdersServiceClient").Type()},
			deps:     map[string]map[string]types.Type{"Consumer": {"API": port}}},
		roots:     []graphRoot{{Name: "api", TypeName: "API", File: rel, Line: line("API")}},
		rootViews: []graphRootView{{Name: "api", TypeName: "API", Command: []string{"server", "api"}, Reach: []string{"Consumer", "Orders"}}},
		compose:   "package app\nfunc (g *Graph) newConsumer() { _ = ConsumerDeps{API: g.Orders()} }\n"}
	for _, n := range nodes {
		m.byName[n.Name] = n
	}
	desc, _ := json.Marshal(ForgeDescriptor{Configs: []ConfigMessage{{FullName: "shop.config.v1.API", Name: "API", Fields: []ConfigField{{Name: "orders_url", ProtoType: "string"}}}}})
	if err := os.MkdirAll(filepath.Join(dir, "gen"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "gen/forge_descriptor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gen/forge_descriptor/fixture.json"), desc, 0o644); err != nil {
		t.Fatal(err)
	}
	in := GraphGenInput{GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/shop"}, Services: services}
	if err := emitGraphCatalog(in, m); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, servicecatalog.GeneratedPath))
	if err != nil {
		t.Fatal(err)
	}
	return raw, dir
}

func TestServiceCatalogIsStableAcrossMovedCode(t *testing.T) {
	base, _ := stableCatalog(t, stableCatalogBase, stableCatalogServices)
	// The fixture must exercise every kind of source a catalog records, or
	// "identical" proves nothing about the one it skipped.
	c, err := servicecatalog.Decode(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Components) != 2 || len(c.Clients) != 1 || len(c.Bindings) != 1 || len(c.Processes) != 1 || len(c.Components[0].ImplementationCandidates) != 1 {
		t.Fatalf("fixture lost a source kind:\n%s", base)
	}
	for _, s := range []servicecatalog.Source{c.Processes[0].Source, c.Components[0].Source,
		c.Components[0].ImplementationCandidates[0].Source, c.Components[1].Source, c.Clients[0].Source} {
		if s.Path == "" || s.Symbol == "" || s.Line != 0 {
			t.Fatalf("source %+v is not named by symbol:\n%s", s, base)
		}
	}
	// Only the unknown source ({"path": "", "line": 0}) has a line key.
	if regexp.MustCompile(`"line": [1-9]`).Match(base) {
		t.Fatalf("the committed catalog records source lines:\n%s", base)
	}

	moved, _ := stableCatalog(t, stableCatalogMoved, stableCatalogServices)
	if !bytes.Equal(base, moved) {
		t.Fatalf("moving declarations changed the committed catalog:\n--- base\n%s\n--- moved\n%s", base, moved)
	}

	withRPC := append([]ServiceDef{}, stableCatalogServices...)
	withRPC[0].Methods = append(append([]Method{}, withRPC[0].Methods...), Method{Name: "ListOrders", InputTypeFQ: "shop.v1.ListOrdersRequest", OutputTypeFQ: "shop.v1.ListOrdersResponse", AuthRequired: true})
	if added, _ := stableCatalog(t, stableCatalogBase, withRPC); bytes.Equal(base, added) {
		t.Fatal("adding a proto rpc left the committed catalog unchanged")
	}
	goMethod := strings.Replace(stableCatalogBase, "Get(id string) (string, error)\n}", "Get(id string) (string, error)\n\tList() ([]string, error)\n}", 1)
	goMethod += "\nfunc (*Impl) List() ([]string, error) { return nil, nil }\n"
	if added, _ := stableCatalog(t, goMethod, stableCatalogServices); !bytes.Equal(base, added) {
		t.Fatalf("adding a Go method changed the committed catalog: it must depend on declarations "+
			"(rpcs, components, bindings), not on Go method sets no forge input sees:\n--- base\n%s\n--- added\n%s", base, added)
	}
	unexported := stableCatalogBase + "\nfunc (*Impl) normalize(id string) string { return id }\n"
	if added, _ := stableCatalog(t, unexported, stableCatalogServices); !bytes.Equal(base, added) {
		t.Fatal("adding an unexported helper method changed the committed catalog")
	}
	for _, comp := range c.Components {
		if len(comp.Methods) != 0 {
			t.Fatalf("component %s records a code method set: %+v", comp.ID, comp.Methods)
		}
	}
	if !slices.ContainsFunc(c.Diagnostics, func(d servicecatalog.Diagnostic) bool {
		return d.Code == servicecatalog.DiagnosticBundleMethodSetsOmitted
	}) {
		t.Fatalf("the catalog does not say its method sets are not recorded: %+v", c.Diagnostics)
	}
}

// The bundle still carries lines: Locate resolves each symbol against the
// source being built, so the moved fixture's bundle points at the moved lines.
func TestServiceCatalogBundleLocatesTheBuiltSource(t *testing.T) {
	raw, dir := stableCatalog(t, stableCatalogMoved, stableCatalogServices)
	if _, err := servicecatalog.DecodeLocated(raw); err == nil {
		t.Fatal("the bundle gate accepted a committed (symbolic) catalog")
	}
	c, err := servicecatalog.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	servicecatalog.Locate(&c, func(rel string) ([]byte, error) { return os.ReadFile(filepath.Join(dir, rel)) })
	located, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := servicecatalog.DecodeLocated(located); err != nil {
		t.Fatalf("located catalog refused: %v\n%s", err, located)
	}
	lines := strings.Split(stableCatalogMoved, "\n")
	at := func(s servicecatalog.Source, want string) {
		t.Helper()
		if s.Symbol != "" || s.Line < 1 || s.Line > len(lines) || !strings.Contains(lines[s.Line-1], want) {
			t.Fatalf("source %+v does not point at %q", s, want)
		}
	}
	at(c.Processes[0].Source, "type API struct")
	at(c.Components[0].Source, "func NewConsumer(")
	at(c.Components[0].ImplementationCandidates[0].Source, "type Impl struct")
	at(c.Components[1].Source, "func NewOrders(")
	at(c.Clients[0].Source, "func NewOrders(")
}

// Go identifiers may be Unicode. A symbol the generator emits for one must
// decode and locate, or the bundle build would fail on a valid project.
func TestServiceCatalogUnicodeSymbolsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	const rel = "internal/app/café.go"
	src := "package app\n\ntype Café interface {\n\tGröße() int\n}\n\ntype Ünit struct{}\n\nfunc (Ünit) Größe() int { return 1 }\n"
	if err := os.MkdirAll(filepath.Join(dir, "internal/app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, rel), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join(dir, rel), src, 0)
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := new(types.Config).Check("example.com/shop/internal/app", fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatal(err)
	}
	p := &packages.Package{Types: pkg, Fset: fset}
	iface := pkg.Scope().Lookup("Café").Type()
	c := servicecatalog.Catalog{Schema: servicecatalog.Schema, Module: "example.com/shop", Components: []servicecatalog.Component{{
		ID: "Café", Name: "Café", Kind: "component", Source: catalogSource(dir, fset, pkg.Scope().Lookup("Café")),
		ImplementationCandidates: catalogImplementations(dir, p, iface)}}}
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := servicecatalog.Decode(raw)
	if err != nil {
		t.Fatalf("a Unicode symbol does not decode: %v\n%s", err, raw)
	}
	comp := decoded.Components[0]
	if comp.Source.Symbol != "Café" || len(comp.ImplementationCandidates) != 1 || comp.ImplementationCandidates[0].Source.Symbol != "Ünit" {
		t.Fatalf("symbols: %s", raw)
	}
	servicecatalog.Locate(&decoded, func(rel string) ([]byte, error) { return os.ReadFile(filepath.Join(dir, rel)) })
	comp = decoded.Components[0]
	got := []int{comp.Source.Line, comp.ImplementationCandidates[0].Source.Line}
	if want := []int{3, 7}; !slices.Equal(got, want) {
		t.Fatalf("located lines %v, want %v", got, want)
	}
	located, _ := json.Marshal(decoded)
	if _, err := servicecatalog.DecodeLocated(located); err != nil {
		t.Fatal(err)
	}
}

// Two pull requests that each change a different service change disjoint
// lines of the committed catalog, so git's plain three-way merge — the one a
// merge queue or GitHub's "update branch" runs, with no merge driver — merges
// them cleanly into exactly what forge generate renders for the merged
// inputs. Nothing else in the file (no fingerprint, no certificate in
// .forge/hashes.json) changes with them.
func TestServiceCatalogIndependentChangesMergeToTheRender(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	svc := func(name, pkg string, rpcs ...string) ServiceDef {
		s := ServiceDef{Name: name, Package: pkg, ProtoFile: pkg + "/" + strings.ToLower(name) + ".proto"}
		for _, r := range rpcs {
			s.Methods = append(s.Methods, Method{Name: r, InputTypeFQ: pkg + "." + r + "Request", OutputTypeFQ: pkg + "." + r + "Response", AuthRequired: true})
		}
		return s
	}
	render := func(billing, users []string) []byte {
		raw, _ := stableCatalog(t, stableCatalogBase, []ServiceDef{
			svc("BillingService", "billing.v1", billing...),
			svc("OrdersService", "shop.v1", "GetOrder"),
			svc("UsersService", "users.v1", users...),
		})
		return raw
	}
	base := render([]string{"GetInvoice"}, []string{"GetUser"})
	ours := render([]string{"GetInvoice", "ListInvoices"}, []string{"GetUser"})
	theirs := render([]string{"GetInvoice"}, []string{"GetUser", "ListUsers"})
	both := render([]string{"GetInvoice", "ListInvoices"}, []string{"GetUser", "ListUsers"})

	dir := t.TempDir()
	for name, body := range map[string][]byte{"base": base, "ours": ours, "theirs": theirs} {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	merged, err := exec.Command("git", "merge-file", "-p", filepath.Join(dir, "ours"), filepath.Join(dir, "base"), filepath.Join(dir, "theirs")).Output()
	if err != nil {
		t.Fatalf("two independent service changes conflict in the catalog (%v):\n%s", err, merged)
	}
	if !bytes.Equal(merged, both) {
		t.Fatalf("the plain merge of two independent changes is not forge's render of the merged inputs:\n--- merged\n%s\n--- render\n%s", merged, both)
	}
}
