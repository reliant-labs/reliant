// Package otelcoltest runs the OpenTelemetry Collector forge pins, for the
// slow tests that prove a rendered collector config loads in it: the
// translator's (pkg/telemetry/collector) and the node agent's (pkg/deploy).
// One harness, so every config forge renders is checked against the same
// collector the same way.
package otelcoltest

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// Image is the collector forge pins (kcl/base.k OTEL_COLLECTOR_IMAGE).
const Image = "ghcr.io/open-telemetry/opentelemetry-collector-releases/opentelemetry-collector-contrib:0.162.0@sha256:39923a8e431bd1f57be82411999d389fcfe40857492e4365456d97a4c1f74be6"

// Collector runs the pinned collector: otelcol-contrib on PATH (it must be
// 0.162.0), else Image in docker.
type Collector struct {
	// Binary is the otelcol-contrib on PATH; empty runs Image in docker.
	Binary string
	// HostNetwork runs the image in the host's network namespace, for a
	// collector that must reach a server the test listens with on loopback.
	HostNetwork bool
}

// Find returns the collector to run, and skips t (naming both reasons) when
// there is neither a binary on PATH nor a usable docker.
func Find(t *testing.T) Collector {
	t.Helper()
	bin, lookErr := exec.LookPath("otelcol-contrib")
	if lookErr == nil {
		return Collector{Binary: bin}
	}
	if dockerErr := dockerUsable(); dockerErr != nil {
		t.Skipf("no collector to run: put an otelcol-contrib 0.162.0 binary on PATH (%v), or make docker usable (%v)", lookErr, dockerErr)
	}
	return Collector{}
}

// dockerUsable reports why docker cannot run Image, or nil.
func dockerUsable() error {
	if out, err := exec.Command("docker", "info").CombinedOutput(); err != nil {
		return fmt.Errorf("docker info: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Command is the collector invocation with args, and env (KEY=VALUE) added
// to its environment for a config that reads ${env:KEY}. Under docker
// (c.Binary empty) the container is named name, runs as the calling user,
// and has dir mounted at its own path, so a config in dir may name files in
// dir by their real paths.
func Command(ctx context.Context, c Collector, dir, name string, env []string, args ...string) *exec.Cmd {
	if c.Binary != "" {
		cmd := exec.CommandContext(ctx, c.Binary, args...)
		if len(env) > 0 {
			cmd.Env = append(os.Environ(), env...)
		}
		return cmd
	}
	full := []string{"run", "--rm", "--name", name, "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "-v", dir + ":" + dir}
	if c.HostNetwork {
		full = append(full, "--network", "host")
	}
	for _, e := range env {
		full = append(full, "-e", e)
	}
	full = append(append(full, Image), args...)
	return exec.CommandContext(ctx, "docker", full...)
}
