// Copyright (c) 2025 Reliant Labs
package tools

import (
	"path/filepath"
	"strings"
)

// Scope rules for code_context.
//
// A language server answers about the whole compilation world, which includes
// the standard library and every third-party dependency. That is almost never
// what the question meant. Measured on this repo, `GetWorkingDirectory` reports
// `fmt.Errorf` as a callee, and a search-heavy function reports three
// `strings` functions plus `sort.Slice` — 5 of 9 callees were stdlib.
//
// Unscoped results cost three separate things, and the third is the one that
// bites:
//  1. Tokens spent on edges nobody asked about.
//  2. Attention: `fmt.Errorf` next to a real collaborator hides the real one.
//  3. THE NODE BUDGET. A call map expands each node with its own language-server
//     query, so tracing into `strings.Split` burns a slot AND a query that could
//     have gone one level deeper into the user's own code. Depth is the scarce
//     resource, and dependency edges spend it on nothing.
//
// What counts as "the project" is the WORKSPACE ROOT, not the module. Sibling
// repos linked by a go.mod replace or a pnpm workspace live beside the current
// repo under one root, and an agent tracing a request from the frontend into
// control-plane is asking a legitimate question. Scoping to the module would
// break exactly the cross-repo tracing this tool is most useful for, so the
// boundary is drawn at the root the user's chat is bound to.
//
// The escape hatch is `scope`: a user who genuinely wants to read into a
// dependency asks for it explicitly, and gets told when results were filtered.

// scopeMode controls how far outside the workspace a query may reach.
type scopeMode string

const (
	// scopeProject keeps only code under the workspace root. Default.
	scopeProject scopeMode = "project"
	// scopeAll keeps everything a language server reports, including the
	// standard library and dependencies.
	scopeAll scopeMode = "all"
)

func parseScope(raw string) scopeMode {
	if strings.EqualFold(strings.TrimSpace(raw), string(scopeAll)) {
		return scopeAll
	}
	return scopeProject
}

// dependencyDirs are vendored/installed dependency trees that can sit INSIDE
// the workspace root. The root test alone would admit them, and
// `node_modules/@types/react/index.d.ts` is a dependency by any reading — the
// user's own code is what the question is about.
var dependencyDirs = map[string]bool{
	"node_modules":  true,
	"vendor":        true,
	".venv":         true,
	"venv":          true,
	"site-packages": true,
	".git":          true,
}

// inScope reports whether a location belongs to the user's own code.
func inScope(root, path string, mode scopeMode) bool {
	if mode == scopeAll {
		return true
	}
	if path == "" {
		return false
	}

	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		// Outside the workspace: the standard library, the module cache, or an
		// absolute path from somewhere else entirely.
		return false
	}

	// Inside the root, but possibly inside a dependency tree within it.
	for _, segment := range strings.Split(filepath.ToSlash(rel), "/") {
		if dependencyDirs[segment] {
			return false
		}
	}
	return true
}

// filterInScope drops out-of-scope locations, reporting how many were removed
// so the caller can say so rather than silently returning less.
func filterInScope(root string, locs []codeLocation, mode scopeMode) ([]codeLocation, int) {
	if mode == scopeAll {
		return locs, 0
	}
	kept := make([]codeLocation, 0, len(locs))
	for _, l := range locs {
		if inScope(root, l.Path, mode) {
			kept = append(kept, l)
		}
	}
	return kept, len(locs) - len(kept)
}
