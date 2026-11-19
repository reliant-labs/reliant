package lint

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func writeGoMarkerFixture(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, src := range files {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// Reliant's `//forge:exclude-contract — <reason>` was silently not a
// directive. It must fail the lint with file:line and the documented form.
func TestGoMarkersLint_ReportsMalformedAndUnknownMarkers(t *testing.T) {
	root := t.TempDir()
	writeGoMarkerFixture(t, root, map[string]string{
		"internal/strategy/doc.go": "// Package strategy.\n//\n//forge:exclude-contract — strategy registry\npackage strategy\n",
		"internal/typo/doc.go":     "//forge:exclude-contrat: typo\npackage typo\n",
		"internal/ok/doc.go":       "// Package ok mentions forge:exclude-contract in prose.\n//\n//forge:exclude-contract: a real reason\npackage ok\n",
		// A marker inside a string literal is fixture text, not a comment.
		"internal/lit/lit.go": "package lit\n\nconst src = `//forge:exclude-contract — inside a literal`\n",
		// Fixtures are not the project's source.
		"internal/x/testdata/bad.go": "//forge:nonsense\npackage bad\n",
		// A suppression covers its line like any forge rule.
		"internal/sup/doc.go": "// forge:lint-disable-next-line forge-go-markers: exercising the parser\n//forge:exclude-contract — fixture\npackage sup\n",
	})

	findings, err := collectGoMarkerFindings(root)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range findings {
		got[f.File+":"+strconv.Itoa(f.Line)] = f.Message + " | " + f.Remediation
		if f.Severity != "error" {
			t.Errorf("%s:%d severity %s, want error (the marker is a silent no-op)", f.File, f.Line, f.Severity)
		}
	}
	if len(got) != 2 {
		t.Fatalf("want exactly the em-dash and the typo, got %d:\n%v", len(got), got)
	}
	emdash := got["internal/strategy/doc.go:3"]
	if !strings.Contains(emdash, "forge:exclude-contract") || !strings.Contains(emdash, "//forge:exclude-contract: <reason>") {
		t.Errorf("em-dash finding must name the marker and the expected form, got %q", emdash)
	}
	if typo := got["internal/typo/doc.go:1"]; !strings.Contains(typo, `did you mean "forge:exclude-contract"`) {
		t.Errorf("typo finding must suggest the near miss, got %q", typo)
	}
}

func TestGoMarkersLint_CleanTree(t *testing.T) {
	root := t.TempDir()
	writeGoMarkerFixture(t, root, map[string]string{
		"internal/a/doc.go": "//forge:not-a-component: CLI only\npackage a\n\n// New builds it.\n//\n// forge:constructor\nfunc New() {}\n",
	})
	findings, err := collectGoMarkerFindings(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("clean tree reported %v", findings)
	}
}
