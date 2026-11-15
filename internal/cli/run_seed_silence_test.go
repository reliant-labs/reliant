package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// `forge env up` reports what it did. maybeAutoSeed is the one hook that can
// decide NOT to seed, and a dev database that was never seeded is
// indistinguishable — from the terminal, from the browser, from the next
// phase of a workflow — from one that was seeded and whose domain is empty.
// A measured run hit exactly that: migrations 0->4 applied, no `auto-seeded`
// line, no `auto-seed skipped` line, and a live app over an empty schema.
//
// So the contract is: every path out of maybeAutoSeed either seeds, prints
// why it did not, or carries a `// quiet:` comment justifying the silence.
// This test reads the source and holds that contract, because the failure
// mode is a return statement someone adds later without a message.

const autoSeedFuncName = "maybeAutoSeed"

func TestMaybeAutoSeedNeverReturnsSilently(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile("run_seed.go")
	if err != nil {
		t.Fatalf("read run_seed.go: %v", err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "run_seed.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse run_seed.go: %v", err)
	}

	fn := findFuncDecl(file, autoSeedFuncName)
	if fn == nil {
		t.Fatalf("%s not found in run_seed.go — if it was renamed, retarget this guard rather than deleting it", autoSeedFuncName)
	}

	// Lines carrying a `// quiet:` justification, and lines that print.
	quiet := map[int]bool{}
	for _, cg := range file.Comments {
		for _, c := range cg.List {
			if strings.Contains(c.Text, "quiet:") {
				quiet[fset.Position(c.Pos()).Line] = true
			}
		}
	}
	prints := map[int]bool{}
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "fmt" || !strings.HasPrefix(sel.Sel.Name, "Print") {
			return true
		}
		prints[fset.Position(call.Pos()).Line] = true
		return true
	})

	var returns []int
	ast.Inspect(fn, func(n ast.Node) bool {
		if ret, ok := n.(*ast.ReturnStmt); ok {
			returns = append(returns, fset.Position(ret.Pos()).Line)
		}
		return true
	})
	if len(returns) == 0 {
		t.Fatalf("no return statements found in %s — the guard is not looking at the function it thinks it is", autoSeedFuncName)
	}

	for _, line := range returns {
		if explainedBefore(line, prints) || explainedBefore(line, quiet) {
			continue
		}
		t.Errorf("run_seed.go:%d: %s returns without a message and without a `// quiet:` justification. "+
			"A `forge env up` that does not seed must say so — an unseeded dev database looks exactly like a seeded empty one.",
			line, autoSeedFuncName)
	}
}

// explainedBefore reports whether one of the marked lines sits in the few
// lines immediately above the return — i.e. inside the same `if` block.
func explainedBefore(returnLine int, marked map[int]bool) bool {
	for l := returnLine - 1; l >= returnLine-3 && l > 0; l-- {
		if marked[l] {
			return true
		}
	}
	return false
}

// The guard above is only as good as its subject. This pins that the two
// paths a measured run actually took are the ones now carrying a message.
func TestMaybeAutoSeedReportsUnresolvedDSNAndFailedEmptyProbe(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile("run_seed.go")
	if err != nil {
		t.Fatalf("read run_seed.go: %v", err)
	}
	body := string(src)

	for _, want := range []string{
		"no DATABASE_URL resolved",
		"could not tell whether the seedable tables are empty",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("maybeAutoSeed no longer reports %q — this was one of the two silent returns that let a live app come up over an empty schema with no line explaining it", want)
		}
	}
}

func findFuncDecl(file *ast.File, name string) *ast.FuncDecl {
	for _, d := range file.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == name {
			return fn
		}
	}
	return nil
}
