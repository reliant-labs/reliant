// Copyright (c) 2025 Reliant Labs
package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGlobFilesFunction_WorktreePath tests glob via daemon.LocalClient
// to ensure it handles worktree paths correctly.
//
// Relocated from internal/llm/tools/glob_test.go when the grep/glob LLM tools
// were removed; this is the canonical direct coverage of LocalClient.GlobFiles,
// which remains in use by the gRPC FileSystemService / web FileBrowser.
func TestGlobFilesFunction_WorktreePath(t *testing.T) {
	tmpDir := t.TempDir()
	worktreePath := filepath.Join(tmpDir, ".reliant", "worktrees", "wt1", "repo")

	if err := os.MkdirAll(worktreePath, 0755); err != nil {
		t.Fatalf("Failed to create worktree: %v", err)
	}

	// Create test files
	for _, name := range []string{"a.go", "b.go", "c.txt"} {
		path := filepath.Join(worktreePath, name)
		if err := os.WriteFile(path, []byte("content"), 0644); err != nil {
			t.Fatalf("Failed to write %s: %v", path, err)
		}
	}

	c := NewLocalClient()
	result, err := c.GlobFiles(context.Background(), "*.go", &GlobOpts{
		BaseDir:    worktreePath,
		MaxResults: 100,
	})
	if err != nil {
		t.Fatalf("GlobFiles failed: %v", err)
	}

	if result.Truncated {
		t.Error("Should not be truncated")
	}

	if len(result.Files) != 2 {
		t.Errorf("Expected 2 .go files, got %d: %v", len(result.Files), result.Files)
	}

	// Verify files are the correct ones
	foundA := false
	foundB := false
	for _, f := range result.Files {
		if strings.HasSuffix(f, "a.go") {
			foundA = true
		}
		if strings.HasSuffix(f, "b.go") {
			foundB = true
		}
	}

	if !foundA || !foundB {
		t.Errorf("Expected to find a.go and b.go, got: %v", result.Files)
	}
}
