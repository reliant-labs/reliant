package forgeconv

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestLintOptionalDepMarkerPosition_CleanPasses asserts that a Deps
// struct whose `// forge:optional-dep` marker sits in the doc-comment
// slot of a field reports zero findings.
func TestLintOptionalDepMarkerPosition_CleanPasses(t *testing.T) {
	t.Parallel()
	res, err := LintOptionalDepMarkerPosition(filepath.Join("testdata", "optional_dep_marker_clean"))
	if err != nil {
		t.Fatalf("LintOptionalDepMarkerPosition: %v", err)
	}
	got := findingsForRule(res.Findings, "forgeconv-optional-dep-marker-position")
	if len(got) != 0 {
		t.Fatalf("expected 0 findings on clean fixture, got %d:\n%s", len(got), res.FormatText())
	}
}

// TestLintOptionalDepMarkerPosition_MisplacedFires asserts that the
// rule catches markers attached to non-Deps targets — both the struct
// itself (`// forge:optional-dep` above `type Deps struct {}`) and a
// free-floating function docstring.
func TestLintOptionalDepMarkerPosition_MisplacedFires(t *testing.T) {
	t.Parallel()
	res, err := LintOptionalDepMarkerPosition(filepath.Join("testdata", "optional_dep_marker_misplaced"))
	if err != nil {
		t.Fatalf("LintOptionalDepMarkerPosition: %v", err)
	}
	got := findingsForRule(res.Findings, "forgeconv-optional-dep-marker-position")
	if len(got) != 2 {
		t.Fatalf("expected 2 findings, got %d:\n%s", len(got), res.FormatText())
	}
	for _, f := range got {
		if f.Severity != SeverityError {
			t.Errorf("rule should be an error (silent failure is the bug we're catching), got %s", f.Severity)
		}
		if !strings.Contains(f.Message, "Deps") {
			t.Errorf("message should mention Deps; got: %s", f.Message)
		}
		if !strings.Contains(f.Remediation, "directly above") {
			t.Errorf("remediation should explain the placement rule; got: %s", f.Remediation)
		}
	}
}
