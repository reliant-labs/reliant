package debug

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fixtureProgram is a tiny long-running Go program: a named function called in
// a loop, with a couple of locals and a background goroutine. It is the
// throwaway target the live Delve tests drive — a function breakpoint by name,
// stepping, locals/eval, and goroutine listing all exercise it.
const fixtureProgram = `package main

import (
	"fmt"
	"time"
)

func compute(i int) int {
	a := i * 2
	b := a + 7
	c := a + b
	return c
}

func main() {
	go func() {
		for {
			time.Sleep(20 * time.Millisecond)
		}
	}()
	for i := 0; ; i++ {
		n := compute(i)
		fmt.Println("computed", i, n)
		time.Sleep(50 * time.Millisecond)
	}
}
`

// buildFixture compiles fixtureProgram with debug flags (-N -l) and returns
// the absolute path to the resulting binary plus its main.go path. The test is
// skipped when go or dlv is unavailable.
func buildFixture(t *testing.T) (binary, source string) {
	t.Helper()
	return buildFixtureFlags(t)
}

// buildFixtureFlags is buildFixture with extra `go build` flags.
func buildFixtureFlags(t *testing.T, flags ...string) (binary, source string) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	if _, err := exec.LookPath("dlv"); err != nil {
		t.Skip("dlv (delve) not on PATH; install with: go install github.com/go-delve/delve/cmd/dlv@latest")
	}

	dir := t.TempDir()
	source = filepath.Join(dir, "main.go")
	if err := os.WriteFile(source, []byte(fixtureProgram), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module dbgfixture\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	binary = filepath.Join(dir, "fixture")
	args := append([]string{"build", "-gcflags=all=-N -l"}, flags...)
	build := exec.CommandContext(context.Background(), "go", append(args, "-o", binary, ".")...)
	build.Dir = dir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building fixture: %v\n%s", err, out)
	}
	return binary, source
}

// startLiveDebugger launches the fixture under a real Delve server and returns
// the connected debugger, cleaning it up (detach, never kill the test process)
// on test end.
func startLiveDebugger(t *testing.T) (*DelveDebugger, string) {
	t.Helper()
	binary, source := buildFixture(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	d := NewDelveDebugger()
	if err := d.Start(ctx, binary, nil, 0); err != nil {
		t.Fatalf("starting delve: %v", err)
	}
	// Start launches a detached, headless dlv and then disconnects; reconnect
	// an RPC client the way the CLI's connectToSession does.
	if err := d.Connect(d.Addr()); err != nil {
		t.Fatalf("connecting: %v", err)
	}
	t.Cleanup(func() {
		// Stop detaches + kills the dlv-launched fixture (forge owns it here).
		_ = d.Stop()
		// Belt-and-suspenders: reap the dlv server we spawned.
		if pid := d.DlvPID(); pid > 0 {
			if p, err := os.FindProcess(pid); err == nil {
				_ = p.Kill()
			}
		}
	})
	return d, source
}

// TestLive_FunctionBreakpointByName covers bug #1 at the engine layer: a
// breakpoint set by function NAME (not file:line) resolves and the breakpoint
// reports the right function. Both a user function and a stdlib function are
// exercised — Delve's location parser handles both.
func TestLive_FunctionBreakpointByName(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a real delve debugger against a built target; runs in task test")
	}
	d, _ := startLiveDebugger(t)

	bp, err := d.SetFunctionBreakpoint("main.compute", "")
	if err != nil {
		t.Fatalf("SetFunctionBreakpoint(main.compute): %v", err)
	}
	if bp.FunctionName != "main.compute" {
		t.Fatalf("breakpoint function = %q, want main.compute", bp.FunctionName)
	}
	if bp.Line == 0 {
		t.Fatalf("breakpoint resolved to line 0; expected a real line in compute")
	}

	// A stdlib function resolves too (the exact spec the bug report used).
	gopark, err := d.SetFunctionBreakpoint("runtime.gopark", "")
	if err != nil {
		t.Fatalf("SetFunctionBreakpoint(runtime.gopark): %v", err)
	}
	if gopark.FunctionName != "runtime.gopark" {
		t.Fatalf("breakpoint function = %q, want runtime.gopark", gopark.FunctionName)
	}
	// Clear it so it doesn't interfere with the continue below.
	if err := d.ClearBreakpoint(gopark.ID); err != nil {
		t.Fatalf("ClearBreakpoint: %v", err)
	}
}

// TestLive_Stepping covers bug #2: after hitting a function breakpoint, the
// step commands advance the current line, and locals/eval observe real values.
func TestLive_Stepping(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a real delve debugger against a built target; runs in task test")
	}
	d, source := startLiveDebugger(t)

	if _, err := d.SetFunctionBreakpoint("main.compute", ""); err != nil {
		t.Fatalf("SetFunctionBreakpoint: %v", err)
	}
	hit, err := d.Continue()
	if err != nil {
		t.Fatalf("Continue: %v", err)
	}
	if hit.Reason != "breakpoint" {
		t.Fatalf("stop reason = %q, want breakpoint", hit.Reason)
	}
	if filepath.Base(hit.File) != filepath.Base(source) || hit.Function != "main.compute" {
		t.Fatalf("stopped at %s:%d (%s), want main.compute in %s", hit.File, hit.Line, hit.Function, source)
	}
	entryLine := hit.Line

	// Step over advances the line within compute.
	s1, err := d.StepOver()
	if err != nil {
		t.Fatalf("StepOver: %v", err)
	}
	if s1.Line <= entryLine {
		t.Fatalf("StepOver did not advance: %d -> %d", entryLine, s1.Line)
	}

	// Step again, then a local that has executed should be observable.
	s2, err := d.StepOver()
	if err != nil {
		t.Fatalf("second StepOver: %v", err)
	}
	if s2.Line <= s1.Line {
		t.Fatalf("second StepOver did not advance: %d -> %d", s1.Line, s2.Line)
	}

	locals, err := d.Locals()
	if err != nil {
		t.Fatalf("Locals: %v", err)
	}
	if !hasVar(locals, "a") {
		t.Fatalf("expected local 'a' in scope after two steps, got %v", varNames(locals))
	}

	// Eval reads a real value.
	v, err := d.Eval("a")
	if err != nil {
		t.Fatalf("Eval(a): %v", err)
	}
	if v.Value == "" {
		t.Fatalf("Eval(a) returned empty value")
	}

	// StepOut returns out of compute (back into main, or a deeper frame).
	if _, err := d.StepOut(); err != nil {
		t.Fatalf("StepOut: %v", err)
	}
}

// TestLive_GoroutinesOnRunningTarget covers bug #3: listing goroutines on an
// attached/running target used to fail with "could not find goroutine array"
// (or block) because the target was never halted. Goroutines() now halts
// first, so the listing succeeds even when the target is actively running.
//
// A fresh `dlv exec` launch is halted at the program entry point, before any
// user goroutine exists; to reproduce the bug's preconditions we let the
// program reach its loop (continue to compute, so main and its background
// goroutine are live), then RESUME it into a running state and list while it
// runs — the exact scenario that previously hung/failed.
func TestLive_GoroutinesOnRunningTarget(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a real delve debugger against a built target; runs in task test")
	}
	d, _ := startLiveDebugger(t)

	bp, err := d.SetFunctionBreakpoint("main.compute", "")
	if err != nil {
		t.Fatalf("SetFunctionBreakpoint: %v", err)
	}
	if _, err := d.Continue(); err != nil {
		t.Fatalf("Continue: %v", err)
	}
	// Remove the breakpoint and resume so the target is genuinely RUNNING
	// when we ask for goroutines — Goroutines() must halt it itself.
	if err := d.ClearBreakpoint(bp.ID); err != nil {
		t.Fatalf("ClearBreakpoint: %v", err)
	}
	resumeIntoRunning(t, d)

	gs, err := d.Goroutines()
	if err != nil {
		t.Fatalf("Goroutines on running target: %v", err)
	}
	if len(gs) < 2 {
		t.Fatalf("expected >= 2 goroutines, got %d", len(gs))
	}
	// A recognizable user/runtime goroutine should be present.
	if !goroutineRunning(gs, "main.") && !goroutineRunning(gs, "runtime.") && !goroutineRunning(gs, "time.") {
		t.Fatalf("expected a recognizable goroutine function, got %v", goroutineFns(gs))
	}
}

// computeLine is the line of `a := i * 2` in fixtureProgram.
const computeLine = 9

// launchRunning launches binary under dlv the way `forge debug start` does
// by default — running — and connects to it.
func launchRunning(t *testing.T, binary, output string) *DelveDebugger {
	t.Helper()
	d := NewDelveDebugger()
	if err := d.Launch(context.Background(), LaunchOptions{Binary: binary, OutputPath: output}); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	// Registered before anything can fail: dlv outlives the test otherwise.
	t.Cleanup(func() {
		_ = d.Stop()
		if pid := d.DlvPID(); pid > 0 {
			if p, err := os.FindProcess(pid); err == nil {
				_ = p.Signal(syscall.SIGTERM) // dlv exec kills its target on SIGTERM
			}
		}
	})
	if err := d.Connect(d.Addr()); err != nil {
		t.Fatalf("connecting: %v", err)
	}
	return d
}

// waitRunning polls until the target is running (dlv --continue resumes it
// just after the server starts listening).
func waitRunning(t *testing.T, d *DelveDebugger) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		st, err := d.State()
		if err == nil && st == TargetRunning {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("target never ran: state=%v err=%v", st, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestLive_LaunchRunsAndBreaksWhileRunning: a launched target runs by
// default (its output reaches OutputPath), a breakpoint can be set while it
// runs — Delve's breakpoint RPCs would otherwise block until the next stop —
// the target keeps running afterwards, and ContinueFor delivers the hit.
func TestLive_LaunchRunsAndBreaksWhileRunning(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a real delve debugger against a built target; runs in task test")
	}
	binary, _ := buildFixture(t)
	output := filepath.Join(t.TempDir(), "logs", "out.log")
	d := launchRunning(t, binary, output)
	waitRunning(t, d)
	// It runs: its output reaches OutputPath.
	deadline := time.Now().Add(10 * time.Second)
	for {
		data, _ := os.ReadFile(output)
		if strings.Contains(string(data), "computed") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("program output never reached %s: %q", output, data)
		}
		time.Sleep(20 * time.Millisecond)
	}

	if _, err := d.SetFunctionBreakpoint("main.compute", ""); err != nil {
		t.Fatalf("SetFunctionBreakpoint on a running target: %v", err)
	}
	// The breakpoint set resumed the target, which may hit it at once:
	// wait without resuming, so a hit already reached is the one reported.
	hit, err := d.WaitForStop(10 * time.Second)
	if err != nil {
		t.Fatalf("WaitForStop: %v", err)
	}
	if hit.Reason != "breakpoint" || hit.Function != "main.compute" {
		t.Fatalf("stopped at %+v, want a breakpoint in main.compute", hit)
	}
}

// TestLive_ContinueForTimesOut: with nothing to stop it, ContinueFor returns
// ErrStillRunning at its deadline and the target is still running.
func TestLive_ContinueForTimesOut(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a real delve debugger against a built target; runs in task test")
	}
	binary, _ := buildFixture(t)
	d := launchRunning(t, binary, "")
	waitRunning(t, d)
	start := time.Now()
	if _, err := d.ContinueFor(300 * time.Millisecond); !errors.Is(err, ErrStillRunning) {
		t.Fatalf("ContinueFor with no breakpoint = %v, want ErrStillRunning", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("ContinueFor overran its timeout: %s", time.Since(start))
	}
	// A fresh connection still sees it running: the timeout left it alone.
	d2 := NewDelveDebugger()
	if err := d2.Connect(d.Addr()); err != nil {
		t.Fatal(err)
	}
	defer d2.Disconnect()
	if st, err := d2.State(); st != TargetRunning {
		t.Fatalf("after timeout state = %v (%v), want running", st, err)
	}
}

// TestLive_TrimpathLineBreakpoint: a binary built with -trimpath records
// `<module>/main.go`, not the absolute path, so the absolute spelling
// fails; SetLineBreakpoint falls through to the module-relative one.
func TestLive_TrimpathLineBreakpoint(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a real delve debugger against a built target; runs in task test")
	}
	binary, source := buildFixtureFlags(t, "-trimpath")
	d := launchRunning(t, binary, "")
	waitRunning(t, d)
	bp, err := d.SetLineBreakpoint([]string{source, "dbgfixture/main.go"}, "main.go", computeLine, "")
	if err != nil {
		t.Fatalf("SetLineBreakpoint on a -trimpath binary: %v", err)
	}
	if bp.Line != computeLine {
		t.Fatalf("breakpoint at line %d, want %d", bp.Line, computeLine)
	}
	// The suffix-matching location expression alone resolves it too.
	bp2, err := d.SetLineBreakpoint([]string{"/nonexistent/main.go"}, "main.go", computeLine+1, "")
	if err != nil {
		t.Fatalf("SetLineBreakpoint via location expression: %v", err)
	}
	if bp2.Line != computeLine+1 {
		t.Fatalf("breakpoint at line %d, want %d", bp2.Line, computeLine+1)
	}
}

// TestLive_StopEndsRunningTarget: Stop on a running owned target halts it
// first (Detach waits for Delve's target lock) and returns promptly.
func TestLive_StopEndsRunningTarget(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a real delve debugger against a built target; runs in task test")
	}
	binary, _ := buildFixture(t)
	d := launchRunning(t, binary, "")
	waitRunning(t, d)
	pid := d.PID()
	done := make(chan error, 1)
	go func() { done <- d.Stop() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Stop: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Stop hung on a running target")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if p, err := os.FindProcess(pid); err != nil || p.Signal(syscall.Signal(0)) != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("target pid %d survived Stop", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// resumeIntoRunning resumes the target asynchronously so it is observably
// running (not halted) when the next inspection call is made. The Continue
// channel is drained in a goroutine because, with no breakpoint set, it only
// returns when the program exits or is halted by Goroutines().
func resumeIntoRunning(t *testing.T, d *DelveDebugger) {
	t.Helper()
	go func() { _, _ = d.Continue() }()
	// Give the resume a moment to take effect so the target is genuinely
	// running when the caller proceeds.
	time.Sleep(150 * time.Millisecond)
}

// --- helpers ---

func hasVar(vars []Variable, name string) bool {
	for _, v := range vars {
		if v.Name == name {
			return true
		}
	}
	return false
}

func varNames(vars []Variable) []string {
	out := make([]string, len(vars))
	for i, v := range vars {
		out[i] = v.Name
	}
	return out
}

func goroutineRunning(gs []GoroutineInfo, fn string) bool {
	for _, g := range gs {
		if strings.Contains(g.Function, fn) {
			return true
		}
	}
	return false
}

func goroutineFns(gs []GoroutineInfo) []string {
	out := make([]string, len(gs))
	for i, g := range gs {
		out[i] = g.Function
	}
	return out
}
