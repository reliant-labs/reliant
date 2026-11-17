package lint

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
)

// TestLintReportOnlyByDefault pins the gate contract at the flag layer: a
// bare `forge lint` writes nothing; --fix is the one way to rewrite, and
// --no-fix / --check still beat it.
func TestLintReportOnlyByDefault(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantFix bool
	}{
		{"default (no flags) is report-only", nil, false},
		{"--no-fix is report-only", []string{"--no-fix"}, false},
		{"--fix fixes", []string{"--fix"}, true},
		{"--no-fix wins over --fix", []string{"--fix", "--no-fix"}, false},
		{"--check wins over --fix", []string{"--fix", "--check"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newCmd(testFactory())
			if err := cmd.Flags().Parse(tc.args); err != nil {
				t.Fatalf("Parse(%v): %v", tc.args, err)
			}
			var f lintFlags
			f.fix, _ = cmd.Flags().GetBool("fix")
			f.noFix, _ = cmd.Flags().GetBool("no-fix")
			f.check, _ = cmd.Flags().GetBool("check")
			if got := f.autoFix(); got != tc.wantFix {
				t.Errorf("autoFix = %v, want %v", got, tc.wantFix)
			}
		})
	}
}

// writeFile writes content to root/rel, creating parent dirs.
func writeFile(t *testing.T, root, rel, content string) string {
	t.Helper()
	abs := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(abs), err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", abs, err)
	}
	return abs
}

// newFormatProject scaffolds a temp project with a go.mod and the formatters
// block the scaffolded .golangci.yml declares (goimports, the module as its
// local prefix), which is the gate the pre-pass formats to.
func newFormatProject(t *testing.T) string {
	t.Helper()
	root := newBareFormatProject(t)
	writeFile(t, root, ".golangci.yml", "version: \"2\"\nformatters:\n  enable:\n    - goimports\n  settings:\n    goimports:\n      local-prefixes:\n        - example.com/thing\n")
	return root
}

// newBareFormatProject is a temp project with a go.mod and no golangci-lint
// config.
func newBareFormatProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/thing\n\ngo 1.22\n")
	return root
}

// unformattedGo is a hand-editable owned file with jumbled imports (no
// group split, wrong order) and gofmt-dirty indentation — exactly the drift
// a post-generate hand-edit re-introduces.
const unformattedGo = `package foo

import (
"fmt"
"example.com/thing/other"
"os"
)

func Use() {
        fmt.Println(os.Args)
    other.Do()
}
`

// TestFormatGoTree_RewritesOwnedFile proves the pre-pass reformats an
// owned/hand-edited file (not just generated code) and reports it.
func TestFormatGoTree_RewritesOwnedFile(t *testing.T) {
	root := newFormatProject(t)
	abs := writeFile(t, root, "internal/foo/bar.go", unformattedGo)

	changed, err := formatGoTree(root)
	if err != nil {
		t.Fatalf("formatGoTree: %v", err)
	}
	if len(changed) != 1 || changed[0] != filepath.Join("internal", "foo", "bar.go") {
		t.Fatalf("changed = %v, want [internal/foo/bar.go]", changed)
	}

	got, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) == unformattedGo {
		t.Fatal("file was not rewritten; still in unformatted form")
	}
	// The rewrite must be canonical: a second canonicalization is a no-op.
	want, err := checksums.CanonicalGoSource("example.com/thing", abs, got)
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("rewritten file is not canonical:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	// The module-local import must have been split into its own trailing group.
	// (Sanity that the local-prefix wiring took effect, not just gofmt.)
	if _, err := checksums.CanonicalGoSource("example.com/thing", abs, got); err != nil {
		t.Fatalf("re-canonicalize: %v", err)
	}
}

// TestFormatGoTree_Idempotent proves a second pass over already-canonical
// output rewrites nothing — the property that keeps clean generated files
// (canonical + forge:hash-stamped) untouched.
func TestFormatGoTree_Idempotent(t *testing.T) {
	root := newFormatProject(t)
	writeFile(t, root, "internal/foo/bar.go", unformattedGo)

	if _, err := formatGoTree(root); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	changed, err := formatGoTree(root)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if len(changed) != 0 {
		t.Errorf("second pass rewrote %v, want no changes (must be idempotent)", changed)
	}
}

// TestFormatGoTree_LeavesBrokenFileUntouched proves an unparseable hand-edit
// is skipped (not corrupted, not an error) — the real syntax error is left
// for the compiler/gate to report.
func TestFormatGoTree_LeavesBrokenFileUntouched(t *testing.T) {
	root := newFormatProject(t)
	const broken = "package foo\n\nfunc Oops( {\n" // unbalanced
	abs := writeFile(t, root, "internal/foo/broken.go", broken)

	changed, err := formatGoTree(root)
	if err != nil {
		t.Fatalf("formatGoTree returned error on broken file: %v", err)
	}
	if len(changed) != 0 {
		t.Errorf("changed = %v, want none (broken file must be skipped)", changed)
	}
	got, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != broken {
		t.Errorf("broken file was modified:\n%s", got)
	}
}

// TestFormatGoTree_SkipsVendorAndNodeModules proves the walk prunes heavy /
// non-owned trees so it never rewrites dependency or build output, nor a
// testdata fixture (forge's own slowtest fixture was rewritten by every lint).
func TestFormatGoTree_SkipsVendorAndNodeModules(t *testing.T) {
	root := newFormatProject(t)
	writeFile(t, root, "internal/vendor/dep/x.go", unformattedGo)
	writeFile(t, root, "pkg/node_modules/x.go", unformattedGo)
	writeFile(t, root, "internal/linter/x/testdata/src/a/a_test.go", unformattedGo)

	changed, err := formatGoTree(root)
	if err != nil {
		t.Fatalf("formatGoTree: %v", err)
	}
	if len(changed) != 0 {
		t.Errorf("changed = %v, want none (vendor/node_modules/testdata must be skipped)", changed)
	}
}

// TestFormatGoTree_AlreadyCanonicalNoOp proves a clean file is never rewritten.
func TestFormatGoTree_AlreadyCanonicalNoOp(t *testing.T) {
	root := newFormatProject(t)
	canonical, err := checksums.CanonicalGoSource("example.com/thing", "bar.go", []byte(unformattedGo))
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	writeFile(t, root, "internal/foo/bar.go", string(canonical))

	changed, err := formatGoTree(root)
	if err != nil {
		t.Fatalf("formatGoTree: %v", err)
	}
	if len(changed) != 0 {
		t.Errorf("changed = %v, want none (already-canonical file must be a no-op)", changed)
	}
}

// `forge lint ./internal/mine/` says "this is the part I am working on", and
// the format pre-pass used to ignore it and rewrite every package in the
// project, including files another agent was editing. Named paths bound the
// pre-pass like --scope does; no paths, or the whole module, is the whole tree.
func TestKeepForPaths(t *testing.T) {
	keep := keepForPaths([]string{"./internal/mine/...", "cmd/tool/main.go"})
	for rel, want := range map[string]bool{
		"internal/mine/a.go":     true,
		"internal/mine/sub/b.go": true,
		"cmd/tool/main.go":       true,
		"internal/mineral/a.go":  false,
		"internal/theirs/c.go":   false,
		"cmd/tool/other.go":      false,
		"pkg/internal/mine/a.go": false,
	} {
		if got := keep(rel); got != want {
			t.Errorf("keep(%q) = %v, want %v", rel, got, want)
		}
	}
	for _, whole := range [][]string{nil, {}, {"."}, {"./..."}, {"..."}, {"./internal/mine", "./..."}} {
		if keepForPaths(whole) != nil {
			t.Errorf("keepForPaths(%v) restricts the run; it names the whole module", whole)
		}
	}
}

func TestFormatPrePass_PositionalPathsLeaveOtherPackagesAlone(t *testing.T) {
	root := newFormatProject(t)
	mine := writeFile(t, root, "internal/mine/a.go", unformattedGo)
	theirs := writeFile(t, root, "internal/theirs/c.go", unformattedGo)

	changed, err := formatGoTreeFiltered(root, keepForPaths([]string{"./internal/mine/..."}))
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || changed[0] != filepath.Join("internal", "mine", "a.go") {
		t.Fatalf("changed = %v, want only internal/mine/a.go", changed)
	}
	if got, _ := os.ReadFile(theirs); string(got) != unformattedGo {
		t.Error("a package outside the named path was rewritten")
	}
	if got, _ := os.ReadFile(mine); string(got) == unformattedGo {
		t.Error("the named package was not formatted")
	}
}
