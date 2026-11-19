package forgeconv

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestLintHandlerTests_HandRolledFires verifies the rule warns on the
// canonical hand-rolled `tests := []struct{name, call}` shape and points
// the user at the codemod / migration skill.
func TestLintHandlerTests_HandRolledFires(t *testing.T) {
	t.Parallel()
	res, err := LintHandlerTests(filepath.Join("testdata", "handler_tests_handrolled"))
	if err != nil {
		t.Fatalf("LintHandlerTests: %v", err)
	}

	got := findingsForRule(res.Findings, "forgeconv-handler-tests-use-tdd")
	if len(got) != 1 {
		t.Fatalf("expected 1 hand-rolled finding, got %d:\n%s", len(got), res.FormatText())
	}
	if got[0].Severity != SeverityWarning {
		t.Errorf("rule should be a warning (advisory), got %s", got[0].Severity)
	}
	if !strings.Contains(got[0].Message, "tdd.RunRPCCases") {
		t.Errorf("message should mention `tdd.RunRPCCases`; got: %s", got[0].Message)
	}
	if !strings.Contains(got[0].Message, "forge project migrate tdd") {
		t.Errorf("message should mention the codemod (`forge project migrate tdd`); got: %s", got[0].Message)
	}
	// Warnings must not gate the build.
	if res.HasErrors() {
		t.Errorf("hand-rolled handler tests should not gate the build; HasErrors() returned true")
	}
}

// TestLintHandlerTests_TDDImportSkips verifies a file that already
// imports `forge/pkg/tdd` is left alone, even when a sibling
// `tests := []struct{...}` slice still happens to live in it. Migrated
// files should not re-warn forever.
func TestLintHandlerTests_TDDImportSkips(t *testing.T) {
	t.Parallel()
	res, err := LintHandlerTests(filepath.Join("testdata", "handler_tests_tdd"))
	if err != nil {
		t.Fatalf("LintHandlerTests: %v", err)
	}
	got := findingsForRule(res.Findings, "forgeconv-handler-tests-use-tdd")
	if len(got) != 0 {
		t.Fatalf("expected 0 findings (file imports pkg/tdd), got %d:\n%s", len(got), res.FormatText())
	}
}

// TestLintHandlerTests_CleanFixture verifies a handler test file that
// uses neither the hand-rolled shape nor pkg/tdd does not trip the rule
// — we only nudge files that demonstrably went the bespoke route.
func TestLintHandlerTests_CleanFixture(t *testing.T) {
	t.Parallel()
	res, err := LintHandlerTests(filepath.Join("testdata", "handler_tests_clean"))
	if err != nil {
		t.Fatalf("LintHandlerTests: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("expected 0 findings on clean fixture, got %d:\n%s", len(res.Findings), res.FormatText())
	}
}

// TestLintHandlerTests_NoHandlersDir verifies projects without a
// handlers/ tree (CLI / library kinds) get an empty result rather than
// an error.
func TestLintHandlerTests_NoHandlersDir(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	res, err := LintHandlerTests(tmp)
	if err != nil {
		t.Fatalf("LintHandlerTests on empty project: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Errorf("empty project should produce 0 findings, got %d", len(res.Findings))
	}
}

// TestIsHandRolledTestsAssign verifies the predicate accepts the
// canonical shape and rejects look-alikes (slice of struct without both
// `name string` AND `call func`, non-`tests` LHS, etc.).
func TestIsHandRolledTestsAssign(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		src  string
		want bool
	}{
		{
			name: "canonical shape with func() error",
			src: `package x
func _() {
	tests := []struct{
		name string
		call func() error
	}{}
	_ = tests
}`,
			want: true,
		},
		{
			name: "client-receiver variant func(client X) error",
			src: `package x
type C struct{}
func _() {
	tests := []struct{
		name string
		call func(c C) error
	}{}
	_ = tests
}`,
			want: true,
		},
		{
			name: "wrong LHS name",
			src: `package x
func _() {
	cases := []struct{
		name string
		call func() error
	}{}
	_ = cases
}`,
			want: false,
		},
		{
			name: "missing call field",
			src: `package x
func _() {
	tests := []struct{
		name string
	}{}
	_ = tests
}`,
			want: false,
		},
		{
			name: "call field is not a func type",
			src: `package x
func _() {
	tests := []struct{
		name string
		call int
	}{}
	_ = tests
}`,
			want: false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := parseAndFindAssign(t, tc.src)
			if got != tc.want {
				t.Errorf("isHandRolledTestsAssign = %v, want %v\nsrc:\n%s", got, tc.want, tc.src)
			}
		})
	}
}

// parseAndFindAssign parses a tiny Go source snippet and reports whether
// any short-form `tests :=` assignment in the file matches the
// hand-rolled shape. Used by TestIsHandRolledTestsAssign to keep the
// predicate test free of fixture-file overhead.
func parseAndFindAssign(t *testing.T, src string) bool {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "x.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		if found {
			return false
		}
		if a, ok := n.(*ast.AssignStmt); ok && isHandRolledTestsAssign(a) {
			found = true
			return false
		}
		return true
	})
	return found
}
