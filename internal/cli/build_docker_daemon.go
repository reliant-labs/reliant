package cli

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/reliant-labs/forge/internal/cliutil"
)

// forge builds every container image with `docker build` (the project image,
// DockerBuild workloads, frontend images), so a build that makes one needs a
// Docker daemon. requireImageDaemon checks once, before the first image, and
// fails with one error instead of docker's own "failed to connect to the
// docker API" from every build in flight.

// dockerDaemonOnce caches the answer for the process: a build asks for every
// image it makes, and a probe that waits out a dead Docker Desktop proxy costs
// seconds each time.
var (
	dockerDaemonOnce   sync.Once
	dockerDaemonResult error
)

// dockerDaemonReachable is a seam so tests state the answer instead of probing
// the host (the suite's tripwire fails any test that reaches the real docker).
var dockerDaemonReachable = func(ctx context.Context) error {
	dockerDaemonOnce.Do(func() { dockerDaemonResult = ensureDockerDaemon(ctx) })
	return dockerDaemonResult
}

// requireImageDaemon is the guard in front of every `docker build` forge
// runs. what names the image for the error.
func requireImageDaemon(ctx context.Context, what string) error {
	err := dockerDaemonReachable(ctx)
	if err == nil {
		return nil
	}
	return cliutil.UserErr("forge build",
		fmt.Sprintf("no Docker daemon answered (%s); forge builds images with docker, and %s needs one", dockerHostLabel(), what),
		"",
		"start Docker (or point DOCKER_HOST / DOCKER_CONTEXT at a running daemon), then re-run.\n  "+firstLine(err.Error()))
}

// dockerHostLabel names the daemon docker would have dialed.
func dockerHostLabel() string {
	if h := os.Getenv("DOCKER_HOST"); h != "" {
		return "DOCKER_HOST=" + h
	}
	if c := os.Getenv("DOCKER_CONTEXT"); c != "" {
		return "docker context " + c
	}
	return "the current docker context"
}
