package daemonruntime

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

// ---------------------------------------------------------------------------
// hashWalkDir
// ---------------------------------------------------------------------------

func TestHashWalkDir_StableHash(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.txt"), "hello")
	writeFile(t, filepath.Join(dir, "b.txt"), "world")

	h1, err := hashWalkDir(context.Background(), dir)
	require.NoError(t, err)

	h2, err := hashWalkDir(context.Background(), dir)
	require.NoError(t, err)

	assert.Equal(t, h1, h2, "same directory state should produce the same hash")
	assert.Len(t, h1, 64, "sha256 hex digest should be 64 chars")
}

func TestHashWalkDir_DetectsNewFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.txt"), "hello")

	h1, err := hashWalkDir(context.Background(), dir)
	require.NoError(t, err)

	writeFile(t, filepath.Join(dir, "b.txt"), "new file")

	h2, err := hashWalkDir(context.Background(), dir)
	require.NoError(t, err)

	assert.NotEqual(t, h1, h2, "adding a file should change the hash")
}

func TestHashWalkDir_DetectsDeletedFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a.txt")
	writeFile(t, target, "hello")
	writeFile(t, filepath.Join(dir, "b.txt"), "keep")

	h1, err := hashWalkDir(context.Background(), dir)
	require.NoError(t, err)

	require.NoError(t, os.Remove(target))

	h2, err := hashWalkDir(context.Background(), dir)
	require.NoError(t, err)

	assert.NotEqual(t, h1, h2, "deleting a file should change the hash")
}

func TestHashWalkDir_DetectsModifiedFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a.txt")
	writeFile(t, target, "original")

	h1, err := hashWalkDir(context.Background(), dir)
	require.NoError(t, err)

	// Ensure modtime differs — sleep briefly then rewrite with different size.
	time.Sleep(10 * time.Millisecond)
	writeFile(t, target, "modified content that is longer")

	h2, err := hashWalkDir(context.Background(), dir)
	require.NoError(t, err)

	assert.NotEqual(t, h1, h2, "modifying a file should change the hash")
}

func TestHashWalkDir_SkipsIgnoredDirs(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.txt"), "root file")

	// Create empty skipped directories first so the parent dir modtime is set.
	for name := range fileTreeSkipDirs {
		require.NoError(t, os.MkdirAll(filepath.Join(dir, name), 0o755))
	}

	// Hash with empty skipped dirs
	h1, err := hashWalkDir(context.Background(), dir)
	require.NoError(t, err)

	// Add files inside every skipped directory — hash should NOT change.
	for name := range fileTreeSkipDirs {
		writeFile(t, filepath.Join(dir, name, "junk.txt"), "should be skipped")
	}

	h2, err := hashWalkDir(context.Background(), dir)
	require.NoError(t, err)

	assert.Equal(t, h1, h2, "files inside skipped directories must not affect the hash")
}

func TestHashWalkDir_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := hashWalkDir(ctx, t.TempDir())
	// The walk itself may return context.Canceled, or it may succeed
	// because the dir is empty (the context check only fires every 1000 files).
	// For an empty dir, no error is expected — so build a dir with >1000 entries.

	dir := t.TempDir()
	for i := 0; i < 1100; i++ {
		writeFile(t, filepath.Join(dir, fmt.Sprintf("f%04d.txt", i)), "x")
	}

	_, err = hashWalkDir(ctx, dir)
	require.Error(t, err, "cancelled context should return an error for large dirs")
	assert.ErrorIs(t, err, context.Canceled)
}

func TestHashWalkDir_EmptyDirectory(t *testing.T) {
	dir := t.TempDir()

	h, err := hashWalkDir(context.Background(), dir)
	require.NoError(t, err)
	assert.Len(t, h, 64, "should return a valid sha256 hex digest")
}

// ---------------------------------------------------------------------------
// hashGitFileTree
// ---------------------------------------------------------------------------

func TestHashGitFileTree_WorksInGitRepo(t *testing.T) {
	dir := initGitRepo(t)
	writeFile(t, filepath.Join(dir, "a.txt"), "hello")
	gitAdd(t, dir, ".")
	gitCommit(t, dir, "init")

	h, err := hashGitFileTree(context.Background(), dir)
	require.NoError(t, err)
	assert.Len(t, h, 64)
}

func TestHashGitFileTree_DetectsUntrackedFile(t *testing.T) {
	dir := initGitRepo(t)
	writeFile(t, filepath.Join(dir, "a.txt"), "tracked")
	gitAdd(t, dir, ".")
	gitCommit(t, dir, "init")

	h1, err := hashGitFileTree(context.Background(), dir)
	require.NoError(t, err)

	writeFile(t, filepath.Join(dir, "untracked.txt"), "new")

	h2, err := hashGitFileTree(context.Background(), dir)
	require.NoError(t, err)

	assert.NotEqual(t, h1, h2, "untracked file should change git status and thus the hash")
}

func TestHashGitFileTree_DetectsModifiedTrackedFile(t *testing.T) {
	dir := initGitRepo(t)
	target := filepath.Join(dir, "a.txt")
	writeFile(t, target, "original")
	gitAdd(t, dir, ".")
	gitCommit(t, dir, "init")

	h1, err := hashGitFileTree(context.Background(), dir)
	require.NoError(t, err)

	writeFile(t, target, "modified")

	h2, err := hashGitFileTree(context.Background(), dir)
	require.NoError(t, err)

	assert.NotEqual(t, h1, h2, "modifying a tracked file should change the hash")
}

func TestHashGitFileTree_NonGitDirReturnsError(t *testing.T) {
	dir := t.TempDir() // not a git repo

	_, err := hashGitFileTree(context.Background(), dir)
	require.Error(t, err, "non-git directory should produce an error from git commands")
}

// ---------------------------------------------------------------------------
// hashFileTree (dispatcher)
// ---------------------------------------------------------------------------

func TestHashFileTree_GitRepo(t *testing.T) {
	dir := initGitRepo(t)
	writeFile(t, filepath.Join(dir, "a.txt"), "hello")
	gitAdd(t, dir, ".")
	gitCommit(t, dir, "init")

	h, err := hashFileTree(context.Background(), dir)
	require.NoError(t, err)
	assert.Len(t, h, 64)
}

func TestHashFileTree_NonGitRepo(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.txt"), "hello")

	h, err := hashFileTree(context.Background(), dir)
	require.NoError(t, err)
	assert.Len(t, h, 64)
}

// ---------------------------------------------------------------------------
// fileTreeSkipDirs map
// ---------------------------------------------------------------------------

func TestFileTreeSkipDirs_ContainsExpectedEntries(t *testing.T) {
	expected := []string{
		"node_modules",
		".git",
		"dist",
		"build",
		"__pycache__",
		".reliant",
		"vendor",
		"bower_components",
		"jspm_packages",
		".next",
		".nuxt",
		"target",
		"coverage",
		"tmp",
		"temp",
	}
	for _, name := range expected {
		assert.True(t, fileTreeSkipDirs[name], "%q should be in fileTreeSkipDirs", name)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func initGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "test@test.com")
	runGit(t, dir, "config", "user.name", "Test")
	return dir
}

func gitAdd(t *testing.T, dir string, args ...string) {
	t.Helper()
	runGit(t, dir, append([]string{"add"}, args...)...)
}

func gitCommit(t *testing.T, dir, msg string) {
	t.Helper()
	runGit(t, dir, "commit", "-m", msg, "--allow-empty")
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v failed: %s", args, out)
}
