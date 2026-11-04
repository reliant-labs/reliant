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

	"gopkg.in/yaml.v3"
)

// repoRoot is the checkout this package sits in (tools/test-affected).
const repoRoot = "../.."

// TestDocGenTargetsCoverEveryDocsGenerator pins the docs-generate lane to the
// generators it stands for, so neither half can drift from the other:
//
//   - Taskfile.yml's generate:docs runs every task that runs a tools/docgen
//     program or the Mintlify conversion, and every tools/docgen program has
//     one;
//   - every path those tasks pass (their inputs and outputs), every repo
//     path a generator's Go source names, and every directory the Mintlify
//     script joins onto the repo root is in docGenPaths;
//   - the gates CI runs check the whole list (the release gate runs `task
//     generate:docs:check`, the merge gate `task generate:docs` when the
//     diff reaches a docs input), and neither they nor ci.yml spell a docs
//     task themselves.
//
// It reads every file it judges itself, so a cached pass is never stale.
func TestDocGenTargetsCoverEveryDocsGenerator(t *testing.T) {
	tf := parseTaskfile(t, filepath.Join(repoRoot, "Taskfile.yml"))
	targets := tf.subtasks("generate:docs")
	if len(targets) == 0 {
		t.Fatal("Taskfile.yml's generate:docs runs no task")
	}

	// Which tasks are docs generators, and which generators they run.
	var docsTargets []string
	ran := map[string]bool{}
	genRun := regexp.MustCompile(`\./tools/docgen/([^/\s]+)/\.\.\.`)
	for _, name := range slices.Sorted(maps.Keys(tf.Tasks)) {
		recipe := strings.Join(tf.commands(name), "\n")
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
			t.Errorf("task %s runs a docs generator but generate:docs does not run it: task generate:docs:check (and so CI and task test:affected) would not check it", name)
		}
	}
	for _, name := range targets {
		if !slices.Contains(docsTargets, name) {
			t.Errorf("generate:docs runs %s, which runs no docs generator (or is not a task)", name)
		}
	}
	entries, err := os.ReadDir(filepath.Join(repoRoot, "tools", "docgen"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() && !ran[e.Name()] {
			t.Errorf("tools/docgen/%s is run by no generate:docs task: add a generate:docs:<name> task for it to generate:docs, or delete it", e.Name())
		}
	}

	// Every path a task passes.
	for _, name := range targets {
		for _, line := range tf.commands(name) {
			for _, tok := range strings.Fields(line) {
				p := strings.TrimSuffix(strings.TrimPrefix(tok, "./"), "/...")
				if !strings.Contains(p, "/") || strings.HasPrefix(p, "-") || strings.HasPrefix(p, "$") || strings.Contains(p, "{{") {
					continue
				}
				if !docGenCovers(t, p) {
					t.Errorf("task %s passes %s, which is not in docGenPaths (tools/test-affected/rules.go): a change to it would not run the docs check", name, p)
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

	// The gates, and the CI that wraps them, call the one list rather than
	// keeping a copy.
	read := func(rel string) []byte {
		b, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	for file, want := range map[string]string{
		"scripts/release-gate.sh": "task generate:docs:check",
		"scripts/merge-gate.sh":   "task generate:docs",
	} {
		if !strings.Contains(string(read(file)), want) {
			t.Errorf("%s must run `%s` (the docs-generate check)", file, want)
		}
	}
	for _, file := range []string{".github/workflows/ci.yml", "scripts/release-gate.sh", "scripts/merge-gate.sh"} {
		src := read(file)
		for _, name := range targets {
			if regexp.MustCompile(`(^|[^\w:-])` + regexp.QuoteMeta(name) + `($|[^\w:-])`).Match(src) {
				t.Errorf("%s names %s itself: run `task generate:docs` / `task generate:docs:check`, so the docs tasks are listed once (generate:docs)", file, name)
			}
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

// taskfile is the little of a Taskfile this test reads: the top-level string
// vars and each task's cmds.
type taskfile struct {
	Vars  map[string]any `yaml:"vars"`
	Tasks map[string]struct {
		Cmds []any `yaml:"cmds"`
	} `yaml:"tasks"`
}

var taskRef = regexp.MustCompile(`\{\{\s*\.([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)

func parseTaskfile(t *testing.T, file string) *taskfile {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var tf taskfile
	if err := yaml.Unmarshal(b, &tf); err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	return &tf
}

// commands are a task's shell commands (a bare string or a `cmd:`), with the
// top-level string vars substituted.
func (tf *taskfile) commands(name string) []string {
	var out []string
	for _, c := range tf.Tasks[name].Cmds {
		switch c := c.(type) {
		case string:
			out = append(out, tf.expand(c))
		case map[string]any:
			if s, ok := c["cmd"].(string); ok {
				out = append(out, tf.expand(s))
			}
		}
	}
	return out
}

// subtasks are the tasks a task's cmds run (`- task: <name>`), in order.
func (tf *taskfile) subtasks(name string) []string {
	var out []string
	for _, c := range tf.Tasks[name].Cmds {
		if m, ok := c.(map[string]any); ok {
			if s, ok := m["task"].(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

// expand substitutes the string vars the Taskfile assigns; others (an `sh:`
// var, a CLI or environment variable) are left as they are.
func (tf *taskfile) expand(s string) string {
	for range 10 {
		next := taskRef.ReplaceAllStringFunc(s, func(ref string) string {
			if v, ok := tf.Vars[taskRef.FindStringSubmatch(ref)[1]].(string); ok {
				return v
			}
			return ref
		})
		if next == s {
			break
		}
		s = next
	}
	return strings.TrimSpace(s)
}
