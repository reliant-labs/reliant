//go:build windows

package pgtest

import (
	"os"
	"os/exec"
	"testing"
)

func TestProcessAliveWindows(t *testing.T) {
	if !processAlive(os.Getpid()) {
		t.Fatal("self reported dead")
	}
	cmd := exec.Command("cmd", "/c", "exit 0")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if processAlive(cmd.Process.Pid) {
		t.Fatal("exited child reported alive")
	}
}
