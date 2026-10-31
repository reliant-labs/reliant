// Copyright (c) 2025 Reliant Labs
package version_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// forgeModule is the module whose scripts/release-lock.sh is the source of
// this repository's copy.
const forgeModule = "github.com/reliant-labs/forge"

// TestReleaseLockIsForgesCopy holds scripts/release-lock.sh (and its self-test)
// byte-identical to the copy in the forge version go.mod pins. forge, reliant
// and control-plane each source the same lock, and the three had drifted apart
// (each fixed a different subset of its races) before this check: forge's copy
// is the one source, and `task release:lock:sync` copies it from the pinned
// module. A pin bump that brings a changed lock fails here until it is synced.
//
// It reads both files itself, so Go's test cache sees an edit to either; the
// one child it runs, `go env GOMODCACHE`, reads no file of this repository.
func TestReleaseLockIsForgesCopy(t *testing.T) {
	dir := pinnedForgeDir(t)
	for _, name := range []string{"release-lock.sh", "test-release-lock.sh"} {
		want, err := os.ReadFile(filepath.Join(dir, "scripts", name))
		if os.IsNotExist(err) {
			t.Skipf("the pinned forge (%s) predates scripts/%s; this check starts holding at the first pin that carries it", dir, name)
		}
		if err != nil {
			t.Fatal(err)
		}
		if got := []byte(readScript(t, name)); !bytes.Equal(got, want) {
			t.Errorf("scripts/%s differs from the pinned forge's copy (%s): run `task release:lock:sync`; change the lock in forge, never here", name, filepath.Join(dir, "scripts", name))
		}
	}
}

// pinnedForgeDir is the module cache directory of the forge version go.mod
// requires (a local replace, when there is one).
func pinnedForgeDir(t *testing.T) string {
	t.Helper()
	root := repoRootFromTest(t)
	mod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	var version, replaced string
	for _, line := range strings.Split(string(mod), "\n") {
		f := strings.Fields(strings.TrimPrefix(strings.TrimSpace(line), "replace "))
		switch {
		case len(f) >= 3 && f[0] == forgeModule && f[1] == "=>" && strings.HasPrefix(f[2], "."):
			replaced = filepath.Join(root, f[2])
		case len(f) >= 2 && f[0] == forgeModule && strings.HasPrefix(f[1], "v"):
			version = f[1]
		}
	}
	if replaced != "" {
		return replaced
	}
	if version == "" {
		t.Fatalf("go.mod requires no %s", forgeModule)
	}
	out, err := exec.CommandContext(context.Background(), "go", "env", "GOMODCACHE").Output()
	if err != nil {
		t.Skipf("go env GOMODCACHE: %v", err)
	}
	dir := filepath.Join(strings.TrimSpace(string(out)), forgeModule+"@"+version)
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("the pinned forge is not in the module cache (%v); `go mod download %s` and re-run", err, forgeModule)
	}
	return dir
}
