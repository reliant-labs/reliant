// File: internal/codegen/composition_graph_mixed.go
//
// Adopting the composition graph one root at a time (forge#2285).
//
// A project on the legacy Infra / NewComponents composition used to be all or
// nothing: any `type Infra struct` or `func NewComponents` turned graph mode
// off for the whole project, and `forge fix composition-graph` converts
// everything at once. The largest projects cannot convert in one step.
//
// A root marked `adopt` (`//forge:root public command=public-api adopt`) is
// the explicit, per-root opt-in. In such a project (CompositionMixed):
//
//   - the legacy composition is generated exactly as before (compose.go,
//     lifecycle.go, mounts_services_gen.go, the per-component commands);
//   - beside it, the graph is generated for the adopted roots ONLY: their
//     constructor methods in MixedComposeFile (owned, reconciled like
//     compose.go), graph_gen.go and routes_gen.go holding exactly what those
//     roots reach (outOfMixedReach), and roots_gen.go's command per root once
//     the command tree declares runRoot;
//   - no default Server root is generated: the legacy server is the server;
//   - a root may reach nothing that builds the legacy composition
//     (validateMixedReach), so no command constructs both.
//
// Not yet projected for adopted roots: deploy/kcl's ROOTS and
// pkg/config/roots_gen.go (a workload keeps the legacy env until the
// project is in graph mode). The last step is `forge fix composition-graph`.

package codegen

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strings"
)

// adoptedRoots is the roots marked adopt.
func adoptedRoots(roots []graphRoot) []graphRoot {
	var out []graphRoot
	for _, r := range roots {
		if r.Adopt {
			out = append(out, r)
		}
	}
	return out
}

// outOfMixedReach is the reachScope beside a legacy composition: every node
// the adopted roots do not reach is left out — graph_gen.go has no accessor
// for it and MixedComposeFile no constructor — and a problem on a node they
// do reach fails generate, as it does in graph mode.
func (m *graphModel) outOfMixedReach(roots []graphRoot, draft string, problems []graphProblem) (map[string]unbuiltNode, error) {
	edges, lazy := graphComposeEdgesLazy(draft, m.nodes)
	// graphRootViews resolves each root's fields in place; this is a draft.
	copied := make([]graphRoot, len(roots))
	for i, r := range roots {
		r.Fields = append([]graphRootField(nil), r.Fields...)
		copied[i] = r
	}
	views, err := graphRootViews(copied, m.nodes, m.u, edges, lazy)
	if err != nil {
		return nil, err
	}
	reached := map[string]bool{}
	for _, v := range views {
		for _, n := range v.Reach {
			reached[n] = true
		}
	}
	var inReach []graphProblem
	unbuilt := map[string]unbuiltNode{}
	for _, p := range problems {
		if reached[p.Node] {
			inReach = append(inReach, p)
			continue
		}
		u := unbuilt[p.Node]
		u.Problems = append(u.Problems, p)
		unbuilt[p.Node] = u
	}
	if len(inReach) > 0 {
		return nil, graphProblemsError(inReach)
	}
	for _, n := range m.nodes {
		if !reached[n.Name] {
			if _, ok := unbuilt[n.Name]; !ok {
				unbuilt[n.Name] = unbuiltNode{}
			}
		}
	}
	return unbuilt, nil
}

// legacyCompositionNames are the declarations of the legacy composition a
// node an adopted root reaches must not build on.
var legacyCompositionNames = map[string]bool{"Infra": true, "OpenInfra": true, "NewComponents": true, "Components": true}

// validateMixedReach refuses an adopted root that reaches a node whose
// construction — its constructor method, or its provider func, and
// everything either calls — builds on the legacy composition: that root's
// process would build the legacy composition too, and with it every resource
// it opens, which is exactly what adopting a root one at a time must not do.
//
// The check is the construction's whole call closure inside internal/app
// (legacyReach), not the one body: a provider that calls a helper that opens
// Infra, or calls NewComponents through a package-level alias, builds it just
// the same. Code outside internal/app cannot reach it: a package internal/app
// imports cannot import internal/app back.
func (m *graphModel) validateMixedReach(projectDir string, providers []graphProvider) error {
	methods := map[string]*ast.FuncDecl{}
	fset := token.NewFileSet()
	if file, err := parser.ParseFile(fset, MixedComposeFile, m.compose, parser.SkipObjectResolution); err == nil {
		methods = graphComposeMethods(file)
	}
	providerFile := map[string]string{}
	for _, p := range providers {
		providerFile[p.Name] = p.File
	}
	var reach *legacyReach
	if m.u != nil && m.u.app != nil && m.u.app.Types != nil && m.u.app.TypesInfo != nil {
		reach = newLegacyReach(m.u.app.Fset, m.u.app.Syntax, m.u.app.Types, m.u.app.TypesInfo, methods)
	} else {
		// Untyped (the load failed): each body alone, by name.
		reach = newLegacyReach(nil, nil, nil, nil, methods)
	}
	parsed := map[string]*ast.File{}
	providerFunc := func(name string) *ast.FuncDecl {
		rel := providerFile[name]
		if rel == "" {
			return nil
		}
		f, ok := parsed[rel]
		if !ok {
			f, _ = parser.ParseFile(token.NewFileSet(), filepath.Join(projectDir, filepath.FromSlash(rel)), nil, parser.SkipObjectResolution)
			parsed[rel] = f
		}
		if f == nil {
			return nil
		}
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Name.Name == "New"+name {
				return fd
			}
		}
		return nil
	}
	var bad []string
	for _, v := range m.rootViews {
		for _, name := range v.Reach {
			where := ""
			if path := reach.method("new" + name); path != "" {
				where = fmt.Sprintf("its constructor (%s's new%s) builds on %s", MixedComposeFile, name, path)
			} else if providerFile[name] != "" {
				if path := reach.provider("New" + name); path != "" {
					where = fmt.Sprintf("its provider (New%s, %s) builds on %s", name, providerFile[name], path)
				} else if reach.pkg == nil {
					if fd := providerFunc(name); fd != nil && namesLegacyComposition(fd) != "" {
						where = fmt.Sprintf("its provider (New%s, %s) builds on %s", name, providerFile[name], namesLegacyComposition(fd))
					}
				}
			}
			if where != "" {
				bad = append(bad, fmt.Sprintf("root %s reaches %s, and %s", v.Name, name, where))
			}
		}
	}
	if len(bad) == 0 {
		return nil
	}
	sort.Strings(bad)
	return fmt.Errorf("an adopted root may not build the legacy composition, or its process would open everything Infra and NewComponents open:\n  - %s\n"+
		"Give the node a provider of its own that builds what it needs from its Deps (`func New<X>(<X>Deps) (T, error)`), the way the graph builds everything else",
		strings.Join(bad, "\n  - "))
}

// legacyReach answers whether a construction in internal/app reaches the
// legacy composition through anything it calls or names: a package-level
// func or method (followed into its body), a package-level var (followed
// into its initializer, so `var mk = NewComponents` is NewComponents), or a
// type alias of a legacy type. Graph methods in MixedComposeFile are read
// from the reconciled draft, the rest of the package from its typed syntax.
// Generated files (*_gen.go) are not followed: their accessors call the
// constructors of other nodes, each of which is checked as a node of its own.
type legacyReach struct {
	pkg     *types.Package
	info    *types.Info
	methods map[string]*ast.FuncDecl // MixedComposeFile's Graph methods
	decls   map[types.Object]ast.Node
	legacy  map[types.Object]bool
	// memo is each followed declaration's path to the legacy composition
	// ("" none); an entry in progress is "", which also ends a cycle.
	memo map[any]string
}

func newLegacyReach(fset *token.FileSet, files []*ast.File, pkg *types.Package, info *types.Info, methods map[string]*ast.FuncDecl) *legacyReach {
	r := &legacyReach{pkg: pkg, info: info, methods: methods, decls: map[types.Object]ast.Node{}, legacy: map[types.Object]bool{}, memo: map[any]string{}}
	if pkg == nil || info == nil {
		return r
	}
	for name := range legacyCompositionNames {
		if obj := pkg.Scope().Lookup(name); obj != nil {
			r.legacy[obj] = true
		}
	}
	for _, f := range files {
		base := ""
		if fset != nil {
			base = filepath.Base(fset.Position(f.Pos()).Filename)
		}
		if base == MixedComposeFile || strings.HasSuffix(base, "_gen.go") {
			continue
		}
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if obj := info.Defs[d.Name]; obj != nil {
					r.decls[obj] = d
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					if vs, ok := spec.(*ast.ValueSpec); ok {
						for _, id := range vs.Names {
							if obj := info.Defs[id]; obj != nil {
								r.decls[obj] = vs
							}
						}
					}
				}
			}
		}
	}
	return r
}

// method is the path from MixedComposeFile's Graph method name to the
// legacy composition, "" when it reaches none (or there is no such method).
func (r *legacyReach) method(name string) string {
	fd := r.methods[name]
	if fd == nil {
		return ""
	}
	key := "method:" + name
	if path, seen := r.memo[key]; seen {
		return path
	}
	r.memo[key] = ""
	path := namesLegacyComposition(fd)
	if path == "" && r.pkg != nil {
		path = r.untypedCalls(fd)
	}
	r.memo[key] = path
	return path
}

// untypedCalls follows what a draft Graph method (not type-checked: the
// draft is not on disk) calls: a package-level declaration it names, or
// another Graph method through its receiver.
func (r *legacyReach) untypedCalls(fd *ast.FuncDecl) string {
	recv := ""
	if fd.Recv != nil && len(fd.Recv.List) == 1 && len(fd.Recv.List[0].Names) == 1 {
		recv = fd.Recv.List[0].Names[0].Name
	}
	sel := map[*ast.Ident]bool{}
	found := ""
	ast.Inspect(fd, func(n ast.Node) bool {
		if found != "" {
			return false
		}
		switch x := n.(type) {
		case *ast.SelectorExpr:
			sel[x.Sel] = true
			if id, ok := x.X.(*ast.Ident); ok && recv != "" && id.Name == recv {
				if r.methods[x.Sel.Name] != nil {
					if path := r.method(x.Sel.Name); path != "" {
						found = "g." + x.Sel.Name + " → " + path
					}
				} else if m := r.graphMethod(x.Sel.Name); m != nil {
					found = r.object(m)
				}
			}
		case *ast.CompositeLit:
			for _, el := range x.Elts {
				if kv, ok := el.(*ast.KeyValueExpr); ok {
					if id, ok := kv.Key.(*ast.Ident); ok {
						sel[id] = true
					}
				}
			}
		case *ast.Ident:
			if !sel[x] {
				if obj := r.pkg.Scope().Lookup(x.Name); obj != nil {
					found = r.object(obj)
				}
			}
		}
		return found == ""
	})
	return found
}

// graphMethod is the Graph method name declared outside the draft.
func (r *legacyReach) graphMethod(name string) types.Object {
	tn, ok := r.pkg.Scope().Lookup("Graph").(*types.TypeName)
	if !ok {
		return nil
	}
	obj, _, _ := types.LookupFieldOrMethod(types.NewPointer(tn.Type()), true, r.pkg, name)
	if fn, ok := obj.(*types.Func); ok {
		return fn
	}
	return nil
}

// provider is the path from the package-level func name to the legacy
// composition, "" when it reaches none.
func (r *legacyReach) provider(name string) string {
	if r.pkg == nil {
		return ""
	}
	obj := r.pkg.Scope().Lookup(name)
	if obj == nil {
		return ""
	}
	return strings.TrimPrefix(r.object(obj), name+" → ")
}

// object is the path from obj to the legacy composition: obj itself when it
// is a legacy declaration or an alias of one, else through its declaration.
func (r *legacyReach) object(obj types.Object) string {
	if obj == nil || obj.Pkg() != r.pkg {
		return ""
	}
	if r.legacy[obj] {
		return obj.Name()
	}
	if tn, ok := obj.(*types.TypeName); ok {
		if named, ok := types.Unalias(tn.Type()).(*types.Named); ok && r.legacy[named.Obj()] {
			return obj.Name() + " → " + named.Obj().Name()
		}
		return ""
	}
	if fn, ok := obj.(*types.Func); ok && r.methods[fn.Name()] != nil {
		if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil && isGraphRecv(sig.Recv().Type()) {
			if path := r.method(fn.Name()); path != "" {
				return "g." + fn.Name() + " → " + path
			}
			return ""
		}
	}
	decl := r.decls[obj]
	if decl == nil {
		return ""
	}
	if path, seen := r.memo[obj]; seen {
		return path
	}
	r.memo[obj] = ""
	found := ""
	ast.Inspect(decl, func(n ast.Node) bool {
		if found != "" {
			return false
		}
		if id, ok := n.(*ast.Ident); ok {
			if use := r.info.Uses[id]; use != nil && use != obj {
				found = r.object(use)
			}
		}
		return found == ""
	})
	if found != "" {
		label := obj.Name()
		if sig, ok := obj.Type().(*types.Signature); ok && sig.Recv() != nil && isGraphRecv(sig.Recv().Type()) {
			label = "g." + label
		}
		found = label + " → " + found
	}
	r.memo[obj] = found
	return found
}

// isGraphRecv is whether t is Graph or *Graph.
func isGraphRecv(t types.Type) bool {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	named, ok := types.Unalias(t).(*types.Named)
	return ok && named.Obj().Name() == "Graph"
}

// namesLegacyComposition is the first legacy composition declaration fd
// refers to by name (not as a field or method of something else, nor a
// composite literal's key), "" when none.
func namesLegacyComposition(fd *ast.FuncDecl) string {
	sel := map[*ast.Ident]bool{}
	ast.Inspect(fd, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			sel[x.Sel] = true
		case *ast.CompositeLit:
			for _, el := range x.Elts {
				if kv, ok := el.(*ast.KeyValueExpr); ok {
					if id, ok := kv.Key.(*ast.Ident); ok {
						sel[id] = true
					}
				}
			}
		}
		return true
	})
	found := ""
	ast.Inspect(fd, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && !sel[id] && legacyCompositionNames[id.Name] && found == "" {
			found = id.Name
		}
		return found == ""
	})
	return found
}
