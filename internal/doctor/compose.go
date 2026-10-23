package doctor

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// ComposeRunner runs `docker compose <args…>` in projectDir and returns its
// stdout. A failure's error carries docker's stderr, so a check can show what
// docker actually said.
//
// It is the ONE way doctor reaches docker. Every compose question — the infra
// check, port discovery, Delve, the in-container telemetry fallbacks — goes
// through it, so a caller decides whether the host's docker is touched at all.
// That matters beyond tidiness: `forge env status` runs these checks, and a
// test of that command on a shared machine must not read (or depend on) the
// developer's own compose stacks.
type ComposeRunner func(ctx context.Context, projectDir string, args ...string) ([]byte, error)

// DockerCompose is the real ComposeRunner: the host's `docker compose`.
func DockerCompose(ctx context.Context, projectDir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "docker", append([]string{"compose"}, args...)...)
	cmd.Dir = projectDir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return out, fmt.Errorf("%w: %s", err, msg)
		}
		return out, err
	}
	return out, nil
}

// compose runs a compose command through the injected runner, or the host's
// docker when none was injected.
func (e *Environment) compose(ctx context.Context, args ...string) ([]byte, error) {
	run := e.Compose
	if run == nil {
		run = DockerCompose
	}
	return run(ctx, e.ProjectDir, args...)
}
