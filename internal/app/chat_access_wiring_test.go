package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestChatAccessIsWired holds the composition root to the owner-and-org chat
// check: every chat and provider_account Deps it builds names Chats, and
// never as nil. chat.New refuses a nil Chats too (validateDeps), but chat's
// own tests fill one in for fixtures that leave it out, so this is the check
// that a production root cannot.
func TestChatAccessIsWired(t *testing.T) {
	want := map[string]int{"chat": 0, "provider_account": 0}
	fset := token.NewFileSet()
	for _, file := range []string{"compose.go", "providers_worker.go"} {
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			sel, ok := lit.Type.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Deps" {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if _, tracked := want[pkg.Name]; !tracked {
				return true
			}
			want[pkg.Name]++
			pos := fset.Position(lit.Pos())
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "Chats" {
					if v, ok := kv.Value.(*ast.Ident); ok && v.Name == "nil" {
						t.Errorf("%s: %s.Deps sets Chats to nil", pos, pkg.Name)
					}
					return true
				}
			}
			t.Errorf("%s: %s.Deps does not wire Chats (chataccess): the handler's owner and org check would be missing", pos, pkg.Name)
			return true
		})
	}
	for pkg, n := range want {
		if n == 0 {
			t.Errorf("found no %s.Deps in compose.go or providers_worker.go: this test no longer sees the wiring it guards", pkg)
		}
	}
}
