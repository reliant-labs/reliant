// Copyright (c) 2025 Reliant Labs
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The go suite's shards balance on scripts/release-gate-go-timings.json. Its
// keys must name packages this module still has: a package that was deleted
// or moved keeps weighting the shard plan for code that no longer exists, and
// nothing else notices. Each key must be a directory of this module holding a
// .go file (a test-only package, such as internal/architecture, counts: the
// suite runs it).
func TestReleaseGateGoTimingsNameExistingPackages(t *testing.T) {
	const module = "github.com/reliant-labs/reliant/"
	raw, err := os.ReadFile(filepath.Join(repoRoot, "scripts", "release-gate-go-timings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var timings map[string]float64
	if err := json.Unmarshal(raw, &timings); err != nil {
		t.Fatalf("release-gate-go-timings.json: %v", err)
	}
	if len(timings) == 0 {
		t.Fatal("release-gate-go-timings.json is empty")
	}
	for pkg := range timings {
		rel, ok := strings.CutPrefix(pkg, module)
		if !ok {
			t.Errorf("%s is not a package of %s", pkg, strings.TrimSuffix(module, "/"))
			continue
		}
		if !hasGoSource(t, filepath.Join(repoRoot, filepath.FromSlash(rel))) {
			t.Errorf("%s has no Go file: drop it from scripts/release-gate-go-timings.json", pkg)
		}
	}
}

func hasGoSource(t *testing.T, dir string) bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() && strings.HasSuffix(name, ".go") {
			return true
		}
	}
	return false
}
