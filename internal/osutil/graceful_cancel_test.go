// Copyright (c) 2025 Reliant Labs
//go:build !windows

package osutil

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// cleanupScript is a child that traps SIGTERM and records that its handler
// ran. It stands in for every real tool whose cleanup matters — git removing
// .git/index.lock is the case that motivated this — because the observable
// question is identical: did the process get a chance to run its handler, or
// was it killed outright?
//
// It creates ready only once the trap is installed, so a test can cancel on
// that condition rather than on a guess about how long bash takes to start.
func cleanupScript(marker, ready string) string {
	return `trap 'echo cleaned > ` + marker + `; exit 0' TERM
	 : > ` + ready + `
	 while true; do sleep 0.05; done`
}

// conditionTimeout bounds every wait for something that SHOULD happen. A
// passing run returns as soon as the condition holds, so the bound only costs
// time on a failure — which is why it is sized for a heavily loaded CI box
// rather than for the milliseconds these steps normally take.
const conditionTimeout = 30 * time.Second

// absenceWindow is how long a test watches for something that must NOT
// happen. Absence cannot be waited on as a condition, so this is the one
// fixed window in the file.
const absenceWindow = 2 * time.Second

// awaitTrapInstalled blocks until the child has created ready. Cancelling any
// earlier measures bash's startup, not the cancellation policy: a SIGTERM that
// lands before `trap` runs kills the shell with no handler installed at all.
func awaitTrapInstalled(t *testing.T, ready string) {
	t.Helper()
	deadline := time.Now().Add(conditionTimeout)
	for {
		if _, err := os.Stat(ready); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("child never reported its SIGTERM trap installed within %v", conditionTimeout)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitForMarker polls until marker holds want or within elapses, and returns
// what it last read.
//
// It waits for the CONTENT, not for the file. `echo cleaned > marker` is two
// steps — the shell creates the file, then writes to it — and a read that
// lands between them sees an empty file that is about to say "cleaned".
// Returning as soon as the file existed reported that as a lost cleanup:
// measured at load ~80, 5 of 192 runs read "" even though the trap was
// installed before the cancel, and every one of those files said "cleaned" a
// moment later.
func waitForMarker(t *testing.T, marker, want string, within time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(within)
	got := ""
	for {
		if b, err := os.ReadFile(marker); err == nil {
			got = strings.TrimSpace(string(b))
			if got == want {
				return got
			}
		}
		if !time.Now().Before(deadline) {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestApplyGracefulCancel_RunsCleanupHandler is the core proof of the whole
// change: a cancelled subprocess must get the chance to clean up.
//
// The subtests are a matched pair, and the "default" one is the regression
// proof rather than decoration — it exercises exactly the same script through
// os/exec's DEFAULT cancellation (Process.Kill) and asserts the cleanup does
// NOT happen. That is the behaviour every subprocess in this repository had
// before this change, so the pair demonstrates the fix rather than merely
// asserting the fixed state.
func TestApplyGracefulCancel_RunsCleanupHandler(t *testing.T) {
	tests := []struct {
		name        string
		graceful    bool
		wantCleanup bool
	}{
		{
			name:        "default cancellation SIGKILLs and loses the cleanup",
			graceful:    false,
			wantCleanup: false,
		},
		{
			name:        "graceful cancellation runs the SIGTERM handler",
			graceful:    true,
			wantCleanup: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			marker := filepath.Join(dir, "cleanup.txt")
			ready := filepath.Join(dir, "ready")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			cmd := exec.CommandContext(ctx, "bash", "-c", cleanupScript(marker, ready))
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			cmd.WaitDelay = 10 * time.Second
			if tc.graceful {
				stop := ApplyGracefulCancel(cmd, DefaultGraceDelay)
				defer stop()
			}

			if err := cmd.Start(); err != nil {
				t.Fatalf("Start: %v", err)
			}
			awaitTrapInstalled(t, ready)
			cancel()
			_ = cmd.Wait()

			within := conditionTimeout
			if !tc.wantCleanup {
				within = absenceWindow
			}
			got := waitForMarker(t, marker, "cleaned", within)
			if tc.wantCleanup && got != "cleaned" {
				t.Fatalf("cleanup marker = %q, want %q — the child was killed without running its SIGTERM handler", got, "cleaned")
			}
			if !tc.wantCleanup && got == "cleaned" {
				t.Fatalf("cleanup ran under DEFAULT cancellation; this test pins the pre-fix behaviour and it no longer reproduces")
			}
		})
	}
}

// TestApplyGracefulCancel_ReachesGrandchildren pins the process-group
// decision. `bash -c` forks for anything beyond a simple command, so the
// process doing the work — and holding git's lock — is often a grandchild.
// Signalling only the direct child would leave it untouched.
func TestApplyGracefulCancel_ReachesGrandchildren(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "grandchild.txt")
	ready := filepath.Join(dir, "ready")
	// ready is created INSIDE the subshell, after its trap: it proves the
	// grandchild exists and is armed, which the direct child starting does not.
	script := `( trap 'echo cleaned > ` + marker + `; exit 0' TERM
	            : > ` + ready + `
	            while true; do sleep 0.05; done ) &
	           wait`

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 10 * time.Second
	stop := ApplyGracefulCancel(cmd, DefaultGraceDelay)
	defer stop()

	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	awaitTrapInstalled(t, ready)
	cancel()
	_ = cmd.Wait()

	if got := waitForMarker(t, marker, "cleaned", conditionTimeout); got != "cleaned" {
		t.Fatalf("grandchild cleanup marker = %q, want %q — SIGTERM did not reach the process group", got, "cleaned")
	}
}

// TestApplyGracefulCancel_EscalatesToKill proves grace is a bound and not a
// promise: a child that ignores SIGTERM is still killed, so cancellation
// remains something that actually happens.
func TestApplyGracefulCancel_EscalatesToKill(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ready := filepath.Join(t.TempDir(), "ready")
	cmd := exec.CommandContext(ctx, "bash", "-c", `trap "" TERM; : > `+ready+`; while true; do sleep 0.05; done`)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// WaitDelay stays long so that anything observed here is OUR escalation
	// timer and not os/exec's WaitDelay-driven kill. That separation is the
	// point of having two knobs.
	cmd.WaitDelay = 60 * time.Second
	stop := ApplyGracefulCancel(cmd, 500*time.Millisecond)
	defer stop()

	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Until the trap ignores TERM, a SIGTERM simply kills the shell and this
	// test passes without the escalation ever being needed.
	awaitTrapInstalled(t, ready)

	start := time.Now()
	cancel()
	_ = cmd.Wait()
	elapsed := time.Since(start)

	if elapsed > 10*time.Second {
		t.Fatalf("Wait took %v for a SIGTERM-ignoring child; the escalation to kill did not fire", elapsed)
	}
}

// TestApplyGracefulCancel_CleanExitNotReportedAsFailure pins the regression in
// the opposite direction. os/exec reports the context error from Wait whenever
// Cancel ran, so a child that took the SIGTERM and exited 0 would be reported
// as a failure unless Cancel signals "already done" when there was nothing to
// cancel.
func TestApplyGracefulCancel_CleanExitNotReportedAsFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-c", `echo done; exit 0`)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 10 * time.Second
	stop := ApplyGracefulCancel(cmd, DefaultGraceDelay)
	defer stop()

	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("Wait err = %v, want nil for a command that completed before any cancellation", err)
	}
	if strings.TrimSpace(string(out)) != "done" {
		t.Fatalf("output = %q, want %q", string(out), "done")
	}

	// Cancelling AFTER the command finished must stay a no-op.
	cancel()
}

// TestApplyGracefulCancel_CancelAfterExitReportsProcessDone covers the
// ordering where the context is cancelled in the same instant the child exits
// on its own. Cancel must report ErrProcessDone so os/exec leaves the
// successful result alone.
func TestApplyGracefulCancel_CancelAfterExitReportsProcessDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", "exit 0")
	stop := ApplyGracefulCancel(cmd, DefaultGraceDelay)
	defer stop()

	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	// The child is reaped; Cancel now has nothing to signal.
	err := cmd.Cancel()
	if !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("Cancel() after exit = %v, want an error satisfying os.ErrProcessDone so a clean run is not reported as cancelled", err)
	}
}

// TestApplyGracefulCancel_NilProcessIsSafe: Cancel can be reached for a
// command that never started.
func TestApplyGracefulCancel_NilProcessIsSafe(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", "true")
	stop := ApplyGracefulCancel(cmd, DefaultGraceDelay)
	defer stop()

	if err := cmd.Cancel(); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("Cancel() on an unstarted command = %v, want os.ErrProcessDone", err)
	}
}

// TestApplyGracefulCancel_StopIsIdempotent: stop is meant to be deferred, and
// a double call must not panic on a closed channel.
func TestApplyGracefulCancel_StopIsIdempotent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", "true")
	stop := ApplyGracefulCancel(cmd, DefaultGraceDelay)
	stop()
	stop()
}

// TestApplyGracefulCancel_StopIsConcurrencySafe: the daemon exec path calls
// stop from whichever goroutine finishes the command, so a racing double call
// must neither race nor panic on a double close. Meaningful under -race.
func TestApplyGracefulCancel_StopIsConcurrencySafe(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", "true")
	stop := ApplyGracefulCancel(cmd, DefaultGraceDelay)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stop()
		}()
	}
	wg.Wait()
}

// TestApplyGracefulCancel_ZeroGraceUsesDefault: a caller passing no grace must
// get the package default rather than an immediate kill.
func TestApplyGracefulCancel_ZeroGraceUsesDefault(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "cleanup.txt")
	ready := filepath.Join(dir, "ready")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-c", cleanupScript(marker, ready))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 10 * time.Second
	stop := ApplyGracefulCancel(cmd, 0)
	defer stop()

	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	awaitTrapInstalled(t, ready)
	cancel()
	_ = cmd.Wait()

	if got := waitForMarker(t, marker, "cleaned", conditionTimeout); got != "cleaned" {
		t.Fatalf("cleanup marker = %q with grace=0; want the default grace to apply, not an immediate kill", got)
	}
}
