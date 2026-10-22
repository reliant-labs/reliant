// Copyright (c) 2025 Reliant Labs
package memtemp

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// deadPID is above Linux's pid_max ceiling (2^22), so no process has it.
const deadPID = 2147483646

// On a machine with a usable tmpfs the directory is in it, and an fsync there
// is immediate. Elsewhere it is an ordinary temp dir: the test still works.
func TestDirIsInMemoryWhereTheMachineHasTmpfs(t *testing.T) {
	dir := Dir(t)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("Dir returned %q, not a directory: %v", dir, err)
	}
	if !usable(root) {
		t.Skipf("%s is not a usable tmpfs here; Dir fell back to %s", root, dir)
	}
	if filepath.Dir(dir) != root {
		t.Fatalf("Dir = %q, want a directory in %s", dir, root)
	}
	f, err := os.Create(filepath.Join(dir, "f"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	start := time.Now()
	for range 50 {
		if _, err := f.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
		if err := f.Sync(); err != nil {
			t.Fatal(err)
		}
	}
	// 50 fsyncs on the shared disk took up to 40s under load; on tmpfs they
	// are microseconds. A second is two orders of magnitude of slack.
	if d := time.Since(start); d > time.Second {
		t.Errorf("50 fsyncs in %s took %s: not in memory", dir, d)
	}
}

// Dir's directory is gone once its test is.
func TestDirIsRemovedWithItsTest(t *testing.T) {
	var dir string
	t.Run("user", func(t *testing.T) { dir = Dir(t) })
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("%s outlived its test (stat: %v)", dir, err)
	}
}

// Reap removes what a dead test process left and nothing else: not a live
// process's directory, not one whose name is not Dir's, not a file.
func TestReapRemovesOnlyDeadProcessesDirectories(t *testing.T) {
	if alive(deadPID) {
		t.Skip("this platform cannot tell a dead pid from a live one; Dir never reaps here")
	}
	base := t.TempDir()
	mk := func(name string) string {
		p := filepath.Join(base, name)
		if err := os.MkdirAll(filepath.Join(p, "sub"), 0o700); err != nil {
			t.Fatal(err)
		}
		return p
	}
	dead := mk(prefix + strconv.Itoa(deadPID) + "-abc")
	live := mk(prefix + strconv.Itoa(os.Getpid()) + "-abc")
	foreign := mk("someone-elses-" + strconv.Itoa(deadPID) + "-abc")
	noPID := mk(prefix + "notapid-abc")
	file := filepath.Join(base, prefix+strconv.Itoa(deadPID)+"-file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if got := Reap(base); len(got) != 1 || got[0] != dead {
		t.Errorf("Reap removed %v, want only %s", got, dead)
	}
	for _, p := range []string{live, foreign, noPID, file} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was removed: %v", filepath.Base(p), err)
		}
	}
}
