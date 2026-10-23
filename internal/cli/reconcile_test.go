package cli

import (
	"strings"
	"testing"
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
