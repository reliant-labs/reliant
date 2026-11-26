package architecture

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ─── nothing asserts the concrete repository type ──────────────────────────
//
// *db.Repo is being retired, and the composition root hands consumers a
// wrapper around it (rowreads.Repo) wherever a role still names both the
// facade's other methods and a read that has left it. That is safe because a
// consumer sees only an interface, and an optional-role assertion
// (x.(Iface)) answers by method set alone (rowreads' method-set test pins
// that the wrapper loses none). It stops being safe the day production code
// asserts the CONCRETE type — x.(*db.Repo) — because the wrapper is not one.
//
// This guard fails any such assertion, or a type-switch case naming the type,
// in non-test code, so the wrapper stays interchangeable.

func TestNoConcreteRepoTypeAssertions(t *testing.T) {
	t.Parallel()

	repo := root(t)
	skipDirs := map[string]bool{
		".git": true, "node_modules": true, "testdata": true, "vendor": true,
		"tmp": true, "data": true, "dist": true, ".reliant": true,
		"gen": true, "web": true, "electron": true, "proto-vendor": true,
		".forge": true,
	}
	isRepoType := func(e ast.Expr) bool {
		if star, ok := e.(*ast.StarExpr); ok {
			e = star.X
		}
		switch x := e.(type) {
		case *ast.SelectorExpr:
			pkg, ok := x.X.(*ast.Ident)
			return ok && ((pkg.Name == "db" && x.Sel.Name == "Repo") || (pkg.Name == "rowreads" && x.Sel.Name == "Repo"))
		case *ast.Ident:
			return x.Name == "Repo"
		}
		return false
	}
	fset := token.NewFileSet()
	err := filepath.WalkDir(repo, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != repo && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// Both shapes the guard fails, x.(T) and switch x.(type), spell ".(",
		// and the type they name spells "Repo". A file with either missing has
		// nothing to find, so it is not parsed: parsing every Go file in the
		// tree was most of this test's time.
		if !bytes.Contains(src, []byte(".(")) || !bytes.Contains(src, []byte("Repo")) {
			return nil
		}
		file, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(repo, path)
		// Inside package db and rowreads the bare name Repo is the type itself,
		// and a value of it is asserted only by that package's own code.
		bare := !strings.HasPrefix(rel, "internal/db/") && !strings.HasPrefix(rel, "internal/rowreads/")
		ast.Inspect(file, func(n ast.Node) bool {
			ta, ok := n.(*ast.TypeAssertExpr)
			if !ok || ta.Type == nil {
				return true
			}
			if id, ok := ta.Type.(*ast.Ident); ok && id.Name == "Repo" && !bare {
				return true
			}
			if isRepoType(ta.Type) {
				t.Errorf("%s:%d asserts the concrete repository type. The composition root hands consumers rowreads.Repo, not *db.Repo, so the assertion would silently fail; assert the role you need.",
					rel, fset.Position(ta.Pos()).Line)
			}
			return true
		})
		ast.Inspect(file, func(n ast.Node) bool {
			cc, ok := n.(*ast.CaseClause)
			if !ok {
				return true
			}
			for _, e := range cc.List {
				if sel, ok := e.(*ast.StarExpr); ok && isRepoType(sel) {
					t.Errorf("%s:%d switches on the concrete repository type; switch on a role instead.", rel, fset.Position(cc.Pos()).Line)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
