package main

import (
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// testFunc is one top-level test function found in a package's _test.go
// files.
type testFunc struct {
	name string
	file string // base name
	// e2eFile: the file is built only under the e2e tag — its build
	// constraint names e2e un-negated (`e2e`, `e2e && linux`), so a plain
	// `go test` never compiles it.
	e2eFile bool
}

// scanTests returns every test function `go test` would discover in dir's
// _test.go files, by parsing them — not by matching text: forge's e2e tests
// embed whole Go test files for the projects they scaffold in string
// literals, and a line-anchored grep reads those as tests of this package.
func scanTests(dir string) ([]testFunc, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no _test.go files in %s", dir)
	}
	sort.Strings(files)
	fset := token.NewFileSet()
	var out []testFunc
	for _, path := range files {
		f, err := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		e2e, err := e2eTagged(f)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv != nil || !isTestFunc(fd) {
				continue
			}
			out = append(out, testFunc{name: fd.Name.Name, file: filepath.Base(path), e2eFile: e2e})
		}
	}
	return out, nil
}

// e2eTagged reports whether f's //go:build line names the e2e tag outside a
// negation.
func e2eTagged(f *ast.File) (bool, error) {
	for _, cg := range f.Comments {
		if cg.Pos() > f.Package {
			break
		}
		for _, c := range cg.List {
			if !constraint.IsGoBuild(c.Text) {
				continue
			}
			expr, err := constraint.Parse(c.Text)
			if err != nil {
				return false, err
			}
			return mentionsPositively(expr, "e2e", false), nil
		}
	}
	return false, nil
}

func mentionsPositively(e constraint.Expr, tag string, negated bool) bool {
	switch x := e.(type) {
	case *constraint.TagExpr:
		return x.Tag == tag && !negated
	case *constraint.NotExpr:
		return mentionsPositively(x.X, tag, !negated)
	case *constraint.AndExpr:
		return mentionsPositively(x.X, tag, negated) || mentionsPositively(x.Y, tag, negated)
	case *constraint.OrExpr:
		return mentionsPositively(x.X, tag, negated) || mentionsPositively(x.Y, tag, negated)
	}
	return false
}

// isTestFunc applies `go test`'s own rule: named Test, or Test followed by a
// character that is not a lower-case letter, taking exactly one *testing.T.
// TestMain takes *testing.M and is not a test.
func isTestFunc(fd *ast.FuncDecl) bool {
	name := fd.Name.Name
	if !strings.HasPrefix(name, "Test") {
		return false
	}
	if rest := name[len("Test"):]; rest != "" {
		r, _ := utf8.DecodeRuneInString(rest)
		if unicode.IsLower(r) {
			return false
		}
	}
	params := fd.Type.Params.List
	if len(params) != 1 || len(params[0].Names) > 1 {
		return false
	}
	star, ok := params[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "T" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "testing"
}
