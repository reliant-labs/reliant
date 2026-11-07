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

// goflagsExempt names the functions that write a GOFLAGS value into an
// environment without WithTrimpath, and why each is right to. Keyed
// "<repo-relative file>:<func>".
var goflagsExempt = map[string]string{
	// Fake shell programs on a private PATH: no go toolchain runs under it.
	"internal/cli/generate_buf_preflight_test.go:preflightProject":                         "the env drives stub programs, never a go build",
	"internal/cli/generate_buf_preflight_test.go:TestPreflightBufTemplate_Flags":           "a GOFLAGS value the preflight parses, never handed to go",
	"internal/cli/ci_verify_scratch_test.go:TestVerifyScratch_FingerprintsLikeTheCheckout": "a stubbed environ the fingerprint reads, never handed to go",
	"internal/cli/lint/contract_inprocess_test.go:TestAnalysisEnvKeepsTrimpath":            "inputs to analysisEnv, asserting it merges -trimpath; never handed to go",
	"internal/kclprojects/gomod.go:downloadEnv":                                            "the env of `go mod download`, which fetches a module and builds nothing",
	"internal/kclprojects/kclprojects_test.go:TestDownloadEnvLeavesTheModuleOut":           "inputs to downloadEnv, asserting it drops -mod; never handed to go",
}

// TestGOFLAGSOverridesKeepTrimpath pins that a child environment which SETS
// GOFLAGS — `GOFLAGS=-mod=mod`, or `GOFLAGS=` to drop the caller's flags —
// merges into it through WithTrimpath (or CheckEnv) instead of replacing it.
//
// An environment GOFLAGS replaces the whole file layer cmd/go would otherwise
// read, so it drops the -trimpath the Reliant workspace image sets in
// $GOROOT/go.env, and a go.mod toolchain line that switches to a downloaded
// toolchain (whose go.env has no such line) drops it too. Without -trimpath
// cmd/go keys every main-module package on its absolute directory: each
// worktree, and every test's t.TempDir() project, compiles into its own cache
// entries that nothing ever reads again. On the shared machine that
// motivated this, 87% of a 152 GiB GOCACHE was such entries, filling at
// ~70 GiB/h — faster than any trim may safely delete
// (storage.BudgetUnreachableError).
//
// The scan covers test files too: tests are where most of these children are
// built, and a t.TempDir() project is the worst case — a new key every run.
func TestGOFLAGSOverridesKeepTrimpath(t *testing.T) {
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
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "internal/goexec/") {
			return nil // the merge itself, and its own tests
		}
		f, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			t.Fatalf("parse %s: %v", path, perr)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			sets, merges := scanGOFLAGS(fn.Body)
			if len(sets) == 0 {
				continue
			}
			sites += len(sets)
			key := rel + ":" + fn.Name.Name
			if _, ok := goflagsExempt[key]; ok {
				seenExempt[key] = true
				continue
			}
			if !merges {
				for _, pos := range sets {
					offenders = append(offenders, rel+":"+strconv.Itoa(fset.Position(pos).Line)+" in "+fn.Name.Name+"()")
				}
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatal(walkErr)
	}
	for key := range goflagsExempt {
		if !seenExempt[key] {
			t.Errorf("goflagsExempt entry %q sets no GOFLAGS any more — delete it", key)
		}
	}
	if sites == 0 {
		t.Fatal("found no GOFLAGS assignments at all — the scan is broken, not the code clean")
	}
	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Fatalf("child environments that set GOFLAGS and so drop -trimpath:\n  %s\n\n"+
			"Wrap the environment in goexec.WithTrimpath(...) (or build it with goexec.CheckEnv), "+
			"which merges -trimpath into the GOFLAGS you set, or add a goflagsExempt entry that "+
			"says why no go build runs under it.", strings.Join(offenders, "\n  "))
	}
}

// scanGOFLAGS reports the string literals in body that assign GOFLAGS
// ("GOFLAGS=…") and whether body calls WithTrimpath or CheckEnv anywhere
// (closures included: an env built in one is often used in the enclosing
// function).
func scanGOFLAGS(body *ast.BlockStmt) (sets []token.Pos, merges bool) {
	ast.Inspect(body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.BasicLit:
			if n.Kind != token.STRING {
				return true
			}
			if v, err := strconv.Unquote(n.Value); err == nil && strings.HasPrefix(v, "GOFLAGS=") {
				sets = append(sets, n.Pos())
			}
		case *ast.CallExpr:
			if pkg, name := selectorOf(n.Fun); pkg == "goexec" && (name == "WithTrimpath" || name == "CheckEnv") {
				merges = true
			}
		}
		return true
	})
	return sets, merges
}
