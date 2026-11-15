//go:build windows

package hostinfra

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestProcessAliveWindows(t *testing.T) {
	if !processAlive(os.Getpid()) {
		t.Fatal("self should be alive")
	}
	cmd := exec.Command("cmd", "/c", "exit", "0")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if processAlive(cmd.Process.Pid) {
		t.Fatal("exited child should not be alive")
	}
}

func TestIsExecutableFileWindows(t *testing.T) {
	dir := t.TempDir()
	for name, want := range map[string]bool{"a.exe": true, "a.txt": false} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		info, _ := os.Stat(p)
		if isExecutableFile(info) != want {
			t.Fatalf("%s: want %v", name, want)
		}
	}
}
