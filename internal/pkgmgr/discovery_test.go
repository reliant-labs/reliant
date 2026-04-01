// Copyright (c) 2025 Reliant Labs
package pkgmgr

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverDirectories_RootOnly(t *testing.T) {
	// Create temp directory structure
	tmpDir := t.TempDir()

	// Create a package.json in root
	createFile(t, filepath.Join(tmpDir, "package.json"), `{"name": "test"}`)

	ctx := context.Background()
	opts := DefaultDiscoveryOptions()

	dirs, err := DiscoverDirectories(ctx, tmpDir, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should only find root
	if len(dirs) != 1 {
		t.Errorf("expected 1 directory, got %d: %v", len(dirs), dirs)
	}

	if dirs[0] != tmpDir {
		t.Errorf("expected root dir %s, got %s", tmpDir, dirs[0])
	}
}

func TestDiscoverDirectories_NPMWorkspaces(t *testing.T) {
	// Create temp directory structure with npm workspaces
	tmpDir := t.TempDir()

	// Create root package.json with workspaces
	createFile(t, filepath.Join(tmpDir, "package.json"), `{
		"name": "monorepo",
		"workspaces": ["packages/*"]
	}`)

	// Create workspace packages
	pkg1Dir := filepath.Join(tmpDir, "packages", "pkg1")
	pkg2Dir := filepath.Join(tmpDir, "packages", "pkg2")
	os.MkdirAll(pkg1Dir, 0755)
	os.MkdirAll(pkg2Dir, 0755)
	createFile(t, filepath.Join(pkg1Dir, "package.json"), `{"name": "pkg1"}`)
	createFile(t, filepath.Join(pkg2Dir, "package.json"), `{"name": "pkg2"}`)

	ctx := context.Background()
	opts := DefaultDiscoveryOptions()

	dirs, err := DiscoverDirectories(ctx, tmpDir, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should find root + 2 packages
	if len(dirs) != 3 {
		t.Errorf("expected 3 directories, got %d: %v", len(dirs), dirs)
	}

	// Check all expected dirs are found
	found := make(map[string]bool)
	for _, d := range dirs {
		found[d] = true
	}

	if !found[tmpDir] {
		t.Error("missing root directory")
	}
	if !found[pkg1Dir] {
		t.Error("missing pkg1 directory")
	}
	if !found[pkg2Dir] {
		t.Error("missing pkg2 directory")
	}
}

func TestDiscoverDirectories_PNPMWorkspace(t *testing.T) {
	// Create temp directory structure with pnpm workspace
	tmpDir := t.TempDir()

	// Create pnpm-workspace.yaml
	createFile(t, filepath.Join(tmpDir, "pnpm-workspace.yaml"), `packages:
  - apps/*
  - packages/*
`)

	// Create workspace packages
	appDir := filepath.Join(tmpDir, "apps", "web")
	pkgDir := filepath.Join(tmpDir, "packages", "shared")
	os.MkdirAll(appDir, 0755)
	os.MkdirAll(pkgDir, 0755)
	createFile(t, filepath.Join(appDir, "package.json"), `{"name": "web"}`)
	createFile(t, filepath.Join(pkgDir, "package.json"), `{"name": "shared"}`)

	ctx := context.Background()
	opts := DefaultDiscoveryOptions()

	dirs, err := DiscoverDirectories(ctx, tmpDir, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should find 2 packages (root is not included since it has no package file, only pnpm-workspace.yaml)
	if len(dirs) != 2 {
		t.Errorf("expected 2 directories, got %d: %v", len(dirs), dirs)
	}
}

func TestDiscoverDirectories_CommonPatterns(t *testing.T) {
	// Create temp directory structure without workspace config
	tmpDir := t.TempDir()

	// Create common subdirectories
	frontendDir := filepath.Join(tmpDir, "frontend")
	backendDir := filepath.Join(tmpDir, "backend")
	os.MkdirAll(frontendDir, 0755)
	os.MkdirAll(backendDir, 0755)

	createFile(t, filepath.Join(frontendDir, "package.json"), `{"name": "frontend"}`)
	createFile(t, filepath.Join(backendDir, "Makefile"), "build:\n\tgo build")

	ctx := context.Background()
	opts := DefaultDiscoveryOptions()

	dirs, err := DiscoverDirectories(ctx, tmpDir, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should find root + frontend + backend
	if len(dirs) < 2 {
		t.Errorf("expected at least 2 directories, got %d: %v", len(dirs), dirs)
	}

	found := make(map[string]bool)
	for _, d := range dirs {
		found[d] = true
	}

	if !found[frontendDir] {
		t.Error("missing frontend directory")
	}
	if !found[backendDir] {
		t.Error("missing backend directory")
	}
}

func TestDiscoverDirectories_SkipsNodeModules(t *testing.T) {
	// Create temp directory structure
	tmpDir := t.TempDir()

	// Create root package.json
	createFile(t, filepath.Join(tmpDir, "package.json"), `{"name": "test"}`)

	// Create node_modules with nested package.json (should be skipped)
	nodeModulesDir := filepath.Join(tmpDir, "node_modules", "some-pkg")
	os.MkdirAll(nodeModulesDir, 0755)
	createFile(t, filepath.Join(nodeModulesDir, "package.json"), `{"name": "some-pkg"}`)

	ctx := context.Background()
	opts := DefaultDiscoveryOptions()

	dirs, err := DiscoverDirectories(ctx, tmpDir, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should only find root, not node_modules
	for _, d := range dirs {
		if filepath.Base(d) == "node_modules" || filepath.Base(filepath.Dir(d)) == "node_modules" {
			t.Errorf("node_modules should be skipped, but found: %s", d)
		}
	}
}

func TestDiscoverDirectories_SkipsGitDir(t *testing.T) {
	// Create temp directory structure
	tmpDir := t.TempDir()

	// Create .git directory (should be skipped)
	gitDir := filepath.Join(tmpDir, ".git", "hooks")
	os.MkdirAll(gitDir, 0755)
	createFile(t, filepath.Join(gitDir, "package.json"), `{"name": "hooks"}`)

	ctx := context.Background()
	opts := DefaultDiscoveryOptions()

	dirs, err := DiscoverDirectories(ctx, tmpDir, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, d := range dirs {
		if filepath.Base(d) == ".git" || filepath.Base(filepath.Dir(d)) == ".git" {
			t.Errorf(".git should be skipped, but found: %s", d)
		}
	}
}

func TestHasPackageFile(t *testing.T) {
	tmpDir := t.TempDir()

	// No package files initially
	if hasPackageFile(tmpDir) {
		t.Error("should not detect package file in empty directory")
	}

	// Add package.json
	createFile(t, filepath.Join(tmpDir, "package.json"), `{}`)
	if !hasPackageFile(tmpDir) {
		t.Error("should detect package.json")
	}

	// Test Makefile
	tmpDir2 := t.TempDir()
	createFile(t, filepath.Join(tmpDir2, "Makefile"), "build:")
	if !hasPackageFile(tmpDir2) {
		t.Error("should detect Makefile")
	}

	// Test Taskfile.yml
	tmpDir3 := t.TempDir()
	createFile(t, filepath.Join(tmpDir3, "Taskfile.yml"), "version: 3")
	if !hasPackageFile(tmpDir3) {
		t.Error("should detect Taskfile.yml")
	}
}

// Helper to create a file with content
func createFile(t *testing.T, path, content string) {
	t.Helper()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("failed to create directory %s: %v", dir, err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write file %s: %v", path, err)
	}
}
