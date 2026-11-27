package cli

import (
	"context"
	"os"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestResolveUpLifecycle pins the pure TTY-aware lifecycle decision behind
// `forge env up` (Part B, the LLM-first fix): an agent / CI invocation with no
// TTY and no explicit flag must resolve to `once` (start + return), never
// `supervise` (the interactive Ctrl-C hold that would hang forever). The
// explicit --watch / --background flags override the TTY default, with
// --background the documented winner when both are somehow set.
//
// Pure-decision test style: inject isTTY and the two flags, assert the
// resolved lifecycle, no real stdin manipulation.
func TestResolveUpLifecycle(t *testing.T) {
	cases := []struct {
		name       string
		isTTY      bool
		watch      bool
		background bool
		want       reconcileLifecycle
	}{
		// The load-bearing case: no TTY, no flag -> once (don't hang an agent).
		{"no tty, no flags -> once (agent/CI)", false, false, false, lifecycleOnce},
		// TTY, no flag -> supervise (today's interactive behaviour).
		{"tty, no flags -> supervise (interactive)", true, false, false, lifecycleSupervise},
		// --watch forces supervise even without a TTY (human piping output).
		{"no tty, --watch -> supervise", false, true, false, lifecycleSupervise},
		{"tty, --watch -> supervise", true, true, false, lifecycleSupervise},
		// --background always detaches + returns, regardless of the TTY.
		{"tty, --background -> once", true, false, true, lifecycleOnce},
		{"no tty, --background -> once", false, false, true, lifecycleOnce},
		// Precedence: --background beats --watch when both are set.
		{"--background beats --watch (no tty)", false, true, true, lifecycleOnce},
		{"--background beats --watch (tty)", true, true, true, lifecycleOnce},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveUpLifecycle(tc.isTTY, tc.watch, tc.background)
			if got != tc.want {
				t.Errorf("resolveUpLifecycle(isTTY=%v, watch=%v, background=%v) = %v, want %v",
					tc.isTTY, tc.watch, tc.background, got, tc.want)
			}
		})
	}
}

// TestUpTeardownSignals pins the signals a supervising `forge env up` tears its
// stack down on. SIGTERM is how a harness or a supervisor stops a command, and
// SIGHUP is its terminal closing; catching only Ctrl-C left both to kill forge
// alone, orphaning children that run in process groups of their own.
func TestUpTeardownSignals(t *testing.T) {
	for _, want := range []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP} {
		if !slices.Contains(upTeardownSignals, want) {
			t.Errorf("upTeardownSignals = %v, missing %v", upTeardownSignals, want)
		}
	}
}

// TestProcRegistryStoppedAfterShutdown pins the guard that keeps the deferred
// startup teardown from stopping a stack the hold already stopped.
func TestProcRegistryStoppedAfterShutdown(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	p := newProcRegistry("proj-stopped", t.TempDir(), "dev")
	if p.stopped() {
		t.Fatal("a fresh registry reports stopped")
	}
	p.shutdown()
	if !p.stopped() {
		t.Fatal("registry does not report stopped after shutdown")
	}
}

// TestUpWatchFlagRegistered confirms `forge env up --watch` is wired with help
// text that names the force-supervise / no-TTY intent — the surface a human
// piping output relies on, and the documented counterpart to the non-TTY
// "return after start" default.
func TestUpWatchFlagRegistered(t *testing.T) {
	cmd := newEnvUpCmd()
	f := cmd.Flags().Lookup("watch")
	if f == nil {
		t.Fatal("--watch flag not registered on forge env up")
	}
	if f.DefValue != "false" {
		t.Errorf("--watch default: got %q, want false", f.DefValue)
	}
	if f.Value.Type() != "bool" {
		t.Errorf("--watch should be a bool flag, got %q", f.Value.Type())
	}
	// The help must point at the TTY-aware lifecycle so a reader of
	// `forge env up --help` understands when the hold happens vs the return.
	if !strings.Contains(strings.ToLower(f.Usage), "tty") {
		t.Errorf("--watch usage should explain the TTY-aware default, got %q", f.Usage)
	}
}

// TestUpWatchBackgroundMutuallyExclusive confirms --watch + --background is
// rejected at flag-parse time: they pull the lifecycle in opposite
// directions (hold vs detach), and a user passing both has a mistaken model.
func TestUpWatchBackgroundMutuallyExclusive(t *testing.T) {
	cmd := newEnvUpCmd()
	cmd.SetArgs([]string{"dev", "--watch", "--background"})
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected --watch + --background to be mutually exclusive")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("error should mention mutual exclusion, got %v", err)
	}
}

// TestTeardownSupervisedExitStopsChildrenOnAnOrdinaryError pins the exit a
// supervising `forge env up` takes when a startup step (the readiness gate,
// the cluster gate) returns an error after children started, with no signal:
// the children it owns stop with it, rather than outliving the command in
// process groups of their own. A run that started nothing stops nothing.
func TestTeardownSupervisedExitStopsChildrenOnAnOrdinaryError(t *testing.T) {
	if testing.Short() {
		t.Skip("starts and stops a real child process; runs in task test")
	}
	dir, projectID, env := testStack(t)
	t.Chdir(dir)

	idle := newProcRegistry(projectID, dir, env)
	teardownSupervisedExit(context.Background(), idle)
	if idle.stopped() {
		t.Error("a run that started nothing tore a stack down")
	}

	reg := newProcRegistry(projectID, dir, env)
	if err := reg.start("api", exec.Command("sleep", "60"), false); err != nil {
		t.Fatal(err)
	}
	mp := reg.processes[0]
	t.Cleanup(func() { _ = killProcessTree(mp.pid, syscall.SIGKILL) })

	teardownSupervisedExit(context.Background(), reg)
	if !reg.stopped() {
		t.Fatal("an error return after a child started did not tear the stack down")
	}
	select {
	case <-mp.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the child outlived the supervising run's error return")
	}
}

// TestTeardownSupervisedExitStopsChildrenOnASignalMidStartup pins the other
// early exit of a holding run (a TTY, or --watch): a signal that arrives
// while the stack is still starting cancels the run's context, and the
// children it already started stop with it instead of being orphaned in
// process groups of their own.
func TestTeardownSupervisedExitStopsChildrenOnASignalMidStartup(t *testing.T) {
	if testing.Short() {
		t.Skip("starts and stops a real child process; runs in task test")
	}
	dir, projectID, env := testStack(t)
	t.Chdir(dir)

	reg := newProcRegistry(projectID, dir, env)
	if err := reg.start("api", exec.Command("sleep", "60"), false); err != nil {
		t.Fatal(err)
	}
	mp := reg.processes[0]
	t.Cleanup(func() { _ = killProcessTree(mp.pid, syscall.SIGKILL) })

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // what signal.NotifyContext does on SIGTERM
	teardownSupervisedExit(ctx, reg)
	if !reg.stopped() {
		t.Fatal("a signal mid-startup did not tear the stack down")
	}
	select {
	case <-mp.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the child outlived the signal")
	}

	// The hold's own teardown already ran: a second one is a no-op.
	teardownSupervisedExit(ctx, reg)
}

// TestNonTTYWatchHint pins the line the non-TTY default prints after it
// returns with the stack running: it names --watch, the way an agent or
// daemon holds the stack under a process it tracks. An explicit --background
// asked for the detached stack and is told nothing more.
func TestNonTTYWatchHint(t *testing.T) {
	hint := nonTTYWatchHint("dev", false)
	if !strings.Contains(hint, "forge env up dev --watch") || strings.Contains(hint, "\n") {
		t.Errorf("the non-TTY return must print one line naming `forge env up dev --watch`, got %q", hint)
	}
	if got := nonTTYWatchHint("dev", true); got != "" {
		t.Errorf("--background must print no hint, got %q", got)
	}
}
