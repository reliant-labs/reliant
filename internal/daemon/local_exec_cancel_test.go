// Copyright (c) 2025 Reliant Labs
//go:build !windows

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This file covers the cancellation half of the exec path, as
// local_exec_waitdelay_test.go covers the drain half. The two knobs are
// separate by design (see ExecGraceDelay), and so are their tests: a change
// that collapses them back into one timer breaks one of these files.

// TestRunCommand_CancelledCommandRunsCleanup is the general fix, proved
// through the real exec path rather than the primitive: a cancelled command
// must get the chance to run its signal handler.
func TestRunCommand_CancelledCommandRunsCleanup(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "cleanup.txt")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan *CommandResult, 1)
	go func() {
		res, err := NewLocalClient().RunCommand(ctx, &RunCommandRequest{
			Command: `trap 'echo cleaned > ` + marker + `; exit 0' TERM
			          while true; do sleep 0.05; done`,
			WorkingDir: dir,
			TimeoutMs:  60_000,
		})
		if err != nil {
			t.Errorf("RunCommand: %v", err)
		}
		done <- res
	}()

	time.Sleep(500 * time.Millisecond) // let the trap install
	cancel()

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("RunCommand did not return after cancellation")
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(marker); err == nil && strings.TrimSpace(string(b)) == "cleaned" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("cancelled command did not run its SIGTERM handler; it was killed outright")
}

// TestRunCommand_CleanCancellationIsNotAFailure pins the regression in the
// opposite direction from the one being fixed.
//
// Setting cmd.Cancel makes os/exec report the context error from Wait even
// when the child exited 0. Without the classification added to
// ClassifyExecOutcome, a command that shut down cleanly on SIGTERM would come
// back as exit 1 with "context canceled" pasted onto its stderr — a failure
// that never happened.
func TestRunCommand_CleanCancellationIsNotAFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type result struct {
		res *CommandResult
		err error
	}
	done := make(chan result, 1)
	go func() {
		res, err := NewLocalClient().RunCommand(ctx, &RunCommandRequest{
			// Exits 0 promptly on SIGTERM: a well-behaved process being
			// cancelled, which is a success, not an error.
			Command:    `trap 'exit 0' TERM; while true; do sleep 0.05; done`,
			WorkingDir: t.TempDir(),
			TimeoutMs:  60_000,
		})
		done <- result{res, err}
	}()

	time.Sleep(500 * time.Millisecond)
	cancel()

	var got result
	select {
	case got = <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("RunCommand did not return after cancellation")
	}

	if got.err != nil {
		t.Fatalf("RunCommand err = %v, want nil", got.err)
	}
	if got.res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0 — a child that exited cleanly on SIGTERM was reported as a failure", got.res.ExitCode)
	}
	if strings.Contains(got.res.Stderr, "context canceled") {
		t.Errorf("Stderr = %q, want no invented error text for a clean shutdown", got.res.Stderr)
	}
	if got.res.TimedOut {
		t.Errorf("TimedOut = true for a cancellation that was not a deadline")
	}
}

// TestRunCommand_TimeoutStillReportedAsTimeout guards the exemption in that
// classification: a deadline must keep precedence, so a process that exits
// promptly on SIGTERM is still reported as timed out rather than silently
// downgraded to success.
func TestRunCommand_TimeoutStillReportedAsTimeout(t *testing.T) {
	res, err := NewLocalClient().RunCommand(context.Background(), &RunCommandRequest{
		Command:    `trap 'exit 0' TERM; while true; do sleep 0.05; done`,
		WorkingDir: t.TempDir(),
		TimeoutMs:  700,
	})
	if err != nil {
		t.Fatalf("RunCommand: %v", err)
	}
	if !res.TimedOut {
		t.Errorf("TimedOut = false, want true — the deadline must not be masked by a clean SIGTERM exit")
	}
	if res.ExitCode != TimeoutExitCode {
		t.Errorf("ExitCode = %d, want %d", res.ExitCode, TimeoutExitCode)
	}
}

// TestRunCommand_NormalCommandOutputNotTruncated is the guard on the knob
// split. Adding cmd.Cancel must not disturb the pipe drain, so a command that
// finishes normally still returns all of its output.
func TestRunCommand_NormalCommandOutputNotTruncated(t *testing.T) {
	res, err := NewLocalClient().RunCommand(context.Background(), &RunCommandRequest{
		Command:    `echo EARLY; sleep 0.3; echo LATE`,
		WorkingDir: t.TempDir(),
		TimeoutMs:  30_000,
	})
	if err != nil {
		t.Fatalf("RunCommand: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0; stderr = %q", res.ExitCode, res.Stderr)
	}
	for _, want := range []string{"EARLY", "LATE"} {
		if !strings.Contains(res.Stdout, want) {
			t.Errorf("Stdout = %q, missing %q — output was truncated", res.Stdout, want)
		}
	}
	if res.OutputIncomplete {
		t.Errorf("OutputIncomplete = true for a command that finished normally")
	}
}

// TestRunCommand_LingeringGrandchildKeepsFullOutput is the specific
// measurement the scope brief calls out: with a grandchild holding the pipe,
// a 10s drain must still collect the late write. It fails if someone
// "simplifies" the two knobs by lowering ExecWaitDelay to the grace value.
func TestRunCommand_LingeringGrandchildKeepsFullOutput(t *testing.T) {
	res, err := NewLocalClient().RunCommand(context.Background(), &RunCommandRequest{
		Command:    `echo EARLY; (sleep 2; echo LATE) & exit 0`,
		WorkingDir: t.TempDir(),
		TimeoutMs:  30_000,
	})
	if err != nil {
		t.Fatalf("RunCommand: %v", err)
	}
	for _, want := range []string{"EARLY", "LATE"} {
		if !strings.Contains(res.Stdout, want) {
			t.Errorf("Stdout = %q, missing %q — the post-exit drain was cut short; ExecWaitDelay must stay the drain bound and not be conflated with the cancellation grace period", res.Stdout, want)
		}
	}
}

// TestExecGraceDelay_ProductionValue guards the default the behavioural tests
// cannot see, mirroring TestExecWaitDelay_ProductionValue.
func TestExecGraceDelay_ProductionValue(t *testing.T) {
	if ExecGraceDelay <= 0 {
		t.Fatalf("ExecGraceDelay = %v — a non-positive grace means an immediate kill and no cleanup", ExecGraceDelay)
	}
	if ExecGraceDelay > ExecWaitDelay {
		t.Fatalf("ExecGraceDelay (%v) > ExecWaitDelay (%v): os/exec's own WaitDelay-driven kill would fire first and pre-empt the grace period",
			ExecGraceDelay, ExecWaitDelay)
	}
}
