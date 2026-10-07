// Copyright (c) 2025 Reliant Labs
package worktree

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestService(t *testing.T) (Service, string, string, func()) {
	// Create temporary directories
	tempDir := t.TempDir()
	baseDir := filepath.Join(tempDir, "worktrees")
	repoDir := filepath.Join(tempDir, "repo")

	// Initialize git repo
	require.NoError(t, os.MkdirAll(repoDir, 0755))

	// Setup git repo with initial commit
	gitCommands := [][]string{
		{"git", "init"},
		{"git", "config", "user.name", "Test User"},
		{"git", "config", "user.email", "test@example.com"},
		{"git", "checkout", "-b", "main"},
		{"git", "commit", "--allow-empty", "-m", "Initial commit"},
	}

	for _, cmd := range gitCommands {
		execCmd := cmd[0]
		args := cmd[1:]
		if err := execGitCommand(repoDir, execCmd, args...); err != nil {
			t.Fatalf("Failed to setup git repo: %v", err)
		}
	}

	// Create service
	service, err := NewService(baseDir, repoDir)
	require.NoError(t, err)

	cleanup := func() {
		_ = os.RemoveAll(tempDir)
	}

	return service, repoDir, baseDir, cleanup
}

func execGitCommand(dir, command string, args ...string) error {
	cmd := exec.Command(command, args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s failed: %w\nOutput: %s", command, err, string(output))
	}
	return nil
}

func TestNewService(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()

	service, err := NewService(tempDir, tempDir)
	assert.NoError(t, err)
	assert.NotNil(t, service)
}

func TestCreateWorktree(t *testing.T) {
	service, _, _, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	opts := CreateOptions{
		Branch:     "feature/test",
		BaseBranch: "main",
	}

	// Test creating a new worktree
	wt, err := service.Create(ctx, "test-worktree", opts)
	assert.NoError(t, err)
	if assert.NotNil(t, wt) {
		assert.Equal(t, "test-worktree", wt.Name)
		assert.Equal(t, "feature/test", wt.Branch)
		assert.Equal(t, "main", wt.BaseBranch)
		assert.Equal(t, StatusActive, wt.Status)
		assert.False(t, wt.CreatedAt.IsZero())
		assert.False(t, wt.UpdatedAt.IsZero())
	}
}

func TestCreateWorktreeAlreadyExists(t *testing.T) {
	service, _, _, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	opts := CreateOptions{
		Branch: "feature/test",
	}

	// Create first worktree
	_, err := service.Create(ctx, "test-worktree", opts)
	assert.NoError(t, err)

	// Try to create same worktree again
	_, err = service.Create(ctx, "test-worktree", opts)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestListWorktrees(t *testing.T) {
	service, _, _, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()

	// Create multiple worktrees
	names := []string{"worktree1", "worktree2", "worktree3"}
	for _, name := range names {
		opts := CreateOptions{Branch: "feature/" + name}
		_, err := service.Create(ctx, name, opts)
		require.NoError(t, err)
	}

	// List all worktrees
	worktrees, err := service.List(ctx, ListOptions{})
	assert.NoError(t, err)
	assert.Len(t, worktrees, 3)

	// Verify names are present
	foundNames := make(map[string]bool)
	for _, wt := range worktrees {
		foundNames[wt.Name] = true
	}
	for _, name := range names {
		assert.True(t, foundNames[name], "Worktree %s not found", name)
	}
}

func TestListWorktreesWithFilter(t *testing.T) {
	service, _, _, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()

	// Create worktrees with different statuses
	_, err := service.Create(ctx, "active-worktree", CreateOptions{Branch: "feature/active"})
	require.NoError(t, err)

	wt2, err := service.Create(ctx, "completed-worktree", CreateOptions{Branch: "feature/completed"})
	require.NoError(t, err)

	// Manually set one as completed
	wt2.Status = StatusCompleted

	// List only active worktrees
	worktrees, err := service.List(ctx, ListOptions{Status: StatusActive})
	assert.NoError(t, err)
	assert.Len(t, worktrees, 1)
	assert.Equal(t, "active-worktree", worktrees[0].Name)
}

func TestGetWorktree(t *testing.T) {
	service, _, _, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()

	// Create a worktree
	opts := CreateOptions{Branch: "feature/test"}
	wt1, err := service.Create(ctx, "test-worktree", opts)
	require.NoError(t, err)

	// Get the worktree
	wt2, err := service.Get(ctx, "test-worktree")
	assert.NoError(t, err)
	assert.Equal(t, wt1.ID, wt2.ID)
	assert.Equal(t, wt1.Name, wt2.Name)
	assert.Equal(t, wt1.Branch, wt2.Branch)
}

func TestGetNonExistentWorktree(t *testing.T) {
	service, _, _, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()

	_, err := service.Get(ctx, "non-existent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestCompleteWorktree(t *testing.T) {
	service, _, _, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()

	// Create a worktree
	opts := CreateOptions{Branch: "feature/test"}
	_, err := service.Create(ctx, "test-worktree", opts)
	require.NoError(t, err)

	// Complete the worktree
	completeOpts := CompleteOptions{
		Push:        false, // Don't actually push in test
		CreatePR:    false,
		DeleteLocal: false,
	}

	err = service.Complete(ctx, "test-worktree", completeOpts)
	assert.NoError(t, err)

	// Verify status changed
	wt, err := service.Get(ctx, "test-worktree")
	assert.NoError(t, err)
	assert.Equal(t, StatusCompleted, wt.Status)
}

func TestDeleteWorktree(t *testing.T) {
	service, _, _, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()

	// Create a worktree
	opts := CreateOptions{Branch: "feature/test"}
	_, err := service.Create(ctx, "test-worktree", opts)
	require.NoError(t, err)

	// Delete the worktree
	err = service.Delete(ctx, "test-worktree")
	assert.NoError(t, err)

	// Verify it's gone
	_, err = service.Get(ctx, "test-worktree")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestCleanupWorktrees(t *testing.T) {
	service, _, _, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()

	// Create worktrees with different statuses
	// Active worktree
	_, err := service.Create(ctx, "active-wt", CreateOptions{Branch: "feature/active"})
	require.NoError(t, err)

	// Completed worktree
	_, err = service.Create(ctx, "completed-wt", CreateOptions{Branch: "feature/completed"})
	require.NoError(t, err)
	err = service.Complete(ctx, "completed-wt", CompleteOptions{})
	require.NoError(t, err)

	// Old worktree (simulate by setting last active time)
	oldWt, err := service.Create(ctx, "old-wt", CreateOptions{Branch: "feature/old"})
	require.NoError(t, err)
	oldWt.LastActive = time.Now().Add(-8 * 24 * time.Hour) // 8 days ago

	// Cleanup completed worktrees
	cleaned, err := service.Cleanup(ctx, CleanupOptions{
		Completed: true,
		Force:     true, // Force to avoid git command issues in test
	})
	assert.NoError(t, err)
	assert.Contains(t, cleaned, "completed-wt")

	// Verify completed worktree is gone
	_, err = service.Get(ctx, "completed-wt")
	assert.Error(t, err)

	// Verify active worktree still exists
	_, err = service.Get(ctx, "active-wt")
	assert.NoError(t, err)
}

func TestMetadataPersistence(t *testing.T) {
	service1, repoDir, baseDir, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	opts := CreateOptions{Branch: "feature/test"}
	wt1, err := service1.Create(ctx, "test-worktree", opts)
	require.NoError(t, err)

	// Create new service instance (simulating restart) using same paths
	service2, err := NewService(baseDir, repoDir)
	require.NoError(t, err)

	// Verify worktree is still accessible
	wt2, err := service2.Get(ctx, "test-worktree")
	assert.NoError(t, err)
	assert.Equal(t, wt1.ID, wt2.ID)
	assert.Equal(t, wt1.Name, wt2.Name)
	assert.Equal(t, wt1.Branch, wt2.Branch)
}

func TestWorktreeWithSessionID(t *testing.T) {
	service, _, _, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	sessionID := "test-session-123"

	opts := CreateOptions{
		Branch:    "feature/test",
		SessionID: sessionID,
	}

	wt, err := service.Create(ctx, "test-worktree", opts)
	assert.NoError(t, err)
	assert.Equal(t, sessionID, wt.SessionID)

	// Test filtering by session ID
	worktrees, err := service.List(ctx, ListOptions{SessionID: sessionID})
	assert.NoError(t, err)
	assert.Len(t, worktrees, 1)
	assert.Equal(t, sessionID, worktrees[0].SessionID)
}

func TestWorktreeStates(t *testing.T) {
	t.Parallel()
	states := []Status{StatusActive, StatusCompleted, StatusAbandoned, StatusMerging}

	for _, state := range states {
		assert.NotEmpty(t, string(state), "Status should have string representation")
	}
}

func BenchmarkCreateWorktree(b *testing.B) {
	tempDir := b.TempDir()
	service, err := NewService(tempDir, tempDir)
	require.NoError(b, err)

	ctx := context.Background()
	opts := CreateOptions{Branch: "feature/bench"}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		name := fmt.Sprintf("worktree-%d", i)
		_, err := service.Create(ctx, name, opts)
		if err != nil {
			b.Fatalf("Failed to create worktree: %v", err)
		}
	}
}

func BenchmarkListWorktrees(b *testing.B) {
	tempDir := b.TempDir()
	service, err := NewService(tempDir, tempDir)
	require.NoError(b, err)

	ctx := context.Background()
	opts := CreateOptions{Branch: "feature/bench"}

	// Create multiple worktrees
	for i := 0; i < 100; i++ {
		name := fmt.Sprintf("worktree-%d", i)
		_, err := service.Create(ctx, name, opts)
		require.NoError(b, err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := service.List(ctx, ListOptions{})
		if err != nil {
			b.Fatalf("Failed to list worktrees: %v", err)
		}
	}
}

// copy_files names exact paths. A bare name copies only the file at the repo
// root — it is never searched for — and a nested file is reached by its path.
func TestCreateWorktreeCopiesExactPathsOnly(t *testing.T) {
	service, repoDir, _, cleanup := setupTestService(t)
	defer cleanup()

	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, "frontend"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, ".env"), []byte("ROOT=1"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "frontend/.env"), []byte("FRONTEND=1"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "backend.env"), []byte("NOT_ASKED=1"), 0644))

	wt, err := service.Create(context.Background(), "test-worktree", CreateOptions{
		Branch:    "feature/test",
		CopyFiles: []string{".env", "frontend/.env", "absent/.env"},
	})
	require.NoError(t, err, "a missing path is skipped, not an error")

	content, err := os.ReadFile(filepath.Join(wt.Path, ".env"))
	require.NoError(t, err)
	assert.Equal(t, "ROOT=1", string(content))

	content, err = os.ReadFile(filepath.Join(wt.Path, "frontend/.env"))
	require.NoError(t, err)
	assert.Equal(t, "FRONTEND=1", string(content))

	_, err = os.Stat(filepath.Join(wt.Path, "backend.env"))
	assert.True(t, os.IsNotExist(err), "only the named paths are copied")
}

// A bare name is NOT a search: ".env" must not reach frontend/.env.
func TestCreateWorktreeBareNameDoesNotSearch(t *testing.T) {
	service, repoDir, _, cleanup := setupTestService(t)
	defer cleanup()

	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, "frontend"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "frontend/.env"), []byte("FRONTEND=1"), 0644))

	wt, err := service.Create(context.Background(), "test-worktree", CreateOptions{
		Branch:    "feature/test",
		CopyFiles: []string{".env"},
	})
	require.NoError(t, err)

	_, err = os.Stat(filepath.Join(wt.Path, "frontend/.env"))
	assert.True(t, os.IsNotExist(err), "\".env\" names the root file only; frontend/.env needs its own path")
}

// An entry that escapes the repo is rejected before any git work.
func TestCreateWorktreeRejectsEscapingCopyPath(t *testing.T) {
	service, _, _, cleanup := setupTestService(t)
	defer cleanup()

	for _, bad := range []string{"../secrets", "/etc/passwd", ""} {
		_, err := service.Create(context.Background(), "test-worktree", CreateOptions{
			Branch:    "feature/test",
			CopyFiles: []string{bad},
		})
		assert.Error(t, err, "copy path %q must be rejected", bad)
	}
}
