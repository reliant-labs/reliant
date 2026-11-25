package codegen

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"testing"
)

// A connect-go v2 project registers its clients with servicecatalog.
// NewClientV2, whose binding and constructor sit where NewClient's do: the
// catalog must recognise it, and still nothing else of that package.
func TestIsCatalogNewClientRecognisesNewClientV2(t *testing.T) {
	fset := token.NewFileSet()
	parse := func(name, src string) *ast.File {
		t.Helper()
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	marker, err := new(types.Config).Check("github.com/reliant-labs/forge/pkg/servicecatalog", fset, []*ast.File{parse("marker.go", `package servicecatalog
type ClientBinding struct{ Service, ConfigField string }
func NewClient[T any](b ClientBinding, ctor func() T) T { return ctor() }
func NewClientV2[T any](b ClientBinding, ctor func() T, base string) T { return ctor() }
func NewOther[T any](b ClientBinding, ctor func() T) T { return ctor() }
`)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	file := parse("providers.go", `package app
import c "github.com/reliant-labs/forge/pkg/servicecatalog"
func ctor() int { return 0 }
var b c.ClientBinding
var v1 = c.NewClient(b, ctor)
var v2 = c.NewClientV2(b, ctor, "http://x")
var other = c.NewOther(b, ctor)
`)
	info := &types.Info{Uses: map[*ast.Ident]types.Object{}}
	if _, err := (&types.Config{Importer: importerFunc(func(string) (*types.Package, error) { return marker, nil })}).Check("example.com/app", fset, []*ast.File{file}, info); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok || len(vs.Values) != 1 {
			return true
		}
		if call, ok := vs.Values[0].(*ast.CallExpr); ok {
			got[vs.Names[0].Name] = isCatalogNewClient(info, call)
		}
		return true
	})
	want := map[string]bool{"v1": true, "v2": true, "other": false}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("isCatalogNewClient(%s) = %v, want %v", name, got[name], w)
		}
	}
}
