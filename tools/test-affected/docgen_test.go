// Copyright (c) 2025 Reliant Labs
package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// repoRoot is the checkout this package sits in (tools/test-affected).
const repoRoot = "../.."

// TestDocGenTargetsCoverEveryDocsGenerator pins the docs-generate lane to the
// generators it stands for, so neither half can drift from the other:
//
//   - DOCGEN_TARGETS in the Makefile names every target that runs a
//     tools/docgen program or the Mintlify conversion, and every
//     tools/docgen program has one;
//   - every path those recipes pass (their inputs and outputs), every repo
//     path a generator's Go source names, and every directory the Mintlify
//     script joins onto the repo root is in docGenPaths;
//   - CI runs `make docs-generate-check` and spells no docs target itself.
//
// It reads every file it judges itself, so a cached pass is never stale.
func TestDocGenTargetsCoverEveryDocsGenerator(t *testing.T) {
	mk := parseMakefile(t, filepath.Join(repoRoot, "Makefile"))
	targets := strings.Fields(mk.expand(mk.vars["DOCGEN_TARGETS"]))
	if len(targets) == 0 {
		t.Fatal("the Makefile defines no DOCGEN_TARGETS")
	}

	// Which targets are docs generators, and which generators they run.
	var docsTargets []string
	ran := map[string]bool{}
	genRun := regexp.MustCompile(`\./tools/docgen/([^/\s]+)/\.\.\.`)
	for _, name := range slices.Sorted(maps.Keys(mk.recipes)) {
		recipe := mk.expand(strings.Join(mk.recipes[name], "\n"))
		ms := genRun.FindAllStringSubmatch(recipe, -1)
		if len(ms) == 0 && !strings.Contains(recipe, "scripts/generate-mintlify-reference.py") {
			continue
		}
		docsTargets = append(docsTargets, name)
		for _, m := range ms {
			ran[m[1]] = true
		}
	}
	for _, name := range docsTargets {
		if !slices.Contains(targets, name) {
			t.Errorf("make %s runs a docs generator but is not in DOCGEN_TARGETS: make docs-generate-check (and so CI and make test-affected) would not check it", name)
		}
	}
	for _, name := range targets {
		if !slices.Contains(docsTargets, name) {
			t.Errorf("DOCGEN_TARGETS names %s, which runs no docs generator (or is not a target)", name)
		}
	}
	entries, err := os.ReadDir(filepath.Join(repoRoot, "tools", "docgen"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() && !ran[e.Name()] {
			t.Errorf("tools/docgen/%s is run by no DOCGEN_TARGETS recipe: add its make target to DOCGEN_TARGETS, or delete it", e.Name())
		}
	}

	// Every path a recipe passes.
	for _, name := range targets {
		for _, line := range mk.recipes[name] {
			line = strings.TrimSpace(mk.expand(line))
			if strings.HasPrefix(strings.TrimPrefix(line, "@"), "echo") {
				continue
			}
			for _, tok := range strings.Fields(line) {
				p := strings.TrimSuffix(strings.TrimPrefix(tok, "./"), "/...")
				if !strings.Contains(p, "/") || strings.HasPrefix(p, "-") || strings.HasPrefix(p, "$") {
					continue
				}
				if !docGenCovers(t, p) {
					t.Errorf("make %s passes %s, which is not in docGenPaths (tools/test-affected/rules.go): a change to it would not run the docs check", name, p)
				}
			}
		}
	}

	// Every repo path a generator's Go source names (presetsDir, the nodes
	// generator's Go output, ...).
	_ = filepath.WalkDir(filepath.Join(repoRoot, "tools", "docgen"), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		src, _ := filepath.Rel(repoRoot, p)
		for _, lit := range goStringLiterals(t, p) {
			if strings.ContainsAny(lit, " \t\n") || !strings.Contains(lit, "/") || path.IsAbs(lit) {
				continue
			}
			if _, err := os.Stat(filepath.Join(repoRoot, filepath.FromSlash(lit))); err != nil {
				continue // not a path in this repo
			}
			if !docGenCovers(t, lit) {
				t.Errorf("%s names %s, which is not in docGenPaths (tools/test-affected/rules.go): a change to it would not run the docs check", filepath.ToSlash(src), lit)
			}
		}
		return nil
	})

	// The Mintlify conversion: ROOT / "a" / "b" is a directory it reads or writes.
	py, err := os.ReadFile(filepath.Join(repoRoot, "scripts", "generate-mintlify-reference.py"))
	if err != nil {
		t.Fatal(err)
	}
	seg := regexp.MustCompile(`"([^"]+)"`)
	for _, m := range regexp.MustCompile(`ROOT((?:\s*/\s*"[^"]+")+)`).FindAllStringSubmatch(string(py), -1) {
		var parts []string
		for _, s := range seg.FindAllStringSubmatch(m[1], -1) {
			parts = append(parts, s[1])
		}
		if dir := path.Join(parts...); !docGenCovers(t, dir) {
			t.Errorf("scripts/generate-mintlify-reference.py reads or writes %s, which is not in docGenPaths (tools/test-affected/rules.go)", dir)
		}
	}

	// CI calls the one list rather than keeping a copy.
	ci, err := os.ReadFile(filepath.Join(repoRoot, ".github", "workflows", "pr-ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ci), "make docs-generate-check") {
		t.Error(".github/workflows/pr-ci.yml must run `make docs-generate-check` (CI's \"Generated code is up to date\" job)")
	}
	for _, name := range targets {
		if regexp.MustCompile(`(^|[^\w-])` + regexp.QuoteMeta(name) + `($|[^\w-])`).Match(ci) {
			t.Errorf(".github/workflows/pr-ci.yml names %s itself: run `make docs-generate-check`, so the docs targets are listed once (DOCGEN_TARGETS)", name)
		}
	}
}

// docGenCovers reports whether the repo path p is a docs-generate input: a
// file (or a path not yet written) by docGenPaths, a directory when any file
// directly in it is.
func docGenCovers(t *testing.T, p string) bool {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(repoRoot, filepath.FromSlash(p)))
	if err != nil {
		return isDocGenPath(p) // a file, or an output not written yet
	}
	for _, e := range entries {
		if !e.IsDir() && isDocGenPath(path.Join(p, e.Name())) {
			return true
		}
	}
	return false
}

func goStringLiterals(t *testing.T, file string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		if _, ok := n.(*ast.ImportSpec); ok {
			return false
		}
		if b, ok := n.(*ast.BasicLit); ok {
			if s, ok := strLit(b); ok {
				out = append(out, s)
			}
		}
		return true
	})
	return out
}

// makefile is the little of a Makefile this test reads: simple variable
// assignments and each rule's recipe (continuation lines joined).
type makefile struct {
	vars    map[string]string
	recipes map[string][]string
}

var (
	makeAssign = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)\s*:?=\s*(.*)$`)
	makeRule   = regexp.MustCompile(`^([A-Za-z0-9_.-]+):(?:\s|$)`)
	makeRef    = regexp.MustCompile(`\$\(([A-Za-z_][A-Za-z0-9_]*)\)`)
)

func parseMakefile(t *testing.T, file string) *makefile {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	mk := &makefile{vars: map[string]string{}, recipes: map[string][]string{}}
	var lines []string
	for _, l := range strings.Split(string(b), "\n") {
		if n := len(lines); n > 0 && strings.HasSuffix(lines[n-1], "\\") {
			lines[n-1] = strings.TrimSuffix(lines[n-1], "\\") + " " + strings.TrimSpace(l)
			continue
		}
		lines = append(lines, l)
	}
	current := ""
	for _, l := range lines {
		if strings.HasPrefix(l, "\t") {
			if current != "" {
				mk.recipes[current] = append(mk.recipes[current], strings.TrimPrefix(l, "\t"))
			}
			continue
		}
		current = ""
		if m := makeRule.FindStringSubmatch(l); m != nil && !strings.Contains(l, ":=") {
			current = m[1]
			if _, ok := mk.recipes[current]; !ok {
				mk.recipes[current] = nil
			}
			continue
		}
		if m := makeAssign.FindStringSubmatch(l); m != nil {
			if _, seen := mk.vars[m[1]]; !seen {
				mk.vars[m[1]] = m[2]
			}
		}
	}
	return mk
}

// expand substitutes the variables the Makefile assigns; others are left as
// they are (a $(shell ...) or an automatic variable).
func (mk *makefile) expand(s string) string {
	for range 10 {
		next := makeRef.ReplaceAllStringFunc(s, func(ref string) string {
			if v, ok := mk.vars[makeRef.FindStringSubmatch(ref)[1]]; ok {
				return v
			}
			return ref
		})
		if next == s {
			break
		}
		s = next
	}
	return s
}
