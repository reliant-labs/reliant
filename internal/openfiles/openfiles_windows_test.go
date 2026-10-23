//go:build windows

package openfiles

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestWindowsHoldsOpenFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "sub", "held.txt")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := Take(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Holds(file) {
		t.Error("an open file was not reported held")
	}
	if !snap.Holds(dir) {
		t.Error("a directory containing an open file was not reported held")
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if snap.Holds(file) || snap.Holds(dir) {
		t.Error("a closed file is still reported held")
	}
	if snap.Holds(filepath.Join(dir, "missing")) {
		t.Error("a missing path was reported held")
	}
}

func TestWindowsHonoursTimeout(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	snap, err := Take(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if !snap.Holds(dir) {
		t.Error("a cancelled query must fail closed as held")
	}
}

// TestWindowsHoldsWorkingDirectory: a process whose working directory is
// inside the tree, with no file open, must be reported. The Restart Manager
// alone misses it, and os.RemoveAll would empty the tree around it.
func TestWindowsHoldsWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	cwd := filepath.Join(dir, "work")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	// ping itself, not `cmd /c ping`: Kill ends only the process it started,
	// and cmd's ping child would outlive it in the same working directory —
	// which the snapshot then (correctly) still reports as holding the tree.
	child := exec.Command("ping", "-n", "30", "127.0.0.1")
	child.Dir = cwd
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	time.Sleep(500 * time.Millisecond) // let it reach its working directory

	snap, err := Take(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Holds(dir) {
		t.Error("a tree containing another process's working directory was not reported held")
	}
	_ = child.Process.Kill()
	_ = child.Wait()
	if snap.Holds(dir) {
		t.Error("the tree is still reported held after the process exited")
	}
}
