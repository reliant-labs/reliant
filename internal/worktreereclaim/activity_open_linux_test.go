//go:build linux

package worktreereclaim

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestRecoveryActivitySafeOpenRejectsSwappedSymlinkAndFIFO(t *testing.T) {
	for _, kind := range []string{"symlink", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "observed")
			if err := os.WriteFile(path, []byte("before"), 0600); err != nil {
				t.Fatal(err)
			}
			// Inventory has observed a regular file, then an injected adversary
			// replaces it before the kernel open. No sleep or filesystem watcher race.
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if !before.Mode().IsRegular() {
				t.Fatal("fixture")
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if kind == "symlink" {
				target := filepath.Join(t.TempDir(), "private")
				if err := os.WriteFile(target, []byte("not read"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := unix.Mkfifo(path, 0600); err != nil {
					t.Fatal(err)
				}
			}
			f, err := activityOpenRegular(path)
			if err == nil {
				f.Close()
				t.Fatal("replacement must fail before reading or blocking")
			}
		})
	}
}
func TestRecoveryActivitySafeOpenRejectsAncestorSymlink(t *testing.T) {
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "file"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "swapped-directory")
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}
	f, err := activityOpenRegular(filepath.Join(alias, "file"))
	if err == nil {
		f.Close()
		t.Fatal("ancestor symlink must never be followed")
	}
}
func TestRecoveryActivitySafeReadBoundedRegular(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("plain"), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := activityReadRegular(path, 5)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "plain" {
		t.Fatal("regular bytes changed")
	}
	if _, err := activityReadRegular(path, 4); err == nil {
		t.Fatal("budget must hold")
	}
}
