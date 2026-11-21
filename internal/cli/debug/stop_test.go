package debug

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"sync"
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
			name:    "connected session releases (the dlv server is someone else's)",
			session: &dbgsvc.SessionInfo{PID: 4242, Connected: true},
			want:    actionRelease,
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

// targetProgram is a long-running Go program that calls tick() once every
// 20ms and prints a counter. It stands in for a live server forge did not
// launch: an attach session onto it must leave it running AND healthy.
const targetProgram = `package main

import (
	"fmt"
	"time"
)

//go:noinline
func tick(i int) int { return i + 1 }

func main() {
	for i := 0; ; i = tick(i) {
		fmt.Println("tick", i)
		time.Sleep(20 * time.Millisecond)
	}
}
`

// buildTarget compiles targetProgram and returns its path.
func buildTarget(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(targetProgram), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module dbgtarget\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "target")
	build := exec.CommandContext(context.Background(), "go", "build", "-o", bin, ".")
	build.Dir = dir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building target: %v\n%s", err, out)
	}
	return bin
}

// TestAttachStopLeavesTargetAlive is the end-to-end proof for the field bug:
// attach to a live process, set a breakpoint, run `stop`, and assert the
// target SURVIVES — including its next pass over the breakpoint address. The
// old stop only closed its connection and then killed dlv, leaving the int3
// in the target, which died of SIGTRAP on the next hit. Also: no orphan dlv
// remains and the session file is cleared.
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

	bin := buildTarget(t)
	out := &lockedBuffer{}
	target := exec.CommandContext(context.Background(), bin)
	target.Stdout = out
	if err := target.Start(); err != nil {
		t.Fatalf("starting target: %v", err)
	}
	targetPID := target.Process.Pid
	exited := make(chan error, 1)
	go func() { exited <- target.Wait() }()
	t.Cleanup(func() { _ = target.Process.Kill() })

	// Work in a temp dir so the session file (.forge/debug-session.json,
	// resolved relative to ".") is isolated.
	withWDT(t, t.TempDir())

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Attach exactly as `forge debug start --attach` does: the target keeps
	// running.
	if err := runDebugStartAttach(ctx, startOptions{attachPID: targetPID, run: true}); err != nil {
		t.Fatalf("attach: %v", err)
	}
	session, err := debugSvc().LoadSession(".")
	if err != nil || session == nil {
		t.Fatalf("LoadSession: %v %v", session, err)
	}
	if session.Owned || session.DlvPID == 0 {
		t.Fatalf("attach session = %+v, want unowned with a dlv pid", session)
	}
	waitForOutputGrowth(t, out, exited, "after attach (the target must not be left halted)")

	// A breakpoint on the hot loop, hit at least once.
	if err := runDebugBreak([]string{"main.tick"}, "", "", false); err != nil {
		t.Fatalf("break: %v", err)
	}
	if err := runContinue(10*time.Second, false, false); err != nil {
		t.Fatalf("continue to the breakpoint: %v", err)
	}

	// Stop with the breakpoint still set (and the target halted on it).
	if err := runStop(ctx, session); err != nil {
		t.Fatalf("runStop: %v", err)
	}

	// PROOF 1: the target keeps running — through many more tick() calls,
	// i.e. many passes over the address the breakpoint was on.
	waitForOutputGrowth(t, out, exited, "after stop")
	waitForOutputGrowth(t, out, exited, "after stop (second window)")

	// PROOF 2: no orphan dlv server remains.
	deadline := time.Now().Add(10 * time.Second)
	for processAlive(session.DlvPID) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if processAlive(session.DlvPID) {
		t.Fatalf("orphan dlv server (pid %d) survived stop — it must be reaped", session.DlvPID)
	}

	// PROOF 3: the session file is cleared.
	if _, err := os.Stat(filepath.Join(".forge", "debug-session.json")); !os.IsNotExist(err) {
		t.Fatalf("session file not cleared after stop (err=%v)", err)
	}
}

// waitForOutputGrowth fails unless the target prints more within a few
// seconds and has not exited.
func waitForOutputGrowth(t *testing.T, out *lockedBuffer, exited <-chan error, when string) {
	t.Helper()
	start := out.Len()
	deadline := time.Now().Add(5 * time.Second)
	for out.Len() < start+40 {
		select {
		case err := <-exited:
			t.Fatalf("REGRESSION: target exited %s: %v\noutput tail: %s", when, err, out.Tail(200))
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("target made no progress %s (output %d -> %d bytes)", when, start, out.Len())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// lockedBuffer is a goroutine-safe output sink.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len()
}

func (b *lockedBuffer) Tail(n int) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.buf.String()
	if len(s) > n {
		s = s[len(s)-n:]
	}
	return s
}

// withWDT changes into dir for the rest of the test.
func withWDT(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
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

// A repeated connect to the same server carries the breakpoints the replaced
// session created, so stop still clears them; a server now serving another
// target (restarted) holds none of them.
func TestCarriedBreakpoints(t *testing.T) {
	prev := &dbgsvc.SessionInfo{Connected: true, Addr: "127.0.0.1:2345", PID: 41, BreakpointIDs: []int{3, 5}}
	if got := carriedBreakpoints(prev, 41); !slices.Equal(got, []int{3, 5}) {
		t.Fatalf("same target: carried %v, want [3 5]", got)
	}
	if got := carriedBreakpoints(prev, 42); got != nil {
		t.Fatalf("restarted target: carried %v, want none", got)
	}
	if got := carriedBreakpoints(nil, 41); got != nil {
		t.Fatalf("first connect: carried %v, want none", got)
	}
}
