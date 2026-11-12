package cli

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/clickstack"
)

// composeLabelsFn lists, one per line, the compose project of every container
// running service `svc` from the checkout `dir`. A seam so the choice below is
// testable without docker.
var composeLabelsFn = func(ctx context.Context, dir, svc string) ([]string, error) {
	out, err := exec.CommandContext(ctx, "docker", "ps",
		"--filter", "label=com.docker.compose.service="+svc,
		"--filter", "label=com.docker.compose.project.working_dir="+canonicalProjectDir(dir),
		"--format", `{{.Label "com.docker.compose.project"}}`).Output()
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(out)), nil
}

// dockerProbeTimeout bounds each docker call `forge obs` makes only to find
// the local ClickStack (the engine probe, the compose-project lookup). They
// answer in milliseconds when docker works; when its engine is missing or
// wedged the CLI can block indefinitely, and `forge obs status` must answer
// immediately either way. A var so a test can shrink it.
var dockerProbeTimeout = 5 * time.Second

// dockerEngineProbeFn checks that docker's engine answers, within
// dockerProbeTimeout, through the same `docker version` round trip the
// build preflight uses (dockerDaemonProbeFn) but on obs's short budget. A
// seam: unit tests answer for a host with no docker at all.
var dockerEngineProbeFn = probeDockerEngine

func probeDockerEngine(ctx context.Context) error {
	probeCtx, cancel := context.WithTimeout(ctx, dockerProbeTimeout)
	defer cancel()
	out, err := dockerDaemonProbeFn(probeCtx)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, exec.ErrNotFound):
		return fmt.Errorf("docker is not installed or not on PATH: %w", err)
	case errors.Is(probeCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil:
		return fmt.Errorf("docker's engine did not answer within %s (is a Docker daemon running? %s)", dockerProbeTimeout, dockerHostLabel())
	}
	return fmt.Errorf("docker's engine is not available: %w%s", err, formatCommandStderr(out))
}

// localClickstackComposeArgs are the `docker compose` arguments that select
// the compose project `forge env up` started env's ClickStack in. env up runs
// each env's compose services under its own project (composeSpecFor:
// forge-<project id>-<env>, so two worktrees and two envs never share a
// container). A bare `docker compose exec clickstack` from the project
// directory names the DEFAULT project, which env up never starts, and reports
// the service as not running while it is.
//
// The running container's own label is the truth (an author may name the
// project in OnCompose.env); with several, the env's default name wins. With
// none found, the default name: the exec then fails with compose's own "not
// running", which is what is true.
func localClickstackComposeArgs(ctx context.Context, dir, env string) []string {
	want := "forge-" + projectIDForDir(dir) + "-" + env
	lookupCtx, cancel := context.WithTimeout(ctx, dockerProbeTimeout)
	defer cancel()
	projects, err := composeLabelsFn(lookupCtx, dir, clickstack.Service)
	if err == nil {
		uniq := map[string]bool{}
		for _, p := range projects {
			uniq[p] = true
		}
		switch {
		case uniq[want]:
		case len(uniq) == 1:
			for p := range uniq {
				want = p
			}
		}
	}
	return []string{"-p", want}
}
