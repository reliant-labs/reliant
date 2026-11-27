// Copyright (c) 2026 Reliant Labs
package diskwatch

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fakeGoProc writes one process into a fake /proc: its comm, mount namespace
// and environment.
func fakeGoProc(t *testing.T, root, pid, comm, mntNS string, env ...string) {
	t.Helper()
	dir := filepath.Join(root, pid)
	if err := os.MkdirAll(filepath.Join(dir, "ns"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "comm"), []byte(comm+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(mntNS, filepath.Join(dir, "ns", "mnt")); err != nil {
		t.Fatal(err)
	}
	environ := strings.Join(env, "\x00") + "\x00"
	if err := os.WriteFile(filepath.Join(dir, "environ"), []byte(environ), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestScanGoCacheRoots: the GOCACHE of each running Go toolchain process in
// this mount namespace, once each, its default when unset; never another
// command's, and never a container's, whose path names a directory there.
func TestScanGoCacheRoots(t *testing.T) {
	root := t.TempDir()
	const self = "mnt:[4026531841]"
	if err := os.MkdirAll(filepath.Join(root, "self", "ns"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(self, filepath.Join(root, "self", "ns", "mnt")); err != nil {
		t.Fatal(err)
	}
	fakeGoProc(t, root, "10", "go", self, "HOME=/home/a", "GOCACHE=/w/one/.gocache")
	fakeGoProc(t, root, "11", "compile", self, "GOCACHE=/w/one/.gocache")
	fakeGoProc(t, root, "12", "link", self, "HOME=/home/b")
	fakeGoProc(t, root, "13", "golangci-lint", self, "GOCACHE=off", "HOME=/home/c")
	fakeGoProc(t, root, "14", "bash", self, "GOCACHE=/w/shell")
	fakeGoProc(t, root, "15", "go", "mnt:[4026532999]", "GOCACHE=/root/.cache/go-build")
	got := scanGoCacheRootsIn(root)
	want := []string{"/w/one/.gocache", "/home/b/.cache/go-build"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("caches = %q, want %q", got, want)
	}
}
