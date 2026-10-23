package codegen

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// DetectDepsDBField checks whether the Deps struct in the given directory
// has a field of type orm.Context (indicating the service needs a database).
// It parses all non-test .go files and looks for a type Deps struct with a
// field whose type is orm.Context.
// Returns true if the Deps struct has a DB field, false otherwise.
func DetectDepsDBField(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}

	fset := token.NewFileSet()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		if strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
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
					// Check for orm.Context type (selector expression)
					if sel, ok := field.Type.(*ast.SelectorExpr); ok {
						if ident, ok := sel.X.(*ast.Ident); ok {
							if ident.Name == "orm" && sel.Sel.Name == "Context" {
								return true, nil
							}
						}
					}
				}
			}
		}
	}

	return false, nil
}

// DetectDepsConfigImport returns the import path of the package that declares
// the type of the component's `Deps.Config` field, when that field is a
// pointer to a type named Config from another package (`Config
// *config.Config`, usually the project's generated pkg/config). It returns ""
// when Deps has no such field.
//
// The test harness hands a component a config only through this field, so
// this is the one fact that decides whether the harness needs a config import
// at all, and which. It used to import `<module>/pkg/config` unconditionally:
// a project with no typed config (no proto/<project>/config/v1, so no pkg/config) got a
// helpers_gen_test.go that did not compile for every component, whether or
// not any of them took a config.
func DetectDepsConfigImport(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
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
						return ""
					}
					sel, ok := star.X.(*ast.SelectorExpr)
					if !ok || sel.Sel.Name != "Config" {
						return ""
					}
					qual, ok := sel.X.(*ast.Ident)
					if !ok {
						return ""
					}
					return fileImportMap(file)[qual.Name]
				}
			}
		}
	}
	return ""
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
	rel, ok := strings.CutPrefix(importPath, modulePath+"/")
	if modulePath == "" || !ok {
		return false
	}
	dir := filepath.Join(projectDir, filepath.FromSlash(rel))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
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
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Name.Name != "Defaults" {
				continue
			}
			if fn.Type.Params.NumFields() == 0 && fn.Type.Results.NumFields() == 1 {
				return true
			}
		}
	}
	return false
}

// fieldNamed reports whether a struct field declares name.
func fieldNamed(field *ast.Field, name string) bool {
	for _, n := range field.Names {
		if n.Name == name {
			return true
		}
	}
	return false
}

// DetectConstructorType returns the pretty-printed FIRST return type of the
// exported `func New(...)` in dir — the type the constructor produces. For a
// handler service whose New returns (*Service, error) it is "*Service"; for
// an internal package whose New returns Service (the contract interface) it
// is "Service". The qualifier prefix (the package selector) is added by the
// caller from the component's import alias.
//
// Returns "" when the directory has no parseable New or the result list is
// empty — the caller falls back to the bootstrap default (*Service).
//
// This is what makes the generated Services registry field type AND the
// inject_gen local-var assignment match the constructor exactly, regardless
// of whether the component exposes a concrete *Service struct (handlers) or
// a Service interface (internal packages) — closing the
// `*item.Service` vs `item.Service` assignability mismatch.
func DetectConstructorType(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
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
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || !IsComponentConstructor(fn) {
				continue
			}
			if fn.Type.Results == nil || len(fn.Type.Results.List) == 0 {
				return "", nil
			}
			return printType(fset, fn.Type.Results.List[0].Type), nil
		}
	}
	return "", nil
}

// DetectFallibleConstructor checks whether the exported New function in the
// given directory returns an error as its last result (i.e. returns (T, error)).
// It parses all non-test .go files and looks for a top-level func New(...).
// Returns true if the constructor is fallible, false otherwise.
// If the directory doesn't exist or contains no New function, returns false.
func DetectFallibleConstructor(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}

	fset := token.NewFileSet()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		if strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			continue // skip unparseable files
		}

		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || !IsComponentConstructor(fn) {
				continue
			}

			// Found func New(...) — check return types
			results := fn.Type.Results
			if results == nil || len(results.List) < 2 {
				return false, nil // single return or no return
			}

			// Check if last return type is "error"
			lastField := results.List[len(results.List)-1]
			if ident, ok := lastField.Type.(*ast.Ident); ok && ident.Name == "error" {
				return true, nil
			}
			return false, nil
		}
	}

	return false, nil
}
