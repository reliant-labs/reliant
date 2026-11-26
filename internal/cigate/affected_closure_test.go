package cigate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

// closureOf fakes `go list -deps` for ./tools/docgen: it compiles
// tools/docgen and internal/render (with an embedded template), plus the
// standard library and a module-cache dependency.
func closureOf(root string) ClosureLister {
	return func(_ context.Context, _ string, _ []string, patterns []string) (io.Reader, error) {
		if strings.Join(patterns, ",") != "./tools/docgen/..." {
			return nil, errors.New("unexpected patterns " + strings.Join(patterns, ","))
		}
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		for _, p := range []map[string]any{
			{"Dir": "/goroot/src/fmt", "Standard": true, "GoFiles": []string{"print.go"}},
			{"Dir": "/gomodcache/x", "GoFiles": []string{"x.go"}},
			{"Dir": filepath.Join(root, "internal", "render"), "GoFiles": []string{"render.go"}, "EmbedFiles": []string{"tmpl/page.tmpl"}},
			{"Dir": filepath.Join(root, "tools", "docgen"), "GoFiles": []string{"main.go"}},
		} {
			if err := enc.Encode(p); err != nil {
				return nil, err
			}
		}
		return &buf, nil
	}
}

func TestGoClosureGatesExtraCheck(t *testing.T) {
	cases := []struct {
		name    string
		changed []string
		paths   []string
		listErr bool
		runs    bool
	}{
		{name: "a dependency's Go file", changed: []string{"internal/render/render.go"}, runs: true},
		{name: "an embedded file in the closure", changed: []string{"internal/render/tmpl/page.tmpl"}, runs: true},
		{name: "a new Go file in a closure package", changed: []string{"internal/render/new.go"}, runs: true},
		{name: "go.mod", changed: []string{"go.mod"}, runs: true},
		{name: "a test file of a closure package", changed: []string{"internal/render/render_test.go"}, runs: false},
		{name: "a package outside the closure", changed: []string{"internal/other/x.go"}, runs: false},
		{name: "outside closure but matches paths", changed: []string{"docs/a.md"}, paths: []string{"docs/**"}, runs: true},
		{name: "go list fails: runs", changed: []string{"internal/other/x.go"}, listErr: true, runs: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			list := closureOf(root)
			if tc.listErr {
				list = func(context.Context, string, []string, []string) (io.Reader, error) {
					return nil, errors.New("boom")
				}
			}
			checks := []Check{{Name: "docs-generate", GoClosure: []string{"./tools/docgen/..."}, Paths: tc.paths}}
			a := Affected{Changed: tc.changed, BaseRef: "origin/main"}
			a = ResolveGoClosures(context.Background(), root, nil, checks, a, list)
			got := GateByPaths(checks, a)[0]
			if runs := got.NotApplicable == ""; runs != tc.runs {
				t.Fatalf("runs = %v, want %v (N/A %q, note %q)", runs, tc.runs, got.NotApplicable, got.Note)
			}
			if !tc.runs && !strings.Contains(got.NotApplicable, "Go build closure of ./tools/docgen/...") {
				t.Errorf("N/A reason = %q", got.NotApplicable)
			}
		})
	}
}

// In full mode (no changed files) go list is never run and the check runs.
func TestGoClosureFullModeRuns(t *testing.T) {
	checks := []Check{{Name: "docs-generate", GoClosure: []string{"./tools/docgen/..."}}}
	a := ResolveGoClosures(context.Background(), t.TempDir(), nil, checks, Affected{Full: true}, func(context.Context, string, []string, []string) (io.Reader, error) {
		t.Fatal("go list ran in full mode")
		return nil, nil
	})
	if got := GateByPaths(checks, a)[0]; got.NotApplicable != "" {
		t.Fatalf("N/A in full mode: %q", got.NotApplicable)
	}
}
