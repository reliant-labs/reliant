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
