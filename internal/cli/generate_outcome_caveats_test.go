// File: internal/cli/generate_outcome_caveats_test.go
//
// The closing line never shows a green check over a project that does not
// compile. An outside evaluator's `forge scaffold` printed a user-owned
// `middleware_test.go:…: assignment mismatch` from the compile check and
// then "✅ Code generation complete": the check passes on a user-owned
// _test.go by design (failing generate over it is a deadlock, see
// validateTestFiles), but the tree still does not compile, and the line
// every agent reads as "done" said otherwise.

package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
)

func renderOutcome(sum checksums.WriteSummary, caveats runCaveats) string {
	var b bytes.Buffer
	printGenerateOutcome(&b, sum, nil, pipelineFlags{}, 10, 10, caveats)
	return b.String()
}

var wroteFiles = checksums.WriteSummary{Touched: 5, Created: []string{"a.go"}, Updated: []string{"b.go"}}

func TestGenerateOutcome_CleanRunIsGreen(t *testing.T) {
	for name, sum := range map[string]checksums.WriteSummary{
		"wrote":      wroteFiles,
		"up to date": {Touched: 5},
	} {
		got := renderOutcome(sum, runCaveats{})
		if !strings.HasPrefix(got, "✅ Code generation complete — 5 file(s) ") {
			t.Errorf("%s: a clean run's line = %q, want the green check", name, got)
		}
		// The lines after the closing line name the changed files
		// (printChangedFiles); none of them is a caveat.
		if strings.Contains(got, "but") || strings.Count(got, "\n") != 1+sum.Changed() {
			t.Errorf("%s: a clean run's line carries a caveat: %q", name, got)
		}
	}
}

func TestGenerateOutcome_UserTestsThatDoNotCompileAreNotGreen(t *testing.T) {
	caveats := runCaveats{compile: validateCaveats{UserTestErrors: 2, UserTestFiles: 1}}
	for name, sum := range map[string]checksums.WriteSummary{
		"wrote":      wroteFiles,
		"up to date": {Touched: 5},
	} {
		got := renderOutcome(sum, caveats)
		if strings.Contains(got, "✅") {
			t.Errorf("%s: green check over a project that does not compile:\n%s", name, got)
		}
		first, _, _ := strings.Cut(got, "\n")
		if !strings.HasPrefix(first, "⚠️  Code generation complete") || !strings.HasSuffix(first, " — but the project does NOT compile.") {
			t.Errorf("%s: closing line = %q, want a warning that ends saying the project does not compile", name, first)
		}
		if !strings.Contains(got, "2 compile error(s) in 1 _test.go file(s) you own") {
			t.Errorf("%s: the caveat does not say whose errors they are and how many:\n%s", name, got)
		}
	}
	// `project new` reads the count from this line; the warning keeps it.
	if got := generateSummary(renderOutcome(wroteFiles, caveats)); got != "5 files written" {
		t.Errorf("generateSummary over the warning line = %q, want %q", got, "5 files written")
	}
}

func TestGenerateOutcome_FailedStepsAreNotGreen(t *testing.T) {
	for _, tc := range []struct {
		name     string
		caveats  runCaveats
		headline string
	}{
		{"soft failure", runCaveats{failedSteps: 1}, "1 step(s) failed"},
		{"kept build failure", runCaveats{buildKept: true, failedSteps: 1}, "the project does NOT compile"},
		{"kept build failure and another", runCaveats{buildKept: true, failedSteps: 2}, "the project does NOT compile and 1 other step(s) failed"},
		{"pin lag", runCaveats{compile: validateCaveats{PinLag: true}}, "the project does NOT compile"},
	} {
		got := renderOutcome(wroteFiles, tc.caveats)
		first, _, _ := strings.Cut(got, "\n")
		if want := " — but " + tc.headline + "."; strings.Contains(got, "✅") || !strings.HasSuffix(first, want) {
			t.Errorf("%s: closing line = %q, want no green check and the suffix %q", tc.name, first, want)
		}
	}
}

func TestGenerateOutcome_NoWritesStillNamesTheCaveat(t *testing.T) {
	got := renderOutcome(checksums.WriteSummary{}, runCaveats{compile: validateCaveats{UserTestErrors: 1, UserTestFiles: 1}})
	if !strings.Contains(got, "WROTE NO FILES") || !strings.Contains(got, "And the project does NOT compile.") {
		t.Errorf("a zero-write run over a project that does not compile:\n%s", got)
	}
}

// validateTestFiles hands back the user-owned errors it warned about, so
// the closing line can name them; forge-owned ones still fail the run.
func TestValidateTestFiles_ReturnsUserOwnedErrorsAsCaveats(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("pkg/middleware/middleware_test.go", "package middleware\n")
	write("pkg/middleware/auth_test.go", "package middleware\n")
	diags := []string{
		"pkg/middleware/middleware_test.go:12:5: assignment mismatch: 2 variables but f returns 1 value",
		"pkg/middleware/middleware_test.go:20:5: assignment mismatch: 2 variables but f returns 1 value",
		"pkg/middleware/auth_test.go:3:1: undefined: g",
		"pkg/middleware/auth_test.go:3:1: undefined: g", // a duplicate counts once
	}
	caveats, err := validateTestFiles(dir, diags)
	if err != nil {
		t.Fatalf("user-owned test files failed the run: %v", err)
	}
	if want := (validateCaveats{UserTestErrors: 3, UserTestFiles: 2}); caveats != want {
		t.Errorf("caveats = %+v, want %+v", caveats, want)
	}

	write("internal/x/helpers_gen_test.go", stampedForgeOwned(t, "internal/x/helpers_gen_test.go", "package x\n"))
	caveats, err = validateTestFiles(dir, append(diags, "internal/x/helpers_gen_test.go:4:2: undefined: h"))
	if err == nil {
		t.Fatal("a forge-owned test file that does not compile passed the run")
	}
	if caveats.UserTestErrors != 3 {
		t.Errorf("caveats beside the failure = %+v, want the 3 user-owned errors", caveats)
	}
}
