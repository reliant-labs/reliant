package templates

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// A scaffolded project lints with revive's blank-imports rule
// (project/golangci.yml.tmpl), which refuses a blank import outside a main or
// test package unless a comment justifies it. Every Go file forge writes must
// pass the linter it ships, or `forge lint` fails on a project nobody has
// touched — which is how cmd-tree-metrics.go.tmpl's bare
// `_ "<module>/internal/metrics"` reached every project, and control-plane's
// lint, through one pin.
//
// TestE2EFreshScaffoldLintExitsZero catches this too, but only after a full
// scaffold and golangci-lint run; this checks the templates themselves, in
// milliseconds, under -short.
func TestGoTemplatesJustifyBlankImports(t *testing.T) {
	blankImport := regexp.MustCompile(`^\s*_\s+"[^"]+"`)
	packageMain := regexp.MustCompile(`(?m)^package main\b`)
	// A file behind a build constraint is never compiled by the lint run:
	// the scaffolded golangci.yml sets no build-tags, and the constraints
	// templates carry (tools, e2e, ignore) are not default tags. tools.go is
	// the canonical blank-import file and lives there on purpose.
	buildConstrained := regexp.MustCompile(`(?m)^//go:build `)

	checked := 0
	err := fs.WalkDir(templateFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go.tmpl") {
			return err
		}
		if strings.HasSuffix(path, "_test.go.tmpl") {
			return nil // revive allows blank imports in test packages
		}
		body, err := fs.ReadFile(templateFS, path)
		if err != nil {
			return err
		}
		if packageMain.Match(body) {
			return nil // and in main packages
		}
		if buildConstrained.Match(body) {
			return nil
		}
		checked++
		lines := strings.Split(string(body), "\n")
		for i, line := range lines {
			if !blankImport.MatchString(line) || strings.Contains(line, "//") {
				continue
			}
			if i > 0 && strings.HasPrefix(strings.TrimSpace(lines[i-1]), "//") {
				continue
			}
			t.Errorf("%s:%d: %s — a blank import outside a main or test package needs a comment, on the line above or the same line, saying why it is linked (revive blank-imports, which every scaffolded project runs)",
				path, i+1, strings.TrimSpace(line))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Fatal("no Go templates were checked; the walk is not looking where the templates are")
	}
}
