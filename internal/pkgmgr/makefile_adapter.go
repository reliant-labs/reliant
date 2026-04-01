// Copyright (c) 2025 Reliant Labs
package pkgmgr

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// MakefileAdapter parses Makefile targets
type MakefileAdapter struct{}

// NewMakefileAdapter creates a new Makefile adapter
func NewMakefileAdapter() *MakefileAdapter {
	return &MakefileAdapter{}
}

// Type returns the package type for Makefile
func (a *MakefileAdapter) Type() PackageType {
	return PackageTypeMakefile
}

// Detect checks if a Makefile exists in the given directory
func (a *MakefileAdapter) Detect(ctx context.Context, dir string) (bool, error) {
	path := a.FilePath(dir)
	_, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}

// FilePath returns the path to the Makefile
func (a *MakefileAdapter) FilePath(dir string) string {
	return filepath.Join(dir, "Makefile")
}

// Parse extracts targets from the Makefile
// Supports the following patterns:
// - Standard targets: target:
// - Targets with help comments: target: ## Description
// - .PHONY declarations
func (a *MakefileAdapter) Parse(ctx context.Context, dir string) ([]Command, error) {
	path := a.FilePath(dir)
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var commands []Command
	var pendingComment string

	// Regex for target lines: target_name: [dependencies]
	targetRegex := regexp.MustCompile(`^([a-zA-Z0-9_-]+)\s*:\s*(.*)$`)

	// Regex for help comments: ## Description
	helpCommentRegex := regexp.MustCompile(`^##\s*(.*)$`)

	// Regex for inline help: target: ## Description
	inlineHelpRegex := regexp.MustCompile(`^([a-zA-Z0-9_-]+)\s*:.*##\s*(.*)$`)

	scanner := bufio.NewScanner(file)
	lineNum := 0

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		lineNum++
		line := scanner.Text()

		// Check for help comment on its own line
		if matches := helpCommentRegex.FindStringSubmatch(line); matches != nil {
			pendingComment = strings.TrimSpace(matches[1])
			continue
		}

		// Skip .PHONY, variables, etc.
		if strings.HasPrefix(line, ".") || strings.HasPrefix(line, "\t") ||
			strings.HasPrefix(line, " ") || strings.Contains(line, "=") {
			pendingComment = "" // Reset pending comment
			continue
		}

		// Check for inline help first
		if matches := inlineHelpRegex.FindStringSubmatch(line); matches != nil {
			name := strings.TrimSpace(matches[1])
			description := strings.TrimSpace(matches[2])

			// Skip internal targets (those starting with _)
			if strings.HasPrefix(name, "_") {
				pendingComment = ""
				continue
			}

			commands = append(commands, Command{
				Name:        name,
				Description: description,
				Command:     "make " + name,
				PackageType: PackageTypeMakefile,
				Source:      path,
				Category:    categorizeTarget(name),
			})
			pendingComment = ""
			continue
		}

		// Check for regular target
		if matches := targetRegex.FindStringSubmatch(line); matches != nil {
			name := strings.TrimSpace(matches[1])

			// Skip internal targets (those starting with _)
			if strings.HasPrefix(name, "_") {
				pendingComment = ""
				continue
			}

			// Use pending comment if available
			description := pendingComment

			commands = append(commands, Command{
				Name:        name,
				Description: description,
				Command:     "make " + name,
				PackageType: PackageTypeMakefile,
				Source:      path,
				Category:    categorizeTarget(name),
			})
			pendingComment = ""
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return commands, nil
}

// categorizeTarget attempts to categorize a make target based on its name
func categorizeTarget(name string) string {
	nameLower := strings.ToLower(name)

	// Build-related
	if strings.Contains(nameLower, "build") || strings.Contains(nameLower, "compile") {
		return "build"
	}

	// Test-related
	if strings.Contains(nameLower, "test") {
		return "test"
	}

	// Dev-related
	if strings.Contains(nameLower, "dev") || strings.Contains(nameLower, "run") ||
		strings.Contains(nameLower, "start") || strings.Contains(nameLower, "serve") {
		return "dev"
	}

	// Clean/maintenance
	if strings.Contains(nameLower, "clean") || strings.Contains(nameLower, "purge") {
		return "clean"
	}

	// Install/setup
	if strings.Contains(nameLower, "install") || strings.Contains(nameLower, "setup") ||
		strings.Contains(nameLower, "deps") {
		return "setup"
	}

	// Lint/format
	if strings.Contains(nameLower, "lint") || strings.Contains(nameLower, "fmt") ||
		strings.Contains(nameLower, "format") || strings.Contains(nameLower, "vet") {
		return "lint"
	}

	// Docker
	if strings.Contains(nameLower, "docker") || strings.Contains(nameLower, "container") {
		return "docker"
	}

	return ""
}
