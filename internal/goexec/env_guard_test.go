package goexec

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/repowalk"
)

// envExempt names the functions that start the go toolchain WITHOUT Env, and
// why each is right to. Keyed "<repo-relative file>:<func>".
var envExempt = map[string]string{
	// forge lint deliberately runs its loader with -mod=mod outside a go.work
	// so it can fetch modules a fresh checkout has not downloaded, and
	// honours the caller's GOFLAGS inside one. It is a checker, not a
	// generator: nothing it writes is diffed against a CI regenerate.
	"internal/cli/lint/contract_inprocess.go:loadPackagesForAnalysis": "lint's loader (contract and slow-tests lanes) sets its own -mod policy",
	// The guard recognises the qualified goexec.Env; inside package goexec the
	// same call is spelled Env(), and a nil env defaults to it.
	"internal/goexec/pkgpatterns.go:ExcludeNodeModules": "defaults to Env() (unqualified inside goexec); callers pass their own",
}

// TestGoToolchainCallsUseEnv pins the boundary Env exists for: every
// function in this module that starts the go toolchain — exec of "go", or a
// golang.org/x/tools/go/packages load, which execs `go list` — builds that
// subprocess's environment with goexec.Env.
//
// A call site that inherits os.Environ() directly passes every test that
// does not export GOFLAGS=-mod=mod, which is all of them, and then rewrites
// go.sum on the first machine that does. Checking the property where it is
// introduced is the only place it can be caught.
func TestGoToolchainCallsUseEnv(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("module root not at %s: %v", root, err)
	}

	fset := token.NewFileSet()
	var offenders []string
	seenExempt := map[string]bool{}
	sites := 0
	walkErr := repowalk.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "testdata", "node_modules", "vendor":
				return filepath.SkipDir
			}
			if path != root && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			t.Fatalf("parse %s: %v", path, perr)
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			starts, usesEnv, configless := scanToolchainUse(fn.Body, f.Name.Name == "goexec")
			for _, pos := range configless {
				p := fset.Position(pos)
				offenders = append(offenders, rel+":"+strconv.Itoa(p.Line)+" in "+fn.Name.Name+
					"() — a controller-tools load with no packages.Config, so it can never carry goexec.Env; use the *WithConfig form")
			}
			if len(starts) == 0 {
				continue
			}
			sites += len(starts)
			key := rel + ":" + fn.Name.Name
			if _, ok := envExempt[key]; ok {
				seenExempt[key] = true
				continue
			}
			if !usesEnv {
				for _, pos := range starts {
					p := fset.Position(pos)
					offenders = append(offenders, rel+":"+strconv.Itoa(p.Line)+" in "+fn.Name.Name+"()")
				}
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatal(walkErr)
	}

	for key := range envExempt {
		if !seenExempt[key] {
			t.Errorf("envExempt entry %q matches no toolchain call any more — delete it", key)
		}
	}
	if sites == 0 {
		t.Fatal("found no go-toolchain call sites at all — the scan is broken, not the code clean")
	}
	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Fatalf("go-toolchain subprocesses that inherit the caller's environment unscrubbed:\n  %s\n\n"+
			"Each one hands a caller's GOFLAGS=-mod=mod to go build / go list, which then "+
			"rewrite go.mod and go.sum as a side effect. Set the command's Env (or the "+
			"packages.Config's Env) from goexec.Env(extra...), or add an envExempt entry "+
			"that says why this one must inherit it.",
			strings.Join(offenders, "\n  "))
	}
}

// scanToolchainUse reports where body starts the go toolchain and whether it
// calls goexec.Env (or CheckEnv, which starts from it) anywhere (closures included — a command built in one
// often gets its Env in the enclosing function).
//
// configless are controller-tools loads made through the convenience forms
// (genall.Generators.ForRoots, loader.LoadRoots). They build an empty
// packages.Config internally, so go/packages execs `go list` with the
// process environment and no call site can scrub it. They were found the
// hard way: an end-to-end run of `forge generate` on control-plane under
// GOFLAGS=-mod=mod showed the CRD and deepcopy loads still receiving it.
func scanToolchainUse(body *ast.BlockStmt, inGoexec bool) (starts []token.Pos, usesEnv bool, configless []token.Pos) {
	ast.Inspect(body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			pkg, name := selectorOf(n.Fun)
			// Inside goexec itself Env is called unqualified.
			if id, ok := n.Fun.(*ast.Ident); ok && inGoexec {
				pkg, name = "goexec", id.Name
			}
			if sel, ok := n.Fun.(*ast.SelectorExpr); ok && (sel.Sel.Name == "ForRoots" || sel.Sel.Name == "LoadRoots") {
				configless = append(configless, n.Pos())
			}
			switch {
			case pkg == "goexec" && (name == "Env" || name == "CheckEnv"):
				usesEnv = true
			case pkg == "exec" && (name == "Command" || name == "CommandContext"):
				argIdx := 0
				if name == "CommandContext" {
					argIdx = 1
				}
				if len(n.Args) > argIdx && isStringLit(n.Args[argIdx], "go") {
					starts = append(starts, n.Pos())
				}
			}
		case *ast.CompositeLit:
			if pkg, name := selectorOf(n.Type); pkg == "packages" && name == "Config" {
				starts = append(starts, n.Pos())
			}
		}
		return true
	})
	return starts, usesEnv, configless
}

func selectorOf(e ast.Expr) (pkg, name string) {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return "", ""
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok {
		return "", ""
	}
	return id.Name, sel.Sel.Name
}

func isStringLit(e ast.Expr, want string) bool {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return false
	}
	s, err := strconv.Unquote(lit.Value)
	return err == nil && s == want
}
