package codegen

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// CommandRPCServices follows a legacy command's typed mount expressions. Graph
// projects use GraphRoot.RPCServices instead. A mount leaf is resolved from the
// SAME inventory rows that emit Mount<Svc>, including collision renames; no
// proto-package, command-name or workload-name heuristic assigns ownership.
// Unknown/external mounts are left unclaimed rather than widening a process.
func CommandRPCServices(projectDir, module, binary string, args []string, services []ServiceDef) ([]string, error) {
	// Legacy inventory proves top-level commands only. Nested command paths
	// require a graph root; matching just a terminal name would conflate roles.
	if len(args) != 1 {
		return nil, nil
	}
	names, err := DiscoverInternalPackageNames(projectDir)
	if err != nil {
		return nil, err
	}
	packages, err := PackageDataFromNames(names, projectDir)
	if err != nil {
		return nil, err
	}
	workers, err := WorkerDataFromSpecs(DiscoverWorkerSpecs(projectDir), projectDir)
	if err != nil {
		return nil, err
	}
	operators, err := OperatorDataFromSpecs(DiscoverOperatorSpecs(projectDir), projectDir)
	if err != nil {
		return nil, err
	}
	rows, _, err := inventoryRows(InventoryGenInput{GenContext: GenContext{ProjectDir: projectDir, ModulePath: module}, Services: services,
		Packages: packages, Workers: workers, Operators: operators})
	if err != nil {
		return nil, err
	}
	leaves := map[string]string{}
	for _, row := range rows {
		for _, s := range services {
			if row.ProtoServiceName == s.Name {
				leaves["Mount"+row.FieldName] = s.Package + "." + s.Name
			}
		}
	}
	index, roots, err := readMountDecls(projectDir, module, binary, args[0])
	if err != nil {
		return nil, err
	}
	if len(roots) != 1 {
		return nil, nil
	}
	found := map[string]bool{}
	walkMountDecls(roots[0], module, index, leaves, map[string]bool{}, found)
	var out []string
	for s := range found {
		out = append(out, s)
	}
	sort.Strings(out)
	return out, nil
}

type mountDecl struct {
	fn      *ast.FuncDecl
	imports map[string]string
	pkg     string
}

func readMountDecls(projectDir, module, binary, command string) (map[string][]mountDecl, []string, error) {
	index := map[string][]mountDecl{}
	var roots []string
	for _, rel := range []string{filepath.Join("cmd", binary, "cmd"), filepath.Join("internal", "app")} {
		entries, err := os.ReadDir(filepath.Join(projectDir, rel))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		pkg := module + "/" + filepath.ToSlash(rel)
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(projectDir, rel, e.Name()), nil, 0)
			if err != nil {
				return nil, nil, err
			}
			imports := map[string]string{}
			for _, imp := range f.Imports {
				path, _ := strconv.Unquote(imp.Path.Value)
				alias := filepath.Base(path)
				if imp.Name != nil {
					alias = imp.Name.Name
				}
				imports[alias] = path
			}
			for _, d := range f.Decls {
				fn, ok := d.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				key := pkg + "." + fn.Name.Name
				// Only the terminal command's constructor is a starting point. The
				// shared Serve pipeline is reached through calls but doesn't enumerate
				// mounts; group helpers and mount method expressions do.
				if rel == filepath.Join("cmd", binary, "cmd") {
					ast.Inspect(fn.Body, func(n ast.Node) bool {
						kv, ok := n.(*ast.KeyValueExpr)
						if !ok {
							return true
						}
						k, ok := kv.Key.(*ast.Ident)
						if !ok || k.Name != "Use" {
							return true
						}
						s, ok := kv.Value.(*ast.BasicLit)
						if !ok || s.Kind != token.STRING {
							return true
						}
						use, _ := strconv.Unquote(s.Value)
						words := strings.Fields(use)
						if len(words) > 0 && words[0] == command {
							roots = append(roots, key)
						}
						return true
					})
				}
				index[key] = append(index[key], mountDecl{fn: fn, imports: imports, pkg: pkg})
			}
		}
	}
	return index, roots, nil
}

func walkMountDecls(key, module string, index map[string][]mountDecl, leaves map[string]string, seen, found map[string]bool) {
	walk := func(key string) { walkMountDecls(key, module, index, leaves, seen, found) }
	if seen[key] {
		return
	}
	seen[key] = true
	decls := index[key]
	for _, d := range decls {
		if strings.HasPrefix(key, module+"/internal/app.") {
			if rpc := leaves[d.fn.Name.Name]; rpc != "" {
				found[rpc] = true
				return
			}
		}
		ast.Inspect(d.fn.Body, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CallExpr:
				if id, ok := x.Fun.(*ast.Ident); ok {
					walk(d.pkg + "." + id.Name)
				}
			case *ast.SelectorExpr:
				if id, ok := x.X.(*ast.Ident); ok {
					if pkg := d.imports[id.Name]; pkg != "" {
						walk(pkg + "." + x.Sel.Name)
					} else {
						walk(d.pkg + "." + x.Sel.Name)
					}
				} else {
					// (*app.Components).MountX: inspect the receiver's imported type.
					ast.Inspect(x.X, func(n ast.Node) bool {
						// Within app, grouped mounts use (*Components).MountX
						// without an imported selector (CP deployment groups).
						if id, ok := n.(*ast.Ident); ok && id.Name == "Components" && d.pkg == module+"/internal/app" {
							walk(d.pkg + "." + x.Sel.Name)
						}
						if typ, ok := n.(*ast.SelectorExpr); ok {
							if id, ok := typ.X.(*ast.Ident); ok && d.imports[id.Name] == module+"/internal/app" {
								walk(module + "/internal/app." + x.Sel.Name)
							}
						}
						return true
					})
				}
			}
			return true
		})
	}
}
