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

// NormalizeBranchName trims surrounding whitespace from a user-supplied
// branch name and falls back to "main" when the result is empty. Callers
// should validate the result with ValidateBranchName before handing it to
// git or persisting it.
func NormalizeBranchName(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "main"
	}
	return trimmed
}

// ValidateBranchName rejects branch names git itself would refuse
// (a conservative pure-Go mirror of `git check-ref-format --branch`).
// Validating up front lets callers fail with a clear error BEFORE any
// on-disk state (a half-initialized .git) is created.
func ValidateBranchName(name string) error {
	invalid := func(reason string) error {
		return fmt.Errorf("invalid branch name %q: %s", name, reason)
	}
	if name == "" {
		return invalid("must not be empty")
	}
	if name == "@" {
		return invalid("must not be \"@\"")
	}
	if strings.HasPrefix(name, "-") {
		return invalid("must not start with \"-\"")
	}
	if strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") || strings.Contains(name, "//") {
		return invalid("must not start or end with \"/\" or contain \"//\"")
	}
	if strings.HasSuffix(name, ".") || strings.Contains(name, "..") {
		return invalid("must not end with \".\" or contain \"..\"")
	}
	if strings.Contains(name, "@{") {
		return invalid("must not contain \"@{\"")
	}
	for _, component := range strings.Split(name, "/") {
		if strings.HasPrefix(component, ".") {
			return invalid("path components must not start with \".\"")
		}
		if strings.HasSuffix(component, ".lock") {
			return invalid("path components must not end with \".lock\"")
		}
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7F {
			return invalid("must not contain control characters")
		}
		switch r {
		case ' ', '~', '^', ':', '?', '*', '[', '\\':
			return invalid(fmt.Sprintf("must not contain %q", r))
		}
	}
	return nil
}

// repoHasCommits reports whether the repository at path has at least one
// commit (i.e. HEAD resolves). An unborn HEAD (fresh `git init`) returns false.
func repoHasCommits(ctx context.Context, path string) bool {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--verify", "--quiet", "HEAD")
	cmd.Dir = path
	return cmd.Run() == nil
}

// HasCommits reports whether the repository at path has at least one commit.
// An unborn HEAD (a repo created by `git init` with nothing committed yet)
// returns false. Exported wrapper around repoHasCommits for callers outside
// this package (the daemon worktree handler gates on it).
func HasCommits(ctx context.Context, path string) bool {
	return repoHasCommits(ctx, path)
}

// EnsureInitialCommit gives a repository a root commit if it has none, so an
// unborn HEAD becomes a real branch that `git worktree add <path> <branch>`
// can resolve. This is the fix for both new-project auto-init (which creates
// .git without a commit, since a fresh cloud pod has no git identity for a
// real commit) and a user's manual `git init` (also commitless): in both
// cases .git exists and is_git_repo is true, but worktree creation fails with
// "invalid reference" until a commit exists for the branch to point at.
//
// The commit is EMPTY (--allow-empty) on purpose: we materialize the branch
// ref without sweeping the user's working-tree files into a commit they did
// not ask for. It lands on whatever branch HEAD currently names (the unborn
// branch from `git init -b <branch>`), so the resulting branch matches the
// project's default. A repo that already has commits is left untouched.
//
// Identity: `git commit` needs a user.name/user.email. A fresh cloud
// workspace pod has neither, so we pass a per-invocation fallback identity
// via `-c` (mirroring the reliant@localhost guard in the daemon's worktree
// commit path) WITHOUT mutating the repo's config — a real identity the user
// later configures still wins for their own commits.
func EnsureInitialCommit(ctx context.Context, path string) error {
	if path == "" {
		return fmt.Errorf("path is required")
	}
	if !IsGitRepository(path) {
		return fmt.Errorf("not a git repository: %s", path)
	}
	if repoHasCommits(ctx, path) {
		return nil
	}

	cmd := exec.CommandContext(ctx, "git",
		"-c", "user.email=reliant@localhost",
		"-c", "user.name=Reliant",
		"commit", "--allow-empty", "-m", "Initial commit")
	cmd.Dir = path
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to create initial commit: %w, output: %s", err, string(output))
	}
	return nil
}

// InitGitRepository initializes a git repository, or adopts one that already
// exists at the path.
//
// Adoption makes init idempotent and retry-safe: a previous attempt may have
// created .git and then failed (e.g. an invalid initial branch name), leaving
// a directory that is a repository on disk but was never recorded upstream.
// Retrying must self-heal rather than fail with "already a git repository".
// When adopting:
//   - an unborn repo (zero commits) gets HEAD pointed at the requested branch;
//   - a repo that already has commits is left as-is (no branch move, no
//     fabricated initial commit) — it is already initialized;
//   - an existing .gitignore is never clobbered.
func InitGitRepository(ctx context.Context, opts InitGitRepositoryOptions) error {
	if opts.Path == "" {
		return fmt.Errorf("path is required")
	}

	// Normalize + validate the branch name BEFORE creating any state, so a
	// bad name (e.g. "main " with a trailing space) fails cleanly instead of
	// leaving a half-initialized .git behind.
	initialBranch := NormalizeBranchName(opts.InitialBranch)
	if err := ValidateBranchName(initialBranch); err != nil {
		return err
	}

	adopted := IsGitRepository(opts.Path)
	hasCommits := false
	if adopted {
		hasCommits = repoHasCommits(ctx, opts.Path)
		if !hasCommits {
			// Unborn repo: point HEAD at the requested branch.
			cmd := exec.CommandContext(ctx, "git", "symbolic-ref", "HEAD", "refs/heads/"+initialBranch)
			cmd.Dir = opts.Path
			if output, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("failed to set initial branch on existing repository: %w, output: %s", err, string(output))
			}
		}
	} else {
		cmd := exec.CommandContext(ctx, "git", "init", "-b", initialBranch)
		cmd.Dir = opts.Path
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to initialize git repository: %w, output: %s", err, string(output))
		}
	}

	// Create .gitignore if patterns provided. When adopting an existing
	// repository, never overwrite a .gitignore the user already has.
	if len(opts.GitignorePatterns) > 0 {
		gitignorePath := filepath.Join(opts.Path, ".gitignore")
		gitignoreExists := false
		if adopted {
			if _, err := os.Stat(gitignorePath); err == nil {
				gitignoreExists = true
			}
		}
		if !gitignoreExists {
			content := strings.Join(opts.GitignorePatterns, "\n") + "\n"
			if err := os.WriteFile(gitignorePath, []byte(content), 0644); err != nil {
				return fmt.Errorf("failed to create .gitignore: %w", err)
			}
		}
	}

	// Create initial commit if requested. Skipped when adopting a repository
	// that already has commits — there is nothing to "initialize" and
	// sweeping unrelated files into a fabricated commit would be surprising.
	if opts.InitialCommit && !hasCommits {
		// Stage all files
		cmd := exec.CommandContext(ctx, "git", "add", ".")
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