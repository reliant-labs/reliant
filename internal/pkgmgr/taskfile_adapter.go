// Copyright (c) 2025 Reliant Labs
package pkgmgr

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// TaskfileAdapter parses Taskfile.yml (go-task format)
type TaskfileAdapter struct{}

// NewTaskfileAdapter creates a new Taskfile adapter
func NewTaskfileAdapter() *TaskfileAdapter {
	return &TaskfileAdapter{}
}

// Type returns the package type for Taskfile
func (a *TaskfileAdapter) Type() PackageType {
	return PackageTypeTaskfile
}

// Detect checks if a Taskfile.yml or Taskfile.yaml exists in the given directory
func (a *TaskfileAdapter) Detect(ctx context.Context, dir string) (bool, error) {
	// Check both .yml and .yaml extensions
	for _, ext := range []string{".yml", ".yaml"} {
		path := filepath.Join(dir, "Taskfile"+ext)
		if _, err := os.Stat(path); err == nil {
			return true, nil
		}
	}
	return false, nil
}

// FilePath returns the path to the Taskfile
func (a *TaskfileAdapter) FilePath(dir string) string {
	// Prefer .yml over .yaml
	ymlPath := filepath.Join(dir, "Taskfile.yml")
	if _, err := os.Stat(ymlPath); err == nil {
		return ymlPath
	}
	yamlPath := filepath.Join(dir, "Taskfile.yaml")
	if _, err := os.Stat(yamlPath); err == nil {
		return yamlPath
	}
	return ymlPath // Default to .yml
}

// taskfile represents the structure of a Taskfile
type taskfile struct {
	Version string          `yaml:"version"`
	Tasks   map[string]task `yaml:"tasks"`
}

// task represents a single task in a Taskfile
type task struct {
	Desc     string   `yaml:"desc"`
	Summary  string   `yaml:"summary"`
	Cmds     []any    `yaml:"cmds"` // Can be string or object with cmd/task keys
	Deps     []any    `yaml:"deps"` // Can be string or object with task key
	Dir      string   `yaml:"dir"`
	Internal bool     `yaml:"internal"`
	Aliases  []string `yaml:"aliases"`
}

// Parse extracts tasks from the Taskfile
func (a *TaskfileAdapter) Parse(ctx context.Context, dir string) ([]Command, error) {
	path := a.FilePath(dir)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var tf taskfile
	if err := yaml.Unmarshal(data, &tf); err != nil {
		return nil, err
	}

	if tf.Tasks == nil {
		return []Command{}, nil
	}

	var commands []Command
	for name, t := range tf.Tasks {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		// Skip internal tasks
		if t.Internal {
			continue
		}

		// Skip tasks that start with underscore (convention for internal tasks)
		if strings.HasPrefix(name, "_") {
			continue
		}

		// Use desc, fall back to summary
		description := t.Desc
		if description == "" {
			description = t.Summary
		}

		// Parse dependencies
		deps := parseDeps(t.Deps)

		commands = append(commands, Command{
			Name:         name,
			Description:  description,
			Command:      "task " + name,
			PackageType:  PackageTypeTaskfile,
			Source:       path,
			Category:     categorizeTaskfileName(name),
			Dependencies: deps,
		})
	}

	return commands, nil
}

// parseDeps extracts dependency names from the deps field
func parseDeps(deps []any) []string {
	var result []string
	for _, dep := range deps {
		switch d := dep.(type) {
		case string:
			result = append(result, d)
		case map[string]any:
			// Handle object format like {task: "name", vars: {...}}
			if taskName, ok := d["task"].(string); ok {
				result = append(result, taskName)
			}
		}
	}
	return result
}

// categorizeTaskfileName categorizes a task based on its name
func categorizeTaskfileName(name string) string {
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
	if strings.Contains(nameLower, "clean") {
		return "clean"
	}

	// Install/setup
	if strings.Contains(nameLower, "install") || strings.Contains(nameLower, "setup") ||
		strings.Contains(nameLower, "deps") {
		return "setup"
	}

	// Lint/format
	if strings.Contains(nameLower, "lint") || strings.Contains(nameLower, "fmt") ||
		strings.Contains(nameLower, "format") {
		return "lint"
	}

	// Docker
	if strings.Contains(nameLower, "docker") || strings.Contains(nameLower, "container") {
		return "docker"
	}

	// Generate
	if strings.Contains(nameLower, "generate") || strings.Contains(nameLower, "gen") {
		return "generate"
	}

	return ""
}
