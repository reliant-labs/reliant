// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// TestGlobTool_WorktreeDirectory tests that the glob tool works correctly
// when the working directory is inside a .reliant/worktrees path.
// This previously returned "No files found" because the old SkipHidden checked
// absolute paths and found ".reliant" in the parent directory path.
func TestGlobTool_WorktreeDirectory(t *testing.T) {
	// Create a temp directory simulating worktree structure
	tmpDir := t.TempDir()
	worktreePath := filepath.Join(tmpDir, ".reliant", "worktrees", "feature-xyz", "project")

	// Create directory structure
	dirs := []string{
		filepath.Join(worktreePath, "cmd"),
		filepath.Join(worktreePath, "internal", "pkg"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatalf("Failed to create dir %s: %v", d, err)
		}
	}

	// Create test files
	files := map[string]string{
		filepath.Join(worktreePath, "main.go"):                    "package main",
		filepath.Join(worktreePath, "go.mod"):                     "module test",
		filepath.Join(worktreePath, "cmd", "root.go"):             "package cmd",
		filepath.Join(worktreePath, "internal", "pkg", "util.go"): "package pkg",
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("Failed to write %s: %v", path, err)
		}
	}

	toolCtx := &rctx.ToolContext{
		Context:  context.Background(),
		Worktree: &rctx.WorktreeInfo{Path: worktreePath},
	}

	tool := NewGlobTool()

	tests := []struct {
		name         string
		pattern      string
		wantFiles    []string
		notWantEmpty bool
	}{
		{
			name:         "**/*.go should find all go files",
			pattern:      "**/*.go",
			wantFiles:    []string{"main.go", "root.go", "util.go"},
			notWantEmpty: true,
		},
		{
			name:         "*.go should find root go files",
			pattern:      "*.go",
			wantFiles:    []string{"main.go"},
			notWantEmpty: true,
		},
		{
			name:         "cmd/*.go should find cmd go files",
			pattern:      "cmd/*.go",
			wantFiles:    []string{"root.go"},
			notWantEmpty: true,
		},
		{
			name:         "go.mod should find go.mod",
			pattern:      "go.mod",
			wantFiles:    []string{"go.mod"},
			notWantEmpty: true,
		},
		{
			name:         "internal/**/*.go should find internal files",
			pattern:      "internal/**/*.go",
			wantFiles:    []string{"util.go"},
			notWantEmpty: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := GlobParams{Pattern: tt.pattern}
			inputJSON, _ := json.Marshal(params)

			resp, err := tool.Run(toolCtx, ToolCall{
				ID:    "test-1",
				Name:  "glob",
				Input: string(inputJSON),
			})
			if err != nil {
				t.Fatalf("Glob failed: %v", err)
			}

			output := resp.Content

			// Should not return "No files found"
			if tt.notWantEmpty && strings.Contains(output, "No files found") {
				t.Errorf("Expected to find files with pattern %q but got 'No files found'", tt.pattern)
			}

			// Check expected files are present
			for _, wantFile := range tt.wantFiles {
				if !strings.Contains(output, wantFile) {
					t.Errorf("Expected output to contain %q for pattern %q, got:\n%s",
						wantFile, tt.pattern, output)
				}
			}
		})
	}
}

// TestGlobTool_SkipsActualHiddenDirs tests that glob correctly skips
// actual hidden directories and ignored directories within the project.
func TestGlobTool_SkipsActualHiddenDirs(t *testing.T) {
	tmpDir := t.TempDir()

	// Create directory structure with hidden/ignored dirs
	dirs := []string{
		filepath.Join(tmpDir, "src"),
		filepath.Join(tmpDir, ".git", "objects"),
		filepath.Join(tmpDir, "node_modules", "pkg"),
		filepath.Join(tmpDir, ".reliant"),
		filepath.Join(tmpDir, "vendor", "github.com"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatalf("Failed to create dir %s: %v", d, err)
		}
	}

	// Create files in each location
	files := map[string]string{
		filepath.Join(tmpDir, "main.go"):                         "package main",
		filepath.Join(tmpDir, "src", "app.go"):                   "package src",
		filepath.Join(tmpDir, ".git", "objects", "abc"):          "git object",
		filepath.Join(tmpDir, "node_modules", "pkg", "index.js"): "module",
		filepath.Join(tmpDir, ".reliant", "config.yaml"):         "config",
		filepath.Join(tmpDir, "vendor", "github.com", "dep.go"):  "vendor",
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("Failed to write %s: %v", path, err)
		}
	}

	toolCtx := &rctx.ToolContext{
		Context:  context.Background(),
		Worktree: &rctx.WorktreeInfo{Path: tmpDir},
	}

	tool := NewGlobTool()

	params := GlobParams{Pattern: "**/*"}
	inputJSON, _ := json.Marshal(params)

	resp, err := tool.Run(toolCtx, ToolCall{
		ID:    "test-1",
		Name:  "glob",
		Input: string(inputJSON),
	})
	if err != nil {
		t.Fatalf("Glob failed: %v", err)
	}

	output := resp.Content

	// Should find files in normal directories
	if !strings.Contains(output, "main.go") {
		t.Error("Expected to find main.go")
	}
	if !strings.Contains(output, "app.go") {
		t.Error("Expected to find src/app.go")
	}

	// Should NOT find files in ignored/noisy directories
	if strings.Contains(output, ".git") {
		t.Error("Should not include .git directory contents")
	}
	if strings.Contains(output, "node_modules") {
		t.Error("Should not include node_modules directory contents")
	}
	if strings.Contains(output, "vendor") {
		t.Error("Should not include vendor directory contents")
	}
	// .reliant directories should also be skipped (it's in IgnoredDirs)
	if strings.Contains(output, "config.yaml") {
		t.Error("Should not include .reliant directory contents")
	}

	// Test with include_ignored=true - should find all files including noisy directories
	params = GlobParams{Pattern: "**/*", IncludeIgnored: true}
	inputJSON, _ = json.Marshal(params)

	resp, err = tool.Run(toolCtx, ToolCall{
		ID:    "test-2",
		Name:  "glob",
		Input: string(inputJSON),
	})
	if err != nil {
		t.Fatalf("Glob with include_ignored failed: %v", err)
	}

	output = resp.Content

	// With include_ignored=true, should find files in noisy directories
	if !strings.Contains(output, ".git") {
		t.Error("With include_ignored=true, should find .git contents")
	}
	if !strings.Contains(output, "node_modules") {
		t.Error("With include_ignored=true, should find node_modules contents")
	}
	if !strings.Contains(output, "config.yaml") {
		t.Error("With include_ignored=true, should find .reliant/config.yaml")
	}
}

// TestGlobTool_AbsolutePathInWorktree tests using an explicit absolute path
// that happens to be in a worktree location.
func TestGlobTool_AbsolutePathInWorktree(t *testing.T) {
	tmpDir := t.TempDir()
	worktreePath := filepath.Join(tmpDir, ".reliant", "worktrees", "test", "project")

	if err := os.MkdirAll(worktreePath, 0755); err != nil {
		t.Fatalf("Failed to create worktree path: %v", err)
	}

	// Create a test file
	testFile := filepath.Join(worktreePath, "test.go")
	if err := os.WriteFile(testFile, []byte("package test"), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	toolCtx := &rctx.ToolContext{
		Context:  context.Background(),
		Worktree: &rctx.WorktreeInfo{Path: worktreePath},
	}

	tool := NewGlobTool()

	// Use explicit absolute path
	params := GlobParams{
		Pattern: "*.go",
		Path:    worktreePath,
	}
	inputJSON, _ := json.Marshal(params)

	resp, err := tool.Run(toolCtx, ToolCall{
		ID:    "test-1",
		Name:  "glob",
		Input: string(inputJSON),
	})
	if err != nil {
		t.Fatalf("Glob failed: %v", err)
	}

	output := resp.Content

	if strings.Contains(output, "No files found") {
		t.Errorf("Expected to find test.go but got: %s", output)
	}

	if !strings.Contains(output, "test.go") {
		t.Errorf("Expected output to contain test.go, got: %s", output)
	}
}

// TestGlobTool_RelativePatterns tests that relative patterns work from
// the current working directory.
func TestGlobTool_RelativePatterns(t *testing.T) {
	tmpDir := t.TempDir()

	// Create nested structure
	nestedPath := filepath.Join(tmpDir, "a", "b", "c")
	if err := os.MkdirAll(nestedPath, 0755); err != nil {
		t.Fatalf("Failed to create nested path: %v", err)
	}

	files := []string{
		filepath.Join(tmpDir, "root.go"),
		filepath.Join(tmpDir, "a", "level1.go"),
		filepath.Join(tmpDir, "a", "b", "level2.go"),
		filepath.Join(tmpDir, "a", "b", "c", "level3.go"),
	}
	for _, f := range files {
		if err := os.WriteFile(f, []byte("package x"), 0644); err != nil {
			t.Fatalf("Failed to write %s: %v", f, err)
		}
	}

	toolCtx := &rctx.ToolContext{
		Context:  context.Background(),
		Worktree: &rctx.WorktreeInfo{Path: tmpDir},
	}

	tool := NewGlobTool()

	tests := []struct {
		name      string
		pattern   string
		wantCount int
	}{
		// Note: ripgrep's --glob "*.go" matches at all levels, not just root
		// Use explicit path patterns for specific directory matching
		{"**/*.go matches all", "**/*.go", 4},
		{"a/*.go matches level1", "a/*.go", 1},
		{"a/**/*.go matches nested", "a/**/*.go", 3},
		{"a/b/c/*.go matches deepest", "a/b/c/*.go", 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := GlobParams{Pattern: tt.pattern}
			inputJSON, _ := json.Marshal(params)

			resp, err := tool.Run(toolCtx, ToolCall{
				ID:    "test-1",
				Name:  "glob",
				Input: string(inputJSON),
			})
			if err != nil {
				t.Fatalf("Glob failed: %v", err)
			}

			output := resp.Content

			if strings.Contains(output, "No files found") && tt.wantCount > 0 {
				t.Errorf("Expected %d files but got 'No files found' for pattern %q",
					tt.wantCount, tt.pattern)
			}

			// Count files in output
			lines := strings.Split(strings.TrimSpace(output), "\n")
			gotCount := 0
			for _, line := range lines {
				if strings.HasSuffix(line, ".go") {
					gotCount++
				}
			}

			if gotCount != tt.wantCount {
				t.Errorf("Pattern %q: expected %d files, got %d. Output:\n%s",
					tt.pattern, tt.wantCount, gotCount, output)
			}
		})
	}
}

// TestGlobFilesFunction_WorktreePath tests glob via daemon.LocalClient
// to ensure it handles worktree paths correctly.
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

	c := daemon.NewLocalClient()
	result, err := c.GlobFiles(context.Background(), "*.go", &daemon.GlobOpts{
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

// TestGlobTool_RelativePath tests that relative paths like ".." and "./subdir"
// are resolved correctly against the working directory.
func TestGlobTool_RelativePath(t *testing.T) {
	tmpDir := t.TempDir()

	// Create structure:
	//   tmpDir/
	//     project/
	//       main.go
	//       sub/
	//         helper.go
	//     sibling/
	//       other.go
	projectDir := filepath.Join(tmpDir, "project")
	subDir := filepath.Join(projectDir, "sub")
	siblingDir := filepath.Join(tmpDir, "sibling")

	for _, d := range []string{projectDir, subDir, siblingDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatalf("Failed to create dir %s: %v", d, err)
		}
	}

	files := map[string]string{
		filepath.Join(projectDir, "main.go"):  "package main",
		filepath.Join(subDir, "helper.go"):    "package sub",
		filepath.Join(siblingDir, "other.go"): "package sibling",
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("Failed to write %s: %v", path, err)
		}
	}

	tool := NewGlobTool()

	tests := []struct {
		name      string
		worktree  string
		path      string // relative path param
		pattern   string
		wantFiles []string
	}{
		{
			name:      ".. resolves to parent",
			worktree:  projectDir,
			path:      "..",
			pattern:   "**/*.go",
			wantFiles: []string{"main.go", "helper.go", "other.go"},
		},
		{
			name:      "./sub resolves to subdir",
			worktree:  projectDir,
			path:      "./sub",
			pattern:   "*.go",
			wantFiles: []string{"helper.go"},
		},
		{
			name:      "../sibling resolves to sibling dir",
			worktree:  projectDir,
			path:      "../sibling",
			pattern:   "*.go",
			wantFiles: []string{"other.go"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			toolCtx := &rctx.ToolContext{
				Context:  context.Background(),
				Worktree: &rctx.WorktreeInfo{Path: tt.worktree},
			}

			params := GlobParams{Pattern: tt.pattern, Path: tt.path}
			inputJSON, _ := json.Marshal(params)

			resp, err := tool.Run(toolCtx, ToolCall{
				ID:    "test-rel",
				Name:  "glob",
				Input: string(inputJSON),
			})
			if err != nil {
				t.Fatalf("Glob failed: %v", err)
			}

			output := resp.Content

			if strings.Contains(output, "No files found") {
				t.Errorf("Expected to find files with path=%q pattern=%q but got 'No files found'", tt.path, tt.pattern)
			}

			for _, wantFile := range tt.wantFiles {
				if !strings.Contains(output, wantFile) {
					t.Errorf("Expected output to contain %q for path=%q pattern=%q, got:\n%s",
						wantFile, tt.path, tt.pattern, output)
				}
			}
		})
	}
}

// TestGlobTool_PatternNormalization tests that the glob tool correctly normalizes
// patterns that would otherwise silently fail with ripgrep.
func TestGlobTool_PatternNormalization(t *testing.T) {
	tmpDir := t.TempDir()

	// Create directory structure
	dirs := []string{
		filepath.Join(tmpDir, "project", "src"),
		filepath.Join(tmpDir, "project", "src", "sub"),
		filepath.Join(tmpDir, "sibling"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatalf("Failed to create dir %s: %v", d, err)
		}
	}

	// Create test files
	files := map[string]string{
		filepath.Join(tmpDir, "project", "main.go"):            "package main",
		filepath.Join(tmpDir, "project", "README.md"):          "# readme",
		filepath.Join(tmpDir, "project", "src", "app.go"):      "package src",
		filepath.Join(tmpDir, "project", "src", "sub", "d.go"): "package sub",
		filepath.Join(tmpDir, "sibling", "other.go"):           "package sibling",
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("Failed to write %s: %v", path, err)
		}
	}

	projectDir := filepath.Join(tmpDir, "project")

	tool := NewGlobTool()

	t.Run("./ prefix is stripped and finds files", func(t *testing.T) {
		toolCtx := &rctx.ToolContext{
			Context:  context.Background(),
			Worktree: &rctx.WorktreeInfo{Path: projectDir},
		}

		params := GlobParams{Pattern: "./**/*.go"}
		inputJSON, _ := json.Marshal(params)

		resp, err := tool.Run(toolCtx, ToolCall{
			ID:    "test-dot-slash",
			Name:  "glob",
			Input: string(inputJSON),
		})
		if err != nil {
			t.Fatalf("Glob failed: %v", err)
		}

		output := resp.Content
		if strings.Contains(output, "No files found") {
			t.Errorf("Pattern './**/*.go' should find files after normalization, got: %s", output)
		}
		// Should find all .go files
		for _, want := range []string{"main.go", "app.go", "d.go"} {
			if !strings.Contains(output, want) {
				t.Errorf("Expected to find %q in output, got:\n%s", want, output)
			}
		}
	})

	t.Run("./*.go prefix finds root files", func(t *testing.T) {
		toolCtx := &rctx.ToolContext{
			Context:  context.Background(),
			Worktree: &rctx.WorktreeInfo{Path: projectDir},
		}

		params := GlobParams{Pattern: "./*.go"}
		inputJSON, _ := json.Marshal(params)

		resp, err := tool.Run(toolCtx, ToolCall{
			ID:    "test-dot-star",
			Name:  "glob",
			Input: string(inputJSON),
		})
		if err != nil {
			t.Fatalf("Glob failed: %v", err)
		}

		output := resp.Content
		if strings.Contains(output, "No files found") {
			t.Errorf("Pattern './*.go' should find files after normalization, got: %s", output)
		}
		if !strings.Contains(output, "main.go") {
			t.Errorf("Expected to find main.go, got:\n%s", output)
		}
	})

	t.Run("../*.go pattern adjusts search path", func(t *testing.T) {
		srcDir := filepath.Join(projectDir, "src")
		toolCtx := &rctx.ToolContext{
			Context:  context.Background(),
			Worktree: &rctx.WorktreeInfo{Path: srcDir},
		}

		params := GlobParams{Pattern: "../*.go"}
		inputJSON, _ := json.Marshal(params)

		resp, err := tool.Run(toolCtx, ToolCall{
			ID:    "test-dotdot",
			Name:  "glob",
			Input: string(inputJSON),
		})
		if err != nil {
			t.Fatalf("Glob failed: %v", err)
		}

		output := resp.Content
		if strings.Contains(output, "No files found") {
			t.Errorf("Pattern '../*.go' should find files after normalization, got: %s", output)
		}
		if !strings.Contains(output, "main.go") {
			t.Errorf("Expected to find main.go, got:\n%s", output)
		}
	})

	t.Run("../**/*.go pattern finds all files in parent", func(t *testing.T) {
		srcDir := filepath.Join(projectDir, "src")
		toolCtx := &rctx.ToolContext{
			Context:  context.Background(),
			Worktree: &rctx.WorktreeInfo{Path: srcDir},
		}

		params := GlobParams{Pattern: "../**/*.go"}
		inputJSON, _ := json.Marshal(params)

		resp, err := tool.Run(toolCtx, ToolCall{
			ID:    "test-dotdot-recursive",
			Name:  "glob",
			Input: string(inputJSON),
		})
		if err != nil {
			t.Fatalf("Glob failed: %v", err)
		}

		output := resp.Content
		if strings.Contains(output, "No files found") {
			t.Errorf("Pattern '../**/*.go' should find files after normalization, got: %s", output)
		}
		for _, want := range []string{"main.go", "app.go", "d.go"} {
			if !strings.Contains(output, want) {
				t.Errorf("Expected to find %q in output, got:\n%s", want, output)
			}
		}
	})

	t.Run("../sibling/**/*.go adjusts path correctly", func(t *testing.T) {
		toolCtx := &rctx.ToolContext{
			Context:  context.Background(),
			Worktree: &rctx.WorktreeInfo{Path: projectDir},
		}

		params := GlobParams{Pattern: "../sibling/**/*.go"}
		inputJSON, _ := json.Marshal(params)

		resp, err := tool.Run(toolCtx, ToolCall{
			ID:    "test-sibling",
			Name:  "glob",
			Input: string(inputJSON),
		})
		if err != nil {
			t.Fatalf("Glob failed: %v", err)
		}

		output := resp.Content
		if strings.Contains(output, "No files found") {
			t.Errorf("Pattern '../sibling/**/*.go' should find files, got: %s", output)
		}
		if !strings.Contains(output, "other.go") {
			t.Errorf("Expected to find other.go, got:\n%s", output)
		}
	})

	t.Run("absolute path in pattern returns helpful error", func(t *testing.T) {
		toolCtx := &rctx.ToolContext{
			Context:  context.Background(),
			Worktree: &rctx.WorktreeInfo{Path: projectDir},
		}

		params := GlobParams{Pattern: "/home/user/project/**/*.go"}
		inputJSON, _ := json.Marshal(params)

		resp, err := tool.Run(toolCtx, ToolCall{
			ID:    "test-abs-path",
			Name:  "glob",
			Input: string(inputJSON),
		})
		if err != nil {
			t.Fatalf("Glob failed: %v", err)
		}

		output := resp.Content
		if !strings.Contains(output, "Absolute paths") {
			t.Errorf("Expected error message about absolute paths, got:\n%s", output)
		}
		if !strings.Contains(output, "path") {
			t.Errorf("Expected suggestion to use path parameter, got:\n%s", output)
		}
	})

	t.Run("./src/**/*.go prefix stripped with directory component", func(t *testing.T) {
		toolCtx := &rctx.ToolContext{
			Context:  context.Background(),
			Worktree: &rctx.WorktreeInfo{Path: projectDir},
		}

		params := GlobParams{Pattern: "./src/**/*.go"}
		inputJSON, _ := json.Marshal(params)

		resp, err := tool.Run(toolCtx, ToolCall{
			ID:    "test-dot-src",
			Name:  "glob",
			Input: string(inputJSON),
		})
		if err != nil {
			t.Fatalf("Glob failed: %v", err)
		}

		output := resp.Content
		if strings.Contains(output, "No files found") {
			t.Errorf("Pattern './src/**/*.go' should find files after stripping ./, got: %s", output)
		}
		for _, want := range []string{"app.go", "d.go"} {
			if !strings.Contains(output, want) {
				t.Errorf("Expected to find %q in output, got:\n%s", want, output)
			}
		}
	})
}
