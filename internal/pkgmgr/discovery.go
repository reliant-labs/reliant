// Copyright (c) 2025 Reliant Labs
//
// forge:exclude-contract
//
// Leaf utility package: the exported surface is concrete helpers over the
// stdlib or the OS, with no collaborator to fake and no second implementation.
// An interface here would have exactly one implementor and one caller shape,
// which is indirection without a seam.
package pkgmgr

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/reliant/internal/logging"
)

// DiscoveryOptions configures how package files are discovered
type DiscoveryOptions struct {
	// MaxDepth is the maximum directory depth to search (0 = unlimited, default 10)
	MaxDepth int
	// SkipDirs are directory names to skip during discovery (case-insensitive)
	SkipDirs []string
}

// DefaultDiscoveryOptions returns sensible defaults for discovery
func DefaultDiscoveryOptions() DiscoveryOptions {
	return DiscoveryOptions{
		MaxDepth: 10, // Reasonable depth limit
		SkipDirs: []string{
			// Package manager directories
			"node_modules",
			"vendor",
			".pnpm-store",
			"bower_components",

			// Version control
			".git",
			".svn",
			".hg",

			// Build outputs
			"dist",
			"build",
			".next",
			".nuxt",
			"target", // Rust/Java
			"out",
			"output",

			// Cache directories
			"cache",
			".cache",
			".turbo",
			".nx",
			".parcel-cache",

			// Test fixtures (often have fake package.json)
			"__tests__",
			"__mocks__",
			"__fixtures__",
			"fixtures",
			"testdata",
			"test-fixtures",

			// Python
			"__pycache__",
			".venv",
			"venv",

			// Coverage and temp
			"coverage",
			".coverage",
			".nyc_output",
			"tmp",
			".tmp",
			"temp",

			// IDE
			".vscode",
			".idea",
			".vs",
		},
	}
}

// DiscoverDirectories finds all directories containing package files.
// It walks the directory tree, skipping directories in SkipDirs,
// and returns all directories that contain package.json, Makefile, or Taskfile.
func DiscoverDirectories(ctx context.Context, rootDir string, opts DiscoveryOptions) ([]string, error) {
	dirs := make(map[string]bool)

	// Always include root if it has a package file
	if hasPackageFile(rootDir) {
		dirs[rootDir] = true
	}

	// Build skip set for fast lookup (case-insensitive)
	skipSet := make(map[string]bool)
	for _, s := range opts.SkipDirs {
		skipSet[strings.ToLower(s)] = true
	}

	// Walk the directory tree looking for package files
	walkDirs := discoverAllPackageDirectories(ctx, rootDir, opts.MaxDepth, skipSet)
	for _, d := range walkDirs {
		dirs[d] = true
	}

	logging.Debug("Discovered directories with package files",
		"root", rootDir,
		"count", len(dirs))

	// Convert map to slice
	result := make([]string, 0, len(dirs))
	for d := range dirs {
		result = append(result, d)
	}

	return result, nil
}

// discoverAllPackageDirectories walks the directory tree and finds all
// directories containing package files (package.json, Makefile, Taskfile)
func discoverAllPackageDirectories(ctx context.Context, rootDir string, maxDepth int, skipSet map[string]bool) []string {
	var dirs []string

	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Check depth limit
		if maxDepth > 0 && depth > maxDepth {
			return
		}

		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}

			name := entry.Name()

			// Skip directories in skip list (case-insensitive)
			if skipSet[strings.ToLower(name)] {
				continue
			}

			// Skip hidden directories (except specific ones we might care about)
			if strings.HasPrefix(name, ".") {
				continue
			}

			childPath := filepath.Join(dir, name)

			// Check if this directory has any package files
			if hasPackageFile(childPath) {
				dirs = append(dirs, childPath)
			}

			// Continue walking into subdirectories
			walk(childPath, depth+1)
		}
	}

	walk(rootDir, 1)
	return dirs
}

// hasPackageFile checks if a directory contains any recognized package file
func hasPackageFile(dir string) bool {
	packageFiles := []string{
		"package.json",
		"Makefile",
		"Taskfile.yml",
		"Taskfile.yaml",
	}

	for _, file := range packageFiles {
		if _, err := os.Stat(filepath.Join(dir, file)); err == nil {
			return true
		}
	}
	return false
}
