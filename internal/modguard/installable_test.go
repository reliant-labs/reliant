// Copyright (c) 2025 Reliant Labs
package modguard_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRootGoModHasNoReplace keeps `go install github.com/reliant-labs/forge/cmd/forge@vX.Y.Z`
// working.
//
// Go refuses to install a module whose go.mod carries ANY replace directive:
//
//	go: github.com/reliant-labs/forge/cmd/forge@v0.0.5 (in github.com/reliant-labs/forge@v0.0.5):
//	    The go.mod file for the module providing named packages contains one or
//	    more replace directives. It must not contain directives that would cause
//	    it to be interpreted differently than if it were the main module.
//
// That is not a warning and there is no flag. v0.0.3 installs; v0.0.4 and
// v0.0.5 do not, because a `replace forge/pkg => ./pkg` entered in between.
// control-plane's CI pinned itself to `@v0.0.3` as a result — a released
// toolchain nobody can install is worse than an unreleased one.
//
// (That replace pointed at the forge/pkg submodule, which has since been
// folded into this module, but the rule outlived it.) To build against a
// local checkout of a dependency, use a go.work (gitignored, machine-local),
// which resolves it for local development WITHOUT following the module into
// the published artifact. That is the same split reliant and control-plane
// use for their own sibling dependencies.
//
// This guard is deliberately blunt — no replace at all in the root go.mod —
// because Go's rule is equally blunt: it rejects the module for having any.
func TestRootGoModHasNoReplace(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}

	// Both spellings: a bare `replace x => y` line and an entry inside a
	// `replace ( ... )` block.
	inBlock := false
	for i, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "replace ("):
			inBlock = true
			continue
		case inBlock && trimmed == ")":
			inBlock = false
			continue
		}
		isReplace := strings.HasPrefix(trimmed, "replace ") ||
			(inBlock && strings.Contains(trimmed, "=>") && !strings.HasPrefix(trimmed, "//"))
		if !isReplace {
			continue
		}
		t.Errorf("go.mod:%d carries a replace directive:\n\n    %s\n\n"+
			"Go refuses `go install <module>/cmd/...@version` for ANY module whose go.mod "+
			"has one, so this makes every published tag uninstallable — measured: v0.0.3 "+
			"installs, v0.0.4 and v0.0.5 do not.\n"+
			"To build against a local checkout of a dependency, use a gitignored go.work instead:\n"+
			"    go work init . <path-to-the-checkout>", i+1, trimmed)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolve working directory: %v", err)
	}
	for {
		b, readErr := os.ReadFile(filepath.Join(dir, "go.mod"))
		if readErr == nil && strings.Contains(string(b), "module github.com/reliant-labs/forge\n") {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("walked to the filesystem root without finding forge's go.mod")
		}
		dir = parent
	}
}
