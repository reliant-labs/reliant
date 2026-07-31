// Copyright (c) 2025 Reliant Labs
package daemon

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/cgroupmem"
)

// realWaitDelayError produces the genuine error os/exec returns when a
// command's own process exits but a child it spawned keeps the output pipe
// open past WaitDelay, so the classification test works on a real error value
// rather than a hand-built stand-in.
func realWaitDelayError(t *testing.T) error {
	t.Helper()
	cmd := exec.Command("bash", "-c", `echo out; sleep 20 & exit 0`)
	cmd.WaitDelay = 500 * time.Millisecond
	var buf bytes.Buffer
	cmd.Stdout = &buf
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	err := cmd.Wait()
	if !errors.Is(err, exec.ErrWaitDelay) {
		t.Fatalf("want ErrWaitDelay, got %v", err)
	}
	return err
}

// TestClassifyExecOutcome_WaitDelay pins the rule that keeps the fix from
// inventing failures.
//
// os/exec surfaces ErrWaitDelay from Wait() only when the process "otherwise
// exited normally on its own" — a non-zero exit produces an *exec.ExitError,
// which takes precedence (see (*exec.Cmd).Wait). So ErrWaitDelay always means
// the command SUCCEEDED and only its output collection was cut short. Falling
// through to the generic non-ExitError branch would report exit 1 and paste
// "exec: WaitDelay expired before I/O complete" onto stderr, turning every
// successful command with a lingering child into a fabricated failure.
func TestClassifyExecOutcome_WaitDelay(t *testing.T) {
	var noSnap cgroupmem.OOMSnapshot
	err := realWaitDelayError(t)

	t.Run("reports success, not a fabricated failure", func(t *testing.T) {
		out := ClassifyExecOutcome(err, nil, "", nil, noSnap)
		if out.ExitCode != 0 {
			t.Errorf("ExitCode = %d, want 0 — the command itself exited 0", out.ExitCode)
		}
		if out.TimedOut {
			t.Errorf("TimedOut = true, want false")
		}
		if !out.OutputIncomplete {
			t.Errorf("OutputIncomplete = false, want true")
		}
	})

	t.Run("explains itself on stderr and hides the os/exec wording", func(t *testing.T) {
		out := ClassifyExecOutcome(err, nil, "", nil, noSnap)
		if !strings.Contains(out.Stderr, "output collection was cut off") {
			t.Errorf("Stderr = %q, want the actionable explanation", out.Stderr)
		}
		if strings.Contains(out.Stderr, "WaitDelay") {
			t.Errorf("Stderr = %q, must not leak the os/exec internal wording", out.Stderr)
		}
	})

	t.Run("keeps the command's own stderr", func(t *testing.T) {
		out := ClassifyExecOutcome(err, nil, "a real warning", nil, noSnap)
		if !strings.HasPrefix(out.Stderr, "a real warning") {
			t.Errorf("Stderr = %q, want the captured stderr kept ahead of the explanation", out.Stderr)
		}
	})

	t.Run("a timeout still wins", func(t *testing.T) {
		// If the deadline fired, the caller needs the timeout classification;
		// an incidental drain overrun must not downgrade it to success.
		out := ClassifyExecOutcome(err, context.DeadlineExceeded, "", nil, noSnap)
		if !out.TimedOut {
			t.Errorf("TimedOut = false, want true")
		}
		if out.ExitCode != TimeoutExitCode {
			t.Errorf("ExitCode = %d, want %d", out.ExitCode, TimeoutExitCode)
		}
	})
}

// TestExecWaitDelay_ProductionValue guards the one thing the behavioural tests
// cannot see: they shorten ExecWaitDelay, so a bad production default would
// still pass them.
func TestExecWaitDelay_ProductionValue(t *testing.T) {
	if ExecWaitDelay <= 0 {
		t.Fatalf("ExecWaitDelay = %v — a non-positive value disables the bound entirely", ExecWaitDelay)
	}
	// Must stay small against the 60s default command timeout, or the bound
	// eats a meaningful slice of the budget it is supposed to protect.
	if ExecWaitDelay > 15*time.Second {
		t.Fatalf("ExecWaitDelay = %v, want <= 15s", ExecWaitDelay)
	}
}
