package cli

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/reliant-labs/forge/internal/testcollect"
)

// collectDocker is the testcollect.Docker the docker CLI reaches.
type collectDocker struct{}

func (collectDocker) run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("docker %s: %s", args[0], msg)
		}
		return nil, fmt.Errorf("docker %s: %w", args[0], err)
	}
	return stdout.Bytes(), nil
}

// Containers finds the service's containers by the labels compose stamps on
// them, stopped ones included: a stopped container still holds what its
// process flushed at exit.
func (d collectDocker) Containers(ctx context.Context, c testcollect.ComposeTarget) ([]string, error) {
	args := []string{"ps", "-aq", "--no-trunc", "--filter", "label=com.docker.compose.service=" + c.Service}
	if c.WorkingDir != "" {
		args = append(args, "--filter", "label=com.docker.compose.project.working_dir="+c.WorkingDir)
	}
	if c.Project != "" {
		args = append(args, "--filter", "label=com.docker.compose.project="+c.Project)
	}
	out, err := d.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(out)), nil
}

// CopyOut is `docker cp <container>:<dir>/. -`: a tar of the directory's
// contents, read through the daemon, so the image needs no tar or shell (a
// distroless cover stage has neither), and a stopped container answers too.
func (d collectDocker) CopyOut(ctx context.Context, container, dir string) ([]byte, error) {
	out, err := d.run(ctx, "cp", container+":"+strings.TrimSuffix(dir, "/")+"/.", "-")
	if err != nil {
		if s := err.Error(); strings.Contains(s, "Could not find the file") || strings.Contains(s, "No such container:path") {
			return nil, testcollect.ErrNoSuchPath
		}
		return nil, err
	}
	return out, nil
}
