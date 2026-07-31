// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/daemon"
)

// shortWaitDelay shrinks the production pipe-drain bound so these tests finish
// in about a second instead of ten. It does NOT change what is being tested:
// the assertions are all "bounded well below the lingering child's lifetime",
// which is false for any value when the bound is missing entirely.
func shortWaitDelay(t *testing.T) {
	t.Helper()
	prev := daemon.ExecWaitDelay
	daemon.ExecWaitDelay = time.Second
	t.Cleanup(func() { daemon.ExecWaitDelay = prev })
}

// lingeringChildCommand is the shape that hung the green gate for 608 seconds:
// the shell finishes its work and exits, but a process it spawned outlives it
// still holding the inherited stdout pipe. `next build` workers are this shape.
// Because cmd.Stdout is a bytes.Buffer, os/exec reads the command's output
// through an OS pipe and cmd.Wait() blocks until EOF — which does not arrive
// until the LAST holder of the write end exits, not when bash does.
const lingeringChildCommand = `echo "work done"; sleep 25 & exit 0`

// TestHandleExecRun_LingeringChildDoesNotHangWait is the regression test for
// the hang. Before the fix this returned after ~25s (the grandchild's
// lifetime); the command's own work took milliseconds.
func TestHandleExecRun_LingeringChildDoesNotHangWait(t *testing.T) {
	shortWaitDelay(t)

	start := time.Now()
	res := runExec(t, daemon.RunCommandRequest{
		Command:    lingeringChildCommand,
		WorkingDir: t.TempDir(),
		TimeoutMs:  60_000,
	})
	elapsed := time.Since(start)

	if elapsed > 10*time.Second {
		t.Fatalf("exec.run took %v for a command whose own work took milliseconds — "+
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

// TestHandleExecRun_LingeringChildKeepsOutputAndExitCode pins what the bound
// costs. Cutting the drain short must not lose output already written, and
// must not invent a failure: the command really did succeed.
func TestHandleExecRun_LingeringChildKeepsOutputAndExitCode(t *testing.T) {
	shortWaitDelay(t)

	res := runExec(t, daemon.RunCommandRequest{
		Command:    lingeringChildCommand,
		WorkingDir: t.TempDir(),
		TimeoutMs:  60_000,
	})

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

// TestHandleExecRun_TimeoutIsEnforcedDespiteLingeringChild covers the broader
// instance of the same bug: without the bound, SIGKILLing the shell on timeout
// orphans its live children, which keep the pipe open — so TimeoutMs did not
// actually bound anything. Here the timeout is 2s and the child lives 25s.
func TestHandleExecRun_TimeoutIsEnforcedDespiteLingeringChild(t *testing.T) {
	shortWaitDelay(t)

	start := time.Now()
	res := runExec(t, daemon.RunCommandRequest{
		Command:    `echo "started"; sleep 25 & sleep 25`,
		WorkingDir: t.TempDir(),
		TimeoutMs:  2_000,
	})
	elapsed := time.Since(start)

	if elapsed > 10*time.Second {
		t.Fatalf("exec.run took %v for a command with a 2s timeout — the timeout is "+
			"unenforceable while cmd.Wait() blocks on an orphaned child's pipe", elapsed)
	}
	if !res.TimedOut {
		t.Errorf("TimedOut = false, want true")
	}
	if res.ExitCode == 0 {
		t.Errorf("ExitCode = 0, want non-zero for a killed command")
	}
}

// TestHandleExecRun_SlowButWorkingCommandIsNotCut is the regression guard that
// matters most: the bound must never truncate a command that is simply slow.
// It cannot, structurally — WaitDelay's clock starts only once the process has
// exited or the context is done — and this pins that with a command that runs
// for several multiples of the bound and still returns complete output.
func TestHandleExecRun_SlowButWorkingCommandIsNotCut(t *testing.T) {
	shortWaitDelay(t) // 1s bound, 4s command

	res := runExec(t, daemon.RunCommandRequest{
		Command:    `echo start; sleep 4; echo end`,
		WorkingDir: t.TempDir(),
		TimeoutMs:  60_000,
	})

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

// TestHandleExecRun_DurationIsReported pins that the daemon measures and
// returns the command's own runtime, which is the number the shell tool
// compares against its wall clock.
func TestHandleExecRun_DurationIsReported(t *testing.T) {
	res := runExec(t, daemon.RunCommandRequest{
		Command:    `sleep 1`,
		WorkingDir: t.TempDir(),
		TimeoutMs:  60_000,
	})
	if res.DurationMs < 900 {
		t.Errorf("DurationMs = %d, want >= 900 for a 1s command", res.DurationMs)
	}
}
