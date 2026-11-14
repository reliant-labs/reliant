package lint

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
)

// analyzerFixtureGo is shaped like forge's own
// internal/linter/slowtest/testdata/src/a/a_test.go: an analysistest fixture
// whose import block goimports would regroup (the bare "helper" import reads
// as stdlib, and the module import belongs in the local group). Regrouping
// adds a line, which moves every `// want` below it off the line the analyzer
// reports — the analyzer's tests go red over a file nobody edited.
const analyzerFixtureGo = `package a

import (
	"testing"

	"helper"

	"example.com/thing/internal/x"
	"github.com/foo/bar"
)

func TestA(t *testing.T) { // want ` + "`TestA is not marked slow`" + `
	_, _, _ = helper.H, x.X, bar.B
}
`

// TestFormatLane_NeverTouchesOrReportsTestdata pins that no lint rewrite
// path — the report-only diff (the default), --fix, a --scope or positional
// path naming the fixture, and the frontend prettier walk — rewrites or
// reports a file under a path segment named testdata.
func TestFormatLane_NeverTouchesOrReportsTestdata(t *testing.T) {
	root := newFormatProject(t)
	const fixture = "internal/linter/x/testdata/src/a/a_test.go"
	writeFile(t, root, fixture, analyzerFixtureGo)
	// A testdata directory that is not the walk's first level below a
	// source tree, and one directly under it.
	writeFile(t, root, "pkg/testdata/golden.go", unformattedGo)
	// The positive control: an owned file the lane must still report and fix.
	writeFile(t, root, "internal/foo/bar.go", unformattedGo)
	testdataFiles := []string{fixture, "pkg/testdata/golden.go"}

	assertUntouched := func(t *testing.T) {
		t.Helper()
		if got := readRel(t, root, fixture); got != analyzerFixtureGo {
			t.Errorf("%s was rewritten:\n%s", fixture, got)
		}
		if got := readRel(t, root, "pkg/testdata/golden.go"); got != unformattedGo {
			t.Errorf("pkg/testdata/golden.go was rewritten:\n%s", got)
		}
	}
	reported := func(pending []formatPending) []string {
		var files []string
		for _, p := range pending {
			files = append(files, p.File)
		}
		return files
	}

	t.Run("report-only diff", func(t *testing.T) {
		pending, err := collectFormatPending(context.Background(), root, nil, false, nil)
		if err != nil {
			t.Fatalf("collectFormatPending: %v", err)
		}
		files := reported(pending)
		for _, f := range testdataFiles {
			if slices.Contains(files, f) {
				t.Errorf("report-only lane reported %s: %v", f, files)
			}
		}
		if !slices.Contains(files, "internal/foo/bar.go") {
			t.Errorf("report-only lane missed the owned control file: %v", files)
		}
		assertUntouched(t)
	})

	t.Run("a scope or path naming the fixture", func(t *testing.T) {
		scope, err := parseLintScope(root, []string{"internal/linter/x/testdata"})
		if err != nil {
			t.Fatalf("parseLintScope: %v", err)
		}
		if o := formatOutcome(context.Background(), root, nil, scope, nil, true); o.gated || len(o.findings) != 0 {
			t.Errorf("--scope on testdata: gated=%v findings=%+v", o.gated, o.findings)
		}
		pending, err := collectFormatPending(context.Background(), root, keepForPaths([]string{"./internal/linter/x/testdata/..."}), false, nil)
		if err != nil {
			t.Fatalf("collectFormatPending: %v", err)
		}
		if len(pending) != 0 {
			t.Errorf("positional testdata path reported %v", reported(pending))
		}
		assertUntouched(t)
	})

	t.Run("--fix", func(t *testing.T) {
		noTypeErrors := func(string, []string) map[string]string { return nil }
		changed, reverted, err := formatGoTreeVerified(root, nil, noTypeErrors)
		if err != nil {
			t.Fatalf("formatGoTreeVerified: %v", err)
		}
		if len(reverted) != 0 {
			t.Errorf("reverted = %+v", reverted)
		}
		want := []string{filepath.FromSlash("internal/foo/bar.go")}
		if !slices.Equal(changed, want) {
			t.Errorf("changed = %v, want %v (the control only)", changed, want)
		}
		assertUntouched(t)
	})

	t.Run("frontend prettier walk", func(t *testing.T) {
		feDir := filepath.Join(root, "frontends", "web")
		writeFile(t, root, "frontends/web/src/testdata/fixture.ts", "export const  x=1\n")
		writeFile(t, root, "frontends/web/src/components/__tests__/testdata/case.tsx", "export const  y=2\n")
		writeFile(t, root, "frontends/web/src/app/page.tsx", "export const  z=3\n")
		files, err := ownedFrontendSources(root, feDir)
		if err != nil {
			t.Fatalf("ownedFrontendSources: %v", err)
		}
		want := []string{filepath.FromSlash("src/app/page.tsx")}
		if !slices.Equal(files, want) {
			t.Errorf("prettier would see %v, want %v (no testdata)", files, want)
		}
	})
}

func TestIsTestdataPath(t *testing.T) {
	for rel, want := range map[string]bool{
		"internal/linter/slowtest/testdata/src/a/a_test.go": true,
		"testdata/x.go":                        true,
		"pkg/testdata":                         true,
		"internal/testdatagen/x.go":            false,
		"internal/mytestdata/x.go":             false,
		"internal/foo/testdata_helpers.go":     false,
		filepath.Join("a", "testdata", "b.go"): true,
	} {
		if got := isTestdataPath(rel); got != want {
			t.Errorf("isTestdataPath(%q) = %v, want %v", rel, got, want)
		}
	}
}
