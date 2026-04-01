// Copyright (c) 2025 Reliant Labs
//go:build integration

package worktree

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupRealGitRepo(t *testing.T) (string, func()) {
	tempDir := t.TempDir()
	repoDir := filepath.Join(tempDir, "test-repo")

	// Create and initialize git repository
	require.NoError(t, os.MkdirAll(repoDir, 0755))

	// Initialize git repo
	cmd := exec.Command("git", "init")
	cmd.Dir = repoDir
	require.NoError(t, cmd.Run())

	// Configure git
	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = repoDir
	require.NoError(t, cmd.Run())

	cmd = exec.Command("git", "config", "user.email", "test@example.com")
	cmd.Dir = repoDir
	require.NoError(t, cmd.Run())

	// Create initial commit
	readmeFile := filepath.Join(repoDir, "README.md")
	require.NoError(t, os.WriteFile(readmeFile, []byte("# Test Repository"), 0644))

	cmd = exec.Command("git", "add", "README.md")
	cmd.Dir = repoDir
	require.NoError(t, cmd.Run())

	cmd = exec.Command("git", "commit", "-m", "Initial commit")
	cmd.Dir = repoDir
	require.NoError(t, cmd.Run())

	// Create a feature branch
	cmd = exec.Command("git", "checkout", "-b", "feature/base")
	cmd.Dir = repoDir
	require.NoError(t, cmd.Run())

	featureFile := filepath.Join(repoDir, "feature.txt")
	require.NoError(t, os.WriteFile(featureFile, []byte("Feature content"), 0644))

	cmd = exec.Command("git", "add", "feature.txt")
	cmd.Dir = repoDir
	require.NoError(t, cmd.Run())

	cmd = exec.Command("git", "commit", "-m", "Add feature")
	cmd.Dir = repoDir
	require.NoError(t, cmd.Run())

	// Return to main branch
	cmd = exec.Command("git", "checkout", "main")
	cmd.Dir = repoDir
	require.NoError(t, cmd.Run())

	cleanup := func() {
		// Clean up any worktrees first
		cmd := exec.Command("git", "worktree", "prune")
		cmd.Dir = repoDir
		_ = cmd.Run() // Ignore errors

		os.RemoveAll(tempDir)
	}

	return repoDir, cleanup
}

func TestIntegrationCreateWorktreeWithRealGit(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	repoDir, cleanup := setupRealGitRepo(t)
	defer cleanup()

	// Create worktree service
	tempDir := t.TempDir()
	baseDir := filepath.Join(tempDir, "worktrees")
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))

	service, err := NewService(logger, baseDir, repoDir)
	require.NoError(t, err)

	ctx := context.Background()

	// Test creating a worktree
	opts := CreateOptions{
		Branch:     "feature/integration-test",
		BaseBranch: "main",
	}

	wt, err := service.Create(ctx, "integration-test", opts)
	require.NoError(t, err)
	assert.Equal(t, "integration-test", wt.Name)
	assert.Equal(t, "feature/integration-test", wt.Branch)
	assert.Equal(t, "main", wt.BaseBranch)

	// Verify worktree directory exists
	assert.DirExists(t, wt.Path)

	// Verify git worktree was actually created
	cmd := exec.Command("git", "worktree", "list")
	cmd.Dir = repoDir
	output, err := cmd.Output()
	require.NoError(t, err)

	outputStr := string(output)
	assert.Contains(t, outputStr, wt.Path)
	assert.Contains(t, outputStr, wt.Branch)

	// Verify we can work in the worktree
	testFile := filepath.Join(wt.Path, "test.txt")
	require.NoError(t, os.WriteFile(testFile, []byte("test content"), 0644))

	// Check git status in worktree
	cmd = exec.Command("git", "status", "--porcelain")
	cmd.Dir = wt.Path
	output, err = cmd.Output()
	require.NoError(t, err)
	assert.Contains(t, string(output), "test.txt")
}

func TestIntegrationResumeWorktree(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	repoDir, cleanup := setupRealGitRepo(t)
	defer cleanup()

	tempDir := t.TempDir()
	baseDir := filepath.Join(tempDir, "worktrees")
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	service, err := NewService(logger, baseDir, repoDir)
	require.NoError(t, err)

	ctx := context.Background()

	// Create a worktree first
	opts := CreateOptions{
		Branch:     "feature/resume-test",
		BaseBranch: "main",
	}

	wt1, err := service.Create(ctx, "resume-test", opts)
	require.NoError(t, err)

	// Resume the worktree
	wt2, err := service.Resume(ctx, "resume-test")
	require.NoError(t, err)
	assert.Equal(t, wt1.ID, wt2.ID)
	assert.Equal(t, wt1.Path, wt2.Path)

	// Verify worktree still exists in git
	cmd := exec.Command("git", "worktree", "list")
	cmd.Dir = repoDir
	output, err := cmd.Output()
	require.NoError(t, err)
	assert.Contains(t, string(output), wt2.Path)
}

func TestIntegrationDeleteWorktree(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	repoDir, cleanup := setupRealGitRepo(t)
	defer cleanup()

	tempDir := t.TempDir()
	baseDir := filepath.Join(tempDir, "worktrees")
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	service, err := NewService(logger, baseDir, repoDir)
	require.NoError(t, err)

	ctx := context.Background()

	// Create a worktree
	opts := CreateOptions{
		Branch:     "feature/delete-test",
		BaseBranch: "main",
	}

	wt, err := service.Create(ctx, "delete-test", opts)
	require.NoError(t, err)

	// Verify worktree exists
	assert.DirExists(t, wt.Path)

	// Delete the worktree
	err = service.Delete(ctx, "delete-test")
	require.NoError(t, err)

	// Verify worktree directory is gone
	assert.NoDirExists(t, wt.Path)

	// Verify git worktree is removed
	cmd := exec.Command("git", "worktree", "list")
	cmd.Dir = repoDir
	output, err := cmd.Output()
	require.NoError(t, err)
	assert.NotContains(t, string(output), wt.Path)
}

func TestIntegrationMultipleWorktrees(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	repoDir, cleanup := setupRealGitRepo(t)
	defer cleanup()

	tempDir := t.TempDir()
	baseDir := filepath.Join(tempDir, "worktrees")
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	service, err := NewService(logger, baseDir, repoDir)
	require.NoError(t, err)

	ctx := context.Background()

	// Create multiple worktrees
	worktreeNames := []string{"feature-1", "feature-2", "hotfix-1"}
	createdWorktrees := make([]*Worktree, 0, len(worktreeNames))

	for i, name := range worktreeNames {
		opts := CreateOptions{
			Branch:     fmt.Sprintf("feature/test-%d", i+1),
			BaseBranch: "main",
		}

		wt, err := service.Create(ctx, name, opts)
		require.NoError(t, err)
		createdWorktrees = append(createdWorktrees, wt)
	}

	// List all worktrees
	worktrees, err := service.List(ctx, ListOptions{})
	require.NoError(t, err)
	assert.Len(t, worktrees, len(worktreeNames))

	// Verify git sees all worktrees
	cmd := exec.Command("git", "worktree", "list")
	cmd.Dir = repoDir
	output, err := cmd.Output()
	require.NoError(t, err)
	outputStr := string(output)

	for _, wt := range createdWorktrees {
		assert.Contains(t, outputStr, wt.Path)
		assert.DirExists(t, wt.Path)
	}
}

func TestIntegrationWorktreeWithCommits(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	repoDir, cleanup := setupRealGitRepo(t)
	defer cleanup()

	tempDir := t.TempDir()
	baseDir := filepath.Join(tempDir, "worktrees")
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	service, err := NewService(logger, baseDir, repoDir)
	require.NoError(t, err)

	ctx := context.Background()

	// Create a worktree
	opts := CreateOptions{
		Branch:     "feature/commit-test",
		BaseBranch: "main",
	}

	wt, err := service.Create(ctx, "commit-test", opts)
	require.NoError(t, err)

	// Make changes in the worktree
	testFile := filepath.Join(wt.Path, "new-feature.txt")
	require.NoError(t, os.WriteFile(testFile, []byte("New feature content"), 0644))

	// Add and commit changes
	cmd := exec.Command("git", "add", "new-feature.txt")
	cmd.Dir = wt.Path
	require.NoError(t, cmd.Run())

	cmd = exec.Command("git", "commit", "-m", "Add new feature")
	cmd.Dir = wt.Path
	require.NoError(t, cmd.Run())

	// Verify the commit exists in the worktree branch
	cmd = exec.Command("git", "log", "--oneline", "-1")
	cmd.Dir = wt.Path
	output, err := cmd.Output()
	require.NoError(t, err)
	assert.Contains(t, string(output), "Add new feature")

	// Verify the commit doesn't exist in main branch
	cmd = exec.Command("git", "log", "--oneline", "-1")
	cmd.Dir = repoDir
	output, err = cmd.Output()
	require.NoError(t, err)
	assert.NotContains(t, string(output), "Add new feature")
}

func TestIntegrationCompleteWorktreeWithUncommittedChanges(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	repoDir, cleanup := setupRealGitRepo(t)
	defer cleanup()

	tempDir := t.TempDir()
	baseDir := filepath.Join(tempDir, "worktrees")
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	service, err := NewService(logger, baseDir, repoDir)
	require.NoError(t, err)

	ctx := context.Background()

	// Create a worktree
	opts := CreateOptions{
		Branch:     "feature/incomplete",
		BaseBranch: "main",
	}

	wt, err := service.Create(ctx, "incomplete", opts)
	require.NoError(t, err)

	// Make uncommitted changes
	testFile := filepath.Join(wt.Path, "uncommitted.txt")
	require.NoError(t, os.WriteFile(testFile, []byte("Uncommitted content"), 0644))

	// Try to complete worktree with uncommitted changes
	completeOpts := CompleteOptions{Push: false}
	err = service.Complete(ctx, "incomplete", completeOpts)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "uncommitted changes")

	// Verify status is still active
	wt, err = service.Get(ctx, "incomplete")
	require.NoError(t, err)
	assert.Equal(t, StatusActive, wt.Status)
}

func TestIntegrationCleanupMultipleWorktrees(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	repoDir, cleanup := setupRealGitRepo(t)
	defer cleanup()

	tempDir := t.TempDir()
	baseDir := filepath.Join(tempDir, "worktrees")
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	service, err := NewService(logger, baseDir, repoDir)
	require.NoError(t, err)

	ctx := context.Background()

	// Create multiple worktrees
	names := []string{"cleanup-1", "cleanup-2", "cleanup-3"}
	for _, name := range names {
		opts := CreateOptions{
			Branch:     "feature/" + name,
			BaseBranch: "main",
		}
		_, err := service.Create(ctx, name, opts)
		require.NoError(t, err)
	}

	// Complete some worktrees
	err = service.Complete(ctx, "cleanup-1", CompleteOptions{Push: false})
	require.NoError(t, err)
	err = service.Complete(ctx, "cleanup-2", CompleteOptions{Push: false})
	require.NoError(t, err)

	// Cleanup completed worktrees
	cleaned, err := service.Cleanup(ctx, CleanupOptions{
		Completed: true,
		Force:     true,
	})
	require.NoError(t, err)
	assert.Len(t, cleaned, 2)
	assert.Contains(t, cleaned, "cleanup-1")
	assert.Contains(t, cleaned, "cleanup-2")

	// Verify git worktrees are actually removed
	cmd := exec.Command("git", "worktree", "list")
	cmd.Dir = repoDir
	output, err := cmd.Output()
	require.NoError(t, err)
	outputStr := string(output)

	// Should not contain cleaned worktrees
	assert.NotContains(t, outputStr, "cleanup-1")
	assert.NotContains(t, outputStr, "cleanup-2")
	// Should still contain active worktree
	assert.Contains(t, outputStr, "cleanup-3")
}

func TestIntegrationRepositoryIDConsistency(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	repoDir, cleanup := setupRealGitRepo(t)
	defer cleanup()

	tempDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	// Create service instance 1
	service1, err := NewService(logger, tempDir, repoDir)
	require.NoError(t, err)

	ctx := context.Background()
	s1 := service1.(*service)
	repoID1, err := s1.generateRepoID(ctx)
	require.NoError(t, err)

	// Create service instance 2 with same repo
	service2, err := NewService(logger, tempDir, repoDir)
	require.NoError(t, err)

	s2 := service2.(*service)
	repoID2, err := s2.generateRepoID(ctx)
	require.NoError(t, err)

	// Repository IDs should be identical
	assert.Equal(t, repoID1, repoID2)

	// Create worktree with first service
	opts := CreateOptions{Branch: "feature/consistency-test"}
	wt1, err := service1.Create(ctx, "consistency-test", opts)
	require.NoError(t, err)

	// Should be able to access with second service
	wt2, err := service2.Get(ctx, "consistency-test")
	require.NoError(t, err)
	assert.Equal(t, wt1.ID, wt2.ID)
}
