// Copyright (c) 2025 Reliant Labs
package fileutil

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/reliant-labs/reliant/internal/logging"
)

// NormalizeGlobResult holds the result of normalizing a glob pattern.
type NormalizeGlobResult struct {
	// Pattern is the normalized glob pattern (may differ from input).
	Pattern string
	// PathAdjustment is a relative path to prepend to the search path (e.g., ".." for ../ patterns).
	// Empty string means no adjustment needed.
	PathAdjustment string
	// ErrorMessage is set when the pattern cannot be used and we should return a helpful message.
	// Empty string means the pattern is valid.
	ErrorMessage string
}

// NormalizeGlobPattern normalizes a glob pattern for use with ripgrep and doublestar.
// It fixes common patterns that would silently return 0 results, and returns helpful
// error messages for patterns that can't be automatically fixed.
//
// Common issues handled:
//   - "./**/*.go" → "**/*.go" (strip leading ./)
//   - "../**/*.go" → pattern="**/*.go", pathAdjustment=".." (adjust search path)
//   - Absolute paths in pattern → error with suggestion to use path parameter
func NormalizeGlobPattern(pattern, searchPath string) NormalizeGlobResult {
	original := pattern

	// Detect absolute paths in the pattern (e.g., "/home/user/project/**/*.go")
	// On Windows, also check for drive letters like "C:\..."
	if filepath.IsAbs(pattern) {
		// Try to extract a useful glob from the absolute path
		// e.g., "/home/user/project/**/*.go" → suggest using path="/home/user/project" pattern="**/*.go"
		if idx := strings.Index(pattern, "**"); idx > 0 {
			trimSet := string(filepath.Separator)
			if filepath.Separator != '/' {
				trimSet += "/"
			}
			suggestedPath := strings.TrimRight(pattern[:idx], trimSet)
			suggestedPattern := pattern[idx:]
			return NormalizeGlobResult{
				ErrorMessage: fmt.Sprintf(
					"Absolute paths in glob patterns are not supported. "+
						"Use the 'path' parameter instead.\n"+
						"Try: pattern=%q, path=%q (was: %q)",
					suggestedPattern, suggestedPath, original),
			}
		}
		// Check for patterns like "/path/to/*.go" (with a single wildcard)
		if idx := strings.LastIndex(pattern, "/"); idx > 0 {
			base := pattern[idx+1:]
			if strings.ContainsAny(base, "*?[") {
				suggestedPath := pattern[:idx]
				return NormalizeGlobResult{
					ErrorMessage: fmt.Sprintf(
						"Absolute paths in glob patterns are not supported. "+
							"Use the 'path' parameter instead.\n"+
							"Try: pattern=%q, path=%q (was: %q)",
						base, suggestedPath, original),
				}
			}
		}
		// Plain absolute path with no wildcards - it's probably a directory
		return NormalizeGlobResult{
			ErrorMessage: fmt.Sprintf(
				"Absolute paths in glob patterns are not supported. "+
					"Use the 'path' parameter to set the search directory and 'pattern' for the glob.\n"+
					"Try: pattern=%q, path=%q (was: %q)",
				"**/*", pattern, original),
		}
	}

	// Handle ../ patterns: extract the relative path prefix and adjust search path
	if strings.HasPrefix(pattern, "..") {
		// Split into path prefix and glob suffix
		// e.g., "../../src/**/*.go" → pathAdj="../../src", pattern="**/*.go"
		// e.g., "../*.go" → pathAdj="..", pattern="*.go"
		parts := splitRelativePrefix(pattern)
		if parts.pathPrefix != "" {
			return NormalizeGlobResult{
				Pattern:        parts.globSuffix,
				PathAdjustment: parts.pathPrefix,
			}
		}
	}

	// Strip leading ./ which causes ripgrep to return 0 results
	// e.g., "./**/*.go" → "**/*.go", "./*.go" → "*.go"
	for strings.HasPrefix(pattern, "./") {
		pattern = pattern[2:]
	}
	// Also handle Windows-style .\
	if runtime.GOOS == "windows" {
		for strings.HasPrefix(pattern, ".\\") {
			pattern = pattern[2:]
		}
	}

	return NormalizeGlobResult{
		Pattern: pattern,
	}
}

type relPrefixParts struct {
	pathPrefix string // e.g., "../src" or ".."
	globSuffix string // e.g., "**/*.go" or "*.go"
}

// splitRelativePrefix splits a pattern starting with ../ into a path prefix and glob suffix.
// The path prefix contains all ../ segments and any non-glob directory components.
// The glob suffix is the remaining pattern containing wildcards.
func splitRelativePrefix(pattern string) relPrefixParts {
	// Use forward slashes for splitting
	normalized := filepath.ToSlash(pattern)
	// Remove trailing slash before splitting to avoid empty components
	normalized = strings.TrimRight(normalized, "/")
	parts := strings.Split(normalized, "/")

	// Find where the glob part starts (first component with wildcards)
	splitIdx := 0
	for i, part := range parts {
		if strings.ContainsAny(part, "*?[") {
			splitIdx = i
			break
		}
		splitIdx = i + 1
	}

	if splitIdx == 0 {
		// The first component itself has wildcards
		// This shouldn't happen for ../ patterns, but handle gracefully
		return relPrefixParts{}
	}

	pathPrefix := strings.Join(parts[:splitIdx], string(filepath.Separator))
	globSuffix := strings.Join(parts[splitIdx:], "/")

	// If the entire pattern is path components (no glob part), use **/* as the pattern
	if globSuffix == "" {
		globSuffix = "**/*"
	}

	return relPrefixParts{
		pathPrefix: pathPrefix,
		globSuffix: globSuffix,
	}
}

var (
	rgPath     string
	rgPathOnce sync.Once
)

func lookupRg() {
	var err error
	rgPath, err = exec.LookPath("rg")
	if err != nil {
		logging.Warn("Ripgrep (rg) not found in $PATH. You should consider installing rg for faster filesearch.")
		rgPath = ""
	}
}

// GetRgCmd creates a ripgrep command for file listing.
// includeIgnored controls whether to include commonly ignored directories (node_modules, vendor, etc.).
// Hidden files (starting with .) are always included.
func GetRgCmd(globPattern string, includeIgnored bool) *exec.Cmd {
	rgPathOnce.Do(lookupRg)
	if rgPath == "" {
		return nil
	}
	rgArgs := []string{
		"--files",
		"-L",
		"--null",
		"--hidden",    // Always include hidden files (.github, .vscode, etc.)
		"--no-ignore", // Don't respect .gitignore - we filter noisy dirs ourselves via ShouldSkip
	}
	if globPattern != "" {
		// Note: Do NOT prepend '/' to glob patterns.
		// In ripgrep, a leading '/' anchors the pattern to the root of the search directory,
		// which breaks patterns like "*grep*.go" (would only match in root, not subdirs).
		// Patterns without leading '/' match recursively by default.
		rgArgs = append(rgArgs, "--glob", globPattern)
	}
	cmd := exec.Command(rgPath, rgArgs...)
	cmd.Dir = "."
	return cmd
}

type FileInfo struct {
	Path    string
	ModTime time.Time
}

// IgnoredDirs contains directories that are excluded from search by default.
// These are typically large dependency directories, build outputs, or internal tool data.
var IgnoredDirs = map[string]bool{
	// Internal tool data
	".reliant": true,
	".git":     true,

	// Dependencies
	"node_modules":     true,
	"vendor":           true,
	"bower_components": true,
	"jspm_packages":    true,

	// Build outputs
	"dist":      true,
	"build":     true,
	"target":    true,
	"bin":       true,
	"obj":       true,
	"out":       true,
	"generated": true,

	// Cache/temporary
	"__pycache__": true,
	"coverage":    true,
	"tmp":         true,
	"temp":        true,
	"logs":        true,
}

// ShouldSkip returns true if the path should be excluded from search results.
// It checks if any path component is in the IgnoredDirs list.
// Note: Hidden files/directories (starting with .) are NOT excluded by default,
// only explicitly listed noisy directories are excluded.
func ShouldSkip(path string) bool {
	parts := strings.Split(path, string(os.PathSeparator))
	for _, part := range parts {
		if IgnoredDirs[part] {
			return true
		}
	}
	return false
}

func GlobWithDoublestar(pattern, searchPath string, limit int, includeIgnored bool) ([]string, bool, error) {
	fsys := os.DirFS(searchPath)
	relPattern := strings.TrimPrefix(pattern, "/")
	var matches []FileInfo

	err := doublestar.GlobWalk(fsys, relPattern, func(path string, d fs.DirEntry) error {
		if d.IsDir() {
			return nil
		}
		if !includeIgnored && ShouldSkip(path) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		absPath := path
		if !strings.HasPrefix(absPath, searchPath) && searchPath != "." {
			absPath = filepath.Join(searchPath, absPath)
		} else if !strings.HasPrefix(absPath, "/") && searchPath == "." {
			absPath = filepath.Join(searchPath, absPath) // Ensure relative paths are joined correctly
		}

		matches = append(matches, FileInfo{Path: absPath, ModTime: info.ModTime()})
		if limit > 0 && len(matches) >= limit*2 {
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return nil, false, fmt.Errorf("glob walk error: %w", err)
	}

	sort.Slice(matches, func(i, j int) bool {
		return matches[i].ModTime.After(matches[j].ModTime)
	})

	truncated := false
	if limit > 0 && len(matches) > limit {
		matches = matches[:limit]
		truncated = true
	}

	results := make([]string, len(matches))
	for i, m := range matches {
		results[i] = m.Path
	}
	return results, truncated, nil
}
