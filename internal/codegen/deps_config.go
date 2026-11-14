package codegen

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// DepsConfig is the config a component's `Deps.Config` field takes, as the
// generated test harness and webhook tests need it: the package declaring
// the type, the type's name, and the package-level constructor of its
// defaults.
//
// The harness hands a component a config only through this field, so this is
// the one fact that decides whether the harness needs a config import at all,
// and which. It used to import `<module>/pkg/config` unconditionally: a
// project with no typed config (no proto/<project>/config/v1, so no
// pkg/config) got a helpers_gen_test.go that did not compile for every
// component, whether or not any of them took a config.
type DepsConfig struct {
	// ImportPath is the package that declares the field's type. Empty means
	// Deps takes no config the harness can build, and the harness then has
	// no config to default, no WithConfig option and no config import.
	ImportPath string
	// TypeName is the type the field points to: "Config" for the root
	// config, or a config block or per-process message ("TraderConfig",
	// "GcpCostConfig") for a component that takes its own block (the
	// composition graph's K3).
	TypeName string
	// DefaultsFunc is the package-level constructor returning that type's
	// defaults — "Defaults" for Config, "Defaults<TypeName>" for a block or
	// per-process message (both generated into pkg/config by
	// config.go.tmpl) — or "" when the package declares none, and the
	// harness can only build the zero &<TypeName>{}.
	DefaultsFunc string
}

// DetectDepsConfig reads the `Config` field of the Deps struct declared in
// dir and returns the config it takes.
//
// The field must be a pointer to a named type of another package:
//
//   - `Config *<pkg>.Config` is the root config, from any package — the
//     scaffolded shape. Its default is `<pkg>.Defaults()` when that package
//     declares it (ConfigPackageDeclaresDefaults), else `&<pkg>.Config{}`.
//   - `Config *<pkg>.<M>` is a component's own config block or a
//     per-process config message. It is recognised only when the project
//     package <pkg> proves <M> is something the harness can build: it
//     declares `func Defaults<M>() *<M>` (the generated pkg/config does, for
//     every block and per-process message it aliases), which is the default;
//     or it declares `type <M> struct` (the generated proto package itself,
//     or a hand-written config struct), whose zero `&<pkg>.<M>{}` is. A <M>
//     the package declares neither way — an interface, a named scalar, a
//     type outside the project — leaves the field to the component's own
//     tests, as before: the harness never names a value it cannot build.
//
// A value-typed field (`Config <pkg>.<M>`) is not recognised. A config is a
// proto message, which must not be copied (go vet's copylocks), and
// compose.go wires a block only as a pointer (configBlocks.resolve), so the
// harness leaves that field at its zero value rather than generate a copy.
func DetectDepsConfig(dir, projectDir, modulePath string) DepsConfig {
	importPath, typeName := depsConfigType(dir)
	if importPath == "" {
		return DepsConfig{}
	}
	if typeName == "Config" {
		dc := DepsConfig{ImportPath: importPath, TypeName: typeName}
		if ConfigPackageDeclaresDefaults(projectDir, modulePath, importPath) {
			dc.DefaultsFunc = "Defaults"
		}
		return dc
	}
	decls, ok := readProjectPackageDecls(projectDir, modulePath, importPath)
	if !ok {
		return DepsConfig{}
	}
	if fn := "Defaults" + typeName; decls.constructors[fn] == typeName {
		return DepsConfig{ImportPath: importPath, TypeName: typeName, DefaultsFunc: fn}
	}
	if decls.structs[typeName] {
		return DepsConfig{ImportPath: importPath, TypeName: typeName}
	}
	return DepsConfig{}
}

// depsConfigType returns the import path and type name of the Deps.Config
// field declared in dir when it is `*<pkg>.<Type>`, or "" for both.
func depsConfigType(dir string) (importPath, typeName string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", ""
	}
	fset := token.NewFileSet()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, perr := parser.ParseFile(fset, filepath.Join(dir, entry.Name()), nil, parser.SkipObjectResolution)
		if perr != nil {
			continue
		}
		for _, decl := range file.Decls {
			genDecl, ok := decl.(*ast.GenDecl)
			if !ok || genDecl.Tok != token.TYPE {
				continue
			}
			for _, spec := range genDecl.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok || typeSpec.Name.Name != "Deps" {
					continue
				}
				structType, ok := typeSpec.Type.(*ast.StructType)
				if !ok {
					continue
				}
				for _, field := range structType.Fields.List {
					if !fieldNamed(field, "Config") {
						continue
					}
					star, ok := field.Type.(*ast.StarExpr)
					if !ok {
						return "", ""
					}
					sel, ok := star.X.(*ast.SelectorExpr)
					if !ok {
						return "", ""
					}
					qual, ok := sel.X.(*ast.Ident)
					if !ok {
						return "", ""
					}
					path := fileImportMap(file)[qual.Name]
					if path == "" {
						return "", ""
					}
					return path, sel.Sel.Name
				}
			}
		}
	}
	return "", ""
}

// ConfigPackageDeclaresDefaults reports whether the project package at
// importPath declares a package-level `func Defaults()` returning one value —
// the generated pkg/config's constructor for the config Load yields with
// nothing set (config.go.tmpl). The test harness defaults a component's
// config to it when it exists. It is read from the source rather than assumed
// from the path: a CLI's stub pkg/config, a hand-written one, or a config
// package outside pkg/config declares Config without it, and a harness
// calling a function that does not exist would not compile.
//
// The config loader step runs before the harness step, so a generate that
// (re)writes pkg/config/config_gen.go is seen by the harness in the same run.
func ConfigPackageDeclaresDefaults(projectDir, modulePath, importPath string) bool {
	decls, ok := readProjectPackageDecls(projectDir, modulePath, importPath)
	return ok && decls.nullary["Defaults"]
}

// projectPackageDecls is what the harness needs to know a project package
// declares before it names one of its identifiers.
type projectPackageDecls struct {
	// nullary holds every package-level func taking no parameters and
	// returning one value.
	nullary map[string]bool
	// constructors maps each of those whose result is `*<T>`, T a local
	// type name, to T.
	constructors map[string]string
	// structs holds every type declared as a struct (not an alias).
	structs map[string]bool
}

// readProjectPackageDecls parses the non-test Go files of the project
// package at importPath. ok is false when importPath is outside the project
// (modulePath) or its directory cannot be read.
func readProjectPackageDecls(projectDir, modulePath, importPath string) (projectPackageDecls, bool) {
	out := projectPackageDecls{nullary: map[string]bool{}, constructors: map[string]string{}, structs: map[string]bool{}}
	rel, ok := strings.CutPrefix(importPath, modulePath+"/")
	if modulePath == "" || !ok {
		return out, false
	}
	dir := filepath.Join(projectDir, filepath.FromSlash(rel))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out, false
	}
	fset := token.NewFileSet()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, perr := parser.ParseFile(fset, filepath.Join(dir, entry.Name()), nil, parser.SkipObjectResolution)
		if perr != nil {
			continue
		}
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Recv != nil || d.Type.Params.NumFields() != 0 || d.Type.Results.NumFields() != 1 {
					continue
				}
				out.nullary[d.Name.Name] = true
				if star, ok := d.Type.Results.List[0].Type.(*ast.StarExpr); ok {
					if ident, ok := star.X.(*ast.Ident); ok {
						out.constructors[d.Name.Name] = ident.Name
					}
				}
			case *ast.GenDecl:
				if d.Tok != token.TYPE {
					continue
				}
				for _, spec := range d.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok || ts.Assign.IsValid() {
						continue
					}
					if _, ok := ts.Type.(*ast.StructType); ok {
						out.structs[ts.Name.Name] = true
					}
				}
			}
		}
	}
	return out, true
}
