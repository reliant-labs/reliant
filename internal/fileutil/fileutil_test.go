// Copyright (c) 2025 Reliant Labs
package fileutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestShouldSkip_IgnoredDirs tests ShouldSkip behavior with various path types.
// ShouldSkip only filters commonly ignored/noisy directories, NOT hidden files in general.
// IMPORTANT: ShouldSkip checks path components directly, so absolute paths containing
// ".reliant" in parent directories WILL be skipped. Callers should use RELATIVE paths
// (relative to the search root) to avoid this issue.
func TestShouldSkip_IgnoredDirs(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		expected bool
	}{
		// IMPORTANT: Absolute paths with .reliant in parent dirs ARE skipped.
		// This is expected behavior - callers must use relative paths instead.
		{
			name:     "absolute path with .reliant in parent IS skipped (use relative paths instead)",
			path:     "/Users/user/.reliant/worktrees/abc123/project/cmd/main.go",
			expected: true, // Contains .reliant in path - callers should use relative paths
		},
		{
			name:     "worktree path with internal directory IS skipped",
			path:     "/home/dev/.reliant/worktrees/feature-1/internal/pkg/file.go",
			expected: true, // Contains .reliant in path
		},
		{
			name:     "worktree root go.mod IS skipped",
			path:     "/Users/test/.reliant/worktrees/rand-10/go.mod",
			expected: true, // Contains .reliant in path
		},

		// Relative paths should work correctly
		{
			name:     "relative path cmd/main.go",
			path:     "cmd/main.go",
			expected: false,
		},
		{
			name:     "relative path internal/pkg/file.go",
			path:     "internal/pkg/file.go",
			expected: false,
		},

		// These SHOULD be skipped - actual .reliant directories within project
		{
			name:     "actual .reliant config dir in project",
			path:     ".reliant/config.yaml",
			expected: true,
		},
		{
			name:     "nested .reliant dir",
			path:     "project/.reliant/settings.json",
			expected: true,
		},

		// Noisy directories should be skipped
		{
			name:     "node_modules should be skipped",
			path:     "node_modules/package/index.js",
			expected: true,
		},
		{
			name:     "vendor should be skipped",
			path:     "vendor/github.com/pkg/file.go",
			expected: true,
		},
		{
			name:     ".git should be skipped",
			path:     ".git/config",
			expected: true,
		},

		// Hidden files are NOT skipped (only directories in IgnoredDirs)
		{
			name:     "hidden file .gitignore is NOT skipped",
			path:     ".gitignore",
			expected: false, // Hidden files are included
		},
		{
			name:     "hidden file in subdir is NOT skipped",
			path:     "config/.env",
			expected: false, // Hidden files are included
		},
		{
			name:     ".github directory is NOT skipped",
			path:     ".github/workflows/ci.yml",
			expected: false, // .github is not in IgnoredDirs
		},
		{
			name:     ".vscode directory is NOT skipped",
			path:     ".vscode/settings.json",
			expected: false, // .vscode is not in IgnoredDirs
		},

		// Regular files should not be skipped
		{
			name:     "regular go file",
			path:     "main.go",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ShouldSkip(tt.path)
			if got != tt.expected {
				t.Errorf("ShouldSkip(%q) = %v, want %v", tt.path, got, tt.expected)
			}
		})
	}
}

// TestShouldSkip_RelativePaths tests that ShouldSkip works correctly
// when using relative paths computed from an absolute path.
func TestShouldSkip_RelativePaths(t *testing.T) {
	tests := []struct {
		name       string
		absPath    string
		searchRoot string
		expected   bool
	}{
		{
			name:       "regular file in worktree should not be skipped",
			absPath:    "/Users/user/.reliant/worktrees/abc/project/cmd/main.go",
			searchRoot: "/Users/user/.reliant/worktrees/abc/project",
			expected:   false,
		},
		{
			name:       "node_modules in worktree should be skipped",
			absPath:    "/Users/user/.reliant/worktrees/abc/project/node_modules/pkg/index.js",
			searchRoot: "/Users/user/.reliant/worktrees/abc/project",
			expected:   true,
		},
		{
			name:       ".reliant config IN project should be skipped",
			absPath:    "/Users/user/.reliant/worktrees/abc/project/.reliant/config.yaml",
			searchRoot: "/Users/user/.reliant/worktrees/abc/project",
			expected:   true,
		},
		{
			name:       "go.mod at worktree root should not be skipped",
			absPath:    "/Users/user/.reliant/worktrees/abc/project/go.mod",
			searchRoot: "/Users/user/.reliant/worktrees/abc/project",
			expected:   false,
		},
		{
			name:       ".github in worktree should NOT be skipped",
			absPath:    "/Users/user/.reliant/worktrees/abc/project/.github/workflows/ci.yml",
			searchRoot: "/Users/user/.reliant/worktrees/abc/project",
			expected:   false, // .github is not in IgnoredDirs
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Compute relative path from search root
			relPath, err := filepath.Rel(tt.searchRoot, tt.absPath)
			if err != nil {
				t.Fatalf("Failed to compute relative path: %v", err)
			}

			got := ShouldSkip(relPath)
			if got != tt.expected {
				t.Errorf("ShouldSkip(rel=%q) = %v, want %v (abs was %q)",
					relPath, got, tt.expected, tt.absPath)
			}
		})
	}
}

// TestGlobWithDoublestar_WorktreePaths tests that GlobWithDoublestar works
// correctly when the search path is within a .reliant worktree directory.
func TestGlobWithDoublestar_WorktreePaths(t *testing.T) {
	// Create a temporary directory structure simulating a worktree
	tmpDir := t.TempDir()

	// Simulate worktree path structure: /tmp/.reliant/worktrees/test/project
	worktreePath := filepath.Join(tmpDir, ".reliant", "worktrees", "test", "project")
	err := os.MkdirAll(filepath.Join(worktreePath, "cmd"), 0755)
	if err != nil {
		t.Fatalf("Failed to create test directory: %v", err)
	}

	// Create some test files
	testFiles := []string{
		filepath.Join(worktreePath, "main.go"),
		filepath.Join(worktreePath, "go.mod"),
		filepath.Join(worktreePath, "cmd", "root.go"),
	}
	for _, f := range testFiles {
		if err := os.WriteFile(f, []byte("package main"), 0644); err != nil {
			t.Fatalf("Failed to create test file %s: %v", f, err)
		}
	}

	// Test globbing from within the worktree (with includeIgnored=false, the default)
	matches, truncated, err := GlobWithDoublestar("**/*.go", worktreePath, 100, false)
	if err != nil {
		t.Fatalf("GlobWithDoublestar failed: %v", err)
	}

	if truncated {
		t.Error("Results should not be truncated")
	}

	// Should find both .go files
	if len(matches) != 2 {
		t.Errorf("Expected 2 matches, got %d: %v", len(matches), matches)
	}

	// Verify the files found are the expected ones
	foundMain := false
	foundCmd := false
	for _, m := range matches {
		if filepath.Base(m) == "main.go" {
			foundMain = true
		}
		if filepath.Base(m) == "root.go" {
			foundCmd = true
		}
	}
	if !foundMain {
		t.Error("Expected to find main.go")
	}
	if !foundCmd {
		t.Error("Expected to find cmd/root.go")
	}
}

// TestGlobWithDoublestar_IncludeIgnored tests that includeIgnored parameter works correctly.
// Hidden files (starting with .) are INCLUDED by default.
// Only noisy directories (node_modules, vendor, etc.) are excluded by default.
func TestGlobWithDoublestar_IncludeIgnored(t *testing.T) {
	// Create a temporary directory structure
	tmpDir := t.TempDir()

	// Create directories
	err := os.MkdirAll(filepath.Join(tmpDir, ".github", "workflows"), 0755)
	if err != nil {
		t.Fatalf("Failed to create .github directory: %v", err)
	}
	err = os.MkdirAll(filepath.Join(tmpDir, "src"), 0755)
	if err != nil {
		t.Fatalf("Failed to create src directory: %v", err)
	}
	err = os.MkdirAll(filepath.Join(tmpDir, "node_modules", "pkg"), 0755)
	if err != nil {
		t.Fatalf("Failed to create node_modules directory: %v", err)
	}

	// Create test files
	testFiles := []string{
		filepath.Join(tmpDir, ".github", "workflows", "ci.yml"),
		filepath.Join(tmpDir, "src", "main.go"),
		filepath.Join(tmpDir, ".gitignore"),
		filepath.Join(tmpDir, "node_modules", "pkg", "index.js"),
	}
	for _, f := range testFiles {
		if err := os.WriteFile(f, []byte("test"), 0644); err != nil {
			t.Fatalf("Failed to create test file %s: %v", f, err)
		}
	}

	// Test 1: Default behavior (includeIgnored=false)
	// Should find: src/main.go, .github/workflows/ci.yml, .gitignore
	// Should NOT find: node_modules/pkg/index.js (noisy directory)
	matches, _, err := GlobWithDoublestar("**/*", tmpDir, 100, false)
	if err != nil {
		t.Fatalf("GlobWithDoublestar failed: %v", err)
	}

	// Should find 3 files (hidden files included, node_modules excluded)
	if len(matches) != 3 {
		t.Errorf("With includeIgnored=false: expected 3 matches, got %d: %v", len(matches), matches)
	}

	// Verify hidden files are included
	foundGitignore := false
	foundGithub := false
	for _, m := range matches {
		if filepath.Base(m) == ".gitignore" {
			foundGitignore = true
		}
		if filepath.Base(m) == "ci.yml" {
			foundGithub = true
		}
	}
	if !foundGitignore {
		t.Error("Expected to find .gitignore (hidden files should be included)")
	}
	if !foundGithub {
		t.Error("Expected to find .github/workflows/ci.yml (hidden dirs should be included)")
	}

	// Test 2: With includeIgnored=true - should find all files
	matches, _, err = GlobWithDoublestar("**/*", tmpDir, 100, true)
	if err != nil {
		t.Fatalf("GlobWithDoublestar failed: %v", err)
	}

	// Should find all 4 files including node_modules
	if len(matches) != 4 {
		t.Errorf("With includeIgnored=true: expected 4 matches, got %d: %v", len(matches), matches)
	}
}

// TestNormalizeGlobPattern tests the pattern normalization function.
func TestNormalizeGlobPattern(t *testing.T) {
	tests := []struct {
		name          string
		pattern       string
		searchPath    string
		wantPattern   string
		wantPathAdj   string
		wantError     bool
		errorContains string
	}{
		// Patterns that should pass through unchanged
		{
			name:        "simple wildcard unchanged",
			pattern:     "*.go",
			searchPath:  "/project",
			wantPattern: "*.go",
		},
		{
			name:        "double star unchanged",
			pattern:     "**/*.go",
			searchPath:  "/project",
			wantPattern: "**/*.go",
		},
		{
			name:        "subdir pattern unchanged",
			pattern:     "src/**/*.ts",
			searchPath:  "/project",
			wantPattern: "src/**/*.ts",
		},
		{
			name:        "brace expansion unchanged",
			pattern:     "*.{go,ts}",
			searchPath:  "/project",
			wantPattern: "*.{go,ts}",
		},
		{
			name:        "character class unchanged",
			pattern:     "[abc].go",
			searchPath:  "/project",
			wantPattern: "[abc].go",
		},
		{
			name:        "exact filename unchanged",
			pattern:     "go.mod",
			searchPath:  "/project",
			wantPattern: "go.mod",
		},
		{
			name:        "all files unchanged",
			pattern:     "**/*",
			searchPath:  "/project",
			wantPattern: "**/*",
		},

		// ./ prefix stripping
		{
			name:        "strip ./ from ./**/*.go",
			pattern:     "./**/*.go",
			searchPath:  "/project",
			wantPattern: "**/*.go",
		},
		{
			name:        "strip ./ from ./*.go",
			pattern:     "./*.go",
			searchPath:  "/project",
			wantPattern: "*.go",
		},
		{
			name:        "strip ./ from ./src/**/*.go",
			pattern:     "./src/**/*.go",
			searchPath:  "/project",
			wantPattern: "src/**/*.go",
		},
		{
			name:        "strip multiple ./ prefixes",
			pattern:     "././*.go",
			searchPath:  "/project",
			wantPattern: "*.go",
		},

		// ../ patterns - path adjustment
		{
			name:        "../*.go adjusts path",
			pattern:     "../*.go",
			searchPath:  "/project/src",
			wantPattern: "*.go",
			wantPathAdj: "..",
		},
		{
			name:        "../../*.go adjusts path",
			pattern:     "../../*.go",
			searchPath:  "/project/src/sub",
			wantPattern: "*.go",
			wantPathAdj: filepath.Join("..", ".."),
		},
		{
			name:        "../**/*.go adjusts path",
			pattern:     "../**/*.go",
			searchPath:  "/project/src",
			wantPattern: "**/*.go",
			wantPathAdj: "..",
		},
		{
			name:        "../sibling/**/*.go adjusts path",
			pattern:     "../sibling/**/*.go",
			searchPath:  "/project/src",
			wantPattern: "**/*.go",
			wantPathAdj: filepath.Join("..", "sibling"),
		},
		{
			name:        "../sibling/ with no glob defaults to **/*",
			pattern:     "../sibling/",
			searchPath:  "/project/src",
			wantPattern: "**/*",
			wantPathAdj: filepath.Join("..", "sibling"),
		},

		// Absolute paths - error
		{
			name:          "absolute path with ** glob",
			pattern:       "/home/user/project/**/*.go",
			searchPath:    "/somewhere",
			wantError:     true,
			errorContains: "Absolute paths in glob patterns are not supported",
		},
		{
			name:          "absolute path with single wildcard",
			pattern:       "/home/user/project/*.go",
			searchPath:    "/somewhere",
			wantError:     true,
			errorContains: "Use the 'path' parameter",
		},
		{
			name:          "absolute path no wildcard",
			pattern:       "/home/user/project",
			searchPath:    "/somewhere",
			wantError:     true,
			errorContains: "path",
		},
		{
			name:          "absolute path suggests correct split",
			pattern:       "/home/user/project/**/*.go",
			searchPath:    "/somewhere",
			wantError:     true,
			errorContains: "/home/user/project",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := NormalizeGlobPattern(tt.pattern, tt.searchPath)

			if tt.wantError {
				if result.ErrorMessage == "" {
					t.Errorf("NormalizeGlobPattern(%q) expected error, got pattern=%q pathAdj=%q",
						tt.pattern, result.Pattern, result.PathAdjustment)
				}
				if tt.errorContains != "" && !strings.Contains(result.ErrorMessage, tt.errorContains) {
					t.Errorf("NormalizeGlobPattern(%q) error=%q, want it to contain %q",
						tt.pattern, result.ErrorMessage, tt.errorContains)
				}
				return
			}

			if result.ErrorMessage != "" {
				t.Errorf("NormalizeGlobPattern(%q) unexpected error: %s",
					tt.pattern, result.ErrorMessage)
				return
			}

			if result.Pattern != tt.wantPattern {
				t.Errorf("NormalizeGlobPattern(%q).Pattern = %q, want %q",
					tt.pattern, result.Pattern, tt.wantPattern)
			}
			if result.PathAdjustment != tt.wantPathAdj {
				t.Errorf("NormalizeGlobPattern(%q).PathAdjustment = %q, want %q",
					tt.pattern, result.PathAdjustment, tt.wantPathAdj)
			}
		})
	}
}

// TestSplitRelativePrefix tests the internal relative prefix splitting logic.
func TestSplitRelativePrefix(t *testing.T) {
	tests := []struct {
		name       string
		pattern    string
		wantPrefix string
		wantGlob   string
	}{
		{
			name:       "simple ../*.go",
			pattern:    "../*.go",
			wantPrefix: "..",
			wantGlob:   "*.go",
		},
		{
			name:       "double ../../*.go",
			pattern:    "../../*.go",
			wantPrefix: filepath.Join("..", ".."),
			wantGlob:   "*.go",
		},
		{
			name:       "with directory ../ sibling/**/*.go",
			pattern:    "../sibling/**/*.go",
			wantPrefix: filepath.Join("..", "sibling"),
			wantGlob:   "**/*.go",
		},
		{
			name:       "no glob suffix defaults to **/*",
			pattern:    "../other",
			wantPrefix: filepath.Join("..", "other"),
			wantGlob:   "**/*",
		},
		{
			name:       "trailing slash no glob defaults to **/*",
			pattern:    "../other/",
			wantPrefix: filepath.Join("..", "other"),
			wantGlob:   "**/*",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := splitRelativePrefix(tt.pattern)
			if result.pathPrefix != tt.wantPrefix {
				t.Errorf("splitRelativePrefix(%q).pathPrefix = %q, want %q",
					tt.pattern, result.pathPrefix, tt.wantPrefix)
			}
			if result.globSuffix != tt.wantGlob {
				t.Errorf("splitRelativePrefix(%q).globSuffix = %q, want %q",
					tt.pattern, result.globSuffix, tt.wantGlob)
			}
		})
	}
}
