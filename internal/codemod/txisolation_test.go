package codemod

import (
	"bufio"
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"strings"
	"testing"
)

// The behaviour-preservation proof. testdata/txisolation/shapes.go holds every
// pre-v1.0.0 transaction call shape; before.txt is what its TestShapes
// printed against forge 68d5f76fa (the last release with the old defaults):
// the isolation, read-only and deferrable settings the server reported inside
// each transaction, and how many times each shape ran a body that always
// fails 40001. This test points the same module at THIS forge — where the old
// calls no longer compile, the state `forge project upgrade` runs codemods
// in — rewrites it, and requires the identical lines.
func TestTxIsolation_KeepsEveryShapesWireLevelAndAttempts(t *testing.T) {
	if testing.Short() {
		t.Skip("type-checks a scratch module against forge and runs it on embedded postgres; runs in task test")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join("testdata", "txisolation")
	dir := t.TempDir()
	for _, name := range []string{"shapes.go", "shapes_test.go"} {
		copyFile(t, filepath.Join(fixture, name), filepath.Join(dir, name))
	}
	writeScratchGoMod(t, root, dir, "example.com/txscratch")

	rep, err := runTxIsolation(dir, Options{Write: true})
	if err != nil {
		t.Fatalf("codemod: %v", err)
	}
	if got, want := readFile(t, dir, "shapes.go"), readFile(t, fixture, "shapes.go.golden"); got != want {
		t.Errorf("rewritten shapes.go differs from shapes.go.golden:\n%s", got)
	}
	// 17 calls (Repo.RunTx and database/sql's BeginTx untouched), the
	// unsetOptions literal, and the two old constant names left in literals
	// that were already explicit.
	if len(rep.Rewrites) != 20 {
		t.Errorf("%d rewrites, want 20:\n%s", len(rep.Rewrites), strings.Join(rep.Rewrites, "\n"))
	}
	for _, line := range rep.Rewrites {
		if strings.Contains(line, "repo.RunTx") || strings.Contains(line, " db.BeginTx") {
			t.Errorf("rewrote a call that is not pkg/orm's: %s", line)
		}
	}
	if len(rep.Next) == 0 || !strings.Contains(rep.Next[0], "unsetOptions") {
		t.Errorf("Next should name the options passed by variable first, got %q", rep.Next)
	}

	again, err := runTxIsolation(dir, Options{Write: true})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if again.Changed() || len(again.Next) != 0 {
		t.Errorf("second run is not a no-op: %+v", again)
	}

	goCmd(t, dir, "vet", "./...")
	out := goCmd(t, dir, "test", "-count=1", "-v", "-run", "^TestShapes$", ".")
	var after []string
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "SHAPE ") {
			after = append(after, sc.Text())
		}
	}
	if got, want := strings.Join(after, "\n"), strings.TrimSpace(readFile(t, fixture, "before.txt")); got != want {
		t.Errorf("wire level / attempts changed by the rewrite.\nafter:\n%s\n\nbefore:\n%s", got, want)
	}
}

// The policy spellings: the shortest form that says exactly the old
// behaviour.
func TestTxIsolation_PolicyExpr(t *testing.T) {
	r := &txRewriter{prefix: "orm."}
	cases := []struct {
		level, attempts int
		readOnly        bool
		want, desc      string
	}{
		{oldSerializable, 7, false, "orm.Serializable", "serializable, 7 attempts"},
		{oldSerializable, 1, false, "orm.Serializable.WithMaxAttempts(1)", "serializable, 1 attempt"},
		{oldReadCommitted, 1, false, "orm.ReadCommitted", "read committed, 1 attempt"},
		{oldReadCommitted, 7, false, "orm.ReadCommitted.WithMaxAttempts(orm.DefaultTxMaxAttempts)", "read committed, 7 attempts"},
		{oldRepeatableRead, 3, false, "orm.RepeatableRead.WithMaxAttempts(3)", "repeatable read, 3 attempts"},
		{oldSerializable, 7, true, "orm.Serializable.WithReadOnly()", "serializable read only deferrable, 7 attempts"},
	}
	for _, tc := range cases {
		if got := r.policyExpr(tc.level, tc.attempts, "", tc.readOnly, "", false); got != tc.want {
			t.Errorf("policyExpr(%d, %d, %v) = %q, want %q", tc.level, tc.attempts, tc.readOnly, got, tc.want)
		}
		if got := policyDesc(tc.level, tc.attempts, "", tc.readOnly, ""); got != tc.desc {
			t.Errorf("policyDesc(%d, %d, %v) = %q, want %q", tc.level, tc.attempts, tc.readOnly, got, tc.desc)
		}
	}
	if got := r.policyExpr(oldSerializable, 0, "", false, "ro", false); got != "orm.TxOptions{Isolation: orm.Serializable, ReadOnly: ro, MaxAttempts: 0}" {
		t.Errorf("computed ReadOnly spelled %q", got)
	}
	// Where an orm.TxOptions is declared, a bare level (an orm.Isolation)
	// would not compile: the struct is spelled.
	if got := r.policyExpr(oldReadCommitted, 1, "", false, "", true); got != "orm.TxOptions{Isolation: orm.ReadCommitted, MaxAttempts: 1}" {
		t.Errorf("typed read committed spelled %q", got)
	}
}

// A project with no pkg/orm transaction calls is not loaded at all.
func TestTxIsolation_NothingToDoWithoutORM(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"go.mod":  "module example.com/plain\n\ngo 1.27.0\n",
		"main.go": "package main\n\nimport \"database/sql\"\n\nfunc f(db *sql.DB) { _, _ = db.BeginTx(nil, nil) }\n",
	})
	rep, err := runTxIsolation(dir, Options{Write: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Changed() || len(rep.Next) != 0 {
		t.Errorf("rewrote a project that does not use pkg/orm: %+v", rep)
	}
}

// A computed ReadOnly or MaxAttempts is spliced into the rewrite verbatim:
// one longer than a diagnostic line, or spanning lines with a line comment,
// must come out as the same expression, never shortened or joined (a joined
// line comment would swallow the rest of the literal).
func TestTxIsolation_ComputedFieldsKeepTheirExactSource(t *testing.T) {
	readOnly := "cfg.Database.ReadReplicaPreferred && // only the reporting path\n\t\trequest.Header.Get(\"X-Reliant-Read-Only-Hint-For-Reporting\") == \"yes-please\""
	attempts := "budget.AttemptsFor(ctx, \"a-rather-long-operation-name-that-pushes-this-past-eighty-columns\") + 1"
	src := "package p\n\nvar _ = orm.TxOptions{Isolation: IsolationSerializable, ReadOnly: " + readOnly + ", MaxAttempts: " + attempts + "}\n"
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	var lit *ast.CompositeLit
	ast.Inspect(f, func(n ast.Node) bool {
		if l, ok := n.(*ast.CompositeLit); ok && lit == nil {
			lit = l
		}
		return true
	})
	r := &txRewriter{fset: fset, info: &types.Info{Types: map[ast.Expr]types.TypeAndValue{}}, file: f, src: []byte(src), rel: "p.go", prefix: "orm."}
	text, desc, _, ok := r.mapTxOptionsLit(lit, false, false)
	if !ok {
		t.Fatal("the literal was not rewritten")
	}
	for _, want := range []string{"ReadOnly: " + readOnly, "MaxAttempts: " + attempts} {
		if !strings.Contains(text, want) {
			t.Errorf("rewrite does not keep %q verbatim:\n%s", want, text)
		}
	}
	if _, err := parser.ParseExpr(text); err != nil {
		t.Errorf("rewrite is not a valid expression: %v\n%s", err, text)
	}
	if strings.Contains(desc, "\n") {
		t.Errorf("the diagnostic description spans lines: %q", desc)
	}
}
