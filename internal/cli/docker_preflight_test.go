package cli

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// withDockerProbe swaps the daemon probe seam for the duration of a test.
func withDockerProbe(t *testing.T, fn func(context.Context) (string, error)) {
	t.Helper()
	prev := dockerDaemonProbeFn
	dockerDaemonProbeFn = fn
	t.Cleanup(func() { dockerDaemonProbeFn = prev })
}

// withReachableDocker stubs the daemon probe to answer, for tests of the
// cluster phase that stub every k3d/kubectl seam and must not depend on a
// live Docker daemon either. reconcileDeclaredClusters probes the daemon
// before anything else, so without this those tests passed only on hosts
// that happened to run Docker — and failed on the Windows runner, whose
// docker CLI has no engine behind it.
func withReachableDocker(t *testing.T) {
	t.Helper()
	withDockerProbe(t, func(context.Context) (string, error) { return "28.0.1\n", nil })
}

// A daemon that answers is a clean pass with no error decoration.
func TestEnsureDockerDaemonHealthy(t *testing.T) {
	withDockerProbe(t, func(context.Context) (string, error) { return "28.0.1\n", nil })
	if err := ensureDockerDaemon(context.Background()); err != nil {
		t.Fatalf("expected nil for a reachable daemon, got %v", err)
	}
}

// A MISSING binary is the one case that earns an install hint. This is the
// distinction the old k3d error collapsed: it printed "install k3d" for every
// failure class, including a daemon outage on a host where k3d was present.
func TestEnsureDockerDaemonMissingBinary(t *testing.T) {
	withDockerProbe(t, func(context.Context) (string, error) {
		return "", &exec.Error{Name: "docker", Err: exec.ErrNotFound}
	})
	err := ensureDockerDaemon(context.Background())
	if err == nil {
		t.Fatal("expected an error when the docker binary is absent")
	}
	if !strings.Contains(err.Error(), "not on PATH") {
		t.Errorf("expected an install-oriented message, got %q", err)
	}
	// A missing binary must NOT be blamed on the daemon or suggest a restart.
	if strings.Contains(err.Error(), "Restart it") {
		t.Errorf("missing binary must not suggest restarting the daemon: %q", err)
	}
}

// An installed client talking to a dead daemon must surface the daemon's own
// words plus a restart hint — never an "install docker" misdiagnosis.
func TestEnsureDockerDaemonUnreachableSurfacesOutput(t *testing.T) {
	withDockerProbe(t, func(context.Context) (string, error) {
		return "\x1b[31mCannot connect to the Docker daemon at unix:///var/run/docker.sock.\x1b[0m",
			errors.New("exit status 1")
	})
	err := ensureDockerDaemon(context.Background())
	if err == nil {
		t.Fatal("expected an error for an unreachable daemon")
	}
	got := err.Error()
	if !strings.Contains(got, "Cannot connect to the Docker daemon") {
		t.Errorf("daemon output must be preserved verbatim, got %q", got)
	}
	// ANSI colour codes must be stripped: an error string no longer passes
	// through a terminal's colour handling and renders them as literal junk.
	if strings.Contains(got, "\x1b[") {
		t.Errorf("ANSI escapes must be stripped from the error, got %q", got)
	}
	if !strings.Contains(got, "hint:") {
		t.Errorf("expected an actionable hint, got %q", got)
	}
}

// A probe that outruns the preflight deadline reports "did not respond",
// which is a different (and more accurate) claim than "cannot reach".
func TestEnsureDockerDaemonTimeoutIsDistinct(t *testing.T) {
	withDockerProbe(t, func(ctx context.Context) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	})
	prev := dockerPreflightTimeout
	dockerPreflightTimeout = 20 * time.Millisecond
	t.Cleanup(func() { dockerPreflightTimeout = prev })
	err := ensureDockerDaemon(context.Background())
	if err == nil {
		t.Fatal("expected an error when the daemon never answers")
	}
	if !strings.Contains(err.Error(), "did not respond") {
		t.Errorf("expected a timeout-shaped message, got %q", err)
	}
}

// A CANCELLED parent context is the user interrupting (Ctrl-C), not a wedged
// daemon. Reporting it as "the daemon did not respond" would send the user
// chasing an outage they caused themselves.
func TestEnsureDockerDaemonParentCancelNotBlamedOnDaemon(t *testing.T) {
	withDockerProbe(t, func(ctx context.Context) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := ensureDockerDaemon(ctx)
	if err == nil {
		t.Fatal("expected an error when the caller cancels")
	}
	if strings.Contains(err.Error(), "did not respond within") {
		t.Errorf("a caller-cancelled probe must not be reported as a daemon timeout: %q", err)
	}
}

// Every supported platform gets a hint, and the desktop platforms name the
// VM-behind-the-UI failure mode that motivated this check.
func TestDockerRestartHintForOS(t *testing.T) {
	darwin := dockerRestartHintForOS("darwin")
	if !strings.Contains(darwin, "open -a Docker") {
		t.Errorf("darwin hint must give the relaunch command, got %q", darwin)
	}
	if !strings.Contains(darwin, "VM behind it") {
		t.Errorf("darwin hint should name the UI-alive/VM-dead mode, got %q", darwin)
	}
	if !strings.Contains(dockerRestartHintForOS("windows"), "Docker Desktop") {
		t.Error("windows hint should reference Docker Desktop")
	}
	if !strings.Contains(dockerRestartHintForOS("linux"), "systemctl") {
		t.Error("linux hint should give the systemd command")
	}
	// An unknown OS must still produce a non-empty, actionable hint.
	if strings.TrimSpace(dockerRestartHintForOS("plan9")) == "" {
		t.Error("unknown OS must still yield a hint")
	}
}

func TestFormatCommandStderr(t *testing.T) {
	if got := formatCommandStderr("   \n\t \n"); got != "" {
		t.Errorf("blank output must render as empty, got %q", got)
	}
	got := formatCommandStderr("\x1b[31mFATA\x1b[0m[0084] runtime failed\nsecond line")
	want := "\n  FATA[0084] runtime failed\n  second line"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
