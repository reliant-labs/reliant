package codegen

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/reliant-labs/forge/internal/templates"
)

// A component's package is imported into forge's generated internal/app
// files — compose.go constructs it, lifecycle.go supervises it — under an
// import name, and by default that name is the package's own:
// `orders.New(orders.Deps{…})`. An import name lives in the file scope beside
// everything else the file names: its other imports (fmt, slog, time, …),
// NewComponents' locals (c, infra, err), the builtins it calls, and every
// package-level name of package app. So a service called `c` rendered
// `c.New(c.Deps{…})` right after `c := &Components{}`, and `fmt`, `err`,
// `infra`, `slog` broke the same way: the very first `forge generate` of
// `forge project new x --service c` failed to compile.
//
// The rule: a component keeps its package name as its import name unless the
// file already uses that name for something else; then it is imported as
// `<role><Package>` (`svcC`, `wkrErr`) — the role-prefixed form forge already
// gives two components that share a package name. It holds an uppercase
// letter, so it can never equal a Go package name, a builtin, or any
// lowercase local. Only a colliding component is renamed, so no existing
// project's compose.go changes: every alias that compiled before is kept.
//
// "Already uses" is not a list forge maintains. It is read off the templates
// themselves — appTemplateIdentifiers renders each with placeholder
// components through every branch and collects what the output names — plus
// the lowercase package-level names declared in the project's own
// internal/app files (providers.go, auth.go: yours), which share package app's
// scope. A local added to a template is covered the moment it is written.

// appTemplateIdentifiers is every identifier compose.go.tmpl and
// lifecycle.go.tmpl write for something other than a component's package:
// import names, locals, params, receivers, the builtins they reference.
// Selector field names (`.New`, `.Log`) and composite-literal keys are not
// identifiers in the file scope and are left out.
var appTemplateIdentifiers = sync.OnceValue(func() map[string]bool {
	taken := map[string]bool{}
	const module = "example.com/forgeidents"
	placeholder := func(role string) (string, string) {
		return "zzforgeplaceholder" + role, "internal/" + role + "/zzforgeplaceholder"
	}
	svcAlias, svcPath := placeholder("handlers")
	wkrAlias, wkrPath := placeholder("workers")
	opAlias, opPath := placeholder("operators")
	assignments := []InjectAssignment{
		{Field: "Logger", Expr: "infra.Log", Comment: "c"},
		{Field: "Config", Expr: "infra.Cfg"},
		{Field: "Clock", Expr: frameworkClockExpr},
		{Field: "IDGen", Expr: frameworkIDGenExpr},
		{Field: "Store", Expr: "db.NewStore(infra.DB)"},
		{Field: "Other", Expr: "c.Other"},
	}
	compose := InjectGenData{
		Module:            module,
		NeedsConfigImport: true,
		NeedsFmt:          true,
		NeedsTime:         true,
		NeedsULID:         true,
		NeedsDBStore:      true,
		DBStoreImport:     module + "/internal/db",
		Fields: []composeField{
			{FieldName: "A", Alias: svcAlias, FieldType: "*" + svcAlias + ".Service"},
			{FieldName: "B", Alias: wkrAlias, FieldType: wkrAlias + ".Service"},
		},
		Order: []InjectComponentData{
			{FieldName: "A", VarName: "a", LocalVar: injectVarName("a"), Alias: svcAlias, ImportPath: svcPath, Fallible: true, Assignments: assignments},
			{FieldName: "B", VarName: "b", LocalVar: injectVarName("b"), Alias: wkrAlias, ImportPath: wkrPath, Wrapped: true, MiddlewareCtor: "NewServiceWithForgeMiddleware", Assignments: assignments},
			{FieldName: "C", VarName: "cc", LocalVar: injectVarName("cc"), Alias: svcAlias, ImportPath: svcPath, Fallible: true, Wrapped: true, MiddlewareCtor: "NewServiceWithForgeMiddleware", Assignments: assignments},
		},
		HasCycle:   true,
		CycleEdges: []BuildEdge{{Consumer: "A", Field: "B", Type: "x", Producer: "B"}},
	}
	lifecycle := lifecycleData{
		Module:           module,
		LeaderElectionID: "x",
		Workers:          []lifeComp{{Name: "w", FieldName: "W", Alias: wkrAlias, ImportPath: wkrPath, FieldType: wkrAlias + ".Service"}},
		Operators:        []lifeComp{{Name: "o", FieldName: "O", Alias: opAlias, ImportPath: opPath, FieldType: "*" + opAlias + ".Controller"}},
	}
	for name, data := range map[string]any{"compose.go.tmpl": compose, "lifecycle.go.tmpl": lifecycle} {
		src, err := templates.ProjectTemplates().Render(name, data)
		if err != nil {
			// A template that does not render with this data fails the real
			// render too, loudly; nothing to protect here.
			continue
		}
		for ident := range fileScopeIdentifiers(string(src)) {
			if !strings.HasPrefix(ident, "zzforgeplaceholder") {
				taken[ident] = true
			}
		}
	}
	return taken
})

// fileScopeIdentifiers returns every name a Go source file binds or resolves
// in its own scopes: import names (explicit, or the path's last element),
// declared and referenced identifiers. Selector field names and
// composite-literal keys are excluded — `x.Log` and `Deps{Log: …}` name a
// field, which no import can shadow. Unparseable source yields what parsed.
func fileScopeIdentifiers(src string) map[string]bool {
	out := map[string]bool{}
	file, err := parser.ParseFile(token.NewFileSet(), "", src, parser.SkipObjectResolution)
	if file == nil {
		_ = err
		return out
	}
	for _, imp := range file.Imports {
		if imp.Name != nil {
			out[imp.Name.Name] = true
			continue
		}
		path := strings.Trim(imp.Path.Value, `"`)
		out[path[strings.LastIndex(path, "/")+1:]] = true
	}
	notInScope := map[*ast.Ident]bool{file.Name: true}
	ast.Inspect(file, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			notInScope[x.Sel] = true
		case *ast.KeyValueExpr:
			if id, ok := x.Key.(*ast.Ident); ok {
				notInScope[id] = true
			}
		case *ast.StructType:
			for _, f := range x.Fields.List {
				for _, name := range f.Names {
					notInScope[name] = true
				}
			}
		case *ast.ImportSpec:
			if x.Name != nil {
				notInScope[x.Name] = true
			}
		case *ast.Ident:
			if !notInScope[x] {
				out[x.Name] = true
			}
		}
		return true
	})
	return out
}

// appPackageNames returns the lowercase package-level names declared in the
// project's internal/app files other than compose.go and lifecycle.go — the
// owned providers.go, auth.go, and anything the author added. An import name
// in compose.go may not equal one of them (Go refuses a name declared in both
// the file and the package block).
func appPackageNames(projectDir string) map[string]bool {
	out := map[string]bool{}
	appDir := filepath.Join(projectDir, "internal", "app")
	entries, err := os.ReadDir(appDir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") ||
			name == "compose.go" || name == "lifecycle.go" {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(appDir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil {
					out[d.Name.Name] = true
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						out[s.Name.Name] = true
					case *ast.ValueSpec:
						for _, n := range s.Names {
							out[n.Name] = true
						}
					}
				}
			}
		}
	}
	return out
}

// appImportAlias is the import name a component is given in forge's
// internal/app files: its alias as resolved so far, unless that names
// something the files already use (taken), then `<role><Alias>`. Go itself
// refuses one more: a package may be named `init`, but no import may be
// ("cannot import package as init - init must be a func").
func appImportAlias(alias, role string, taken map[string]bool) string {
	if !taken[alias] && alias != "init" {
		return alias
	}
	return role + upperFirst(alias)
}

// graphTemplateIdentifiers is appTemplateIdentifiers for a project in graph
// mode: every identifier graph_gen.go, routes_gen.go and a compose.go
// constructor method write for something other than a component's package.
// Kept apart from the legacy set so a legacy project's aliases never change.
var graphTemplateIdentifiers = sync.OnceValue(func() map[string]bool {
	taken := map[string]bool{}
	const module = "example.com/forgeidents"
	placeholder := func(role string) (string, string) {
		return "zzforgeplaceholder" + role, "internal/" + role + "/zzforgeplaceholder"
	}
	svcAlias, svcPath := placeholder("handlers")
	wkrAlias, _ := placeholder("workers")
	opAlias, _ := placeholder("operators")
	svcRow := routesServiceView{InventoryServiceData: InventoryServiceData{Name: "a", FieldName: "A", ConnectPkg: "x", ProtoServiceName: "A", HasWebhooks: true}, Type: "*" + svcAlias + ".Service"}
	graphGen := graphGenView{
		Module:  module,
		Imports: []graphImport{{Alias: svcAlias, Path: module + "/" + svcPath}},
		Nodes: []graphNodeView{
			{Name: "A", LogName: "a", Type: "*" + svcAlias + ".Service", Doc: "a"},
			{Name: "B", LogName: "b", Type: "*" + svcAlias + ".Service", Doc: "b", HasLifecycle: true, NilGuard: true, NodeFields: "Run: c.Run"},
		},
		Roots:     []graphRootView{{Name: "server", TypeName: "Server", Generated: true, Fields: []graphRootFieldView{{Name: "B", Type: "*B", Accessor: "B"}}, ForceOrder: []graphRootFieldView{{Name: "B", Type: "*B", Accessor: "B"}}, Plan: "b"}},
		Operators: &graphOperatorsView{LeaderElectionID: "x", Ops: []graphOperatorView{{Field: "O", Type: "*" + opAlias + ".Controller", Alias: opAlias, Label: "o"}}},
	}
	routesGen := routesGenView{Module: module, Services: []routesServiceView{svcRow}, Audiences: []routesAudienceView{{Name: "UserRoutes", Audience: "user", Default: true, Services: []routesServiceView{svcRow}}}}
	wrapped := &graphNode{Name: "A", Doc: "a", Type: wkrAlias + ".Service", Ctor: wkrAlias + ".New", DepsType: wkrAlias + ".Deps", Fallible: true, Wrapped: true, MiddlewareCtor: "NewServiceWithForgeMiddleware", Comp: &BuildComponent{Alias: wkrAlias}}
	method := "package app\n" + renderGraphMethod(wrapped, []graphAssign{{Field: "Logger", Expr: "g.Log"}})
	for ident := range fileScopeIdentifiers(method) {
		if !strings.HasPrefix(ident, "zzforgeplaceholder") {
			taken[ident] = true
		}
	}
	for name, data := range map[string]any{"graph_gen.go.tmpl": graphGen, "routes_gen.go.tmpl": routesGen} {
		src, err := templates.ProjectTemplates().Render(name, data)
		if err != nil {
			continue
		}
		for ident := range fileScopeIdentifiers(string(src)) {
			if !strings.HasPrefix(ident, "zzforgeplaceholder") {
				taken[ident] = true
			}
		}
	}
	return taken
})
