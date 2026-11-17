package ciguard

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The advisory fixture renders real envs through native KCL. Keep those
// end-to-end assertions in full mode, not repeated on each premerge run.
func TestRenderBackedAdvisoryFixtureIsFullTier(t *testing.T) {
	name := filepath.Join("..", "cli", "upgrade_advisory_test.go")
	body, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	f, err := parser.ParseFile(token.NewFileSet(), name, body, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "scaffoldAdvisoryProject" {
			continue
		}
		// t.Helper() may precede it; no fixture side effect may do so.
		for _, stmt := range fn.Body.List[:min(2, len(fn.Body.List))] {
			gate, ok := stmt.(*ast.IfStmt)
			if !ok {
				continue
			}
			cond := string(body[gate.Cond.Pos()-1 : gate.Cond.End()-1])
			block := string(body[gate.Body.Pos()-1 : gate.Body.End()-1])
			if cond == "testing.Short()" && strings.Contains(block, "t.Skip(") {
				return
			}
		}
		t.Fatal("scaffoldAdvisoryProject must skip native KCL renders before constructing a fixture under -short; full mode retains the assertions")
	}
	t.Fatal("scaffoldAdvisoryProject not found")
}

func TestChangedTestRaceRepetitionRetainedOnMain(t *testing.T) {
	body, err := os.ReadFile(ciWorkflow)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "scripts/ci-changed-tests.sh HEAD^1 full") {
		t.Fatal("main/manual full must retain changed-test race count3")
	}
	wf := readWorkflow(t)
	for _, name := range []string{"test", "test-cli", "test-short-warm"} {
		if _, ok := wf.Jobs[name]; !ok {
			t.Errorf("full coverage job %q missing", name)
		}
	}
}
