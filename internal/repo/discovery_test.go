package repo

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDiscover_SingleRepo(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, ".git"), 0o755))

	found, err := Discover(context.Background(), dir, 2)
	require.NoError(t, err)
	require.Len(t, found, 1)
	require.Equal(t, "", found[0].RelativePath)
	require.Equal(t, filepath.Base(dir), found[0].Name)
}

func TestDiscover_MultiRepo_NoParentGit(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"api", "web"} {
		require.NoError(t, os.MkdirAll(filepath.Join(dir, name, ".git"), 0o755))
	}

	found, err := Discover(context.Background(), dir, 2)
	require.NoError(t, err)
	require.Len(t, found, 2)

	names := map[string]bool{}
	for _, f := range found {
		names[f.Name] = true
	}
	require.True(t, names["api"])
	require.True(t, names["web"])
}

func TestDiscover_MultiRepo_WithParentGit(t *testing.T) {
	dir := t.TempDir()
	// Parent has .git (tracks shared config)
	require.NoError(t, os.Mkdir(filepath.Join(dir, ".git"), 0o755))
	// Children have their own .git
	for _, name := range []string{"api", "web"} {
		require.NoError(t, os.MkdirAll(filepath.Join(dir, name, ".git"), 0o755))
	}

	found, err := Discover(context.Background(), dir, 2)
	require.NoError(t, err)
	// Should find the children, not the parent
	require.Len(t, found, 2)

	names := map[string]bool{}
	for _, f := range found {
		names[f.Name] = true
		require.NotEmpty(t, f.RelativePath)
	}
	require.True(t, names["api"])
	require.True(t, names["web"])
}

func TestDiscover_SkipsIgnoredDirs(t *testing.T) {
	dir := t.TempDir()
	// A git repo nested inside node_modules should be ignored
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "node_modules", "pkg", ".git"), 0o755))
	// But a real child repo should be found
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "api", ".git"), 0o755))

	found, err := Discover(context.Background(), dir, 3)
	require.NoError(t, err)
	require.Len(t, found, 1)
	require.Equal(t, "api", found[0].Name)
}

func TestDiscover_EmptyDir(t *testing.T) {
	dir := t.TempDir()

	found, err := Discover(context.Background(), dir, 2)
	require.NoError(t, err)
	require.Empty(t, found)
}

// mainCheckout fabricates a main checkout whose .git dir lists a linked worktree.
func mainCheckout(t *testing.T, dir string, worktrees ...string) {
	t.Helper()
	for _, w := range worktrees {
		wt := filepath.Join(dir, ".git", "worktrees", w)
		require.NoError(t, os.MkdirAll(wt, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(wt, "commondir"), []byte("../..\n"), 0o644))
	}
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0o755))
}

func linkedCheckout(t *testing.T, dir, mainDir, name string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	gitdir := filepath.Join(mainDir, ".git", "worktrees", name)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: "+gitdir+"\n"), 0o644))
}

func names(found []Found) map[string]bool {
	m := map[string]bool{}
	for _, f := range found {
		m[f.RelativePath] = true
	}
	return m
}

func TestDiscover_LinkedWorktreesCollapseToMain(t *testing.T) {
	root := t.TempDir()
	mainCheckout(t, filepath.Join(root, "zmain"), "a", "b")
	linkedCheckout(t, filepath.Join(root, "a-wt"), filepath.Join(root, "zmain"), "a")
	linkedCheckout(t, filepath.Join(root, "b-wt"), filepath.Join(root, "zmain"), "b")
	mainCheckout(t, filepath.Join(root, "other"))

	found, err := Discover(context.Background(), root, 2)
	require.NoError(t, err)
	require.Equal(t, map[string]bool{"zmain": true, "other": true}, names(found))
}

func TestDiscover_LinkedWorktreeRelativeGitdir(t *testing.T) {
	root := t.TempDir()
	mainCheckout(t, filepath.Join(root, "main"), "w")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "w"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "w", ".git"), []byte("gitdir: ../main/.git/worktrees/w\n"), 0o644))

	found, err := Discover(context.Background(), root, 2)
	require.NoError(t, err)
	require.Equal(t, map[string]bool{"main": true}, names(found))
}

func TestDiscover_LinkedWorktreesWithMainOutsideScan(t *testing.T) {
	outside := t.TempDir()
	for _, n := range []string{"cp", "forge", "reliant"} {
		mainCheckout(t, filepath.Join(outside, n), "t")
	}
	root := t.TempDir()
	for _, n := range []string{"cp", "forge", "reliant"} {
		linkedCheckout(t, filepath.Join(root, n), filepath.Join(outside, n), "t")
	}

	found, err := Discover(context.Background(), root, 2)
	require.NoError(t, err)
	require.Equal(t, map[string]bool{"cp": true, "forge": true, "reliant": true}, names(found))
}

func TestDiscover_TwoLinkedWorktreesMainOutsideKeepFirst(t *testing.T) {
	outside := t.TempDir()
	mainCheckout(t, filepath.Join(outside, "m"), "x", "y")
	root := t.TempDir()
	linkedCheckout(t, filepath.Join(root, "a"), filepath.Join(outside, "m"), "x")
	linkedCheckout(t, filepath.Join(root, "b"), filepath.Join(outside, "m"), "y")

	found, err := Discover(context.Background(), root, 2)
	require.NoError(t, err)
	require.Equal(t, map[string]bool{"a": true}, names(found))
}

func TestDiscover_UnparseableGitFileIsOwnIdentity(t *testing.T) {
	root := t.TempDir()
	for _, n := range []string{"a", "b"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, n), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, n, ".git"), []byte("garbage"), 0o644))
	}
	found, err := Discover(context.Background(), root, 2)
	require.NoError(t, err)
	require.Equal(t, map[string]bool{"a": true, "b": true}, names(found))
}

func TestDiscover_NestedLinkedWorktree(t *testing.T) {
	root := t.TempDir()
	mainCheckout(t, filepath.Join(root, "grp", "main"), "w")
	linkedCheckout(t, filepath.Join(root, "grp2", "w"), filepath.Join(root, "grp", "main"), "w")
	found, err := Discover(context.Background(), root, 2)
	require.NoError(t, err)
	require.Equal(t, map[string]bool{filepath.Join("grp", "main"): true}, names(found))
}
