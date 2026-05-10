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
