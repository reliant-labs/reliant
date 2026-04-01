// Copyright (c) 2025 Reliant Labs
package pkgmgr

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// NPMAdapter parses npm scripts from package.json
type NPMAdapter struct{}

// NewNPMAdapter creates a new npm adapter
func NewNPMAdapter() *NPMAdapter {
	return &NPMAdapter{}
}

// Type returns the package type for npm
func (a *NPMAdapter) Type() PackageType {
	return PackageTypeNPM
}

// Detect checks if a package.json exists in the given directory
func (a *NPMAdapter) Detect(ctx context.Context, dir string) (bool, error) {
	path := a.FilePath(dir)
	_, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}

// FilePath returns the path to package.json
func (a *NPMAdapter) FilePath(dir string) string {
	return filepath.Join(dir, "package.json")
}

// packageJSON represents the structure of package.json we care about
type packageJSON struct {
	Name    string            `json:"name"`
	Scripts map[string]string `json:"scripts"`
}

// Parse extracts npm scripts from package.json and includes built-in npm commands
func (a *NPMAdapter) Parse(ctx context.Context, dir string) ([]Command, error) {
	path := a.FilePath(dir)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var pkg packageJSON
	if err := json.Unmarshal(data, &pkg); err != nil {
		return nil, err
	}

	var commands []Command

	// Parse scripts from package.json
	for name, script := range pkg.Scripts {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		// Generate description from the script command if it's simple
		description := generateNPMDescription(name, script)

		commands = append(commands, Command{
			Name:        name,
			Description: description,
			Command:     "npm run " + name,
			PackageType: PackageTypeNPM,
			Source:      path,
			Category:    categorizeNPMScript(name),
		})
	}

	// Append built-in npm commands
	commands = append(commands, getBuiltinNPMCommands(path)...)

	return commands, nil
}

// getBuiltinNPMCommands returns common npm commands that aren't typically in package.json
func getBuiltinNPMCommands(packageJSONPath string) []Command {
	builtinSource := "npm (built-in)"

	return []Command{
		// Setup/Install commands
		{
			Name:        "install",
			Description: "Install all dependencies from package.json",
			Command:     "npm install",
			PackageType: PackageTypeNPM,
			Source:      builtinSource,
			Category:    "setup",
		},
		{
			Name:        "ci",
			Description: "Clean install using package-lock.json (faster, for CI/CD)",
			Command:     "npm ci",
			PackageType: PackageTypeNPM,
			Source:      builtinSource,
			Category:    "setup",
		},
		// Maintenance commands
		{
			Name:        "update",
			Description: "Update packages to latest versions within semver constraints",
			Command:     "npm update",
			PackageType: PackageTypeNPM,
			Source:      builtinSource,
			Category:    "maintenance",
		},
		{
			Name:        "outdated",
			Description: "Check for outdated packages",
			Command:     "npm outdated",
			PackageType: PackageTypeNPM,
			Source:      builtinSource,
			Category:    "maintenance",
		},
		{
			Name:        "prune",
			Description: "Remove extraneous packages from node_modules",
			Command:     "npm prune",
			PackageType: PackageTypeNPM,
			Source:      builtinSource,
			Category:    "maintenance",
		},
		{
			Name:        "dedupe",
			Description: "Reduce duplication in node_modules",
			Command:     "npm dedupe",
			PackageType: PackageTypeNPM,
			Source:      builtinSource,
			Category:    "maintenance",
		},
		// Security commands
		{
			Name:        "audit",
			Description: "Run security audit on dependencies",
			Command:     "npm audit",
			PackageType: PackageTypeNPM,
			Source:      builtinSource,
			Category:    "security",
		},
		{
			Name:        "audit fix",
			Description: "Automatically fix security vulnerabilities",
			Command:     "npm audit fix",
			PackageType: PackageTypeNPM,
			Source:      builtinSource,
			Category:    "security",
		},
		// Cache commands
		{
			Name:        "cache clean",
			Description: "Clear npm cache",
			Command:     "npm cache clean --force",
			PackageType: PackageTypeNPM,
			Source:      builtinSource,
			Category:    "cache",
		},
	}
}

// generateNPMDescription creates a description for an npm script
func generateNPMDescription(name, script string) string {
	// For well-known scripts, provide standard descriptions
	switch name {
	case "start":
		return "Start the application"
	case "dev":
		return "Start development server"
	case "build":
		return "Build the application"
	case "test":
		return "Run tests"
	case "lint":
		return "Run linter"
	case "format":
		return "Format code"
	case "preview":
		return "Preview production build"
	case "typecheck":
		return "Run type checking"
	}

	// For short scripts, show the command itself
	if len(script) < 50 {
		return script
	}

	// For complex scripts, truncate
	return script[:47] + "..."
}

// categorizeNPMScript categorizes an npm script based on its name
func categorizeNPMScript(name string) string {
	nameLower := strings.ToLower(name)

	// Build-related
	if strings.Contains(nameLower, "build") || strings.Contains(nameLower, "compile") {
		return "build"
	}

	// Test-related
	if strings.Contains(nameLower, "test") || strings.Contains(nameLower, "spec") ||
		strings.Contains(nameLower, "e2e") {
		return "test"
	}

	// Dev-related
	if strings.Contains(nameLower, "dev") || nameLower == "start" ||
		strings.Contains(nameLower, "serve") || strings.Contains(nameLower, "watch") {
		return "dev"
	}

	// Lint/format
	if strings.Contains(nameLower, "lint") || strings.Contains(nameLower, "format") ||
		strings.Contains(nameLower, "prettier") || strings.Contains(nameLower, "eslint") {
		return "lint"
	}

	// Type checking
	if strings.Contains(nameLower, "type") || strings.Contains(nameLower, "tsc") {
		return "typecheck"
	}

	// Clean
	if strings.Contains(nameLower, "clean") {
		return "clean"
	}

	// Preview/deploy
	if strings.Contains(nameLower, "preview") || strings.Contains(nameLower, "deploy") {
		return "deploy"
	}

	return ""
}
