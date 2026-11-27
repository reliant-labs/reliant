// Package testdocker publishes a container's port on a host port docker
// picks, for the smoke tests that run real containers.
//
// WHY. Picking a free port first (listen on :0, close, pass the number to
// `docker run -p 127.0.0.1:<port>:…`) is a race: between the close and
// docker's bind, anything else on the machine can take the port, and the run
// fails with "failed to bind host port 127.0.0.1:40003/tcp: address already
// in use" (control-plane's TestOtlpPublicRateLimitsAgainstEnvoy failed on
// main that way, control-plane#2085). Docker binding a port of its own choosing
// cannot collide. TestNoPrePickedDockerHostPorts keeps the old spelling out
// of forge's tests.
//
// Test-only by intent: nothing outside _test.go files imports it.
//
//forge:exclude-contract: test-only: publishes smoke-test containers' ports; imported only by _test.go files
package testdocker

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// Unavailable is how a smoke test says it cannot get the docker it needs (no
// daemon, an image that will not pull): a skip on a developer machine, a
// failure under CI (CI set, as every GitHub Actions runner sets it). A CI lane
// exists to run its smoke tests, and a skip there turns the lane green while
// it ran nothing — how pkg/obsquery's ClickHouse tests went unrun for days
// (forge#1870).
//
// A Reliant merge queue gate (RELIANT_GATE=1) is the exception, even though
// scripts/gate.sh sets CI too: a gate has no docker engine (no nested engine
// under runc), so the test skips with exactly [GateSkipMessage] and appends
// its name to the stage's skip log (RELIANT_GATE_DOCKER_SKIPS). The gate
// counts those lines into its verdict (reliant's
// gaterun.StageResult.SkippedDockerTests), so the skip is reported as a skip,
// never as a pass. A skip it cannot record (no log, an unwritable one) fails.
func Unavailable(t testing.TB, format string, args ...any) {
	t.Helper()
	msg := fmt.Sprintf(format, args...)
	if InGate() {
		gateSkip(t, msg)
	}
	if os.Getenv("CI") != "" {
		t.Fatalf("%s (CI is set, so this is a failure, not a skip: the lane must run this test)", msg)
	}
	t.Skip(msg)
}

// These equal reliant's gaterun.EnvDockerSkips and gaterun.DockerSkipMessage
// (internal/mergequeue/gaterun/wire.go), the contract its gate counts skips by.
const (
	envGate        = "RELIANT_GATE"
	envDockerSkips = "RELIANT_GATE_DOCKER_SKIPS"
	// GateSkipMessage is the whole skip message of a container test that
	// skipped in a Reliant gate.
	GateSkipMessage = "skip: docker unavailable (gate)"
)

// InGate reports whether this test runs in a Reliant merge queue gate
// (RELIANT_GATE=1, set by reliant's gaterun.GateEnv), which has no docker
// engine.
func InGate() bool {
	return os.Getenv(envGate) == "1"
}

// gateSkip records t in the stage's skip log and skips it with
// GateSkipMessage; why is logged on the line before. A skip the gate cannot
// count is a failure: the gate's stage script sets the log for every stage,
// so a missing log, or one that cannot be written, would otherwise let a
// container test go unrun without the verdict saying so.
func gateSkip(t testing.TB, why string) {
	t.Helper()
	log := os.Getenv(envDockerSkips)
	if log == "" {
		t.Fatalf("%s (RELIANT_GATE=1 but %s is not set, so this skip could not be counted in the gate's verdict: a failure, not a skip)", why, envDockerSkips)
	}
	if err := recordSkip(log, t.Name()); err != nil {
		t.Fatalf("%s (could not record the skip in %s: %v; an uncounted skip in a gate is a failure)", why, log, err)
	}
	t.Logf("%s", why)
	t.Skip(GateSkipMessage)
}

// recordSkip appends name to the skip log as one line, in one short O_APPEND
// write, so concurrent test processes cannot interleave inside a line.
func recordSkip(log, name string) error {
	f, err := os.OpenFile(log, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	_, werr := fmt.Fprintln(f, name)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

// Publish is the `docker run` flag pair that publishes containerPort on a
// loopback port docker picks. PublishedPort reads which one it picked.
func Publish(containerPort int) []string {
	return []string{"-p", "127.0.0.1::" + strconv.Itoa(containerPort)}
}

// PublishedPort is the loopback port docker published containerPort of
// container on (see Publish).
func PublishedPort(t testing.TB, container string, containerPort int) int {
	t.Helper()
	out, err := exec.Command("docker", "port", container, strconv.Itoa(containerPort)+"/tcp").CombinedOutput()
	if err != nil {
		t.Fatalf("docker port %s %d: %v\n%s", container, containerPort, err, out)
	}
	port, ok := parsePublishedPort(string(out))
	if !ok {
		t.Fatalf("docker port %s %d: no published port in %q", container, containerPort, out)
	}
	return port
}

// parsePublishedPort reads `docker port`'s answer: one "host:port" line per
// binding ("127.0.0.1:49153", or "[::1]:49153" beside it when IPv6 is bound).
func parsePublishedPort(out string) (int, bool) {
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if i := strings.LastIndex(line, ":"); i >= 0 {
			if port, err := strconv.Atoi(strings.TrimSpace(line[i+1:])); err == nil && port > 0 {
				return port, true
			}
		}
	}
	return 0, false
}
