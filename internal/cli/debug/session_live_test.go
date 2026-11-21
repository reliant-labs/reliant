package debug

import (
	"context"
	"os/exec"
	"runtime"
	"testing"
	"time"

	dbgsvc "github.com/reliant-labs/forge/internal/debug"
)

// requireLiveDelve skips unless a real dlv can run here.
func requireLiveDelve(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("drives a real delve debugger against a built target; runs in task test")
	}
	if runtime.GOOS == "windows" {
		t.Skip("attach/signal model is unix-only")
	}
	if _, err := exec.LookPath("dlv"); err != nil {
		t.Skip("dlv not installed")
	}
}

// launchTarget runs targetProgram under a dlv server, running, as `start`
// would, and returns the debugger (unconnected).
func launchTarget(t *testing.T) *dbgsvc.DelveDebugger {
	t.Helper()
	d := dbgsvc.NewDelveDebugger()
	if err := d.Launch(context.Background(), dbgsvc.LaunchOptions{Binary: buildTarget(t)}); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	t.Cleanup(func() {
		if c := dbgsvc.NewDelveDebugger(); c.Connect(d.Addr()) == nil {
			_ = c.Stop()
		}
		reapDlv(context.Background(), &dbgsvc.SessionInfo{Addr: d.Addr(), DlvPID: d.DlvPID(), PID: d.PID(), Owned: true})
	})
	return d
}

func connectT(t *testing.T, addr string) *dbgsvc.DelveDebugger {
	t.Helper()
	d := dbgsvc.NewDelveDebugger()
	if err := d.Connect(addr); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(d.Disconnect)
	return d
}

func hitCount(t *testing.T, addr string) uint64 {
	t.Helper()
	d := connectT(t, addr)
	bps, err := d.ListBreakpoints()
	if err != nil {
		t.Fatalf("ListBreakpoints: %v", err)
	}
	var n uint64
	for _, bp := range bps {
		if bp.ID > 0 {
			n += bp.HitCount
		}
	}
	return n
}

func waitState(t *testing.T, addr string, want dbgsvc.TargetState) {
	t.Helper()
	d := connectT(t, addr)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if st, _ := d.State(); st == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("target never reached state %v", want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestContinueReportsUnobservedStop pins defect 6's other half: a target
// resumed by start reaches a breakpoint while nobody waits, and the next
// `continue` reports THAT stop instead of resuming past it. A later
// `continue` resumes to the next hit, and a bounded one times out with the
// target still running.
func TestContinueReportsUnobservedStop(t *testing.T) {
	requireLiveDelve(t)
	withWDT(t, t.TempDir())
	d := launchTarget(t)
	session := &dbgsvc.SessionInfo{Type: "delve", Addr: d.Addr(), PID: d.PID(), DlvPID: d.DlvPID(), Owned: true, Resumed: true}
	if err := debugSvc().SaveSession(".", session); err != nil {
		t.Fatal(err)
	}
	waitState(t, d.Addr(), dbgsvc.TargetRunning)
	if err := runDebugBreak([]string{"main.tick"}, "", "", false); err != nil {
		t.Fatalf("break on a running target: %v", err)
	}
	waitState(t, d.Addr(), dbgsvc.TargetHalted) // the loop hits it by itself

	if err := runContinue(5*time.Second, false, false); err != nil {
		t.Fatalf("continue: %v", err)
	}
	if n := hitCount(t, d.Addr()); n != 1 {
		t.Fatalf("continue resumed past the unobserved stop: %d hits, want 1", n)
	}
	if err := runContinue(5*time.Second, false, false); err != nil {
		t.Fatalf("second continue: %v", err)
	}
	if n := hitCount(t, d.Addr()); n != 2 {
		t.Fatalf("second continue: %d hits, want 2", n)
	}

	// Clear the breakpoint: nothing stops the target now, so a bounded
	// continue must give up, leaving it running.
	if err := runDebugClear(1); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if err := runContinue(500*time.Millisecond, false, false); err == nil {
		t.Fatal("continue with nothing to hit returned success")
	}
	waitState(t, d.Addr(), dbgsvc.TargetRunning)
	if s, _ := debugSvc().LoadSession("."); s == nil || !s.Resumed {
		t.Fatalf("timed-out continue must record the target as resumed: %+v", s)
	}
}

// TestConnectStopReleases: a dlv server forge did not start is handed back
// as found — breakpoints cleared, target running, server still up.
func TestConnectStopReleases(t *testing.T) {
	requireLiveDelve(t)
	withWDT(t, t.TempDir())
	d := launchTarget(t) // stands in for an env's delve runner
	waitState(t, d.Addr(), dbgsvc.TargetRunning)
	// Another client's breakpoint, set before forge connects. main.main is
	// past, so it never stops the target.
	external, err := connectT(t, d.Addr()).SetFunctionBreakpoint("main.main", "")
	if err != nil {
		t.Fatalf("external breakpoint: %v", err)
	}

	if err := runConnect(d.Addr(), false); err != nil {
		t.Fatalf("connect: %v", err)
	}
	s, _ := debugSvc().LoadSession(".")
	if s == nil || !s.Connected || s.Owned || s.PID != d.PID() {
		t.Fatalf("connect session = %+v, want connected, unowned, pid %d", s, d.PID())
	}
	if err := runDebugBreak([]string{"main.tick"}, "", "", false); err != nil {
		t.Fatalf("break: %v", err)
	}
	if err := runContinue(10*time.Second, false, false); err != nil {
		t.Fatalf("continue: %v", err)
	}
	// A repeated connect to the same server keeps the breakpoint this
	// session created (review B1): stop must still clear it.
	if err := runConnect(d.Addr(), false); err != nil {
		t.Fatalf("repeated connect: %v", err)
	}
	// As `forge debug stop` does: the session as break left it, naming the
	// breakpoint this session created.
	if s, _ = debugSvc().LoadSession("."); s == nil || len(s.BreakpointIDs) != 1 {
		t.Fatalf("session after break and a repeated connect = %+v, want one recorded breakpoint", s)
	}
	if err := runStop(context.Background(), s); err != nil {
		t.Fatalf("stop: %v", err)
	}

	waitState(t, d.Addr(), dbgsvc.TargetRunning) // the server answers: still up
	if n := hitCount(t, d.Addr()); n != 0 {
		t.Fatalf("stop left %d breakpoint hits' worth of breakpoints behind", n)
	}
	c := connectT(t, d.Addr())
	bps, err := c.ListBreakpoints()
	if err != nil {
		t.Fatal(err)
	}
	survived := false
	for _, bp := range bps {
		switch {
		case bp.ID == external.ID:
			survived = true
		case bp.ID > 0:
			t.Fatalf("breakpoint %d survived stop of a connected session", bp.ID)
		}
	}
	if !survived {
		t.Fatalf("stop cleared breakpoint %d, which another client set before forge connected", external.ID)
	}
}
