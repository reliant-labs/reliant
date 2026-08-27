package daemonruntime

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/filetree"
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
	skipped := filetree.SkipDirNames()
	for _, name := range skipped {
		require.NoError(t, os.MkdirAll(filepath.Join(dir, name), 0o755))
	}

	// Hash with empty skipped dirs
	h1, err := hashWalkDir(context.Background(), dir)
	require.NoError(t, err)

	// Add files inside every skipped directory — hash should NOT change.
	for _, name := range skipped {
		writeFile(t, filepath.Join(dir, name, "junk.txt"), "should be skipped")
	}

	h2, err := hashWalkDir(context.Background(), dir)
	require.NoError(t, err)

	assert.Equal(t, h1, h2, "files inside skipped directories must not affect the hash")
}

func TestHashWalkDir_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	// A cancelled walk may return context.Canceled, or may succeed because the
	// dir is empty (the context check only fires every 1000 files). For an
	// empty dir no error is expected — so build a dir with >1000 entries.
	dir := t.TempDir()
	for i := 0; i < 1100; i++ {
		writeFile(t, filepath.Join(dir, fmt.Sprintf("f%04d.txt", i)), "x")
	}

	_, err := hashWalkDir(ctx, dir)
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
// canonical skip set
// ---------------------------------------------------------------------------

// The watcher no longer owns a skip list. It shares the canonical one with the
// file-tree walk, which is the whole point: the two lists had drifted, and the
// weaker of them guarded the unbounded walk. Every name the watcher's own list
// used to carry must still be honoured.
func TestHashWalkDir_UsesCanonicalSkipSet(t *testing.T) {
	legacy := []string{
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
	for _, name := range legacy {
		assert.True(t, filetree.IsSkippedDir(name), "%q must still be skipped", name)
	}
}

// A budget-truncated walk still hashes: the poller must degrade to watching a
// bounded prefix of a huge non-git project rather than walking it whole every
// five seconds.
func TestHashWalkDir_BoundedByNodeBudget(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 64; i++ {
		writeFile(t, filepath.Join(dir, fmt.Sprintf("f%03d.txt", i)), "x")
	}

	h, err := hashWalkDir(context.Background(), dir)
	require.NoError(t, err)
	assert.Len(t, h, 64)

	// Truncation is enforced inside the shared walker; prove the walker itself
	// stops at the budget rather than trusting the hash to reveal it.
	count := 0
	truncated, err := filetree.WalkHashable(dir, 10, func(string, fs.DirEntry) error {
		count++
		return nil
	})
	require.NoError(t, err)
	assert.True(t, truncated, "walk over 65 entries with a budget of 10 must truncate")
	assert.Equal(t, 10, count, "walk must stop exactly at the budget")
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
