package scaffolds

import (
	"path/filepath"
	"testing"
)

func TestLintRoot_Clean(t *testing.T) {
	t.Parallel()
	res, err := LintRoot(filepath.Join("testdata", "clean"))
	if err != nil {
		t.Fatalf("LintRoot returned error: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("expected zero findings on clean fixture, got %d: %+v", len(res.Findings), res.Findings)
	}
	if res.HasErrors() {
		t.Fatal("clean fixture must not produce errors")
	}
}

func TestLintRoot_ScaffoldMarkerPresent(t *testing.T) {
	t.Parallel()
	res, err := LintRoot(filepath.Join("testdata", "scaffold_marker_present"))
	if err != nil {
		t.Fatalf("LintRoot returned error: %v", err)
	}
	if !findingMatches(res.Findings, "scaffold-not-customized") {
		t.Fatalf("expected a scaffold-not-customized finding, got: %+v", res.Findings)
	}
	// WARNING severity, not error: a fresh scaffold always carries
	// FORGE_SCAFFOLD markers, so uncustomized scaffold is pending work —
	// it must be surfaced but must never gate the build (`forge lint`
	// has to exit 0 on forge's own freshly-scaffolded output).
	if res.HasErrors() {
		t.Fatalf("scaffold-not-customized must be a warning, got error severity: %+v", res.Findings)
	}
	for _, f := range res.Findings {
		if f.Rule == "scaffold-not-customized" && f.Severity != SeverityWarning {
			t.Fatalf("scaffold-not-customized severity = %q, want %q", f.Severity, SeverityWarning)
		}
	}
}

// Regression: a FORGE_SCAFFOLD marker inside a Go STRING LITERAL is
// fixture data, not a placeholder. The rule already tried to allow this
// by requiring the marker at the start of a line — but inside a RAW
// (backtick) literal, which is how tests embed multi-line source, the
// marker does begin its line, so the line-prefix test saw two real
// placeholders in internal/cli/scaffold/fixtures_test.go and `forge lint`
// reported pending work in forge's own repo that nobody could ever clear.
// Counting only markers that the Go parser reports as COMMENTS fixes it.
func TestLintRoot_MarkerInsideStringLiteralIsNotPendingWork(t *testing.T) {
	t.Parallel()
	res, err := LintRoot(filepath.Join("testdata", "marker_in_string_literal"))
	if err != nil {
		t.Fatalf("LintRoot returned error: %v", err)
	}
	if findingMatches(res.Findings, "scaffold-not-customized") {
		t.Fatalf("markers inside a string literal are fixture data, not pending work; got: %+v", res.Findings)
	}
}

func TestLintRoot_GenMissingHeader(t *testing.T) {
	t.Parallel()
	res, err := LintRoot(filepath.Join("testdata", "gen_missing_header"))
	if err != nil {
		t.Fatalf("LintRoot returned error: %v", err)
	}
	if !findingMatches(res.Findings, "gen-missing-header") {
		t.Fatalf("expected a gen-missing-header finding, got: %+v", res.Findings)
	}
	if !res.HasErrors() {
		t.Fatal("missing canonical header is an error-severity finding")
	}
}

func TestLintRoot_GenMissingSource(t *testing.T) {
	t.Parallel()
	res, err := LintRoot(filepath.Join("testdata", "gen_missing_source")) //nolint:staticcheck // testdata path
	if err != nil {
		t.Fatalf("LintRoot returned error: %v", err)
	}
	if !findingMatches(res.Findings, "gen-missing-source") {
		t.Fatalf("expected a gen-missing-source finding, got: %+v", res.Findings)
	}
	// Source missing is a warning, not an error: the file is still
	// recognisable as forge-owned thanks to the canonical header.
	if res.HasErrors() {
		t.Fatal("missing Source: line should be a warning, not an error")
	}
}

func TestIsGenFilename(t *testing.T) {
	t.Parallel()
	cases := []struct {
		path string
		want bool
	}{
		{"handlers/api/handlers_crud_gen.go", true},
		{"handlers/api/handlers_crud_gen_test.go", true},
		{"handlers/api/mock_gen.go", true},
		{"handlers/api/service.go", false},
		{"handlers/api/handlers.go", false},
		{"pkg/middleware/auth_gen.go", true},
		{"pkg/middleware/auth.go", false},
	}
	for _, c := range cases {
		if got := isGenFilename(c.path); got != c.want {
			t.Errorf("isGenFilename(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func findingMatches(findings []Finding, rule string) bool {
	for _, f := range findings {
		if f.Rule == rule {
			return true
		}
	}
	return false
}
