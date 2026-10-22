// Copyright (c) 2025 Reliant Labs
package main

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// The web lane's import graph. vitest's own `related` mode builds the same
// graph by transforming every test file (~25s of CPU for this suite); scanning
// specifiers is milliseconds. It over-approximates on purpose: every quoted
// "./x", "../x" or "@/x" in a file is an edge, whether it sits in an import, a
// dynamic import(), a vi.mock(), an import.meta.glob or a plain string. A test
// selected for no reason costs seconds; one wrongly skipped costs a red main.

var webSpecifier = regexp.MustCompile(`["'` + "`" + `]((?:\.{1,2}(?:/[^"'` + "`" + `\s]*)?|@/[^"'` + "`" + `\s]*))["'` + "`" + `]`)

// typeOnly matches `import type ... from "x"` and `export type ... from "x"`:
// frontends/web/tsconfig sets verbatimModuleSyntax, so esbuild erases them and they are
// never a runtime edge. (`import { type A } from "x"` is NOT erased: it stays
// as a side-effect import, so it is kept.)
var typeOnly = regexp.MustCompile(`(?s)\b(?:import|export)\s+type\b[^;"']*?\bfrom\s*["'][^"']*["']`)

var webExts = []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".json", ".css"}

// isWebCode is a file whose text the graph scans.
func isWebCode(p string) bool {
	if !strings.HasPrefix(p, "frontends/web/") {
		return false
	}
	switch path.Ext(p) {
	case ".ts", ".tsx", ".js", ".jsx", ".mjs":
		return true
	}
	return false
}

// IsWebTestFile is what vitest's default include picks up, minus the Playwright specs.
func IsWebTestFile(p string) bool {
	if !strings.HasPrefix(p, "frontends/web/") || strings.HasPrefix(p, "frontends/web/e2e/") || strings.Contains(p, "/node_modules/") {
		return false
	}
	b := path.Base(p)
	for _, suf := range []string{".test.ts", ".test.tsx", ".test.js", ".test.jsx", ".spec.ts", ".spec.tsx"} {
		if strings.HasSuffix(b, suf) {
			return true
		}
	}
	return false
}

// fileReadMarkers make a web test read files vitest's graph cannot see.
var fileReadMarkers = []string{"readFileSync", "readdirSync", "existsSync", "statSync", "readFile(", "readdir(", "globSync", "fast-glob", "process.cwd()", "__dirname"}

// walkMarkers make a web test read a whole tree: any web source change selects it.
var walkMarkers = []string{"readdirSync", "readdir(", "globSync", "fast-glob", "import.meta.glob", "walkDir", "walkSync"}

// WebGraph is the import graph of frontends/web/, as the files reaching each test.
type WebGraph struct {
	// Tests are the vitest test files (repo-relative).
	Tests []string
	// Reach[f] is the set of tests whose import closure contains f (f included
	// for a test file).
	Reach map[string][]string
	// Text is the source of tests that read files at run time.
	Text map[string]string
	// Walkers are tests that read whole trees.
	Walkers []string
}

// BuildWebGraph scans the frontends/web/ sources under root. files is every tracked or
// untracked repo path; read returns a file's text.
func BuildWebGraph(files []string, read func(string) (string, bool)) *WebGraph {
	exists := map[string]bool{}
	var code []string
	for _, f := range files {
		if strings.HasPrefix(f, "frontends/web/") {
			exists[f] = true
		}
		if isWebCode(f) {
			code = append(code, f)
		}
	}
	sort.Strings(code)

	deps := map[string][]string{}
	wg := &WebGraph{Reach: map[string][]string{}, Text: map[string]string{}}
	for _, f := range code {
		text, ok := read(f)
		if !ok {
			continue
		}
		deps[f] = webDeps(f, text, exists)
		if !IsWebTestFile(f) {
			continue
		}
		wg.Tests = append(wg.Tests, f)
		if containsAny(text, fileReadMarkers) {
			wg.Text[f] = text
		}
		if containsAny(text, walkMarkers) {
			wg.Walkers = append(wg.Walkers, f)
		}
	}
	for _, t := range wg.Tests {
		seen := map[string]bool{t: true}
		stack := []string{t}
		for len(stack) > 0 {
			f := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			for _, d := range deps[f] {
				if !seen[d] {
					seen[d] = true
					stack = append(stack, d)
				}
			}
		}
		for f := range seen {
			wg.Reach[f] = append(wg.Reach[f], t)
		}
	}
	for f := range wg.Reach {
		sort.Strings(wg.Reach[f])
	}
	return wg
}

func containsAny(text string, markers []string) bool {
	for _, m := range markers {
		if strings.Contains(text, m) {
			return true
		}
	}
	return false
}

// webDeps are the tracked files the specifiers of one file may name.
func webDeps(from, text string, exists map[string]bool) []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	text = typeOnly.ReplaceAllString(text, "")
	for _, m := range webSpecifier.FindAllStringSubmatch(text, -1) {
		spec := m[1]
		if i := strings.IndexAny(spec, "?#"); i >= 0 {
			spec = spec[:i]
		}
		var target string
		if strings.HasPrefix(spec, "@/") {
			target = path.Join("frontends/web/src", strings.TrimPrefix(spec, "@/"))
		} else {
			target = path.Join(path.Dir(from), spec)
		}
		if strings.ContainsAny(target, "*?[{") {
			for f := range exists {
				if matchGlob(target, f) {
					add(f)
				}
			}
			continue
		}
		for _, f := range resolveWeb(target, exists) {
			add(f)
		}
	}
	return out
}

// resolveWeb returns the file(s) a specifier can resolve to: the path itself,
// with an extension, as a directory index, or with a TS ESM ".js" spelled for a
// ".ts" source.
func resolveWeb(target string, exists map[string]bool) []string {
	var out []string
	if exists[target] {
		out = append(out, target)
	}
	if ext := path.Ext(target); ext == ".js" || ext == ".jsx" || ext == ".mjs" {
		stem := strings.TrimSuffix(target, ext)
		for _, e := range []string{".ts", ".tsx"} {
			if exists[stem+e] {
				out = append(out, stem+e)
			}
		}
	}
	for _, e := range webExts {
		if exists[target+e] {
			out = append(out, target+e)
		}
		if exists[path.Join(target, "index"+e)] {
			out = append(out, path.Join(target, "index"+e))
		}
	}
	return out
}

// diskReader reads repo-relative files under root.
func diskReader(root string) func(string) (string, bool) {
	return func(p string) (string, bool) {
		b, err := os.ReadFile(filepath.Join(root, p))
		if err != nil {
			return "", false
		}
		return string(b), true
	}
}
