// Copyright (c) 2025 Reliant Labs
package gitutil

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// IsGitRepository checks if a directory is a git repository
func IsGitRepository(path string) bool {
	gitDir := filepath.Join(path, ".git")
	info, err := os.Stat(gitDir)
	if err != nil {
		return false
	}
	return info.IsDir() || info.Mode().IsRegular()
}

// GetCurrentBranch returns the current git branch name
func GetCurrentBranch(ctx context.Context, path string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = path
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get current branch: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

// GetDefaultBranch attempts to determine the default branch (main or master)
func GetDefaultBranch(ctx context.Context, path string) string {
	// Try to get the default branch from remote
	cmd := exec.CommandContext(ctx, "git", "symbolic-ref", "refs/remotes/origin/HEAD")
	cmd.Dir = path
	output, err := cmd.Output()
	if err == nil {
		branch := strings.TrimSpace(string(output))
		branch = strings.TrimPrefix(branch, "refs/remotes/origin/")
		if branch != "" {
			return branch
		}
	}

	// Check if main exists
	cmd = exec.CommandContext(ctx, "git", "rev-parse", "--verify", "main")
	cmd.Dir = path
	if cmd.Run() == nil {
		return "main"
	}

	// Check if master exists
	cmd = exec.CommandContext(ctx, "git", "rev-parse", "--verify", "master")
	cmd.Dir = path
	if cmd.Run() == nil {
		return "master"
	}

	// Default to main
	return "main"
}

// InitGitRepositoryOptions holds options for initializing a git repository
type InitGitRepositoryOptions struct {
	Path              string
	InitialBranch     string
	GitignorePatterns []string
	InitialCommit     bool
}

// InitGitRepository initializes a new git repository
func InitGitRepository(ctx context.Context, opts InitGitRepositoryOptions) error {
	if opts.Path == "" {
		return fmt.Errorf("path is required")
	}

	// Check if already a git repo
	if IsGitRepository(opts.Path) {
		return fmt.Errorf("directory is already a git repository")
	}

	// Set default branch name
	initialBranch := opts.InitialBranch
	if initialBranch == "" {
		initialBranch = "main"
	}

	// Initialize git repository
	cmd := exec.CommandContext(ctx, "git", "init", "-b", initialBranch)
	cmd.Dir = opts.Path
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to initialize git repository: %w, output: %s", err, string(output))
	}

	// Create .gitignore if patterns provided
	if len(opts.GitignorePatterns) > 0 {
		gitignorePath := filepath.Join(opts.Path, ".gitignore")
		content := strings.Join(opts.GitignorePatterns, "\n") + "\n"
		if err := os.WriteFile(gitignorePath, []byte(content), 0644); err != nil {
			return fmt.Errorf("failed to create .gitignore: %w", err)
		}
	}

	// Create initial commit if requested
	if opts.InitialCommit {
		// Stage all files
		cmd = exec.CommandContext(ctx, "git", "add", ".")
		cmd.Dir = opts.Path
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to stage files: %w, output: %s", err, string(output))
		}

		// Create commit
		cmd = exec.CommandContext(ctx, "git", "commit", "-m", "Initial commit")
		cmd.Dir = opts.Path
		if output, err := cmd.CombinedOutput(); err != nil {
			// It's OK if there's nothing to commit
			if !strings.Contains(string(output), "nothing to commit") {
				return fmt.Errorf("failed to create initial commit: %w, output: %s", err, string(output))
			}
		}
	}

	return nil
}

// DefaultGitignorePatterns returns common .gitignore patterns for Reliant projects
func EnsureGitignoreContains(projectPath, pattern string) error {
	if projectPath == "" {
		return fmt.Errorf("project path is required")
	}
	if strings.TrimSpace(pattern) == "" {
		return fmt.Errorf("gitignore pattern is required")
	}

	gitignorePath := filepath.Join(projectPath, ".gitignore")
	normalizedPattern := strings.TrimSpace(pattern)

	data, err := os.ReadFile(gitignorePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return os.WriteFile(gitignorePath, []byte(normalizedPattern+"\n"), 0644)
		}
		return fmt.Errorf("failed to read .gitignore: %w", err)
	}

	if gitignoreHasPattern(string(data), normalizedPattern) {
		return nil
	}

	content := string(data)
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += normalizedPattern + "\n"

	if err := os.WriteFile(gitignorePath, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to update .gitignore: %w", err)
	}
	return nil
}

func gitignoreHasPattern(content, pattern string) bool {
	normalize := func(value string) string {
		trimmed := strings.TrimSpace(value)
		return strings.TrimSuffix(trimmed, "/")
	}

	target := normalize(pattern)
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if normalize(trimmed) == target {
			return true
		}
	}
	return false
}

func DefaultGitignorePatterns() []string {
	return []string{
		"# Reliant",
		".reliant/",
		"",
		"# OS",
		".DS_Store",
		"Thumbs.db",
		"",
		"# Editor",
		".vscode/",
		".idea/",
		"*.swp",
		"*.swo",
		"*~",
		"",
		"# Dependencies",
		"node_modules/",
		"vendor/",
		"",
		"# Build outputs",
		"dist/",
		"build/",
		"*.o",
		"*.so",
		"*.exe",
		"",
		"# Environment",
		".env",
		".env.local",
		".env.*.local",
	}
}
