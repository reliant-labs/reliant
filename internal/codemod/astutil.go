package codemod

import (
	"go/ast"
	"os"
	"path/filepath"
	"strings"
)

// astutil.go — the source-rewriting helpers several codemods share.

// txEdit replaces src[start:end] with text.
type txEdit struct {
	start, end int
	text       string
}

// goModulePath is the module path dir's go.mod declares, or "" when it
// has none.
func goModulePath(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.Trim(strings.TrimSpace(rest), `"`)
		}
	}
	return ""
}

// importName is the name file refers to the package at path by: its
// explicit import name, or def for an unnamed import. "" when the file does
// not import it, or imports it blank or dot (no selector names it).
func importName(file *ast.File, path, def string) string {
	for _, spec := range file.Imports {
		if strings.Trim(spec.Path.Value, `"`) != path {
			continue
		}
		if spec.Name == nil {
			return def
		}
		if spec.Name.Name == "_" || spec.Name.Name == "." {
			return ""
		}
		return spec.Name.Name
	}
	return ""
}

// packageRefs returns the selector expressions pkg.<Sel> in file whose
// left side is the imported package (not a local of the same name), keeping
// only the selectors in only when it is non-nil.
func packageRefs(file *ast.File, pkg string, only map[string]bool) []*ast.SelectorExpr {
	var refs []*ast.SelectorExpr
	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		x, ok := sel.X.(*ast.Ident)
		if !ok || x.Name != pkg || x.Obj != nil {
			return true
		}
		if only == nil || only[sel.Sel.Name] {
			refs = append(refs, sel)
		}
		return true
	})
	return refs
}

// nameTaken reports whether name is already an import name or a package-level
// declaration in file, so a new import under it would collide.
func nameTaken(file *ast.File, name string) bool {
	for _, spec := range file.Imports {
		if spec.Name != nil && spec.Name.Name == name {
			return true
		}
		if spec.Name == nil && filepath.Base(strings.Trim(spec.Path.Value, `"`)) == name {
			return true
		}
	}
	return file.Scope != nil && file.Scope.Lookup(name) != nil
}
