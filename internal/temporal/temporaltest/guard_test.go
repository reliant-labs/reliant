// Copyright (c) 2025 Reliant Labs
package temporaltest

import (
	"bytes"
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
)

const (
	sdkTestsuitePath = "go.temporal.io/sdk/testsuite"
	sdkWorkerPath    = "go.temporal.io/sdk/worker"
)

// notYetMigrated lists files allowed to keep the SDK default, by module-relative
// path, each with the reason. It is meant to be empty; an entry is a debt with
// a named owner, not an exemption.
var notYetMigrated = map[string]string{}

// TestNoHarnessUsesTheSDKDeadlockDefault keeps the one-second default from
// coming back one new test at a time. It rejects, anywhere in the module:
//
//   - testsuite.WorkflowTestSuite: its environments use the SDK default. Use
//     temporaltest.WorkflowTestSuite, a drop-in replacement.
//   - in a _test.go file, worker.New(c, queue, worker.Options{...}): a worker
//     a test builds gets the SDK default too. Wrap the literal in
//     temporaltest.WorkerOptions.
//
// Production workers are not this package's business — their options are
// built in internal/workersetup — so the worker.New rule is test-files only.
func TestNoHarnessUsesTheSDKDeadlockDefault(t *testing.T) {
	root := moduleRoot(t)
	self, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}

	var violations []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			switch name {
			case "node_modules", "vendor", "dist", "testdata":
				return filepath.SkipDir
			}
			if path == self {
				// The one place the SDK type is wrapped (and, in the
				// proof test, deliberately used raw).
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.Contains(src, []byte(sdkTestsuitePath)) && !bytes.Contains(src, []byte(sdkWorkerPath)) {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		uses := deadlockDefaultUses(t, path, src)
		if _, allowed := notYetMigrated[rel]; allowed {
			if len(uses) == 0 {
				t.Errorf("%s no longer uses the SDK default; remove it from notYetMigrated", rel)
			}
			return nil
		}
		for _, v := range uses {
			violations = append(violations, rel+":"+v)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	if len(violations) > 0 {
		sort.Strings(violations)
		t.Fatalf("Temporal harnesses using the SDK's 1s deadlock detector "+
			"(see internal/temporal/temporaltest):\n  %s", strings.Join(violations, "\n  "))
	}
}

// deadlockDefaultUses reports each position in one file that builds a
// Temporal harness on the SDK's deadlock default.
func deadlockDefaultUses(t *testing.T, path string, src []byte) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	testsuiteName := importName(file, sdkTestsuitePath)
	workerName := importName(file, sdkWorkerPath)
	isTestFile := strings.HasSuffix(path, "_test.go")

	var uses []string
	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.SelectorExpr:
			if isPackageSelector(node, testsuiteName, "WorkflowTestSuite") {
				uses = append(uses, position(fset, node)+": testsuite.WorkflowTestSuite (use temporaltest.WorkflowTestSuite)")
			}
		case *ast.CallExpr:
			if !isTestFile || len(node.Args) != 3 {
				return true
			}
			fn, ok := node.Fun.(*ast.SelectorExpr)
			if !ok || !isPackageSelector(fn, workerName, "New") {
				return true
			}
			lit, ok := node.Args[2].(*ast.CompositeLit)
			if !ok {
				return true
			}
			if typ, ok := lit.Type.(*ast.SelectorExpr); ok && isPackageSelector(typ, workerName, "Options") {
				uses = append(uses, position(fset, node)+": worker.New with a bare worker.Options literal (wrap it in temporaltest.WorkerOptions)")
			}
		}
		return true
	})
	return uses
}

// importName is the identifier a file refers to importPath by, or "" when the
// file does not import it.
func importName(file *ast.File, importPath string) string {
	for _, spec := range file.Imports {
		p, err := strconv.Unquote(spec.Path.Value)
		if err != nil || p != importPath {
			continue
		}
		if spec.Name != nil {
			return spec.Name.Name
		}
		return filepath.Base(importPath)
	}
	return ""
}

func isPackageSelector(sel *ast.SelectorExpr, pkgName, name string) bool {
	if pkgName == "" || pkgName == "_" || sel.Sel.Name != name {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	return ok && ident.Name == pkgName
}

func position(fset *token.FileSet, n ast.Node) string {
	return strconv.Itoa(fset.Position(n.Pos()).Line)
}

// moduleRoot walks up from this package to the directory holding go.mod.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above this package")
		}
		dir = parent
	}
}
