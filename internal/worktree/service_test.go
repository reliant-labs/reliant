// Copyright (c) 2025 Reliant Labs
package worktree

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testService creates a minimal service with logger for unit tests
func testService() *service {
	return &service{
		logger: slog.Default(),
	}
}

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

// Tests for recursive file copy functionality

func TestFindMatchingFiles(t *testing.T) {
	// Create temporary directory structure
	tmpDir := t.TempDir()

	// Create directory structure:
	// tmpDir/
	//   .env
	//   .env.local
	//   frontend/
	//     .env
	//     .env.local
	//   backend/
	//     .env
	//     config.yaml
	//   .git/
	//     config  (should be ignored)

	dirs := []string{
		"frontend",
		"backend",
		".git",
	}
	for _, d := range dirs {
		require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, d), 0755))
	}

	files := map[string]string{
		".env":                "ROOT_VAR=1",
		".env.local":          "ROOT_LOCAL=1",
		"frontend/.env":       "FRONTEND_VAR=1",
		"frontend/.env.local": "FRONTEND_LOCAL=1",
		"backend/.env":        "BACKEND_VAR=1",
		"backend/config.yaml": "key: value",
		".git/config":         "git config",
	}
	for f, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(tmpDir, f), []byte(content), 0644))
	}

	s := testService()

	tests := []struct {
		name     string
		patterns []string
		want     []string
	}{
		{
			name:     "find all .env files recursively",
			patterns: []string{".env"},
			want:     []string{".env", "backend/.env", "frontend/.env"},
		},
		{
			name:     "find all .env.local files recursively",
			patterns: []string{".env.local"},
			want:     []string{".env.local", "frontend/.env.local"},
		},
		{
			name:     "explicit path",
			patterns: []string{"frontend/.env"},
			want:     []string{"frontend/.env"},
		},
		{
			name:     "mixed patterns and paths",
			patterns: []string{".env", "backend/config.yaml"},
			want:     []string{".env", "backend/.env", "backend/config.yaml", "frontend/.env"},
		},
		{
			name:     "non-existent file",
			patterns: []string{"nonexistent.txt"},
			want:     []string{},
		},
		{
			name:     "git directory is skipped",
			patterns: []string{"config"},
			want:     []string{}, // .git/config should not be found
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := s.findMatchingFiles(tmpDir, tt.patterns)

			// Convert to map for easier comparison (order doesn't matter)
			gotMap := make(map[string]bool)
			for _, f := range got {
				gotMap[f] = true
			}
			wantMap := make(map[string]bool)
			for _, f := range tt.want {
				wantMap[f] = true
			}

			assert.Equal(t, wantMap, gotMap, "findMatchingFiles() mismatch")
		})
	}
}

func TestCopyFilePaths(t *testing.T) {
	// Create source directory
	srcDir := t.TempDir()

	// Create destination directory
	dstDir := t.TempDir()

	// Create source structure
	require.NoError(t, os.MkdirAll(filepath.Join(srcDir, "frontend"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(srcDir, "backend/config"), 0755))

	files := map[string]string{
		".env":                "ROOT=1",
		"frontend/.env":       "FRONTEND=1",
		"backend/config/.env": "BACKEND_CONFIG=1",
	}
	for f, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(srcDir, f), []byte(content), 0644))
	}

	s := testService()

	// Copy files
	relativePaths := []string{".env", "frontend/.env", "backend/config/.env"}
	s.copyFilePaths(srcDir, dstDir, relativePaths)

	// Verify files were copied with correct content and structure
	for _, relPath := range relativePaths {
		dstPath := filepath.Join(dstDir, relPath)
		content, err := os.ReadFile(dstPath)
		require.NoError(t, err, "file %s was not copied", relPath)

		srcContent, _ := os.ReadFile(filepath.Join(srcDir, relPath))
		assert.Equal(t, string(srcContent), string(content), "file %s content mismatch", relPath)
	}

	// Verify directory structure was created
	_, err := os.Stat(filepath.Join(dstDir, "frontend"))
	assert.NoError(t, err, "frontend directory was not created")

	_, err = os.Stat(filepath.Join(dstDir, "backend/config"))
	assert.NoError(t, err, "backend/config directory was not created")
}

func TestCopyFilesIntegration(t *testing.T) {
	// Create source directory with nested .env files
	srcDir := t.TempDir()

	// Create destination directory
	dstDir := t.TempDir()

	// Create source structure
	dirs := []string{"frontend", "backend", "services/auth", "services/api"}
	for _, d := range dirs {
		require.NoError(t, os.MkdirAll(filepath.Join(srcDir, d), 0755))
	}

	files := map[string]string{
		".env":               "ROOT=1",
		"frontend/.env":      "FRONTEND=1",
		"backend/.env":       "BACKEND=1",
		"services/auth/.env": "AUTH=1",
		"services/api/.env":  "API=1",
	}
	for f, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(srcDir, f), []byte(content), 0644))
	}

	s := testService()
	ctx := context.Background()

	// Copy just ".env" - should find all of them
	s.copyFiles(ctx, srcDir, dstDir, []string{".env"})

	// Verify all .env files were copied
	for relPath := range files {
		dstPath := filepath.Join(dstDir, relPath)
		_, err := os.Stat(dstPath)
		assert.NoError(t, err, "file %s was not copied", relPath)
	}
}

func TestCreateWorktreeWithCopyFiles(t *testing.T) {
	service, repoDir, _, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()

	// Create files in the repo to copy
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, "frontend"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, ".env"), []byte("ROOT=1"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "frontend/.env"), []byte("FRONTEND=1"), 0644))

	opts := CreateOptions{
		Branch:    "feature/test",
		CopyFiles: []string{".env"}, // Should copy both .env files
	}

	wt, err := service.Create(ctx, "test-worktree", opts)
	require.NoError(t, err)

	// Verify root .env was copied
	content, err := os.ReadFile(filepath.Join(wt.Path, ".env"))
	assert.NoError(t, err)
	assert.Equal(t, "ROOT=1", string(content))

	// Verify frontend/.env was copied
	content, err = os.ReadFile(filepath.Join(wt.Path, "frontend/.env"))
	assert.NoError(t, err)
	assert.Equal(t, "FRONTEND=1", string(content))
}

func TestCopyDirectory(t *testing.T) {
	// Create source directory
	srcDir := t.TempDir()

	// Create destination directory
	dstDir := t.TempDir()

	// Create source structure with a directory containing multiple files
	require.NoError(t, os.MkdirAll(filepath.Join(srcDir, "frontend/src"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(srcDir, "frontend/config"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(srcDir, "backend"), 0755))

	files := map[string]string{
		"frontend/src/index.ts": "export const index = 1;",
		"frontend/src/utils.ts": "export const utils = 2;",
		"frontend/config/.env":  "FRONTEND_ENV=1",
		"frontend/package.json": `{"name": "frontend"}`,
		"backend/server.go":     "package main",
		"backend/config.yaml":   "port: 8080",
		"root.txt":              "root file",
	}
	for f, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(srcDir, f), []byte(content), 0644))
	}

	s := testService()

	// Test copying a directory - should copy all files within it
	s.copyFilePaths(srcDir, dstDir, []string{"frontend"})

	// Verify all files in frontend directory were copied
	frontendFiles := []string{
		"frontend/src/index.ts",
		"frontend/src/utils.ts",
		"frontend/config/.env",
		"frontend/package.json",
	}
	for _, relPath := range frontendFiles {
		dstPath := filepath.Join(dstDir, relPath)
		content, err := os.ReadFile(dstPath)
		require.NoError(t, err, "file %s was not copied", relPath)

		srcContent, _ := os.ReadFile(filepath.Join(srcDir, relPath))
		assert.Equal(t, string(srcContent), string(content), "file %s content mismatch", relPath)
	}

	// Verify files outside the directory were NOT copied
	_, err := os.Stat(filepath.Join(dstDir, "backend/server.go"))
	assert.Error(t, err, "backend/server.go should not have been copied")

	_, err = os.Stat(filepath.Join(dstDir, "root.txt"))
	assert.Error(t, err, "root.txt should not have been copied")
}

func TestFindMatchingFilesWithDirectory(t *testing.T) {
	tmpDir := t.TempDir()

	// Create directory structure
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, "frontend/src"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, "backend"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "frontend/src/index.ts"), []byte("content"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "frontend/src/utils.ts"), []byte("content"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "frontend/package.json"), []byte("content"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "backend/server.go"), []byte("content"), 0644))

	s := testService()

	// Test finding files in a directory
	matches := s.findMatchingFiles(tmpDir, []string{"frontend"})

	// Should find all files within frontend directory
	expectedFiles := map[string]bool{
		"frontend/src/index.ts": true,
		"frontend/src/utils.ts": true,
		"frontend/package.json": true,
	}

	gotMap := make(map[string]bool)
	for _, f := range matches {
		gotMap[f] = true
	}

	for expectedFile := range expectedFiles {
		assert.True(t, gotMap[expectedFile], "expected file %s to be found", expectedFile)
	}

	// Should not find files outside the directory
	_, found := gotMap["backend/server.go"]
	assert.False(t, found, "backend/server.go should not be found")
}
