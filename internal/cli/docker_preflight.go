// Package cli — Docker daemon reachability preflight for the cluster phase.
//
// Every k3d operation IS a docker operation: k3d nodes are containers, the
// k3d registry is a container, the cluster network is a docker network. When
// the daemon is unreachable, each downstream shell-out fails on its own
// timeout with its own idiosyncratic wording — `k3d cluster list` reports
// "runtime failed to list nodes: ... 500 Internal Server Error", `docker
// build` reports something else again — and the user reads N cryptic
// cascading failures instead of one true cause.
//
// Docker reachability is a genuine PRECONDITION of the cluster phase and it
// is cheap to test, so it is tested up front. This mirrors the argument the
// doctor tool checks make in their own package doc: surface the gap before
// the failure cascades.
//
// The eager probe also SAVES wall-clock in the failure case. Docker Desktop's
// host-side apiproxy sits in front of a VM; when the VM is gone the proxy
// keeps dialing and only surfaces a 500 after its own ~20s deadline, once per
// call. One bounded probe replaces that repeated stall.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// dockerPreflightTimeout bounds the daemon probe. A warm, healthy daemon
// answers `docker version` in well under a second, but a healthy daemon on a
// LOADED machine does not: on a shared host at load ~100 it answered in 6s, 9s
// and 25s (forge report b8882151), and a 10s bound refused a deploy whose
// daemon was fine. A dead daemon does not cost this budget — a missing socket
// is refused at once, and Docker Desktop's proxy returns its own 500 after
// ~20s — so the bound is sized for a slow answer, not for a wedged one.
// FORGE_DOCKER_PREFLIGHT_TIMEOUT (a Go duration) overrides it.
// It is a var, not a const, so tests can shorten it.
var dockerPreflightTimeout = 45 * time.Second

// dockerPreflightTimeoutEnv names the override of dockerPreflightTimeout.
const dockerPreflightTimeoutEnv = "FORGE_DOCKER_PREFLIGHT_TIMEOUT"

// dockerPreflightBudget is the probe's bound: the env override when it parses
// as a positive duration, else dockerPreflightTimeout.
func dockerPreflightBudget() time.Duration {
	if v := strings.TrimSpace(os.Getenv(dockerPreflightTimeoutEnv)); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return dockerPreflightTimeout
}

// dockerDaemonProbeFn is the seam production wires to the real probe and
// tests replace, so preflight branches are exercised without a daemon.
var dockerDaemonProbeFn = probeDockerDaemon

// probeDockerDaemon asks the daemon for its own version — the smallest
// round-trip that proves the SERVER side is alive. `docker info` would also
// work but makes the daemon enumerate container/image state; `--format
// {{.Server.Version}}` fails fast and unambiguously when only the client is
// reachable, because the Server section is exactly what's missing then.
func probeDockerDaemon(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", "version", "--format", "{{.Server.Version}}")
	// A CLI plugin that inherited the output pipe must not hold the read
	// open past the deadline: the probe is bounded, not just the process.
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// ensureDockerDaemon reports whether the Docker daemon is reachable, turning
// the three distinguishable failures into three distinguishable messages:
// docker absent, daemon unreachable, daemon unresponsive. Returning a plain
// error (rather than printing) keeps the phase's error path single-exit.
func ensureDockerDaemon(ctx context.Context) error {
	budget := dockerPreflightBudget()
	probeCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	out, err := dockerDaemonProbeFn(probeCtx)
	if err == nil {
		return nil
	}

	if errors.Is(err, exec.ErrNotFound) {
		return fmt.Errorf("the docker CLI is not installed or not on PATH — "+
			"forge runs local clusters on Docker (install: https://docs.docker.com/get-docker/): %w", err)
	}

	// Distinguish "the daemon never answered" from "the daemon answered with
	// an error". Only the parent ctx being live tells us the deadline was
	// OURS; a cancelled parent means the user interrupted, not a wedged
	// daemon, and must not be reported as one.
	if errors.Is(probeCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		return fmt.Errorf("the Docker daemon did not respond within %s.%s%s"+
			"\n  hint: a healthy daemon on a heavily loaded machine can be this slow; allow it longer with %s=2m",
			budget, formatCommandStderr(out), dockerRestartHint(), dockerPreflightTimeoutEnv)
	}

	return fmt.Errorf("cannot reach the Docker daemon: %w%s%s",
		err, formatCommandStderr(out), dockerRestartHint())
}

// dockerRestartHint is the actionable tail appended to every daemon-side
// failure, resolved for the host OS.
func dockerRestartHint() string { return dockerRestartHintForOS(runtime.GOOS) }

// dockerRestartHintForOS is the pure core of the hint, split from the
// runtime.GOOS lookup so every platform branch is unit-testable on one host
// (the same shape doctor's installHintForOS uses).
//
// The hint is deliberately platform-shaped: on macOS and Windows the daemon
// lives in a VM behind Docker Desktop and "restart Docker Desktop" is the fix
// that actually works — including for the failure mode where the Desktop UI
// is running happily while the VM behind it is gone.
func dockerRestartHintForOS(goos string) string {
	switch goos {
	case "darwin":
		return "\n  hint: the Docker Desktop UI can be running while the Linux VM behind it is not." +
			"\n        Restart it:  osascript -e 'quit app \"Docker\"' && open -a Docker"
	case "windows":
		return "\n  hint: restart Docker Desktop, then retry."
	default:
		return "\n  hint: start the daemon:  sudo systemctl start docker"
	}
}

// ansiEscape matches the SGR color sequences k3d and docker wrap their log
// levels in. Left in place they render as literal `[31m` garbage inside a Go
// error string, which no longer passes through a terminal's color handling.
var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// formatCommandStderr renders a failed command's captured output as an
// indented block beneath the error line, or "" when there was nothing to
// show. Callers concatenate it directly, so the leading newline belongs here
// rather than at every call site.
func formatCommandStderr(out string) string {
	cleaned := strings.TrimSpace(ansiEscape.ReplaceAllString(out, ""))
	if cleaned == "" {
		return ""
	}
	var b strings.Builder
	for _, line := range strings.Split(cleaned, "\n") {
		b.WriteString("\n  ")
		b.WriteString(strings.TrimRight(line, " \t"))
	}
	return b.String()
}
