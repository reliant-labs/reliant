package repo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

// mkMain creates dir with a `.git` directory.
func mkMain(t *testing.T, dir string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0o755))
}

// mkLinked creates dir as a linked worktree of the main checkout at mainDir,
// writing `.git` as a file pointing at <main>/.git/worktrees/<name>.
func mkLinked(t *testing.T, dir, mainDir, gitdirLine, commondir string) {
	t.Helper()
	g := filepath.Join(mainDir, ".git", "worktrees", filepath.Base(dir))
	require.NoError(t, os.MkdirAll(g, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(g, "commondir"), []byte(commondir+"\n"), 0o644))
	require.NoError(t, os.MkdirAll(dir, 0o755))
	if gitdirLine == "" {
		gitdirLine = "gitdir: " + g
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git"), []byte(gitdirLine+"\n"), 0o644))
}

func discoverRels(t *testing.T, dir string) []string {
	t.Helper()
	found, err := Discover(context.Background(), dir, 2)
	require.NoError(t, err)
	var rels []string
	for _, f := range found {
		rels = append(rels, f.RelativePath)
	}
	sort.Strings(rels)
	return rels
}

func TestDiscover_DropsSiblingWorktrees(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "reliant")
	mkMain(t, main)
	mkMain(t, filepath.Join(dir, "forge"))
	mkLinked(t, filepath.Join(dir, "a-reliant-early"), main, "", "../..") // sorts before main
	mkLinked(t, filepath.Join(dir, "reliant-foo"), main, "", "../..")
	require.Equal(t, []string{"forge", "reliant"}, discoverRels(t, dir))
}

func TestDiscover_UIWorkspaceKeepsAllLinked(t *testing.T) {
	dir := t.TempDir()
	mains := t.TempDir()
	for _, n := range []string{"a", "b", "c"} {
		m := filepath.Join(mains, n)
		mkMain(t, m)
		mkLinked(t, filepath.Join(dir, n), m, "", "../..")
	}
	require.Equal(t, []string{"a", "b", "c"}, discoverRels(t, dir))
}

func TestDiscover_SubmoduleKept(t *testing.T) {
	dir := t.TempDir()
	mkMain(t, filepath.Join(dir, "super"))
	sub := filepath.Join(dir, "sub")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git", "modules", "x"), 0o755))
	require.NoError(t, os.MkdirAll(sub, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sub, ".git"), []byte("gitdir: ../.git/modules/x\n"), 0o644))
	require.Equal(t, []string{"sub", "super"}, discoverRels(t, dir))
}

func TestDiscover_WorktreesOfOutOfScanMainKept(t *testing.T) {
	dir := t.TempDir()
	m := filepath.Join(t.TempDir(), "main")
	mkMain(t, m)
	mkLinked(t, filepath.Join(dir, "one"), m, "", "../..")
	mkLinked(t, filepath.Join(dir, "two"), m, "", "../..")
	require.Equal(t, []string{"one", "two"}, discoverRels(t, dir))
}

func TestDiscover_RootMainWithChildWorktree(t *testing.T) {
	dir := t.TempDir()
	mkMain(t, dir)
	mkLinked(t, filepath.Join(dir, "wt"), dir, "", "../..")
	require.Equal(t, []string{""}, discoverRels(t, dir))
}

func TestDiscover_RelativeGitdirAndSymlinkedCommon(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	require.NoError(t, os.MkdirAll(real, 0o755))
	link := filepath.Join(base, "link")
	require.NoError(t, os.Symlink(real, link))

	scan := filepath.Join(base, "scan")
	main := filepath.Join(scan, "main")
	mkMain(t, main)
	// Worktree reaches the main's common dir via a relative gitdir and an
	// absolute commondir routed through the symlink.
	wt := filepath.Join(scan, "wt")
	g := filepath.Join(main, ".git", "worktrees", "wt")
	require.NoError(t, os.MkdirAll(g, 0o755))
	require.NoError(t, os.MkdirAll(wt, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: ../main/.git/worktrees/wt\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(g, "commondir"), []byte("../..\n"), 0o644))
	require.Equal(t, []string{"main"}, discoverRels(t, scan))

	// Symlinked scan root: same checkout reached through a link still matches.
	scanLink := filepath.Join(link, "scan2")
	require.NoError(t, os.MkdirAll(filepath.Join(real, "scan2"), 0o755))
	m2 := filepath.Join(real, "scan2", "main")
	mkMain(t, m2)
	mkLinked(t, filepath.Join(real, "scan2", "wt"), m2, "gitdir: "+filepath.Join(scanLink, "main", ".git", "worktrees", "wt"), filepath.Join(scanLink, "main", ".git"))
	require.Equal(t, []string{"main"}, discoverRels(t, filepath.Join(real, "scan2")))
}

func TestDiscover_MalformedGitFileKept(t *testing.T) {
	dir := t.TempDir()
	mkMain(t, filepath.Join(dir, "main"))
	bad := filepath.Join(dir, "bad")
	require.NoError(t, os.MkdirAll(bad, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bad, ".git"), []byte("garbage\n"), 0o644))
	require.Equal(t, []string{"bad", "main"}, discoverRels(t, dir))
}

func TestDiscover_RealGitWorktree(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns git; skipped under -short")
	}
	dir := t.TempDir()
	main := filepath.Join(dir, "proj")
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = main
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	require.NoError(t, os.MkdirAll(main, 0o755))
	run("init")
	run("commit", "--allow-empty", "-m", "init")
	run("worktree", "add", filepath.Join(dir, "aaa-proj-wt"), "-b", "wt")
	require.Equal(t, []string{"proj"}, discoverRels(t, dir))

	_, linked, ok := gitIdentity(filepath.Join(dir, "aaa-proj-wt"))
	require.True(t, ok)
	require.True(t, linked)
}
