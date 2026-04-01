// Copyright (c) 2025 Reliant Labs
package worktree

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

var (
	tracer = otel.Tracer("worktree")
)

// Service provides worktree management functionality
type Service interface {
	Create(ctx context.Context, name string, opts CreateOptions) (*Worktree, error)
	Import(ctx context.Context, path string, opts ImportOptions) (*Worktree, error)
	List(ctx context.Context, opts ListOptions) ([]*Worktree, error)
	Get(ctx context.Context, name string) (*Worktree, error)
	GetCurrent(ctx context.Context) (*Worktree, error)
	Delete(ctx context.Context, name string) error
	Complete(ctx context.Context, name string, opts CompleteOptions) error
	Cleanup(ctx context.Context, opts CleanupOptions) ([]string, error)
}

type service struct {
	logger      *slog.Logger
	baseDir     string
	currentRepo string
	metadata    *Metadata
}

// NewService creates a new worktree service
func NewService(baseDir, currentRepo string) (Service, error) {
	if baseDir == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		baseDir = filepath.Join(homeDir, ".reliant", "worktrees")
	}

	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create worktree directory: %w", err)
	}

	s := &service{
		logger:      slog.Default(),
		baseDir:     baseDir,
		currentRepo: currentRepo,
	}

	if err := s.loadMetadata(); err != nil {
		s.logger.Warn("failed to load metadata, initializing new", "error", err)
		s.metadata = &Metadata{
			Version:   "1.0",
			Worktrees: make(map[string]*Worktree),
			UpdatedAt: time.Now(),
		}
	}

	return s, nil
}

// Create creates a new worktree
func (s *service) Create(ctx context.Context, name string, opts CreateOptions) (*Worktree, error) {
	ctx, span := tracer.Start(ctx, "worktree.Create",
		trace.WithAttributes(attribute.String("name", name)))
	defer span.End()

	s.logger.Info("creating worktree", "name", name, "branch", opts.Branch)

	// Validate that we're in a git repository
	gitDir := filepath.Join(s.currentRepo, ".git")
	if _, err := os.Stat(gitDir); err != nil {
		return nil, fmt.Errorf("not a git repository. Initialize git first with 'git init'")
	}

	// Validate that the repository has at least one commit
	checkCmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	checkCmd.Dir = s.currentRepo
	if err := checkCmd.Run(); err != nil {
		return nil, fmt.Errorf("repository has no commits. Create an initial commit before creating worktrees")
	}

	// Generate repository ID
	repoID, err := s.generateRepoID(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to generate repo ID: %w", err)
	}

	// Create worktree path
	worktreePath := filepath.Join(s.baseDir, repoID, name)
	worktreeID := fmt.Sprintf("%s/%s", repoID, name)

	// Handle force option - clean up existing resources
	if opts.Force {
		s.logger.Info("force mode enabled, cleaning up existing resources", "name", name)

		// 0. Prune git worktrees first to clear any missing but registered worktrees
		s.logger.Info("force mode: pruning git worktrees to clear stale entries")
		pruneCmd := exec.CommandContext(ctx, "git", "worktree", "prune", "-v")
		pruneCmd.Dir = s.currentRepo
		if output, err := pruneCmd.CombinedOutput(); err != nil {
			s.logger.Warn("force mode: git worktree prune failed", "error", err, "output", string(output))
		}

		// 1. Remove from metadata if exists
		if _, exists := s.metadata.Worktrees[worktreeID]; exists {
			s.logger.Info("force mode: removing existing worktree from metadata", "worktreeID", worktreeID)
			delete(s.metadata.Worktrees, worktreeID)
			if s.metadata.CurrentID == worktreeID {
				s.metadata.CurrentID = ""
			}
		}

		// 2. Remove git worktree if exists
		if _, err := os.Stat(worktreePath); err == nil {
			s.logger.Info("force mode: removing existing git worktree", "path", worktreePath)
			removeCmd := exec.CommandContext(ctx, "git", "worktree", "remove", worktreePath, "--force")
			removeCmd.Dir = s.currentRepo
			if output, err := removeCmd.CombinedOutput(); err != nil {
				s.logger.Warn("force mode: git worktree remove failed, trying direct deletion", "error", err, "output", string(output))
			}
			// Force delete the directory
			if err := os.RemoveAll(worktreePath); err != nil {
				s.logger.Warn("force mode: failed to remove worktree directory", "error", err)
			}
		}

		// 3. Delete branch if exists
		if opts.Branch != "" {
			branchCheckCmd := exec.CommandContext(ctx, "git", "rev-parse", "--verify", opts.Branch)
			branchCheckCmd.Dir = s.currentRepo
			if branchCheckCmd.Run() == nil {
				s.logger.Info("force mode: deleting existing branch", "branch", opts.Branch)
				delBranchCmd := exec.CommandContext(ctx, "git", "branch", "-D", opts.Branch)
				delBranchCmd.Dir = s.currentRepo
				if output, err := delBranchCmd.CombinedOutput(); err != nil {
					s.logger.Warn("force mode: failed to delete branch", "error", err, "output", string(output))
				}
			}
		}
	} else {
		// Not force mode - check for conflicts
		// Check if worktree already exists in metadata
		if _, exists := s.metadata.Worktrees[worktreeID]; exists {
			return nil, fmt.Errorf("worktree '%s' already exists in Reliant registry (use force=true to override)", name)
		}

		// Check if git worktree already exists
		cmd := exec.CommandContext(ctx, "git", "worktree", "list", "--porcelain")
		cmd.Dir = s.currentRepo
		output, err := cmd.Output()
		if err == nil {
			lines := strings.Split(string(output), "\n")
			for _, line := range lines {
				if strings.HasPrefix(line, "worktree ") {
					path := strings.TrimPrefix(line, "worktree ")
					if filepath.Base(path) == name {
						return nil, fmt.Errorf("git worktree '%s' already exists at %s (use force=true to override)", name, path)
					}
				}
			}
		}
	}

	// Determine branch names
	branch := opts.Branch
	if branch == "" {
		branch = fmt.Sprintf("worktree/%s-%d", name, time.Now().Unix())
	}

	baseBranch := opts.BaseBranch
	if baseBranch == "" {
		baseBranch = s.getCurrentBranch(ctx)
	}

	// Create worktree directory structure
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0755); err != nil {
		return nil, fmt.Errorf("failed to create worktree directory: %w", err)
	}

	// Check if branch already exists
	branchCheckCmd := exec.CommandContext(ctx, "git", "rev-parse", "--verify", branch)
	branchCheckCmd.Dir = s.currentRepo
	branchExists := branchCheckCmd.Run() == nil

	// Execute git worktree add
	var addCmd *exec.Cmd
	if branchExists {
		// Branch exists - use -B to force reset it to base branch
		s.logger.Info("branch already exists, using -B to reset", "branch", branch, "baseBranch", baseBranch)
		addCmd = exec.CommandContext(ctx, "git", "worktree", "add", "-B", branch, worktreePath, baseBranch)
	} else {
		// Branch doesn't exist - create it with -b
		addCmd = exec.CommandContext(ctx, "git", "worktree", "add", "-b", branch, worktreePath, baseBranch)
	}

	addCmd.Dir = s.currentRepo
	output, err := addCmd.CombinedOutput()
	if err != nil {
		s.logger.Error("git worktree add failed", "error", err, "output", string(output))

		// Parse specific git errors
		outputStr := string(output)
		switch {
		case strings.Contains(outputStr, "already exists"):
			return nil, fmt.Errorf("worktree path '%s' already exists", worktreePath)
		case strings.Contains(outputStr, "already checked out"):
			return nil, fmt.Errorf("branch '%s' is already checked out in another worktree", branch)
		case strings.Contains(outputStr, "not a valid branch"):
			return nil, fmt.Errorf("base branch '%s' is not a valid branch", baseBranch)
		default:
			return nil, fmt.Errorf("git worktree creation failed: %s", strings.TrimSpace(outputStr))
		}
	}

	// Create worktree metadata
	wt := &Worktree{
		ID:         worktreeID,
		Name:       name,
		Path:       worktreePath,
		Branch:     branch,
		BaseBranch: baseBranch,
		RepoID:     repoID,
		SessionID:  opts.SessionID,
		Status:     StatusActive,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
		LastActive: time.Now(),
	}

	// Copy specified files from source repo to worktree
	if len(opts.CopyFiles) > 0 {
		s.logger.Info("copying files to worktree", "patterns", opts.CopyFiles, "from", s.currentRepo, "to", worktreePath)
		s.copyFiles(ctx, s.currentRepo, worktreePath, opts.CopyFiles)
	}

	// Save metadata
	s.metadata.Worktrees[worktreeID] = wt
	s.metadata.CurrentID = worktreeID
	s.metadata.UpdatedAt = time.Now()
	if err := s.saveMetadata(); err != nil {
		s.logger.Error("failed to save metadata", "error", err)
	}

	s.logger.Info("worktree created successfully", "id", worktreeID, "path", worktreePath)

	return wt, nil
}

// Import imports an existing worktree
func (s *service) Import(ctx context.Context, path string, opts ImportOptions) (*Worktree, error) {
	ctx, span := tracer.Start(ctx, "worktree.Import",
		trace.WithAttributes(attribute.String("path", path)))
	defer span.End()

	s.logger.Info("importing worktree", "path", path)

	// Validate path exists
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("failed to get absolute path: %w", err)
	}
	if _, err := os.Stat(absPath); err != nil {
		return nil, fmt.Errorf("worktree path does not exist: %w", err)
	}

	// Validate it's a git worktree
	gitDirPath := filepath.Join(absPath, ".git")
	if _, err := os.Stat(gitDirPath); err != nil {
		return nil, fmt.Errorf("path is not a git worktree (missing .git)")
	}

	// Get current branch using git status
	branchCmd := exec.CommandContext(ctx, "git", "rev-parse", "--abbrev-ref", "HEAD")
	branchCmd.Dir = absPath
	branchOutput, err := branchCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to get current branch: %w", err)
	}
	branch := strings.TrimSpace(string(branchOutput))

	// Try to determine base branch from remote tracking
	baseBranch := ""
	trackingCmd := exec.CommandContext(ctx, "git", "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	trackingCmd.Dir = absPath
	if trackingOutput, err := trackingCmd.Output(); err == nil {
		// Output format: origin/main or origin/feature-branch
		tracking := strings.TrimSpace(string(trackingOutput))
		parts := strings.Split(tracking, "/")
		if len(parts) > 1 {
			baseBranch = strings.Join(parts[1:], "/")
		}
	}
	// Fallback to main if we can't determine base branch
	if baseBranch == "" {
		baseBranch = s.getCurrentBranch(ctx)
	}

	// Generate repository ID
	repoID, err := s.generateRepoID(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to generate repo ID: %w", err)
	}

	// Determine worktree name
	name := opts.Name
	if name == "" {
		name = filepath.Base(absPath)
	}

	worktreeID := fmt.Sprintf("%s/%s", repoID, name)

	// Check if already imported
	if _, exists := s.metadata.Worktrees[worktreeID]; exists {
		return nil, fmt.Errorf("worktree '%s' is already imported", name)
	}

	// Create worktree metadata
	wt := &Worktree{
		ID:         worktreeID,
		Name:       name,
		Path:       absPath,
		Branch:     branch,
		BaseBranch: baseBranch,
		RepoID:     repoID,
		SessionID:  opts.SessionID,
		Status:     StatusActive,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
		LastActive: time.Now(),
	}

	// Save metadata
	s.metadata.Worktrees[worktreeID] = wt
	s.metadata.CurrentID = worktreeID
	s.metadata.UpdatedAt = time.Now()
	if err := s.saveMetadata(); err != nil {
		return nil, fmt.Errorf("failed to save metadata: %w", err)
	}

	s.logger.Info("worktree imported successfully", "id", worktreeID, "path", absPath, "branch", branch)

	return wt, nil
}

// List lists worktrees matching the given options
func (s *service) List(ctx context.Context, opts ListOptions) ([]*Worktree, error) {
	var result []*Worktree

	for _, wt := range s.metadata.Worktrees {
		// Apply filters
		if opts.Status != "" && wt.Status != opts.Status {
			continue
		}
		if opts.RepoID != "" && wt.RepoID != opts.RepoID {
			continue
		}
		if opts.SessionID != "" && wt.SessionID != opts.SessionID {
			continue
		}

		result = append(result, wt)
	}

	return result, nil
}

// Get retrieves a specific worktree
func (s *service) Get(ctx context.Context, name string) (*Worktree, error) {
	repoID, err := s.generateRepoID(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to generate repo ID: %w", err)
	}

	worktreeID := fmt.Sprintf("%s/%s", repoID, name)
	wt, exists := s.metadata.Worktrees[worktreeID]
	if !exists {
		return nil, fmt.Errorf("worktree '%s' not found", name)
	}

	return wt, nil
}

// GetCurrent retrieves the current worktree
func (s *service) GetCurrent(ctx context.Context) (*Worktree, error) {
	if s.metadata.CurrentID == "" {
		return nil, fmt.Errorf("no current worktree")
	}

	wt, exists := s.metadata.Worktrees[s.metadata.CurrentID]
	if !exists {
		return nil, fmt.Errorf("current worktree not found")
	}

	return wt, nil
}

// Delete removes a worktree
func (s *service) Delete(ctx context.Context, name string) error {
	ctx, span := tracer.Start(ctx, "worktree.Delete",
		trace.WithAttributes(attribute.String("name", name)))
	defer span.End()

	repoID, err := s.generateRepoID(ctx)
	if err != nil {
		return fmt.Errorf("failed to generate repo ID: %w", err)
	}

	worktreeID := fmt.Sprintf("%s/%s", repoID, name)
	wt, exists := s.metadata.Worktrees[worktreeID]
	if !exists {
		return fmt.Errorf("worktree '%s' not found", name)
	}

	// Remove git worktree
	cmd := exec.CommandContext(ctx, "git", "worktree", "remove", wt.Path, "--force")
	cmd.Dir = s.currentRepo
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Try to clean up anyway
		s.logger.Warn("git worktree remove failed, attempting cleanup", "error", err, "output", string(output))
	}

	// Remove from metadata
	delete(s.metadata.Worktrees, worktreeID)
	if s.metadata.CurrentID == worktreeID {
		s.metadata.CurrentID = ""
	}
	s.metadata.UpdatedAt = time.Now()

	if err := s.saveMetadata(); err != nil {
		s.logger.Error("failed to save metadata", "error", err)
	}

	// Remove directory if it still exists
	if err := os.RemoveAll(wt.Path); err != nil {
		s.logger.Warn("failed to remove worktree directory", "error", err)
	}

	s.logger.Info("worktree deleted", "id", worktreeID)
	return nil
}

// Complete marks a worktree as completed
func (s *service) Complete(ctx context.Context, name string, opts CompleteOptions) error {
	ctx, span := tracer.Start(ctx, "worktree.Complete",
		trace.WithAttributes(attribute.String("name", name)))
	defer span.End()

	wt, err := s.Get(ctx, name)
	if err != nil {
		return err
	}

	// Check for uncommitted changes
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain")
	cmd.Dir = wt.Path
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to check git status: %w", err)
	}

	if len(output) > 0 {
		return fmt.Errorf("worktree has uncommitted changes")
	}

	// Push to remote if requested
	if opts.Push {
		cmd = exec.CommandContext(ctx, "git", "push", "-u", "origin", wt.Branch)
		cmd.Dir = wt.Path
		if output, err := cmd.CombinedOutput(); err != nil {
			s.logger.Error("failed to push branch", "error", err, "output", string(output))
			return fmt.Errorf("failed to push branch: %w", err)
		}
	}

	// Create PR if requested
	if opts.CreatePR {
		// This would integrate with gh CLI or GitHub API
		s.logger.Info("PR creation requested", "title", opts.PRTitle, "body", opts.PRBody)
	}

	// Update status
	wt.Status = StatusCompleted
	wt.UpdatedAt = time.Now()
	s.metadata.UpdatedAt = time.Now()

	if err := s.saveMetadata(); err != nil {
		s.logger.Error("failed to save metadata", "error", err)
	}

	// Delete local if requested
	if opts.DeleteLocal {
		return s.Delete(ctx, name)
	}

	return nil
}

// Cleanup removes worktrees based on options
func (s *service) Cleanup(ctx context.Context, opts CleanupOptions) ([]string, error) {
	ctx, span := tracer.Start(ctx, "worktree.Cleanup")
	defer span.End()

	var cleaned []string
	now := time.Now()

	for id, wt := range s.metadata.Worktrees {
		shouldClean := false

		// Check status-based cleanup
		if opts.All {
			shouldClean = true
		} else if opts.Completed && wt.Status == StatusCompleted {
			shouldClean = true
		} else if opts.Abandoned && wt.Status == StatusAbandoned {
			shouldClean = true
		}

		// Check age-based cleanup
		if opts.OlderThan > 0 && now.Sub(wt.LastActive) > opts.OlderThan {
			shouldClean = true
		}

		if shouldClean {
			s.logger.Info("cleaning up worktree", "id", id, "status", string(wt.Status))

			if err := s.Delete(ctx, wt.Name); err != nil {
				if !opts.Force {
					return cleaned, fmt.Errorf("failed to delete worktree %s: %w", wt.Name, err)
				}
				s.logger.Warn("failed to delete worktree, continuing", "id", id, "error", err)
			} else {
				cleaned = append(cleaned, wt.Name)
			}
		}
	}

	return cleaned, nil
}

// generateRepoID generates a unique ID for the current repository
func (s *service) generateRepoID(ctx context.Context) (string, error) {
	// Try to get remote URL
	cmd := exec.CommandContext(ctx, "git", "config", "--get", "remote.origin.url")
	cmd.Dir = s.currentRepo
	output, err := cmd.Output()

	var input string
	if err == nil && len(output) > 0 {
		input = strings.TrimSpace(string(output))
	} else {
		// Fall back to absolute path
		abs, err := filepath.Abs(s.currentRepo)
		if err != nil {
			return "", fmt.Errorf("failed to get absolute path: %w", err)
		}
		input = abs
	}

	// Generate hash
	hash := sha256.Sum256([]byte(input))
	return hex.EncodeToString(hash[:])[:12], nil
}

// getCurrentBranch gets the repository's default branch
func (s *service) getCurrentBranch(ctx context.Context) string {
	// Try to get the default branch from the remote
	cmd := exec.CommandContext(ctx, "git", "symbolic-ref", "refs/remotes/origin/HEAD")
	cmd.Dir = s.currentRepo
	output, err := cmd.Output()
	if err == nil {
		// Output format: refs/remotes/origin/main
		// Extract the branch name after the last /
		ref := strings.TrimSpace(string(output))
		parts := strings.Split(ref, "/")
		if len(parts) > 0 {
			return parts[len(parts)-1]
		}
	}

	// Fallback: try to get it from the local HEAD
	cmd = exec.CommandContext(ctx, "git", "rev-parse", "--abbrev-ref", "origin/HEAD")
	cmd.Dir = s.currentRepo
	output, err = cmd.Output()
	if err == nil {
		// Output format: origin/main
		ref := strings.TrimSpace(string(output))
		parts := strings.Split(ref, "/")
		if len(parts) > 1 {
			return parts[1]
		}
	}

	// Final fallback: use main
	s.logger.Warn("failed to get default branch, using main", "error", err)
	return "main"
}

// loadMetadata loads the metadata file
func (s *service) loadMetadata() error {
	ctx := context.Background() // OK to use Background() for initialization
	repoID, err := s.generateRepoID(ctx)
	if err != nil {
		return err
	}

	metadataPath := filepath.Join(s.baseDir, repoID, ".metadata.json")
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		return err
	}

	s.metadata = &Metadata{}
	return json.Unmarshal(data, s.metadata)
}

// saveMetadata saves the metadata file
func (s *service) saveMetadata() error {
	ctx := context.Background() // OK to use Background() for file operations
	repoID, err := s.generateRepoID(ctx)
	if err != nil {
		return err
	}

	metadataDir := filepath.Join(s.baseDir, repoID)
	if err := os.MkdirAll(metadataDir, 0755); err != nil {
		return err
	}

	metadataPath := filepath.Join(metadataDir, ".metadata.json")
	data, err := json.MarshalIndent(s.metadata, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(metadataPath, data, 0644)
}

// copyFiles copies specified files or directories from source to destination
// Patterns can be:
// - Simple names like ".env" - will search recursively for all matching files
// - Explicit paths like "frontend/.env" - will copy that specific file
// - Directory paths like "frontend/" or "frontend" - will recursively copy all files within the directory
func (s *service) copyFiles(ctx context.Context, srcDir, dstDir string, files []string) {
	// Expand file patterns to actual file paths
	expandedFiles := s.findMatchingFiles(srcDir, files)

	if len(expandedFiles) == 0 {
		s.logger.Info("no matching files found to copy", "patterns", files)
		return
	}

	s.logger.Info("copying files to worktree", "count", len(expandedFiles), "files", expandedFiles)
	s.copyFilePaths(srcDir, dstDir, expandedFiles)
}

// copyFilePaths copies a list of relative file paths from source to destination,
// preserving directory structure
// If a path points to a directory, it recursively copies all files within it
func (s *service) copyFilePaths(srcDir, dstDir string, relativePaths []string) {
	for _, relPath := range relativePaths {
		srcPath := filepath.Join(srcDir, relPath)
		dstPath := filepath.Join(dstDir, relPath)

		// Check if source exists
		info, err := os.Stat(srcPath)
		if err != nil {
			s.logger.Warn("source path does not exist", "path", relPath, "error", err)
			continue
		}

		// Handle directories by recursively copying all files within
		if info.IsDir() {
			err := filepath.WalkDir(srcPath, func(path string, d os.DirEntry, err error) error {
				if err != nil {
					return nil // Skip errors, continue walking
				}

				// Skip .git directory
				if d.IsDir() && d.Name() == ".git" {
					return filepath.SkipDir
				}

				// Only copy files, not directories
				if !d.IsDir() {
					// Get relative path from srcDir
					fileRelPath, err := filepath.Rel(srcDir, path)
					if err != nil {
						return nil
					}

					fileDstPath := filepath.Join(dstDir, fileRelPath)

					// Create destination directory if needed
					fileDstDirPath := filepath.Dir(fileDstPath)
					if err := os.MkdirAll(fileDstDirPath, 0755); err != nil {
						s.logger.Warn("failed to create destination directory", "dir", fileDstDirPath, "error", err)
						return nil
					}

					// Read source file
					data, err := os.ReadFile(path)
					if err != nil {
						s.logger.Warn("failed to read file for copying", "file", fileRelPath, "error", err)
						return nil
					}

					// Preserve original file permissions
					fileInfo, err := os.Stat(path)
					perm := os.FileMode(0644)
					if err == nil {
						perm = fileInfo.Mode().Perm()
					}

					// Write to destination
					if err := os.WriteFile(fileDstPath, data, perm); err != nil {
						s.logger.Warn("failed to copy file", "file", fileRelPath, "error", err)
						return nil
					}

					s.logger.Info("copied file to worktree", "file", fileRelPath, "destination", fileDstPath)
				}
				return nil
			})
			if err != nil {
				s.logger.Warn("error walking directory for copying", "dir", relPath, "error", err)
			}
			continue
		}

		// Handle regular files
		// Create destination directory if needed
		dstDirPath := filepath.Dir(dstPath)
		if err := os.MkdirAll(dstDirPath, 0755); err != nil {
			s.logger.Warn("failed to create destination directory", "dir", dstDirPath, "error", err)
			continue
		}

		// Read source file
		data, err := os.ReadFile(srcPath)
		if err != nil {
			s.logger.Warn("failed to read file for copying", "file", relPath, "error", err)
			continue
		}

		// Preserve original file permissions
		perm := os.FileMode(0644)
		if err == nil {
			perm = info.Mode().Perm()
		}

		// Write to destination
		if err := os.WriteFile(dstPath, data, perm); err != nil {
			s.logger.Warn("failed to copy file", "file", relPath, "error", err)
			continue
		}

		s.logger.Info("copied file to worktree", "file", relPath, "destination", dstPath)
	}
}

// findMatchingFiles searches for files matching the given patterns recursively
// Patterns can be:
// - Simple file names like ".env" - matches any file with that name in any directory
// - Paths with directories like "frontend/.env" - matches that specific file path
// - Directory paths like "frontend/" or "frontend" - matches all files within that directory
// Returns relative paths from srcDir
func (s *service) findMatchingFiles(srcDir string, patterns []string) []string {
	var matches []string
	matchSet := make(map[string]bool) // Deduplicate matches

	for _, pattern := range patterns {
		// First check if pattern exists as a file or directory
		fullPath := filepath.Join(srcDir, pattern)
		info, err := os.Stat(fullPath)
		if err == nil {
			if info.IsDir() {
				// If it's a directory, recursively find all files within it
				err := filepath.WalkDir(fullPath, func(path string, d os.DirEntry, err error) error {
					if err != nil {
						return nil // Skip errors, continue walking
					}

					// Skip .git directory
					if d.IsDir() && d.Name() == ".git" {
						return filepath.SkipDir
					}

					// Only add files, not directories
					if !d.IsDir() {
						relPath, err := filepath.Rel(srcDir, path)
						if err == nil && !matchSet[relPath] {
							matches = append(matches, relPath)
							matchSet[relPath] = true
						}
					}
					return nil
				})
				if err != nil {
					s.logger.Warn("error walking directory", "dir", fullPath, "error", err)
				}
				continue
			} else if strings.Contains(pattern, string(filepath.Separator)) || strings.Contains(pattern, "/") {
				// It's a file with a directory separator - add it directly
				if !matchSet[pattern] {
					matches = append(matches, pattern)
					matchSet[pattern] = true
				}
				continue
			}
			// If it's a file without a separator, fall through to filename matching below
		} else if strings.Contains(pattern, string(filepath.Separator)) || strings.Contains(pattern, "/") {
			// Path with separator doesn't exist - skip it
			continue
		}

		// Simple file name - search recursively
		err = filepath.WalkDir(srcDir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil // Skip errors, continue walking
			}

			// Skip .git directory
			if d.IsDir() && d.Name() == ".git" {
				return filepath.SkipDir
			}

			// Skip directories
			if d.IsDir() {
				return nil
			}

			// Check if file name matches the pattern
			if d.Name() == pattern {
				relPath, err := filepath.Rel(srcDir, path)
				if err != nil {
					return nil
				}
				if !matchSet[relPath] {
					matches = append(matches, relPath)
					matchSet[relPath] = true
				}
			}

			return nil
		})

		if err != nil {
			s.logger.Warn("error walking directory", "dir", srcDir, "pattern", pattern, "error", err)
		}
	}

	return matches
}
