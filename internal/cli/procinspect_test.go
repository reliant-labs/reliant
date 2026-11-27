//go:build darwin || linux

package cli

import (
	"os/exec"
	"testing"
)

// TestReadProcArgvDeadPID pins the honest-failure half: an argv that cannot be
// read must report ok=false, never an empty-but-successful slice that would let
// attribution silently treat the process as a nameless match.
func TestReadProcArgvDeadPID(t *testing.T) {
	if argv, ok := readProcArgv(-1); ok || argv != nil {
		t.Errorf("readProcArgv(-1) = %q, %v; want nil, false", argv, ok)
	}
	// A pid that has exited: spawn and reap, then read.
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("run child: %v", err)
	}
	if _, ok := readProcArgv(cmd.Process.Pid); ok {
		t.Log("note: reaped pid still readable (pid reuse or kernel caching); not a correctness failure")
	}
}
