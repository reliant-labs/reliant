package cli

import (
	"context"
	"os/exec"
	"strings"

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
	projects, err := composeLabelsFn(ctx, dir, clickstack.Service)
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
