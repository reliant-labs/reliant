package derivecontract

import (
	"go/ast"
	"go/types"
	"strconv"
	"strings"
)

// Signatures are compared by the type checker (typed.go), never by text.
// What is left here is naming: which identifier an import binds, so the
// rewrite can tell which imports a file still uses.

// nameOf is the identifier an unaliased import of path binds: the package's
// declared name, from the type-checked load, else the Go convention (last
// element, or the one before a /vN suffix), which is exact for the standard
// library.
func (p *pkgSrc) nameOf(path string) string {
	if n, ok := p.names[path]; ok {
		return n
	}
	elems := strings.Split(path, "/")
	last := elems[len(elems)-1]
	if len(elems) > 1 && len(last) > 1 && last[0] == 'v' {
		if n, err := strconv.Atoi(last[1:]); err == nil && n >= 2 {
			return elems[len(elems)-2]
		}
	}
	return last
}

func isStdlib(path string) bool {
	first, _, _ := strings.Cut(path, "/")
	return !strings.Contains(first, ".")
}

func render(e ast.Expr) string { return types.ExprString(e) }

// usedQualifiers is every identifier file uses as a selector's left side —
// a superset of the import names it uses (a local variable can be one), so
// an import it names is never wrongly called unused.
func usedQualifiers(file *ast.File) map[string]bool {
	used := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok {
				used[id.Name] = true
			}
		}
		return true
	})
	return used
}
