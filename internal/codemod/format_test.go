package codemod

import (
	"go/format"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/imports"
)

// The shared post-codemod step (format.go). These tests drive it with a fake
// codemod so they are fast; the real codemods over real fixtures are in
// format_codemods_test.go.

// formatFindings are the files among rels whose bytes the project's linters
// would flag: gofmt, or goimports with localPrefix. Both oracles are the
// engines golangci-lint's formatters run, not forge's own formatter, so the
// step cannot grade itself.
func formatFindings(t *testing.T, dir, localPrefix string, rels []string) []string {
	t.Helper()
	var out []string
	for _, rel := range rels {
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		src, err := os.ReadFile(abs)
		if err != nil {
			t.Fatal(err)
		}
		if g, err := format.Source(src); err != nil {
			t.Fatalf("%s does not parse: %v", rel, err)
		} else if string(g) != string(src) {
			out = append(out, rel+": gofmt")
		}
		prev := imports.LocalPrefix
		imports.LocalPrefix = localPrefix
		g, err := imports.Process(abs, src, &imports.Options{Comments: true, TabIndent: true, TabWidth: 8})
		imports.LocalPrefix = prev
		if err != nil {
			t.Fatalf("%s: goimports: %v", rel, err)
		}
		if string(g) != string(src) {
			out = append(out, rel+": goimports")
		}
	}
	sort.Strings(out)
	return out
}

func writeProject(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// A codemod that writes imports the way astutil places them: the forge import
// lands in the module's own group because both start github.com/reliant-labs/.
// That is control-plane's 39 goimports findings.
const astutilPlaced = `package foo

import (
	"context"
	"errors"

	"github.com/reliant-labs/control-plane/internal/store"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

func Use(ctx context.Context) error {
	_ = store.X
	return svcerr.NotFound("x")
}

func Other() error { return errors.New("y") }
`

const gofmtDirty = "package foo\n\nfunc Use() {\n        println(\"x\")\n}\n"

// fakeCodemod writes content over rel, the way a real codemod's writeFile does,
// and reports a rewrite.
func fakeCodemod(rel, content string) runFunc {
	return func(dir string, opts Options) (Report, error) {
		if opts.Write {
			if err := writeFile(filepath.Join(dir, filepath.FromSlash(rel)), []byte(content)); err != nil {
				return Report{}, err
			}
		}
		return Report{Rewrites: []string{rel}}, nil
	}
}

func TestFormatAfter_LeavesWhatACodemodWroteFormattedToTheProjectsGate(t *testing.T) {
	const module = "github.com/reliant-labs/control-plane"
	golangci := func(prefixes string) string {
		return "version: \"2\"\nformatters:\n  enable:\n    - goimports\n" + prefixes
	}
	for _, tc := range []struct {
		name, config, content string
		// wantPrefix is the local-prefix the project's gate applies; "" none.
		wantPrefix string
		// ungated: no formatter finding is possible to assert (gci owns grouping).
		ungated bool
	}{
		{
			name:       "goimports with the module as its local prefix (control-plane)",
			config:     golangci("  settings:\n    goimports:\n      local-prefixes:\n        - " + module + "\n"),
			content:    astutilPlaced,
			wantPrefix: module,
		},
		{
			name:       "goimports with a different local prefix",
			config:     golangci("  settings:\n    goimports:\n      local-prefixes:\n        - github.com/reliant-labs/forge\n"),
			content:    astutilPlaced,
			wantPrefix: "github.com/reliant-labs/forge",
		},
		{
			name:       "no golangci config falls back to forge's convention, the module",
			content:    astutilPlaced,
			wantPrefix: module,
		},
		{
			name:       "gofmt-only whitespace",
			config:     golangci("  settings:\n    goimports:\n      local-prefixes:\n        - " + module + "\n"),
			content:    gofmtDirty,
			wantPrefix: module,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string]string{
				"go.mod":              "module " + module + "\n\ngo 1.22\n",
				"internal/foo/foo.go": "package foo\n",
			}
			if tc.config != "" {
				files[".golangci.yml"] = tc.config
			}
			dir := writeProject(t, files)
			const rel = "internal/foo/foo.go"

			// The codemod's own bytes carry the findings: the step is what removes them.
			raw := fakeCodemod(rel, tc.content)
			if _, err := raw(dir, Options{Write: true}); err != nil {
				t.Fatal(err)
			}
			if got := formatFindings(t, dir, tc.wantPrefix, []string{rel}); len(got) == 0 {
				t.Fatalf("fixture is vacuous: the raw codemod output has no formatting finding")
			}
			if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(rel)), []byte("package foo\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			rep, err := formatAfter(raw)(dir, Options{Write: true})
			if err != nil || !rep.Changed() {
				t.Fatalf("run: rep=%+v err=%v", rep, err)
			}
			if got := formatFindings(t, dir, tc.wantPrefix, []string{rel}); len(got) != 0 {
				t.Fatalf("the project's formatters still flag what the codemod wrote: %v\n%s", got, readFile(t, dir, rel))
			}
		})
	}
}

// gci owns grouping when it is enabled: the step applies gofmt and leaves the
// import blocks as the codemod placed them.
func TestFormatAfter_GciOwnsImportGrouping(t *testing.T) {
	dir := writeProject(t, map[string]string{
		"go.mod":              "module github.com/reliant-labs/control-plane\n\ngo 1.22\n",
		".golangci.yml":       "version: \"2\"\nformatters:\n  enable:\n    - gci\n    - goimports\n",
		"internal/foo/foo.go": "package foo\n",
	})
	if _, err := formatAfter(fakeCodemod("internal/foo/foo.go", astutilPlaced))(dir, Options{Write: true}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, dir, "internal/foo/foo.go"); got != astutilPlaced {
		t.Fatalf("gci owns grouping, but the step regrouped imports:\n%s", got)
	}
}

// Only files the run changed are touched: a hand-written file that the project's
// gate accepts, or one a codemod never opened, is not reformatted.
func TestFormatAfter_OnlyTouchesFilesTheRunChanged(t *testing.T) {
	const handWritten = "package bar\n\nfunc Keep() {\n        println(\"mine\")\n}\n" // gofmt-dirty on purpose
	dir := writeProject(t, map[string]string{
		"go.mod":              "module example.com/m\n\ngo 1.22\n",
		"internal/foo/foo.go": "package foo\n",
		"internal/bar/bar.go": handWritten,
	})
	if _, err := formatAfter(fakeCodemod("internal/foo/foo.go", gofmtDirty))(dir, Options{Write: true}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, dir, "internal/bar/bar.go"); got != handWritten {
		t.Fatalf("a file the codemod never wrote was reformatted:\n%s", got)
	}
	if got := readFile(t, dir, "internal/foo/foo.go"); strings.Contains(got, "        println") {
		t.Fatalf("the file the codemod wrote was not formatted:\n%s", got)
	}
}

// --dry-run writes nothing, and so does the step.
func TestFormatAfter_DryRunWritesNothing(t *testing.T) {
	dir := writeProject(t, map[string]string{
		"go.mod":              "module example.com/m\n\ngo 1.22\n",
		"internal/foo/foo.go": gofmtDirty,
	})
	if _, err := formatAfter(fakeCodemod("internal/foo/foo.go", "package foo\n"))(dir, Options{}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, dir, "internal/foo/foo.go"); got != gofmtDirty {
		t.Fatalf("a dry run changed a file:\n%s", got)
	}
}

// A codemod's own failure is returned as it is, and a file the codemod left
// unparseable is left for the compiler to report.
func TestFormatAfter_UnparseableOutputIsLeftAlone(t *testing.T) {
	const broken = "package foo\n\nfunc {\n"
	dir := writeProject(t, map[string]string{
		"go.mod":              "module example.com/m\n\ngo 1.22\n",
		"internal/foo/foo.go": "package foo\n",
	})
	if _, err := formatAfter(fakeCodemod("internal/foo/foo.go", broken))(dir, Options{Write: true}); err != nil {
		t.Fatalf("formatting is not the pass that reports a parse error: %v", err)
	}
	if got := readFile(t, dir, "internal/foo/foo.go"); got != broken {
		t.Fatalf("unparseable output was rewritten:\n%s", got)
	}
}

// register installs the step, so a codemod added tomorrow cannot reach
// `forge fix` or `forge project upgrade` without it: registry is only appended
// to there. Proven by registering a codemod whose Run writes unformatted Go and
// looking the result up the way both entry points do.
func TestRegisterInstallsTheFormatStep(t *testing.T) {
	saved := registry
	t.Cleanup(func() { registry = saved })
	register(Codemod{Name: "zz-format-probe", Run: fakeCodemod("internal/foo/foo.go", gofmtDirty)})
	c, ok := Lookup("zz-format-probe")
	if !ok {
		t.Fatal("probe codemod not registered")
	}
	dir := writeProject(t, map[string]string{
		"go.mod":              "module example.com/m\n\ngo 1.22\n",
		"internal/foo/foo.go": "package foo\n",
	})
	if _, err := c.Run(dir, Options{Write: true}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, dir, "internal/foo/foo.go"); strings.Contains(got, "        println") {
		t.Fatalf("a registered codemod's output was not formatted:\n%s", got)
	}
}
