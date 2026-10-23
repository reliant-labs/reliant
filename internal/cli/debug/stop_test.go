package debug

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"testing"
	"time"

	dbgsvc "github.com/reliant-labs/forge/internal/debug"
)

// TestStopActionFor pins the safety invariant: only an OWNED session's target
// may be killed. An attach session (Owned=false) and a nil session MUST
// detach. This is the regression guard for the field bug where `forge debug
// stop` SIGKILL'd a live admin-server that had been attached via --attach.
func TestStopActionFor(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		session *dbgsvc.SessionInfo
		want    stopAction
	}{
		{
			name:    "attach session detaches (never kills the live target)",
			session: &dbgsvc.SessionInfo{PID: 4242, Owned: false},
			want:    actionDetach,
		},
		{
			name:    "owned start session kills its target",
			session: &dbgsvc.SessionInfo{PID: 4242, Owned: true},
			want:    actionKillTarget,
		},
		{
			name:    "docker session tears down the container",
			session: &dbgsvc.SessionInfo{Docker: true, Owned: true},
			want:    actionStopDocker,
		},
		{
			name:    "nil session is treated as not-owned (detach, never kill)",
			session: nil,
			want:    actionDetach,
		},
		{
			name:    "zero-value session (no Owned flag) detaches",
			session: &dbgsvc.SessionInfo{PID: 99},
			want:    actionDetach,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stopActionFor(tc.session); got != tc.want {
				t.Fatalf("stopActionFor(%+v) = %v, want %v", tc.session, got, tc.want)
			}
		})
	}
}

// TestAttachStopLeavesTargetAlive is the end-to-end proof: attach to a live
// process, run `stop`, and assert the target SURVIVES and no orphan dlv
// remains. This is the exact scenario that killed a live admin-server.
//
// Skips when dlv is not installed (CI images without delve) or on non-unix.
func TestAttachStopLeavesTargetAlive(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a real delve debugger against a built target; runs in task test")
	}
	if runtime.GOOS == "windows" {
		t.Skip("attach/signal model is unix-only")
	}
	if _, err := exec.LookPath("dlv"); err != nil {
		t.Skip("dlv not installed; skipping live attach/stop e2e")
	}

	// Launch a long-running target we own (a `sleep`). It stands in for the
	// live admin-server — forge did NOT launch it under dlv, so an attach
	// session onto it must never be killed by stop.
	target := exec.CommandContext(context.Background(), "sleep", "120")
	if err := target.Start(); err != nil {
		t.Fatalf("starting target: %v", err)
	}
	targetPID := target.Process.Pid
	t.Cleanup(func() {
		_ = target.Process.Kill()
		_, _ = target.Process.Wait()
	})

	// Work in a temp dir so the session file (.forge/debug-session.json,
	// resolved relative to ".") is isolated.
	origWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origWD) })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Attach the real Delve debugger to the live target.
	d := dbgsvc.NewDelveDebugger()
	if err := d.StartAttach(ctx, targetPID); err != nil {
		t.Fatalf("StartAttach(%d): %v", targetPID, err)
	}
	dlvPID := d.DlvPID()
	if dlvPID == 0 {
		t.Fatal("expected a non-zero dlv PID after StartAttach")
	}

	// Persist an ATTACH session exactly as runDebugStartAttach would.
	session := &dbgsvc.SessionInfo{
		Type:   "delve",
		Addr:   d.Addr(),
		PID:    targetPID,
		DlvPID: dlvPID,
		Binary: "pid:" + strconv.Itoa(targetPID),
		Owned:  false, // ATTACH — forge does not own the target.
	}
	if err := debugSvc().SaveSession(".", session); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	// Stop the session.
	if err := runStop(ctx, session); err != nil {
		t.Fatalf("runStop: %v", err)
	}

	// PROOF 1: the target process must still be alive.
	if !processAlive(targetPID) {
		t.Fatalf("REGRESSION: stop killed the attached target (pid %d) — it must survive", targetPID)
	}

	// PROOF 2: no orphan dlv server remains.
	deadline := time.Now().Add(5 * time.Second)
	for processAlive(dlvPID) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if processAlive(dlvPID) {
		t.Fatalf("orphan dlv server (pid %d) survived stop — it must be reaped", dlvPID)
	}

	// PROOF 3: the session file is cleared.
	if _, err := os.Stat(filepath.Join(".forge", "debug-session.json")); !os.IsNotExist(err) {
		t.Fatalf("session file not cleared after stop (err=%v)", err)
	}
}

// processAlive reports whether pid refers to a live (non-zombie) process.
//
// signal 0 probes existence, but a process we killed and whose handle we
// released (Process.Release, as the dlv server is) lingers as a zombie until
// its parent reaps it — and the real `forge` binary exits right after stop,
// letting init reap it. In-test, the test process is dlv's parent and never
// reaps, so we additionally consult `ps` state and treat a zombie (Z /
// <defunct>) as dead.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if err := p.Signal(syscall.Signal(0)); err != nil {
		return false // ESRCH: gone.
	}
	// Exists in the kernel — but a zombie is effectively dead. Check state.
	out, err := exec.CommandContext(context.Background(), "ps", "-o", "state=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		// ps failed/unavailable: fall back to the signal-0 result (alive).
		return true
	}
	state := string(out)
	// macOS uses 'Z', Linux 'Z'; both surface zombies. <defunct> is the
	// command-name marker some ps builds use.
	if len(state) > 0 && (state[0] == 'Z') {
		return false
	}
	return true
}
