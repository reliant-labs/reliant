// File: internal/linter/forgeconv/frontend_process_env_nodeonly.go
//
// Node-only build files: the modules no bundler ships.
//
// The rule's premise is that the bundler INLINES a process.env read into
// the artifact. That is true of code the bundler ships, and false of code
// that only ever runs in Node at build or dev-server time. The root
// *.config.* files were always exempt for that reason; this file extends the
// exemption to the two shapes that sit beside them and share their runtime:
//
//   - modules that ONLY build config imports — a Vite plugin in
//     ./vite-plugin-demo-bundle.ts, imported by vite.config.ts, runs inside
//     the Vite process and is never part of the bundle;
//   - a root scripts/ directory of Node entry points (`node scripts/x.mjs`:
//     smoke tests, codegen, Playwright drivers). No bundler takes an entry
//     from there.
//
// ── Reachability, not a directory list ────────────────────────────────
//
// A file qualifies by what imports it, not by where it sits. The Node roots
// are the root build config files and every file under scripts/; N is
// everything they reach through relative (or `@/`) imports. The bundled set
// B is everything the app's own files reach — every scanned file outside N
// that is not path-exempt (tests, mocks, generated output, Next.js
// server-only modules are not app bundles either). A file is Node-only when
// it is in N and not in B. So a helper that vite.config.ts AND a page both
// import stays scanned, and so does a scripts/ module the app imports.
//
// ── The direction of every miss is "scan it" ──────────────────────────
//
// Import extraction is textual and resolution knows relative specifiers and
// the scaffolds' `@/` alias (tsconfig `@/*` → `./src/*`) only. An import
// the resolver cannot follow FROM a Node root leaves its target scanned (a
// warning that is noise, not a hole). The one way a miss could hide a read
// is an app file importing a Node-reachable module through an alias the
// resolver does not know. That fails closed twice over: a Node-reachable file
// any unresolved app import could name (its last segment is the file's name)
// stays scanned, and an app file with a non-literal import() or require()
// exempts nothing at all. Nothing under the conventional app source roots
// (src/, app/, pages/) is ever exempted by reachability either — a Vite
// plugin or build script does not live there.

package forgeconv

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// importSpecifier matches the static and dynamic import forms whose
// specifier is a string literal: `import x from "…"`, `export … from "…"`,
// a bare `import "…"`, `import("…")` and `require("…")`. Capture 1 is the
// specifier, from whichever alternative matched.
var importSpecifier = regexp.MustCompile(
	`(?:\bfrom\s*|\bimport\s*\(?\s*|\brequire\s*\(\s*)["']([^"'\n]+)["']`)

// appSourceRoots are the frontend-relative top-level directories that hold
// app code by convention. Reachability never exempts a file under them; see
// the file header.
var appSourceRoots = map[string]bool{"src": true, "app": true, "pages": true}

// nodeOnlyFiles returns the subset of files (absolute paths under feDir)
// that only Node-run build code reaches. pathExempt reports whether a file
// is already allowlisted by path; such files are not app bundles, so they
// never pull a module into the bundled set.
func nodeOnlyFiles(feDir string, files []string, pathExempt func(string) bool) map[string]bool {
	known := make(map[string]bool, len(files))
	for _, f := range files {
		known[f] = true
	}
	imports := make(map[string][]string, len(files))
	for _, f := range files {
		imports[f] = resolvedImports(feDir, f, known)
	}

	var nodeRoots []string
	for _, f := range files {
		if isNodeRoot(feDir, f) {
			nodeRoots = append(nodeRoots, f)
		}
	}
	nodeReach := closure(nodeRoots, imports)
	if len(nodeReach) == 0 {
		return nil
	}

	var appRoots []string
	for _, f := range files {
		if !nodeReach[f] && !pathExempt(f) {
			appRoots = append(appRoots, f)
		}
	}
	bundled := closure(appRoots, imports)

	// What the resolver could not follow from the app's side fails closed.
	// A non-literal import may name anything, so nothing is exempt. An
	// unresolved specifier (an alias the resolver does not know: `~/x`,
	// `#shared/x`, a vite resolve.alias) may name any file whose name ends it,
	// so such a file stays scanned.
	dynamic, tails := unresolvedAppImports(feDir, bundled, known)
	if dynamic {
		return nil
	}

	out := map[string]bool{}
	for f := range nodeReach {
		if bundled[f] || underAppSourceRoot(feDir, f) || tails[importTail(f)] {
			continue
		}
		out[f] = true
	}
	return out
}

// dynamicImport matches an import() or require() whose argument is not a
// string literal: a specifier built at run time.
var dynamicImport = regexp.MustCompile(`\b(?:import|require)\s*\(\s*[^"'\s)]`)

// unresolvedAppImports reads the app's files and reports whether any imports
// through a non-literal specifier, and the last path segment (extension
// dropped) of every specifier that resolved to no known file.
func unresolvedAppImports(feDir string, app, known map[string]bool) (dynamic bool, tails map[string]bool) {
	tails = map[string]bool{}
	for f := range app {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if dynamicImport.Match(data) {
			return true, nil
		}
		for _, m := range importSpecifier.FindAllSubmatch(data, -1) {
			spec := string(m[1])
			if resolveSpecifier(feDir, f, spec, known) != "" {
				continue
			}
			last := path.Base(strings.TrimSuffix(spec, "/"))
			tails[strings.TrimSuffix(last, path.Ext(last))] = true
		}
	}
	return false, tails
}

// importTail is the segment a specifier naming f ends with: its file name
// without the extension, or its directory's name for an index file.
func importTail(f string) string {
	stem := strings.TrimSuffix(filepath.Base(f), filepath.Ext(f))
	if stem == "index" {
		return filepath.Base(filepath.Dir(f))
	}
	return stem
}

// isNodeRoot reports whether f is a Node entry point: a root build config
// file (vite.config.ts, next.config.mjs, …) or a file under a root scripts/
// directory.
func isNodeRoot(feDir, f string) bool {
	rel, err := filepath.Rel(feDir, f)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	if !strings.Contains(rel, "/") {
		return isConfigScriptName(rel)
	}
	return strings.HasPrefix(rel, "scripts/")
}

func underAppSourceRoot(feDir, f string) bool {
	rel, err := filepath.Rel(feDir, f)
	if err != nil {
		return false
	}
	top, _, nested := strings.Cut(filepath.ToSlash(rel), "/")
	return nested && appSourceRoots[top]
}

// closure returns every file reachable from roots (roots included).
func closure(roots []string, imports map[string][]string) map[string]bool {
	seen := map[string]bool{}
	stack := append([]string(nil), roots...)
	for len(stack) > 0 {
		f := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[f] {
			continue
		}
		seen[f] = true
		stack = append(stack, imports[f]...)
	}
	return seen
}

// resolvedImports reads f and returns the scanned files its imports name.
// An unreadable file, or a specifier that resolves to nothing known (a
// package, an asset, an alias the resolver does not know), contributes no
// edge.
func resolvedImports(feDir, f string, known map[string]bool) []string {
	data, err := os.ReadFile(f)
	if err != nil {
		return nil
	}
	var out []string
	for _, m := range importSpecifier.FindAllSubmatch(data, -1) {
		if target := resolveSpecifier(feDir, f, string(m[1]), known); target != "" {
			out = append(out, target)
		}
	}
	return out
}

// resolveSpecifier maps an import specifier to a known file, the way a
// TypeScript-aware bundler would: the exact path, the path plus a source
// extension, a directory index, and a `.js`-family specifier that names its
// TypeScript source (`./plugin.js` → `./plugin.ts`).
func resolveSpecifier(feDir, importer, spec string, known map[string]bool) string {
	var base string
	switch {
	case strings.HasPrefix(spec, "./") || strings.HasPrefix(spec, "../"):
		base = filepath.Join(filepath.Dir(importer), filepath.FromSlash(spec))
	case strings.HasPrefix(spec, "@/"):
		base = filepath.Join(feDir, "src", filepath.FromSlash(strings.TrimPrefix(spec, "@/")))
	default:
		return ""
	}
	exts := []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs"}
	candidates := []string{base}
	for _, e := range exts {
		candidates = append(candidates, base+e)
	}
	for _, e := range exts {
		candidates = append(candidates, filepath.Join(base, "index"+e))
	}
	switch path.Ext(spec) {
	case ".js", ".jsx", ".mjs", ".cjs":
		stem := strings.TrimSuffix(base, filepath.Ext(base))
		candidates = append(candidates, stem+".ts", stem+".tsx")
	}
	for _, c := range candidates {
		if known[c] {
			return c
		}
	}
	return ""
}
