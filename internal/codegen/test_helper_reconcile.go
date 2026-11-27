package codegen

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/naming"
)

// ReconcileScaffoldTestHelperName updates references to a handler package's
// generated test factories (NewTest<X>, NewTest<X>Server, and With<X>Deps).
// A same-named domain component added after the tests were written can change
// those factories' names from e.g. NewTestWidget to NewTestSvcWidget. Forge
// renamed the symbols, so it fixes the references it left dangling.
//
// Every test can use the factories, including the CRUD test, user-written
// tests and renamed scaffold tests. Since these files are user-owned, only
// exact qualified references to the handler package's factories are changed.
// The rewrite is idempotent and also handles removal of the domain component.
func ReconcileScaffoldTestHelperName(projectDir, servicePkg, targetDir string) (bool, error) {
	entries, err := os.ReadDir(targetDir)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	module, err := GetModulePath(projectDir)
	if err != nil {
		return false, nil // cannot identify a handler package import without a module
	}
	rel, err := filepath.Rel(projectDir, targetDir)
	if err != nil {
		return false, err
	}
	importPath := module + "/" + filepath.ToSlash(rel)

	current := ComputeTestHelperName(servicePkg, projectDir)
	pascal := naming.ToPascalCase(servicePkg)
	stale := "Svc" + pascal
	if current == stale {
		stale = pascal
	}
	if pascal == "" {
		return false, nil
	}
	renames := map[string]string{
		"NewTest" + stale:            "NewTest" + current,
		"NewTest" + stale + "Server": "NewTest" + current + "Server",
		"With" + stale + "Deps":      "With" + current + "Deps",
	}

	var rewrote bool
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(targetDir, e.Name())
		changed, err := reconcileTestHelperSelectors(path, importPath, servicePkg, renames)
		if err != nil {
			return rewrote, fmt.Errorf("reconcile test helpers in %s: %w", path, err)
		}
		rewrote = rewrote || changed
	}
	return rewrote, nil
}

func reconcileTestHelperSelectors(path, importPath, servicePkg string, renames map[string]string) (bool, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	fset := token.NewFileSet()
	// Keep parser object resolution to distinguish a locally shadowed
	// qualifier from the import. No type-check: the references are dangling.
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil || ast.IsGenerated(file) {
		return false, nil
	}
	alias := handlerTestImportAlias(file, importPath, servicePkg)
	if alias == "" {
		return false, nil
	}

	// Replace only the selected identifier bytes, not comments, strings or
	// the surrounding Go. AST traversal visits them in source order.
	var out bytes.Buffer
	last := 0
	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok || id.Name != alias || id.Obj != nil { //nolint:staticcheck // parser resolution detects a locally declared shadow of the import.
			return true
		}
		if to, ok := renames[sel.Sel.Name]; ok {
			off := fset.Position(sel.Sel.Pos()).Offset
			out.Write(src[last:off])
			out.WriteString(to)
			last = off + len(sel.Sel.Name)
		}
		return true
	})
	if last == 0 {
		return false, nil
	}
	out.Write(src[last:])
	checksums.RecordPreWriteAbs(path)
	return true, atomicfile.WriteFile(path, out.Bytes(), 0o644)
}

func handlerTestImportAlias(file *ast.File, importPath, servicePkg string) string {
	for _, imp := range file.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		if p != importPath {
			continue
		}
		if imp.Name == nil {
			return servicePkg
		}
		if imp.Name.Name != "." && imp.Name.Name != "_" {
			return imp.Name.Name
		}
	}
	return ""
}
