package codegen

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/reliant-labs/forge/pkg/servicecatalog"
)

// graphCatalog projects the already resolved graph, never guesses bindings from
// package names, and never opens an endpoint. Descriptor bytes retain the wire
// schema/validation vocabulary used by all other Forge descriptor consumers.
func graphCatalog(in GraphGenInput, m *graphModel) servicecatalog.Catalog {
	c := servicecatalog.Catalog{Schema: servicecatalog.Schema, Module: in.ModulePath,
		Processes: []servicecatalog.Process{}, Services: []servicecatalog.Service{}, Components: []servicecatalog.Component{}, Bindings: []servicecatalog.Binding{}, Clients: []servicecatalog.Client{}, Diagnostics: []servicecatalog.Diagnostic{}}
	services, descriptorErr := ParseDeclaredServices(in.ProjectDir)
	if descriptorErr != nil {
		c.Diagnostics = append(c.Diagnostics, servicecatalog.Diagnostic{Code: "descriptor_unavailable"})
	}
	c.Diagnostics = append(c.Diagnostics,
		// The typed graph proves generated route-set reach, not arbitrary runtime
		// mux registration, reflection, proxy routes, or unregistered API clients.
		servicecatalog.Diagnostic{Code: "coverage_generated_routes_and_registered_clients_only"},
		// The committed catalog records no code method sets (catalogNoMethods).
		servicecatalog.Diagnostic{Code: servicecatalog.DiagnosticBundleMethodSetsOmitted})
	if len(services) == 0 {
		services = in.Services
	}
	c.Services = append(c.Services, catalogServices(services)...)
	compose := parseCatalogCompose(m.compose)
	for _, n := range m.built() {
		comp := catalogComponent(in.ProjectDir, m, n)
		if m.u.produced[n.Name] == nil && n.Kind != nodeRoutes && n.Kind != nodeOperators {
			c.Diagnostics = append(c.Diagnostics, servicecatalog.Diagnostic{Code: "type_unresolved", Component: n.Name, Source: comp.Source})
		}
		c.Components = append(c.Components, comp)
		for _, df := range n.Deps {
			b, ok := catalogBinding(m, n, df, compose)
			if !ok {
				continue
			}
			if b.Resolution == "unresolved" {
				c.Diagnostics = append(c.Diagnostics, servicecatalog.Diagnostic{Code: "binding_unresolved", Component: n.Name + "." + df.Name, Source: comp.Source})
			}
			c.Bindings = append(c.Bindings, b)
		}
	}
	c.Clients = graphCatalogClients(in, m, &c.Diagnostics)
	for _, r := range m.rootViews {
		c.Processes = append(c.Processes, catalogProcess(in.ModulePath, m, r, services, c.Clients))
	}
	sort.Slice(c.Processes, func(i, j int) bool { return c.Processes[i].ID < c.Processes[j].ID })
	sort.Slice(c.Components, func(i, j int) bool { return c.Components[i].ID < c.Components[j].ID })
	sort.Slice(c.Bindings, func(i, j int) bool {
		a, b := c.Bindings[i], c.Bindings[j]
		return a.Consumer+"."+a.Field < b.Consumer+"."+b.Field
	})
	sort.Slice(c.Diagnostics, func(i, j int) bool {
		a, b := c.Diagnostics[i], c.Diagnostics[j]
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		if a.Component != b.Component {
			return a.Component < b.Component
		}
		if a.Source.Path != b.Source.Path {
			return a.Source.Path < b.Source.Path
		}
		return a.Source.Symbol < b.Source.Symbol
	})
	return c
}

// catalogServices projects the declared proto services, methods sorted by ID
// and services by name.
func catalogServices(services []ServiceDef) []servicecatalog.Service {
	out := []servicecatalog.Service{}
	for _, s := range services {
		raw, _ := json.Marshal(s)
		svc := servicecatalog.Service{Name: s.Package + "." + s.Name, ProtoFile: s.ProtoFile, Descriptor: raw, Methods: []servicecatalog.Method{}}
		for _, method := range s.Methods {
			// The descriptor spells protobuf's level ("IDEMPOTENCY_UNKNOWN");
			// the catalog's closed set is UNKNOWN, IDEMPOTENT, NO_SIDE_EFFECTS.
			idem := method.Idempotency
			if idem == "" && method.NoSideEffects {
				idem = "NO_SIDE_EFFECTS"
			}
			idem = servicecatalog.Idempotency(idem)
			svc.Methods = append(svc.Methods, servicecatalog.Method{ID: svc.Name + "/" + method.Name, Name: method.Name, Input: method.InputTypeFQ, Output: method.OutputTypeFQ, ClientStreaming: method.ClientStreaming, ServerStreaming: method.ServerStreaming, Idempotency: idem, AuthRequired: method.AuthRequired})
		}
		sort.Slice(svc.Methods, func(i, j int) bool { return svc.Methods[i].ID < svc.Methods[j].ID })
		out = append(out, svc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// catalogCompose is the parsed owned compose.go a binding's provider
// expression is read from. A compose.go that does not parse leaves both maps
// empty, so every binding stays unresolved rather than guessed.
type catalogCompose struct {
	fset    *token.FileSet
	methods map[string]*ast.FuncDecl
	imports map[string]string
}

func parseCatalogCompose(src string) catalogCompose {
	cx := catalogCompose{fset: token.NewFileSet(), methods: map[string]*ast.FuncDecl{}, imports: map[string]string{}}
	if file, _ := parser.ParseFile(cx.fset, "internal/app/compose.go", src, 0); file != nil {
		cx.methods = graphComposeMethods(file)
		cx.imports = fileImportAliases(file)
	}
	return cx
}

// catalogNoMethods is every code method set the committed catalog carries:
// none. A component's, candidate's or binding's method set is a function of
// arbitrary Go in its package — an unexported helper added to a router type
// changed reliant's catalog — so recording it made gen/ stale on every Go
// change no forge input saw, and turned unrelated pull requests into
// "forge generate is not a no-op" failures in the merge queue. Nothing reads
// it: the bundle copy never carried it (servicecatalog.ForBundle), and a
// person reads Go with `go doc`. The empty list is kept so the document stays
// schema v1, and the bundle_method_sets_omitted diagnostic says empty means "not
// recorded", never "has no methods".
func catalogNoMethods() []servicecatalog.CodeMethod { return []servicecatalog.CodeMethod{} }

// catalogComponent projects one built graph node: its kind, type and source
// (the constructor's, when it resolves).
func catalogComponent(dir string, m *graphModel, n *graphNode) servicecatalog.Component {
	t := m.u.produced[n.Name]
	kind := map[nodeKind]string{nodeComponent: "component", nodeProvider: "provider", nodeRoutes: "route_set", nodeOperators: "operators"}[n.Kind]
	comp := servicecatalog.Component{ID: n.Name, Name: n.Name, Kind: kind, Type: catalogType(t), Methods: catalogNoMethods()}
	if n.Kind == nodeProvider && n.File != "" {
		// A provider is its New<Name> func in internal/app.
		comp.Source = servicecatalog.Source{Path: n.File, Symbol: "New" + n.Name}
	}
	comp.ImplementationCandidates = []servicecatalog.Implementation{}
	if n.Comp == nil {
		return comp
	}
	p := m.u.lookup(m.u.compDir(*n.Comp))
	if p == nil || p.Types == nil {
		return comp
	}
	if ctor := p.Types.Scope().Lookup(n.Comp.compConstructor); ctor != nil {
		comp.Source = catalogSource(dir, p.Fset, ctor)
	}
	if t != nil {
		if _, ok := t.Underlying().(*types.Interface); ok {
			comp.ImplementationCandidates = catalogImplementations(dir, p, t)
		}
	}
	return comp
}

// catalogImplementations lists the concrete types of the component's own
// package that satisfy its interface type, by value or by pointer.
func catalogImplementations(dir string, p *packages.Package, iface types.Type) []servicecatalog.Implementation {
	out := []servicecatalog.Implementation{}
	for _, name := range p.Types.Scope().Names() {
		obj, ok := p.Types.Scope().Lookup(name).(*types.TypeName)
		if !ok {
			continue
		}
		ct := obj.Type()
		if _, ok := ct.Underlying().(*types.Interface); ok {
			continue
		}
		if !types.AssignableTo(ct, iface) {
			ct = types.NewPointer(ct)
		}
		if types.AssignableTo(ct, iface) {
			out = append(out, servicecatalog.Implementation{Type: catalogType(ct), Source: catalogSource(dir, p.Fset, obj), Methods: catalogNoMethods()})
		}
	}
	return out
}

// catalogBinding projects one interface-typed Deps field of n. It reports
// false for a field that is not a non-empty interface: only those bind.
func catalogBinding(m *graphModel, n *graphNode, df DepsField, compose catalogCompose) (servicecatalog.Binding, bool) {
	want := m.u.deps[n.Name][df.Name]
	if want == nil {
		return servicecatalog.Binding{}, false
	}
	iface, ok := want.Underlying().(*types.Interface)
	if !ok || iface.Empty() {
		return servicecatalog.Binding{}, false
	}
	b := servicecatalog.Binding{Consumer: n.Name, Field: df.Name, Interface: catalogType(want), Methods: catalogNoMethods(), Candidates: []string{}, Providers: []string{}, Resolution: "unresolved"}
	// All assignable typed producers are candidates. Identical matches must
	// not erase alternate interface implementations from browse metadata.
	for _, candidate := range m.built() {
		if candidate.Name == n.Name {
			continue
		}
		if ct := m.u.produced[candidate.Name]; ct != nil && types.AssignableTo(ct, want) {
			b.Candidates = append(b.Candidates, candidate.Name)
		}
	}
	b.Providers = append(b.Providers, catalogBindingProviders(m, n, df, compose)...)
	if len(b.Providers) > 0 {
		b.Resolution = "composition_expression"
	}
	sort.Strings(b.Candidates)
	sort.Strings(b.Providers)
	return b, true
}

// catalogBindingProviders names the components the compose.go expression
// assigned to df in n's Deps literal provably reads.
func catalogBindingProviders(m *graphModel, n *graphNode, df DepsField, compose catalogCompose) []string {
	fd := compose.methods["new"+n.Name]
	if fd == nil {
		return nil
	}
	lit := findDepsLiteral(fd, n, compose.imports)
	if lit == nil {
		return nil
	}
	var providers []string
	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if key, ok := kv.Key.(*ast.Ident); !ok || key.Name != df.Name {
			continue
		}
		// Reuse the composition edge recognizer on ONLY this field. Never
		// attribute every accessor in a constructor to every Deps field.
		recv := "g"
		if len(fd.Recv.List[0].Names) > 0 {
			recv = fd.Recv.List[0].Names[0].Name
		}
		src := fmt.Sprintf("package app; func (%s *Graph) new%s() { _ = %s }", recv, n.Name, printType(compose.fset, kv.Value))
		edges, lazy := graphComposeEdgesLazy(src, m.built())
		providers = append(providers, edges[n.Name]...)
		providers = append(providers, lazy[n.Name]...)
	}
	return providers
}

// catalogProcess projects one root: the components it reaches, the services
// its reached route sets serve, and the registered clients it constructs.
func catalogProcess(module string, m *graphModel, r graphRootView, services []ServiceDef, clients []servicecatalog.Client) servicecatalog.Process {
	p := servicecatalog.Process{ID: module + "#" + r.Name, Root: r.TypeName, Command: r.Command, Components: append([]string{}, r.Reach...), Serves: []servicecatalog.Serve{}, Clients: []string{}}
	for _, decl := range m.roots {
		if decl.Name == r.Name && decl.File != "" {
			p.Source = servicecatalog.Source{Path: decl.File, Symbol: decl.TypeName}
		}
	}
	reach := map[string]bool{}
	for _, name := range r.Reach {
		reach[name] = true
		n := m.byName[name]
		if n == nil || n.Kind != nodeRoutes {
			continue
		}
		for _, sv := range m.routesView.Services {
			for _, sn := range n.Services {
				if sn.Name == sv.FieldName {
					p.Serves = append(p.Serves, servicecatalog.Serve{Component: sn.Name, Service: catalogServiceName(sv, services), RouteSet: n.Name, Audience: n.Audience, Resolution: "declared_route_set"})
				}
			}
		}
	}
	for _, cl := range clients {
		if reach[cl.Provider] {
			p.Clients = append(p.Clients, cl.ID)
		}
	}
	sort.Strings(p.Components)
	sort.Strings(p.Clients)
	sort.Slice(p.Serves, func(i, j int) bool { return p.Serves[i].Service < p.Serves[j].Service })
	return p
}

func catalogType(t types.Type) string {
	if t == nil {
		return ""
	}
	return types.TypeString(t, func(p *types.Package) string { return p.Path() })
}

// catalogSource names obj's declaration: its module-relative file and its
// symbol. Never its line: the catalog is committed, and a line moves with
// every unrelated edit above it (servicecatalog.Source; the bundle's copy is
// located at build time). A declaration outside the module, or one with no
// name to find it by (a method of an unnamed interface), is unknown.
func catalogSource(dir string, fset *token.FileSet, obj types.Object) servicecatalog.Source {
	if obj == nil {
		return servicecatalog.Source{}
	}
	path, sym := catalogPath(dir, fset, obj.Pos()), catalogSymbol(obj)
	if path == "" || sym == "" {
		return servicecatalog.Source{}
	}
	return servicecatalog.Source{Path: path, Symbol: sym}
}

// catalogPath is pos's file relative to dir, or "" outside it.
func catalogPath(dir string, fset *token.FileSet, pos token.Pos) string {
	if fset == nil || !pos.IsValid() {
		return ""
	}
	abs, _ := filepath.Abs(dir)
	rel, err := filepath.Rel(abs, fset.Position(pos).Filename)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return ""
	}
	return filepath.ToSlash(rel)
}

// catalogSymbol is the name servicecatalog.Locate finds obj's declaration by:
// "Name" for a func or type, "Type.Name" for a method — of a concrete type,
// or of the named interface that declares it. "" when there is none.
func catalogSymbol(obj types.Object) string {
	if obj == nil {
		return ""
	}
	fn, ok := obj.(*types.Func)
	if !ok {
		return obj.Name()
	}
	recv := fn.Signature().Recv()
	if recv == nil {
		return fn.Name()
	}
	t := types.Unalias(recv.Type())
	if ptr, ok := t.(*types.Pointer); ok {
		t = types.Unalias(ptr.Elem())
	}
	if named, ok := t.(*types.Named); ok {
		return named.Obj().Name() + "." + fn.Name()
	}
	return ""
}

func emitGraphCatalog(in GraphGenInput, m *graphModel) error {
	b, err := json.MarshalIndent(graphCatalog(in, m), "", "  ")
	if err != nil {
		return err
	}
	return writeForgeOwned(in.ProjectDir, servicecatalog.GeneratedPath, append(b, '\n'), in.Checksums)
}
