package goexec

import (
	"bytes"
	"context"
	"io/fs"
	"os/exec"
	"path/filepath"
	"strings"
)

// ExcludeNodeModules rewrites Go package patterns so a `...` wildcard stops
// matching packages that live under a node_modules directory.
//
// The Go tool skips directories named testdata and those starting with "." or
// "_", but not node_modules. An npm package that ships Go sources (reliant's
// web/node_modules/flatted/golang/pkg/flatted, no go.mod of its own) is
// therefore a package of the enclosing module, and anything that analyses
// ./... — golangci-lint, go vet, go/packages — lints third-party code the
// project does not own and cannot fix.
//
// The common case is untouched: with no node_modules directory under dir, or
// when every matched package lies outside one, patterns come back as given.
// Only when a package inside node_modules is matched are the patterns replaced
// by one ./<dir> per remaining package. If `go list` cannot answer (a broken
// go.mod, no toolchain) the patterns also come back as given, so the caller
// reports the real problem itself rather than this helper hiding it.
func ExcludeNodeModules(ctx context.Context, dir string, env []string, patterns []string) []string {
	if !anyWildcard(patterns) {
		return patterns
	}
	// Every comparison below is between symlink-resolved paths. The project
	// may be reached through a symlink (macOS's /var -> /private/var, so
	// every t.TempDir(); a symlinked home or worktree). WalkDir does not
	// descend into a root that is itself a symlink, so hasNodeModulesDir
	// found nothing there. And go list reports a package's Dir under the
	// caller's $PWD spelling when that names its working directory, so
	// resolving one side only made every package a "../" path that was never
	// recognised as under node_modules. Either way the patterns came back
	// unfiltered.
	root := dir
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		root = resolved
	}
	if !hasNodeModulesDir(root) {
		return patterns
	}
	args := append([]string{"list", "-e", "-f", "{{.Dir}}"}, patterns...)
	cmd := Graceful(exec.CommandContext(ctx, "go", args...))
	cmd.Dir = dir
	if env == nil {
		env = Env()
	}
	cmd.Env = env
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return patterns
	}

	var (
		kept     []string
		seen     = map[string]bool{}
		excluded bool
	)
	for _, pkgDir := range strings.Split(stdout.String(), "\n") {
		pkgDir = strings.TrimSpace(pkgDir)
		if pkgDir == "" {
			continue
		}
		pattern := pkgDir
		if resolved, err := filepath.EvalSymlinks(pkgDir); err == nil {
			pkgDir = resolved
		}
		if rel, err := filepath.Rel(root, pkgDir); err == nil && !strings.HasPrefix(rel, "..") {
			if inNodeModules(rel) {
				excluded = true
				continue
			}
			pattern = "." + string(filepath.Separator) + rel
			if rel == "." {
				pattern = "."
			}
		}
		if !seen[pattern] {
			seen[pattern] = true
			kept = append(kept, pattern)
		}
	}
	if !excluded || len(kept) == 0 {
		return patterns
	}
	return kept
}

func anyWildcard(patterns []string) bool {
	for _, p := range patterns {
		if strings.Contains(p, "...") {
			return true
		}
	}
	return false
}

// inNodeModules reports whether a slash- or OS-separated relative path has a
// node_modules element at any depth.
func inNodeModules(rel string) bool {
	for _, el := range strings.FieldsFunc(rel, func(r rune) bool { return r == '/' || r == filepath.Separator }) {
		if el == "node_modules" {
			return true
		}
	}
	return false
}

// hasNodeModulesDir reports whether a node_modules directory exists under dir
// in a part of the tree `./...` would walk. It never descends into one: the
// answer is "exists", and an installed tree can hold hundreds of thousands of
// entries. Trees the Go tool itself ignores are skipped.
func hasNodeModulesDir(dir string) bool {
	found := false
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // an unreadable subtree is simply not counted
		}
		if !d.IsDir() || p == dir {
			return nil
		}
		name := d.Name()
		switch {
		case name == "node_modules":
			found = true
			return fs.SkipAll
		case name == "vendor" || name == "testdata" ||
			strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_"):
			return filepath.SkipDir
		}
		return nil
	})
	return found
}
