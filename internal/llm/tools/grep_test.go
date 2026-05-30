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

// TestGrepContentModeRecordsFileAwareness verifies that grepping in content mode
// records file awareness, allowing subsequent edits without a separate view call.
func TestGrepContentModeRecordsFileAwareness(t *testing.T) {
	tmpDir := t.TempDir()

	testContent := "package main\n\nfunc hello() {}\n"
	testFile := filepath.Join(tmpDir, "aware.go")
	if err := os.WriteFile(testFile, []byte(testContent), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	chatID := "test-grep-awareness"
	thread := "0"

	// Clear any prior awareness state for this chat+thread
	ClearFileRecordsForThread(chatID, thread)

	// Verify no awareness before grep
	if !getLastAwarenessTime(chatID, thread, testFile).IsZero() {
		t.Fatal("Expected no awareness before grep")
	}

	toolCtx := &rctx.ToolContext{
		Daemon:   daemon.NewLocalClient(),
		Context:  context.Background(),
		ChatID:   chatID,
		Thread:   thread,
		Worktree: &rctx.WorktreeInfo{Path: tmpDir},
	}

	tool := NewGrepTool()
	params := GrepParams{
		Pattern:    "func hello",
		Path:       tmpDir,
		OutputMode: "content",
	}
	inputJSON, _ := json.Marshal(params)

	resp, err := tool.Run(toolCtx, ToolCall{
		ID:    "test-awareness",
		Name:  "grep",
		Input: string(inputJSON),
	})
	if err != nil {
		t.Fatalf("Grep failed: %v", err)
	}
	if !strings.Contains(resp.Content, "func hello") {
		t.Fatalf("Expected grep output to contain match, got: %s", resp.Content)
	}

	// After grep in content mode, awareness should be recorded
	awareness := getLastAwarenessTime(chatID, thread, testFile)
	if awareness.IsZero() {
		t.Error("Expected file awareness to be recorded after grep in content mode")
	}
}

// TestGrepNonContentModeDoesNotRecordAwareness verifies that files_with_matches
// and count modes do NOT record file awareness.
func TestGrepNonContentModeDoesNotRecordAwareness(t *testing.T) {
	tmpDir := t.TempDir()

	testContent := "package main\n\nfunc goodbye() {}\n"
	testFile := filepath.Join(tmpDir, "noaware.go")
	if err := os.WriteFile(testFile, []byte(testContent), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	for _, mode := range []string{"files_with_matches", "count"} {
		t.Run(mode, func(t *testing.T) {
			chatID := "test-grep-no-awareness-" + mode
			thread := "0"
			ClearFileRecordsForThread(chatID, thread)

			toolCtx := &rctx.ToolContext{
				Daemon:   daemon.NewLocalClient(),
				Context:  context.Background(),
				ChatID:   chatID,
				Thread:   thread,
				Worktree: &rctx.WorktreeInfo{Path: tmpDir},
			}

			tool := NewGrepTool()
			params := GrepParams{
				Pattern:    "func goodbye",
				Path:       tmpDir,
				OutputMode: mode,
			}
			inputJSON, _ := json.Marshal(params)

			_, err := tool.Run(toolCtx, ToolCall{
				ID:    "test-no-awareness",
				Name:  "grep",
				Input: string(inputJSON),
			})
			if err != nil {
				t.Fatalf("Grep failed: %v", err)
			}

			awareness := getLastAwarenessTime(chatID, thread, testFile)
			if !awareness.IsZero() {
				t.Errorf("Expected NO file awareness for %s mode, but it was recorded", mode)
			}
		})
	}
}

// TestGrepContextLines tests that grep correctly parses and returns context lines
// when using -A (after), -B (before), or -C (context) options. The daemon
// forces --field-context-separator=":" so context and match lines share the
// same colon-delimited format; without that, paths containing "-" (e.g.
// macOS /var/folders/<two-char>/...) caused context lines to be dropped.
func TestGrepContextLines(t *testing.T) {
	// Create a temporary directory with test files
	tmpDir := t.TempDir()

	// Create a test file with known content
	testContent := `package main

import "fmt"

func main() {
	fmt.Println("Hello")
	fmt.Println("World")
}

func helper() {
	return
}
`
	testFile := filepath.Join(tmpDir, "main.go")
	if err := os.WriteFile(testFile, []byte(testContent), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	toolCtx := &rctx.ToolContext{
		Daemon:   daemon.NewLocalClient(),
		Context:  context.Background(),
		Worktree: &rctx.WorktreeInfo{Path: tmpDir},
	}

	tool := NewGrepTool()

	tests := []struct {
		name            string
		params          GrepParams
		wantContextLine string // A substring that should appear in context
	}{
		{
			name: "context after (-A) should include following lines",
			params: GrepParams{
				Pattern:    "func main",
				Path:       tmpDir,
				OutputMode: "content",
				After:      2,
			},
			wantContextLine: "fmt.Println", // Line after "func main()"
		},
		{
			name: "context before (-B) should include preceding lines",
			params: GrepParams{
				Pattern:    "func main",
				Path:       tmpDir,
				OutputMode: "content",
				Before:     2,
			},
			wantContextLine: "import", // Line before "func main()"
		},
		{
			name: "context both (-C) should include surrounding lines",
			params: GrepParams{
				Pattern:    "func main",
				Path:       tmpDir,
				OutputMode: "content",
				Context:    2,
			},
			wantContextLine: "import", // Context should include import
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inputJSON, _ := json.Marshal(tt.params)
			resp, err := tool.Run(toolCtx, ToolCall{
				ID:    "test-1",
				Name:  "grep",
				Input: string(inputJSON),
			})
			if err != nil {
				t.Fatalf("Grep failed: %v", err)
			}

			output := resp.Content

			// Check that we got matches
			if !strings.Contains(output, "Found") {
				t.Errorf("Expected to find matches, got: %s", output)
			}

			// Check that context lines are included
			if !strings.Contains(output, tt.wantContextLine) {
				t.Errorf("Expected context to include %q, got output:\n%s",
					tt.wantContextLine, output)
			}
		})
	}
}

// TestGrepWorktreePaths tests that grep works correctly when searching
// within worktree directories that have .reliant in the path.
func TestGrepWorktreePaths(t *testing.T) {
	// Create a directory structure simulating a worktree
	tmpDir := t.TempDir()
	worktreePath := filepath.Join(tmpDir, ".reliant", "worktrees", "test-wt", "project")

	err := os.MkdirAll(filepath.Join(worktreePath, "cmd"), 0755)
	if err != nil {
		t.Fatalf("Failed to create directories: %v", err)
	}

	// Create test files
	mainContent := "package main\n\nfunc main() {}\n"
	cmdContent := "package cmd\n\nfunc Execute() error { return nil }\n"

	if err := os.WriteFile(filepath.Join(worktreePath, "main.go"), []byte(mainContent), 0644); err != nil {
		t.Fatalf("Failed to write main.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktreePath, "cmd", "root.go"), []byte(cmdContent), 0644); err != nil {
		t.Fatalf("Failed to write cmd/root.go: %v", err)
	}

	toolCtx := &rctx.ToolContext{
		Daemon:   daemon.NewLocalClient(),
		Context:  context.Background(),
		Worktree: &rctx.WorktreeInfo{Path: worktreePath},
	}

	tool := NewGrepTool()

	params := GrepParams{
		Pattern: "func",
		Path:    worktreePath,
		Type:    "go",
	}
	inputJSON, _ := json.Marshal(params)

	resp, err := tool.Run(toolCtx, ToolCall{
		ID:    "test-1",
		Name:  "grep",
		Input: string(inputJSON),
	})
	if err != nil {
		t.Fatalf("Grep failed: %v", err)
	}

	output := resp.Content

	// Should find both files - the .reliant in the path should not cause filtering
	if strings.Contains(output, "No matches found") {
		t.Errorf("Expected to find matches but got: %s", output)
	}

	if !strings.Contains(output, "main.go") {
		t.Errorf("Expected to find main.go in results: %s", output)
	}

	if !strings.Contains(output, "root.go") {
		t.Errorf("Expected to find cmd/root.go in results: %s", output)
	}
}

// TestGlobWorktreePaths tests that glob works correctly when the working
// directory is within a .reliant worktree path.
func TestGlobWorktreePaths(t *testing.T) {
	// Create a directory structure simulating a worktree
	tmpDir := t.TempDir()
	worktreePath := filepath.Join(tmpDir, ".reliant", "worktrees", "test-wt", "project")

	err := os.MkdirAll(filepath.Join(worktreePath, "internal", "pkg"), 0755)
	if err != nil {
		t.Fatalf("Failed to create directories: %v", err)
	}

	// Create test files
	files := []string{
		filepath.Join(worktreePath, "main.go"),
		filepath.Join(worktreePath, "go.mod"),
		filepath.Join(worktreePath, "internal", "pkg", "utils.go"),
	}
	for _, f := range files {
		if err := os.WriteFile(f, []byte("package test"), 0644); err != nil {
			t.Fatalf("Failed to write %s: %v", f, err)
		}
	}

	toolCtx := &rctx.ToolContext{
		Daemon:   daemon.NewLocalClient(),
		Context:  context.Background(),
		Worktree: &rctx.WorktreeInfo{Path: worktreePath},
	}

	tool := NewGlobTool()

	params := GlobParams{
		Pattern: "**/*.go",
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

	// Should find files, not "No files found"
	if strings.Contains(output, "No files found") {
		t.Errorf("Expected to find files but got: %s", output)
	}

	// Should find both .go files
	if !strings.Contains(output, "main.go") {
		t.Errorf("Expected to find main.go: %s", output)
	}
	if !strings.Contains(output, "utils.go") {
		t.Errorf("Expected to find internal/pkg/utils.go: %s", output)
	}
}

// TestGlobSimplePatterns tests basic glob patterns work correctly
func TestGlobSimplePatterns(t *testing.T) {
	tmpDir := t.TempDir()

	// Create test files
	files := []string{
		filepath.Join(tmpDir, "main.go"),
		filepath.Join(tmpDir, "main_test.go"),
		filepath.Join(tmpDir, "README.md"),
	}
	for _, f := range files {
		if err := os.WriteFile(f, []byte("content"), 0644); err != nil {
			t.Fatalf("Failed to write %s: %v", f, err)
		}
	}

	toolCtx := &rctx.ToolContext{
		Daemon:   daemon.NewLocalClient(),
		Context:  context.Background(),
		Worktree: &rctx.WorktreeInfo{Path: tmpDir},
	}

	tool := NewGlobTool()

	tests := []struct {
		name         string
		pattern      string
		wantContains []string
		wantMissing  []string
	}{
		{
			name:         "*.go matches go files",
			pattern:      "*.go",
			wantContains: []string{"main.go", "main_test.go"},
			wantMissing:  []string{"README.md"},
		},
		{
			name:         "*.md matches markdown",
			pattern:      "*.md",
			wantContains: []string{"README.md"},
			wantMissing:  []string{"main.go"},
		},
		{
			name:         "*_test.go matches test files",
			pattern:      "*_test.go",
			wantContains: []string{"main_test.go"},
			wantMissing:  []string{"main.go", "README.md"},
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

			for _, want := range tt.wantContains {
				if !strings.Contains(output, want) {
					t.Errorf("Expected output to contain %q, got: %s", want, output)
				}
			}

			for _, notWant := range tt.wantMissing {
				if strings.Contains(output, notWant) {
					t.Errorf("Expected output to NOT contain %q, got: %s", notWant, output)
				}
			}
		})
	}
}

// TestGrepTool_GlobPatternNormalization tests that the grep tool's glob parameter
// goes through the same pattern normalization as the glob tool.
func TestGrepTool_GlobPatternNormalization(t *testing.T) {
	tmpDir := t.TempDir()

	// Create directory structure
	dirs := []string{
		filepath.Join(tmpDir, "project", "src"),
		filepath.Join(tmpDir, "sibling"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatalf("Failed to create dir %s: %v", d, err)
		}
	}

	// Create test files with searchable content
	files := map[string]string{
		filepath.Join(tmpDir, "project", "main.go"):       "package main\nfunc findme() {}\n",
		filepath.Join(tmpDir, "project", "src", "app.go"): "package src\nfunc findme() {}\n",
		filepath.Join(tmpDir, "sibling", "other.go"):      "package sibling\nfunc findme() {}\n",
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("Failed to write %s: %v", path, err)
		}
	}

	projectDir := filepath.Join(tmpDir, "project")
	tool := NewGrepTool()

	t.Run("./ prefix in glob is stripped", func(t *testing.T) {
		toolCtx := &rctx.ToolContext{
			Daemon:   daemon.NewLocalClient(),
			Context:  context.Background(),
			Worktree: &rctx.WorktreeInfo{Path: projectDir},
		}

		params := GrepParams{
			Pattern:    "findme",
			Glob:       "./**/*.go",
			OutputMode: "files_with_matches",
		}
		inputJSON, _ := json.Marshal(params)

		resp, err := tool.Run(toolCtx, ToolCall{
			ID:    "test-grep-dot-slash",
			Name:  "grep",
			Input: string(inputJSON),
		})
		if err != nil {
			t.Fatalf("Grep failed: %v", err)
		}

		output := resp.Content
		if strings.Contains(output, "No matches found") {
			t.Errorf("Grep with glob './**/*.go' should find files after normalization, got: %s", output)
		}
		if !strings.Contains(output, "main.go") {
			t.Errorf("Expected to find main.go, got:\n%s", output)
		}
	})

	t.Run("absolute path in glob returns helpful error", func(t *testing.T) {
		toolCtx := &rctx.ToolContext{
			Daemon:   daemon.NewLocalClient(),
			Context:  context.Background(),
			Worktree: &rctx.WorktreeInfo{Path: projectDir},
		}

		params := GrepParams{
			Pattern:    "findme",
			Glob:       "/home/user/project/**/*.go",
			OutputMode: "files_with_matches",
		}
		inputJSON, _ := json.Marshal(params)

		resp, err := tool.Run(toolCtx, ToolCall{
			ID:    "test-grep-abs",
			Name:  "grep",
			Input: string(inputJSON),
		})
		if err != nil {
			t.Fatalf("Grep failed: %v", err)
		}

		output := resp.Content
		if !strings.Contains(output, "Absolute paths") {
			t.Errorf("Expected error message about absolute paths, got:\n%s", output)
		}
	})

	t.Run("../ in glob adjusts search path", func(t *testing.T) {
		srcDir := filepath.Join(projectDir, "src")
		toolCtx := &rctx.ToolContext{
			Daemon:   daemon.NewLocalClient(),
			Context:  context.Background(),
			Worktree: &rctx.WorktreeInfo{Path: srcDir},
		}

		params := GrepParams{
			Pattern:    "findme",
			Glob:       "../**/*.go",
			OutputMode: "files_with_matches",
		}
		inputJSON, _ := json.Marshal(params)

		resp, err := tool.Run(toolCtx, ToolCall{
			ID:    "test-grep-dotdot",
			Name:  "grep",
			Input: string(inputJSON),
		})
		if err != nil {
			t.Fatalf("Grep failed: %v", err)
		}

		output := resp.Content
		if strings.Contains(output, "No matches found") {
			t.Errorf("Grep with glob '../**/*.go' should find files, got: %s", output)
		}
		// Should find files in the parent directory
		if !strings.Contains(output, "main.go") {
			t.Errorf("Expected to find main.go in parent, got:\n%s", output)
		}
	})
}
