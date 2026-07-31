// Copyright (c) 2025 Reliant Labs
package daemon

import (
	"context"
	"strings"
	"testing"
	"time"
)

// This file is the sibling of
// internal/toolexec/daemonruntime/cmd_exec_waitdelay_test.go. The two exec
// implementations — LocalClient.RunCommand here, the daemon's exec.run handler
// there — build their own *exec.Cmd, so a fix applied to one is invisible to
// the other. They have drifted before. Each has its own copy of these tests so
// that a path which forgets to bound its pipe drain hangs its own test.

func shortWaitDelay(t *testing.T) {
	t.Helper()
	prev := ExecWaitDelay
	ExecWaitDelay = time.Second
	t.Cleanup(func() { ExecWaitDelay = prev })
}

// lingeringChildCommand: the shell does its work and exits, but a process it
// spawned outlives it holding the inherited stdout pipe. Because cmd.Stdout is
// a bytes.Buffer, os/exec reads through an OS pipe and cmd.Wait() blocks until
// EOF — which waits for the LAST holder of the write end, not for bash.
const lingeringChildCommand = `echo "work done"; sleep 25 & exit 0`

func TestRunCommand_LingeringChildDoesNotHangWait(t *testing.T) {
	shortWaitDelay(t)

	start := time.Now()
	res, err := NewLocalClient().RunCommand(context.Background(), &RunCommandRequest{
		Command:    lingeringChildCommand,
		WorkingDir: t.TempDir(),
		TimeoutMs:  60_000,
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("RunCommand: %v", err)
	}

	if elapsed > 10*time.Second {
		t.Fatalf("RunCommand took %v for a command whose own work took milliseconds — "+
			"cmd.Wait() is still waiting on a grandchild that inherited the output pipe", elapsed)
	}
	if !res.OutputIncomplete {
		t.Errorf("OutputIncomplete = false, want true so the caller can tell output was cut short")
	}
	if !strings.Contains(res.Stderr, "output collection was cut off") {
		t.Errorf("Stderr = %q, want it to explain why collection stopped", res.Stderr)
	}
	if !strings.Contains(res.Combined, "output collection was cut off") {
		t.Errorf("Combined = %q, want it to carry the same explanation as Stderr", res.Combined)
	}
}

func TestRunCommand_LingeringChildKeepsOutputAndExitCode(t *testing.T) {
	shortWaitDelay(t)

	res, err := NewLocalClient().RunCommand(context.Background(), &RunCommandRequest{
		Command:    lingeringChildCommand,
		WorkingDir: t.TempDir(),
		TimeoutMs:  60_000,
	})
	if err != nil {
		t.Fatalf("RunCommand: %v", err)
	}

	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0 — the command itself exited 0 and a cut-off "+
			"pipe drain must not be reported as a command failure (stderr=%q)", res.ExitCode, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "work done") {
		t.Errorf("Stdout = %q, want the output written before the bound expired", res.Stdout)
	}
	if res.TimedOut {
		t.Errorf("TimedOut = true, want false — the command did not exceed its timeout")
	}
}

func TestRunCommand_TimeoutIsEnforcedDespiteLingeringChild(t *testing.T) {
	shortWaitDelay(t)

	start := time.Now()
	res, err := NewLocalClient().RunCommand(context.Background(), &RunCommandRequest{
		Command:    `echo "started"; sleep 25 & sleep 25`,
		WorkingDir: t.TempDir(),
		TimeoutMs:  2_000,
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("RunCommand: %v", err)
	}

	if elapsed > 10*time.Second {
		t.Fatalf("RunCommand took %v for a command with a 2s timeout — the timeout is "+
			"unenforceable while cmd.Wait() blocks on an orphaned child's pipe", elapsed)
	}
	if !res.TimedOut {
		t.Errorf("TimedOut = false, want true")
	}
	if res.ExitCode == 0 {
		t.Errorf("ExitCode = 0, want non-zero for a killed command")
	}
}

func TestRunCommand_SlowButWorkingCommandIsNotCut(t *testing.T) {
	shortWaitDelay(t) // 1s bound, 4s command

	res, err := NewLocalClient().RunCommand(context.Background(), &RunCommandRequest{
		Command:    `echo start; sleep 4; echo end`,
		WorkingDir: t.TempDir(),
		TimeoutMs:  60_000,
	})
	if err != nil {
		t.Fatalf("RunCommand: %v", err)
	}

	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0 (stderr=%q)", res.ExitCode, res.Stderr)
	}
	if res.OutputIncomplete {
		t.Errorf("OutputIncomplete = true for a command that merely ran longer than the bound")
	}
	if !strings.Contains(res.Stdout, "start") || !strings.Contains(res.Stdout, "end") {
		t.Errorf("Stdout = %q, want the complete output of a slow but healthy command", res.Stdout)
	}
	if strings.TrimSpace(res.Stderr) != "" {
		t.Errorf("Stderr = %q, want empty — nothing went wrong", res.Stderr)
	}
}
