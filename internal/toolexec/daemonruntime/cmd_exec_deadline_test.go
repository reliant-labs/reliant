// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/daemon"
)

// TestHandleExecRun_TimeoutHoldsAcrossMachineSleep is the exec.run twin of
// daemon.TestRunCommand_TimeoutHoldsAcrossMachineSleep: a command's timeout
// must hold on the wall clock, because the monotonic clock Go's timers run on
// stops while the machine sleeps. The "sleep" is simulated by moving
// daemon.ExecWallClock an hour ahead 300ms into a command with a 20s timeout;
// that is the state a running command observes on wake.
func TestHandleExecRun_TimeoutHoldsAcrossMachineSleep(t *testing.T) {
	var skew atomic.Int64
	prevClock, prevInterval := daemon.ExecWallClock, daemon.ExecWallClockCheckInterval
	daemon.ExecWallClock = func() time.Time { return time.Now().Round(0).Add(time.Duration(skew.Load())) }
	daemon.ExecWallClockCheckInterval = 20 * time.Millisecond
	t.Cleanup(func() {
		daemon.ExecWallClock, daemon.ExecWallClockCheckInterval = prevClock, prevInterval
	})
	go func() {
		time.Sleep(300 * time.Millisecond)
		skew.Add(int64(time.Hour))
	}()

	start := time.Now()
	res := runExec(t, daemon.RunCommandRequest{
		Command:    "sleep 30; echo finished",
		WorkingDir: t.TempDir(),
		TimeoutMs:  20_000,
	})
	elapsed := time.Since(start)

	if elapsed > 10*time.Second {
		t.Fatalf("exec.run returned after %v: the machine slept past the command's 20s timeout "+
			"an hour ago, but the command ran on for its monotonic budget", elapsed)
	}
	if !res.TimedOut {
		t.Errorf("TimedOut = false, want true; result was %+v", res)
	}
	if strings.Contains(res.Stdout, "finished") {
		t.Errorf("Stdout = %q: the command ran to completion instead of being stopped", res.Stdout)
	}
	if !strings.Contains(res.Stderr, daemon.WallClockTimeoutMessage) {
		t.Errorf("Stderr = %q, want the wall-clock timeout explanation", res.Stderr)
	}
}
