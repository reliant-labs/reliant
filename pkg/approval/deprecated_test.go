package approval

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEveryExportedIdentifierIsDeprecated pins the package's freeze: every
// exported declaration, every exported method (on a concrete type or in an
// interface) carries its own Deprecated paragraph, so a caller holding an
// [Approvals] value, not just one naming the type, sees the deprecation.
func TestEveryExportedIdentifierIsDeprecated(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, src, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		check := func(pos token.Pos, ident string, docs ...*ast.CommentGroup) {
			for _, d := range docs {
				if isDeprecated(d) {
					return
				}
			}
			t.Errorf("%s: %s has no Deprecated paragraph", fset.Position(pos), ident)
		}
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if !d.Name.IsExported() {
					continue
				}
				ident := d.Name.Name
				if d.Recv != nil {
					recv := receiverName(d.Recv.List[0].Type)
					if !ast.IsExported(recv) {
						continue
					}
					ident = recv + "." + ident
				}
				check(d.Pos(), ident, d.Doc)
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						if !s.Name.IsExported() {
							continue
						}
						check(s.Pos(), s.Name.Name, s.Doc, d.Doc)
						if it, ok := s.Type.(*ast.InterfaceType); ok {
							for _, m := range it.Methods.List {
								for _, n := range m.Names {
									if n.IsExported() {
										check(m.Pos(), s.Name.Name+"."+n.Name, m.Doc)
									}
								}
							}
						}
					case *ast.ValueSpec:
						for _, n := range s.Names {
							if n.IsExported() {
								// A grouped spec carries its own paragraph: the
								// group's doc is not shown per identifier.
								docs := []*ast.CommentGroup{s.Doc}
								if d.Lparen == token.NoPos {
									docs = append(docs, d.Doc)
								}
								check(n.Pos(), n.Name, docs...)
							}
						}
					}
				}
			}
		}
	}
}

func isDeprecated(cg *ast.CommentGroup) bool {
	if cg == nil {
		return false
	}
	text := cg.Text()
	return strings.HasPrefix(text, "Deprecated: ") || strings.Contains(text, "\n\nDeprecated: ")
}

func receiverName(expr ast.Expr) string {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if id, ok := expr.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}
