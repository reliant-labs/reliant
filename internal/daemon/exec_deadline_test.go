// Copyright (c) 2025 Reliant Labs
package daemon

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/cgroupmem"
)

// simulateMachineSleep lets a test reproduce what a suspended machine looks
// like to a running command: the wall clock moves on while the monotonic clock,
// which Go's timers run on, does not. A test cannot suspend the machine, but
// skewing ExecWallClock forward is exactly the observable state on wake — the
// monotonic timer has fired for none of the sleep and the wall clock for all of
// it.
//
// The returned func advances the wall clock by d without any monotonic time
// passing, i.e. "the machine slept for d".
func simulateMachineSleep(t *testing.T) (sleep func(d time.Duration)) {
	t.Helper()
	var skew atomic.Int64

	prevClock, prevInterval := ExecWallClock, ExecWallClockCheckInterval
	ExecWallClock = func() time.Time { return time.Now().Round(0).Add(time.Duration(skew.Load())) }
	ExecWallClockCheckInterval = 20 * time.Millisecond
	t.Cleanup(func() {
		ExecWallClock, ExecWallClockCheckInterval = prevClock, prevInterval
	})

	return func(d time.Duration) { skew.Add(int64(d)) }
}

// TestRunCommand_TimeoutHoldsAcrossMachineSleep is the regression test for
// the shell timeout "not being enforced": tool_calls rows whose command ran 53
// minutes against a 590s timeout, or 154 minutes against 120s, with the agent
// blocked on it. Every one of them spanned a machine sleep. The timeout was a
// context.WithTimeout, whose timer runs on the monotonic clock — which stops
// while the machine is asleep — so on wake the command carried on for the rest
// of its AWAKE budget instead of stopping, an hour after its deadline.
//
// Here the command's timeout is 20s and the machine "sleeps" for an hour 300ms
// in. Before the fix this returned after ~20s (the monotonic timeout) — or,
// for a real sleep, the whole residual budget after wake. After it, the wall
// clock is past the deadline at the first check after wake and the command is
// stopped within moments.
func TestRunCommand_TimeoutHoldsAcrossMachineSleep(t *testing.T) {
	sleep := simulateMachineSleep(t)
	go func() {
		time.Sleep(300 * time.Millisecond)
		sleep(time.Hour)
	}()

	start := time.Now()
	res, err := NewLocalClient().RunCommand(context.Background(), &RunCommandRequest{
		Command:    "sleep 30; echo finished",
		WorkingDir: t.TempDir(),
		TimeoutMs:  20_000,
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("RunCommand returned a transport error: %v", err)
	}

	if elapsed > 10*time.Second {
		t.Fatalf("RunCommand returned after %v: the machine slept past the command's 20s "+
			"timeout an hour ago, but the command ran on for its monotonic budget — the "+
			"timeout holds only on a clock that stops while the machine sleeps", elapsed)
	}
	if !res.TimedOut {
		t.Errorf("TimedOut = false, want true; result was %+v", res)
	}
	if strings.Contains(res.Stdout, "finished") {
		t.Errorf("Stdout = %q: the command ran to completion instead of being stopped", res.Stdout)
	}
	if !strings.Contains(res.Stderr, WallClockTimeoutMessage) {
		t.Errorf("Stderr = %q, want it to explain that the timeout elapsed while the machine "+
			"was asleep — the command's own runtime is far shorter than its timeout", res.Stderr)
	}
}

// The wall-clock check must not cut a command short when nothing slept. A
// comparison that mixed monotonic and wall readings, or a deadline computed
// from the wrong clock, would fire on the first tick.
func TestRunCommand_WallClockCheckDoesNotCutAwakeCommand(t *testing.T) {
	simulateMachineSleep(t) // fast checks, no skew

	res, err := NewLocalClient().RunCommand(context.Background(), &RunCommandRequest{
		Command:    "sleep 1; echo finished",
		WorkingDir: t.TempDir(),
		TimeoutMs:  10_000,
	})
	if err != nil {
		t.Fatalf("RunCommand returned a transport error: %v", err)
	}
	if res.TimedOut {
		t.Fatalf("TimedOut = true for a 1s command with a 10s timeout and no sleep; result was %+v", res)
	}
	if !strings.Contains(res.Stdout, "finished") || res.ExitCode != 0 {
		t.Errorf("result = %+v, want the command's real output and exit 0", res)
	}
}

func TestExecDeadline(t *testing.T) {
	t.Run("monotonic timeout stops the command as a deadline", func(t *testing.T) {
		d := StartExecDeadline(context.Background(), 50*time.Millisecond)
		defer d.Release()
		select {
		case <-d.Context().Done():
		case <-time.After(5 * time.Second):
			t.Fatal("context not done 5s after a 50ms timeout")
		}
		if !errors.Is(d.Err(), context.DeadlineExceeded) || errors.Is(d.Err(), ErrWallClockDeadline) {
			t.Errorf("Err() = %v, want a plain context.DeadlineExceeded", d.Err())
		}
	})

	t.Run("wall-clock timeout is a deadline too", func(t *testing.T) {
		sleep := simulateMachineSleep(t)
		d := StartExecDeadline(context.Background(), time.Hour)
		defer d.Release()
		sleep(2 * time.Hour)
		select {
		case <-d.Context().Done():
		case <-time.After(5 * time.Second):
			t.Fatal("context not done 5s after the wall clock passed the deadline")
		}
		if !errors.Is(d.Err(), ErrWallClockDeadline) {
			t.Errorf("Err() = %v, want ErrWallClockDeadline", d.Err())
		}
		if !errors.Is(d.Err(), context.DeadlineExceeded) {
			t.Errorf("Err() = %v, want it to wrap context.DeadlineExceeded so callers see a timeout", d.Err())
		}
	})

	t.Run("caller cancellation stops the command as a cancellation", func(t *testing.T) {
		parent, cancel := context.WithCancel(context.Background())
		d := StartExecDeadline(parent, time.Hour)
		defer d.Release()
		cancel()
		select {
		case <-d.Context().Done():
		case <-time.After(5 * time.Second):
			t.Fatal("context not done 5s after the caller cancelled")
		}
		if !errors.Is(d.Err(), context.Canceled) {
			t.Errorf("Err() = %v, want context.Canceled", d.Err())
		}
	})

	// Release is what adoption into the background manager calls: from then
	// on nothing — not the timeout, not the caller — may stop the command.
	t.Run("released deadline never stops the command", func(t *testing.T) {
		sleep := simulateMachineSleep(t)
		parent, cancel := context.WithCancel(context.Background())
		d := StartExecDeadline(parent, 50*time.Millisecond)
		d.Release()
		cancel()
		sleep(time.Hour)
		time.Sleep(200 * time.Millisecond)
		if err := d.Context().Err(); err != nil {
			t.Fatalf("released deadline's context is done (%v): an adopted command would be killed", err)
		}
		if d.Err() != nil {
			t.Errorf("Err() = %v, want nil for a command that was never stopped", d.Err())
		}
	})

	t.Run("context carries the caller's values", func(t *testing.T) {
		type key struct{}
		parent := context.WithValue(context.Background(), key{}, "v")
		d := StartExecDeadline(parent, time.Hour)
		defer d.Release()
		if got := d.Context().Value(key{}); got != "v" {
			t.Errorf("Value = %v, want the caller's value", got)
		}
	})
}

func TestClassifyExecOutcome_DeadlineStop(t *testing.T) {
	// os/exec reports a command stopped by ExecDeadline as context.Canceled,
	// because the deadline cancels with a cause. It is still a timeout and
	// must read as one.
	out := ClassifyExecOutcome(context.Canceled, context.DeadlineExceeded, "", nil, cgroupmem.OOMSnapshot{})
	if !out.TimedOut || out.ExitCode != TimeoutExitCode {
		t.Errorf("outcome = %+v, want a timeout with exit %d", out, TimeoutExitCode)
	}
	if out.Stderr != context.DeadlineExceeded.Error() {
		t.Errorf("Stderr = %q, want %q", out.Stderr, context.DeadlineExceeded.Error())
	}

	wall := ClassifyExecOutcome(context.Canceled, ErrWallClockDeadline, "partial", nil, cgroupmem.OOMSnapshot{})
	if !wall.TimedOut {
		t.Errorf("TimedOut = false for a wall-clock timeout")
	}
	if !strings.Contains(wall.Stderr, "partial") || !strings.Contains(wall.Stderr, WallClockTimeoutMessage) {
		t.Errorf("Stderr = %q, want the captured stderr plus the wall-clock explanation", wall.Stderr)
	}
}
