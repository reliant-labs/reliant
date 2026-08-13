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

	"github.com/reliant-labs/reliant/internal/gitref"
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

// NormalizeBranchName is a re-export of gitref.NormalizeBranchName. The pure
// implementation lives in internal/gitref so the server tier can validate
// branch names without importing this filesystem/exec-touching package (the
// architecture contract bans gitutil there). Daemon-side callers keep using it
// here for convenience.
func NormalizeBranchName(name string) string { return gitref.NormalizeBranchName(name) }

// ValidateBranchName is a re-export of gitref.ValidateBranchName. See
// NormalizeBranchName for why the implementation lives in internal/gitref.
func ValidateBranchName(name string) error { return gitref.ValidateBranchName(name) }

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

// gitConfigValue returns the effective value of a git config key at path
// (consulting repo, global, and system config), or "" if unset.
func gitConfigValue(ctx context.Context, path, key string) string {
	cmd := exec.CommandContext(ctx, "git", "config", "--get", key)
	cmd.Dir = path
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// CommitWithFallbackIdentity creates a commit at path, supplying an ephemeral
// Reliant identity ONLY for the user.name/user.email that git can't already
// resolve. A fresh cloud workspace pod (or any machine where the user never
// ran `git config --global user.email`) has no identity, so a plain
// `git commit` fails with exit 128 "Author identity unknown". Injecting the
// fallback via `-c` is per-invocation and does NOT mutate repo config, so a
// real identity the user later configures still wins for their own commits.
//
// This is the single commit path for Reliant-initiated commits (project init,
// worktree seeding, worktree auto-commit) so the identity fallback can't drift
// between call sites. allowEmpty maps to `git commit --allow-empty`; the
// caller is responsible for staging (`git add`) beforehand when needed.
func CommitWithFallbackIdentity(ctx context.Context, path, message string, allowEmpty bool) ([]byte, error) {
	var args []string
	if gitConfigValue(ctx, path, "user.name") == "" {
		args = append(args, "-c", "user.name=Reliant")
	}
	if gitConfigValue(ctx, path, "user.email") == "" {
		args = append(args, "-c", "user.email=reliant@localhost")
	}
	args = append(args, "commit")
	if allowEmpty {
		args = append(args, "--allow-empty")
	}
	args = append(args, "-m", message)

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = path
	return cmd.CombinedOutput()
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

	// Empty commit (--allow-empty) on purpose: materialize the branch ref
	// without sweeping the user's working-tree files into a commit they did
	// not ask for.
	if output, err := CommitWithFallbackIdentity(ctx, path, "Initial commit", true); err != nil {
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

		// Create commit. Uses the shared fallback identity so a fresh cloud pod
		// (or any machine without a configured git identity) doesn't fail with
		// "Author identity unknown".
		if output, err := CommitWithFallbackIdentity(ctx, opts.Path, "Initial commit", false); err != nil {
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
