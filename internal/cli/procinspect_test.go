//go:build darwin || linux

package cli

import (
	"os/exec"
	"testing"
	"time"
)

// TestReadProcArgvRealProcess exercises the PLATFORM argv primitive against a
// real child process with a known command line. Duplicate attribution is only
// as good as this read: the darwin path parses the KERN_PROCARGS2 block by
// hand, so a fixture-only test would not catch an off-by-one that shifts argv
// against the exec path or the environment.
//
// The child is one this test spawns and kills itself — nothing else on the
// machine is signalled.
func TestReadProcArgvRealProcess(t *testing.T) {
	// `sleep` with two positional-looking args gives a stable, unmistakable
	// argv that no other process on the box shares.
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	// The kernel publishes the arg block at exec; give a slow machine a moment
	// rather than racing it.
	var argv []string
	var ok bool
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if argv, ok = readProcArgv(cmd.Process.Pid); ok {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ok {
		t.Fatalf("readProcArgv(%d) reported unreadable for a live same-uid child", cmd.Process.Pid)
	}
	if len(argv) != 2 {
		t.Fatalf("argv = %q, want 2 elements (the binary and \"30\")", argv)
	}
	// argv[0] is the program as invoked; the load-bearing part is that the
	// POSITIONAL argument survived at the right index — that is what command
	// attribution reads.
	if argv[1] != "30" {
		t.Errorf("argv[1] = %q, want \"30\" — argv is misaligned against the exec path/env block", argv[1])
	}
	if got := processCommand(argv); got != "sleep 30" {
		t.Errorf("processCommand(%q) = %q, want \"sleep 30\"", argv, got)
	}
}

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
