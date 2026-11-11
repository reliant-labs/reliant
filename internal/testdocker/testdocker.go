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
func Unavailable(t testing.TB, format string, args ...any) {
	t.Helper()
	msg := fmt.Sprintf(format, args...)
	if os.Getenv("CI") != "" {
		t.Fatalf("%s (CI is set, so this is a failure, not a skip: the lane must run this test)", msg)
	}
	t.Skip(msg)
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
