// Copyright (c) 2025 Reliant Labs
package buildmode_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestGoModHasNoForgeReplace keeps the container and CI builds able to resolve
// forge at the version go.mod pins.
//
// The local development loop bridges this repo to the sibling ../forge checkout
// with a go.work file — gitignored and machine-local, written by
// `forge project new`. That is the supported way to develop against an
// unreleased forge, and it composes with everything below.
//
// A `replace` directive is NOT the same mechanism and cannot be substituted for
// it. A replace lives in go.mod, applies in EVERY build mode, and `GOWORK=off`
// does not disable it — so a replace pointing at ../forge makes the Docker build
// fail the moment it runs outside this checkout:
//
//	main.go:6:2: github.com/reliant-labs/forge/pkg@v0.0.4:
//	             replacement directory ../forge/pkg does not exist
//
// That failure is measured, not hypothetical. The Dockerfile sets GOWORK=off and
// its comment claimed this "resolves the pinned forge module from go.mod" — true
// only once the replace is gone, which it was not. The tag was pushed, the
// version was pinned, and neither consumer would have consumed it.
//
// Adding a forge replace back is therefore a silent regression: local builds and
// `go test ./...` stay green because go.work covers them, and only a container
// build notices. This guard is the thing that notices.
func TestGoModHasNoForgeReplace(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}

	// Matches both the single-line form and an entry inside a replace block,
	// for the main module and the /pkg submodule.
	forgeReplace := regexp.MustCompile(`(?m)^\s*(?:replace\s+)?github\.com/reliant-labs/forge(?:/\S+)?\s+=>`)

	if loc := forgeReplace.FindIndex(data); loc != nil {
		line := lineContaining(string(data), loc[0])
		t.Errorf("go.mod carries a replace for forge:\n\n    %s\n\n"+
			"A replace applies in every build mode — GOWORK=off does NOT disable it — so the "+
			"Docker build fails with \"replacement directory does not exist\" once it runs "+
			"without the sibling checkout, and the pinned version is never consumed.\n"+
			"To develop against a local forge, use the gitignored go.work instead:\n"+
			"    go work init . ../forge/pkg", strings.TrimSpace(line))
	}
}

// TestForgeIsPinnedToAVersion is the other half: dropping the replace is only
// safe if a real version is required, or the module cannot resolve at all.
func TestForgeIsPinnedToAVersion(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}

	pinned := regexp.MustCompile(`(?m)^\s*github\.com/reliant-labs/forge(?:/\S+)?\s+v\d+\.\d+\.\d+`)
	if !pinned.Match(data) {
		t.Error("go.mod requires no forge module at a concrete version — with no replace " +
			"and no require, a container build cannot resolve forge at all")
	}
}

func lineContaining(s string, idx int) string {
	start := strings.LastIndexByte(s[:idx], '\n') + 1
	end := strings.IndexByte(s[idx:], '\n')
	if end < 0 {
		return s[start:]
	}
	return s[start : idx+end]
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolve working directory: %v", err)
	}
	for {
		// The repo root is the go.mod that declares THIS module, not any
		// nested one.
		b, readErr := os.ReadFile(filepath.Join(dir, "go.mod"))
		if readErr == nil && strings.Contains(string(b), "module github.com/reliant-labs/reliant") {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("walked to the filesystem root without finding the repo go.mod")
		}
		dir = parent
	}
}
