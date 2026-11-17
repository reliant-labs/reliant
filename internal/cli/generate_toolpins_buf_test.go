package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Tool resolution can run inside a JSON reader's on-demand descriptor build.
// Pin the stream at every diagnostic site without installing tools or relying
// on the host's PATH/cache state. Both the resolver and final notices must use
// stderr; routing only the descriptor generator's progress is insufficient.
func TestCodegenToolDiagnosticsKeepStdoutClean(t *testing.T) {
	src, err := os.ReadFile("generate_toolpins.go")
	if err != nil {
		t.Fatal(err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), "generate_toolpins.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	notices, resolvers := 0, 0
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if pkg.Name == "fmt" && (sel.Sel.Name == "Printf" || sel.Sel.Name == "Println" || sel.Sel.Name == "Print") {
			t.Error("tool diagnostics must not use implicit stdout")
		}
		var stream ast.Expr
		if pkg.Name == "fmt" && sel.Sel.Name == "Fprintf" {
			notices++
			stream = call.Args[0]
		}
		if pkg.Name == "cigate" && sel.Sel.Name == "NewResolver" {
			resolvers++
			stream = call.Args[1]
		}
		if stream != nil {
			out, ok := stream.(*ast.SelectorExpr)
			if !ok {
				t.Error("diagnostic stream must be os.Stderr")
				return true
			}
			owner, ok := out.X.(*ast.Ident)
			if !ok || owner.Name != "os" || out.Sel.Name != "Stderr" {
				t.Error("diagnostic stream must be os.Stderr")
			}
		}
		return true
	})
	if notices != 2 || resolvers != 1 {
		t.Fatalf("covered %d notices and %d resolvers; review new diagnostic paths", notices, resolvers)
	}
}

// A pinned buf must replace the program exec.Command resolved off forge's own
// PATH — prepending its directory to the CHILD's PATH changes nothing about
// which binary the parent execs — and clear the not-found a missing buf left
// in cmd.Err.
func TestWithPinnedTools_ExecsThePinnedBuf(t *testing.T) {
	root := t.TempDir()
	pinned := filepath.Join(t.TempDir(), "buf")
	pinMu.Lock()
	pinCache[root+"\x00"+os.Getenv("PATH")] = &codegenTools{Dirs: []string{filepath.Dir(pinned)}, paths: map[string]string{"buf": pinned}}
	pinMu.Unlock()
	t.Cleanup(func() {
		pinMu.Lock()
		delete(pinCache, root+"\x00"+os.Getenv("PATH"))
		pinMu.Unlock()
	})

	cmd := exec.Command("buf", "generate")
	cmd.Err = exec.ErrNotFound // what exec.Command records when PATH has no buf
	if err := withPinnedTools(cmd, root); err != nil {
		t.Fatal(err)
	}
	if cmd.Path != pinned || cmd.Err != nil {
		t.Errorf("cmd.Path = %q, cmd.Err = %v; want the pinned %s and no error", cmd.Path, cmd.Err, pinned)
	}

	other := exec.Command("go", "version")
	goPath := other.Path
	if err := withPinnedTools(other, root); err != nil {
		t.Fatal(err)
	}
	if other.Path != goPath {
		t.Errorf("a non-buf command was repointed to %q", other.Path)
	}
}
