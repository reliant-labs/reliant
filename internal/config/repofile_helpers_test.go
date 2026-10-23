// Copyright (c) 2025 Reliant Labs
package config_test

import (
	"os"
	"path/filepath"
	"testing"
)

// repoRootFromTest walks up from the package directory to the module root.
func repoRootFromTest(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolve working directory: %v", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("walked to the filesystem root without finding go.mod — this guard " +
				"would have read no files at all")
		}
		dir = parent
	}
}

// readRepoFile reads a repo file a guard pins, failing on a missing or empty one.
func readRepoFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if len(data) == 0 {
		t.Fatalf("%s is empty", path)
	}
	return string(data)
}
