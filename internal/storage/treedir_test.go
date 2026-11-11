//go:build unix

package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
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

// G-3: a directory swapped for a symlink between the walk examining it and
// opening it must not redirect the removal into the symlink's target.
func TestRemoveTreeDoesNotFollowASymlinkSwappedInMidWalk(t *testing.T) {
	base := t.TempDir()
	victim, secret := victimDir(t, base)
	tree := filepath.Join(base, "tree")
	a := filepath.Join(tree, "a")
	if err := os.MkdirAll(a, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Read-only, like a module cache directory, so the walk would make it
	// writable.
	if err := os.Chmod(a, 0o555); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(base, "moved")
	t.Cleanup(func() { _ = os.Chmod(moved, 0o700) })

	d := unpacedDeleter(nil)
	swapped := false
	d.pass.afterLstat = func(p string) {
		if p != a || swapped {
			return
		}
		swapped = true
		// Moving a directory to another parent rewrites its "..".
		if err := os.Chmod(a, 0o755); err != nil {
			t.Error(err)
		}
		if err := os.Rename(a, moved); err != nil {
			t.Error(err)
		}
		if err := os.Symlink(victim, a); err != nil {
			t.Error(err)
		}
	}
	if err := d.removeTree(context.Background(), tree); err != nil {
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

// Normal cleanup: a tree of read-only directories, files and a symlink out of
// the tree is removed entirely, and only the link — never its target — goes.
func TestRemoveTreeRemovesReadOnlyTreesAndOnlyTheLink(t *testing.T) {
	base := t.TempDir()
	victim, secret := victimDir(t, base)
	tree := filepath.Join(base, "mod@v1.0.0")
	for _, f := range []string{"go.mod", "pkg/x.go", "pkg/sub/y.go"} {
		p := filepath.Join(tree, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o444); err != nil {
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
	if err := unpacedDeleter(nil).removeTree(context.Background(), tree); err != nil {
		t.Fatalf("removeTree: %v", err)
	}
	if exists(tree) {
		t.Fatal("the tree was not removed")
	}
	assertVictimUntouched(t, victim, secret)
}

// The root itself may be a symlink: the link is removed, not the directory it
// names.
func TestRemoveTreeOfASymlinkRemovesTheLink(t *testing.T) {
	base := t.TempDir()
	victim, secret := victimDir(t, base)
	link := filepath.Join(base, "link")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatal(err)
	}
	if err := unpacedDeleter(nil).removeTree(context.Background(), link); err != nil {
		t.Fatalf("removeTree: %v", err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatalf("the link survives: %v", err)
	}
	assertVictimUntouched(t, victim, secret)
}
