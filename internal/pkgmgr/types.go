// Copyright (c) 2025 Reliant Labs
package pkgmgr

import (
	"context"
	"time"
)

// PackageType identifies the type of package manager
type PackageType string

const (
	PackageTypeMakefile PackageType = "makefile"
	PackageTypeNPM      PackageType = "npm"
	PackageTypeTaskfile PackageType = "taskfile"
)

// Command represents a runnable command from a package manager file
type Command struct {
	// Name is the command/target name (e.g., "build", "test", "dev")
	Name string `json:"name"`

	// Description is an optional description of what the command does
	Description string `json:"description,omitempty"`

	// Command is the actual command string to execute
	Command string `json:"command"`

	// PackageType identifies which package manager this command comes from
	PackageType PackageType `json:"package_type"`

	// Source is the file path where this command was defined
	Source string `json:"source"`

	// Category is an optional grouping (e.g., "build", "test", "dev")
	Category string `json:"category,omitempty"`

	// Dependencies lists other commands that should run first
	Dependencies []string `json:"dependencies,omitempty"`

	// WorkingDir is the full path to the directory where this command should run
	// This is the directory containing the package file (e.g., package.json, Makefile)
	WorkingDir string `json:"working_dir"`

	// RelativePath is the path relative to the project root for display purposes
	// Empty string means the command is at the project root
	RelativePath string `json:"relative_path,omitempty"`
}

// Adapter defines the interface for package manager adapters
// Each adapter is responsible for detecting and parsing a specific package format
type Adapter interface {
	// Type returns the package type this adapter handles
	Type() PackageType

	// Detect checks if the package file exists in the given directory
	Detect(ctx context.Context, dir string) (bool, error)

	// Parse extracts commands from the package file in the given directory
	Parse(ctx context.Context, dir string) ([]Command, error)

	// FilePath returns the expected file path for this package type in the given directory
	FilePath(dir string) string
}

// ProcessInfo represents information about a running or completed process
type ProcessInfo struct {
	ID         string     `json:"id"`
	Command    string     `json:"command"`
	Status     string     `json:"status"` // running, completed, failed, killed
	WorktreeID string     `json:"worktree_id"`
	WorkingDir string     `json:"working_dir"`
	PID        int        `json:"pid,omitempty"`
	StartTime  time.Time  `json:"start_time"`
	EndTime    *time.Time `json:"end_time,omitempty"`
	ExitCode   *int       `json:"exit_code,omitempty"`
}

// CommandListResponse contains available commands for a directory
type CommandListResponse struct {
	// Commands grouped by package type
	Commands map[PackageType][]Command `json:"commands"`

	// DetectedTypes lists which package types were detected
	DetectedTypes []PackageType `json:"detected_types"`
}
