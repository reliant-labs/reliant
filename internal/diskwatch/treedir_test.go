// Copyright (c) 2025 Reliant Labs
//go:build unix

package diskwatch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// victimDir is a directory outside the tree being removed, 0500 so a chmod
// through a followed symlink shows, holding one file.
func victimDir(t *testing.T, base string) (dir, secret string) {
	t.Helper()
	dir = filepath.Join(base, "victim")
	secret = filepath.Join(dir, "secret")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secret, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	return dir, secret
}

func assertVictimUntouched(t *testing.T, dir, secret string) {
	t.Helper()
	if !exists(secret) {
		t.Fatal("a file outside the tree was removed through a symlink")
	}
	info, err := os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o500 {
		t.Fatalf("the symlink's target was chmodded to %o, want it left 0500", got)
	}
}

// G-2: a directory under a go-build work dir swapped for a symlink between
// the walk examining it and opening it — another user racing the trim in a
// shared /tmp — must not redirect the removal into the symlink's target.
func TestRemoveTreeDoesNotFollowASymlinkSwappedInMidWalk(t *testing.T) {
	base := t.TempDir()
	victim, secret := victimDir(t, base)
	tree := filepath.Join(base, "go-build1234")
	a := filepath.Join(tree, "b001")
	if err := os.MkdirAll(a, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a, "a.out"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(base, "moved")

	p := &pacer{rate: 1 << 20}
	swapped := false
	p.afterLstat = func(path string) {
		if path != a || swapped {
			return
		}
		swapped = true
		if err := os.Rename(a, moved); err != nil {
			t.Error(err)
		}
		if err := os.Symlink(victim, a); err != nil {
			t.Error(err)
		}
	}
	if _, err := p.removeTree(context.Background(), tree, os.Getuid()); err != nil {
		t.Fatalf("removeTree: %v", err)
	}
	if !swapped {
		t.Fatal("the swap hook never ran")
	}
	assertVictimUntouched(t, victim, secret)
	if exists(tree) {
		t.Fatal("the tree (and the symlink swapped into it) should be gone")
	}
}

// Normal cleanup: a read-only tree with a symlink out of it is removed
// entirely and its bytes counted; only the link — never its target — goes.
func TestRemoveTreeRemovesReadOnlyTreesAndOnlyTheLink(t *testing.T) {
	base := t.TempDir()
	victim, secret := victimDir(t, base)
	tree := filepath.Join(base, "mod@v1.0.0")
	for _, f := range []string{"go.mod", "pkg/x.go", "pkg/sub/y.go"} {
		path := filepath.Join(tree, f)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, make([]byte, 8192), 0o444); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(victim, filepath.Join(tree, "pkg", "link")); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"pkg/sub", "pkg", "."} {
		if err := os.Chmod(filepath.Join(tree, dir), 0o555); err != nil {
			t.Fatal(err)
		}
	}
	freed, err := (&pacer{rate: 1 << 20}).removeTree(context.Background(), tree, anyOwner)
	if err != nil {
		t.Fatalf("removeTree: %v", err)
	}
	if exists(tree) {
		t.Fatal("the tree was not removed")
	}
	if freed < 3*8192 {
		t.Fatalf("freed %d bytes, want at least the three files' %d", freed, 3*8192)
	}
	assertVictimUntouched(t, victim, secret)
}

// The owner check is made on the entry the walk holds, not only on the path
// trimGoWorkDirs listed.
func TestRemoveTreeRefusesAForeignOwner(t *testing.T) {
	tree := filepath.Join(t.TempDir(), "go-build1234")
	if err := os.MkdirAll(filepath.Join(tree, "b001"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := (&pacer{rate: 1 << 20}).removeTree(context.Background(), tree, os.Getuid()+1)
	if !errors.Is(err, errForeignOwner) {
		t.Fatalf("removeTree = %v, want the foreign owner refused", err)
	}
	if !exists(filepath.Join(tree, "b001")) {
		t.Fatal("a tree owned by another uid was removed")
	}
}

// The owner is checked on every descendant, not only the root: a directory
// another user owns inside this user's go-build tree is neither chmodded,
// traversed nor removed. Its owner is moved by the ownerOf seam, since a
// test cannot chown.
func TestRemoveTreeRefusesAForeignOwnedDescendant(t *testing.T) {
	tree := filepath.Join(t.TempDir(), "go-build1234")
	foreign := filepath.Join(tree, "b001", "theirs")
	if err := os.MkdirAll(foreign, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(foreign, "data"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(foreign, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(foreign, 0o700) })
	p := &pacer{rate: 1 << 20, ownerOf: func(path string, uid int) int {
		if path == foreign {
			return uid + 1
		}
		return uid
	}}
	_, err := p.removeTree(context.Background(), tree, os.Getuid())
	if !errors.Is(err, errForeignOwner) {
		t.Fatalf("removeTree = %v, want the foreign-owned descendant refused", err)
	}
	if !exists(filepath.Join(foreign, "data")) {
		t.Fatal("a descendant owned by another uid was removed")
	}
	info, err := os.Lstat(foreign)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o500 {
		t.Fatalf("the foreign-owned descendant was chmodded to %o, want it left 0500", got)
	}
}

// G-2: another user's stale go-build dir in a shared /tmp is skipped. The
// trim's uid is moved instead of the directory's owner, which a test cannot
// chown.
func TestTrimGoWorkDirs_SkipsAnotherUsersDirs(t *testing.T) {
	m := newTrimMachine(t, 100*GiB, CacheTrimConfig{})
	stale := filepath.Join(m.tmpdir, "go-build1234")
	old := m.now.Add(-13 * time.Hour)
	writeFile(t, filepath.Join(stale, "b001", "exe", "a.out"), 4096, old)
	for _, d := range []string{filepath.Join(stale, "b001", "exe"), filepath.Join(stale, "b001"), stale} {
		if err := os.Chtimes(d, old, old); err != nil {
			t.Fatal(err)
		}
	}
	m.w.mu.Lock()
	m.w.trim.uid = func() int { return os.Getuid() + 1 }
	m.w.mu.Unlock()

	m.w.TrimCaches(context.Background(), false)

	if !exists(filepath.Join(stale, "b001", "exe", "a.out")) {
		t.Fatal("a go-build dir owned by another uid was removed")
	}
}
