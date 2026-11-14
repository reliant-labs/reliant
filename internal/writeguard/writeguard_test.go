package writeguard

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// truncatingWriters are the standard-library calls that truncate before they
// write. Keyed by import path, then function name.
var truncatingWriters = map[string]map[string]bool{
	"os":        {"WriteFile": true},
	"io/ioutil": {"WriteFile": true},
}

// exempt lists the only places allowed to call a truncating writer. Adding an
// entry needs a reason a reviewer can disagree with.
var exempt = map[string]string{
	"internal/atomicfile": "the atomic writer itself; os.WriteFile is its fallback for non-regular files, which rename cannot replace",
}

func TestNoTruncatingWritesInForgeCode(t *testing.T) {
	root := filepath.Join("..", "..", "internal")
	var findings []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "testdata", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel := filepath.ToSlash(strings.TrimPrefix(path, filepath.Join("..", "..")+string(filepath.Separator)))
		if _, ok := exempt[filepath.ToSlash(filepath.Dir(rel))]; ok {
			return nil
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			t.Errorf("parse %s: %v", rel, perr)
			return nil
		}
		for _, hit := range truncatingCalls(f) {
			findings = append(findings, rel+":"+strconv.Itoa(fset.Position(hit.Pos()).Line)+": "+hit.Fun.(*ast.SelectorExpr).Sel.Name)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(findings)
	if len(findings) > 0 {
		t.Fatalf("direct os.WriteFile truncates the target before writing it, so a crash mid-write leaves an empty or partial file.\n"+
			"Use atomicfile.WriteFile (github.com/reliant-labs/forge/internal/atomicfile) — same signature — instead:\n  %s",
			strings.Join(findings, "\n  "))
	}
}

func truncatingCalls(f *ast.File) []*ast.CallExpr {
	local := map[string]string{} // local package name -> import path
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		if _, ok := truncatingWriters[p]; !ok {
			continue
		}
		name := filepath.Base(p)
		if imp.Name != nil {
			name = imp.Name.Name
		}
		local[name] = p
	}
	if len(local) == 0 {
		return nil
	}
	var out []*ast.CallExpr
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if p, ok := local[id.Name]; ok && truncatingWriters[p][sel.Sel.Name] {
			out = append(out, call)
		}
		return true
	})
	return out
}

// The guard has to see aliased imports and method-value use, or it is decoration.
func TestTruncatingCallsDetection(t *testing.T) {
	src := `package x
import (
	"os"
	osx "os"
	ioutil "io/ioutil"
)
func f() {
	_ = os.WriteFile("a", nil, 0o644)
	_ = osx.WriteFile("b", nil, 0o644)
	_ = ioutil.WriteFile("c", nil, 0o644)
	_ = os.ReadFile("d")
}`
	f, err := parser.ParseFile(token.NewFileSet(), "x.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(truncatingCalls(f)); got != 3 {
		t.Fatalf("found %d truncating calls, want 3 (os, aliased os, ioutil; not ReadFile)", got)
	}
}
