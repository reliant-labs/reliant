package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/doctor"
)

// sourceDeclaration is reporting data, not a second resolver. Doctor never
// fetches sources or adopts a sibling checkout just to describe a declaration.
type sourceDeclaration struct {
	name   string
	path   string
	source *config.GitSource
}

func buildSourceDeclarations(cfg *config.ProjectConfig, entities *KCLEntities) []sourceDeclaration {
	var out []sourceDeclaration
	addContexts := func(prefix string, contexts map[string]config.BuildContext) {
		for _, name := range sortedKeys(contexts) {
			c := contexts[name]
			out = append(out, sourceDeclaration{name: prefix + name, path: c.Path, source: c.Source})
		}
	}
	if cfg != nil {
		addContexts("docker.build_contexts.", cfg.Docker.BuildContexts)
	}
	if entities == nil {
		return out
	}
	for _, fe := range entities.Frontends {
		out = append(out, sourceDeclaration{name: fmt.Sprintf("frontend %q", fe.Name), path: fe.Path, source: fe.Source})
	}
	for _, w := range entities.Workloads {
		if sh := w.Build.Shell; sh != nil {
			out = append(out, sourceDeclaration{name: "ShellBuild " + w.Name, path: sh.Cwd, source: sh.Source})
		}
		if d := w.Build.Docker; d != nil {
			addContexts("DockerBuild "+w.Name+".build_contexts.", d.BuildContexts)
		}
	}
	return out
}

func sourceDoctorCheck(cfg *config.ProjectConfig, entities *KCLEntities) doctor.CheckResult {
	result := doctor.CheckResult{Name: "sources: declarations", Status: doctor.StatusPass}
	var lines []string
	pinned, filesystem := 0, 0
	for _, d := range buildSourceDeclarations(cfg, entities) {
		if d.source != nil {
			if err := d.source.Validate(); err != nil {
				result.Status = doctor.StatusFail
				lines = append(lines, d.name+": "+err.Error())
				continue
			}
			pinned++
			lines = append(lines, d.name+": pinned "+toGitSource(d.source).String())
		} else {
			filesystem++
			path := d.path
			if path == "" {
				path = "."
			}
			lines = append(lines, d.name+": filesystem/BuildKit reference "+path+" (not a GitSource pin)")
		}
	}
	result.Message = fmt.Sprintf("%d GitSource pin(s), %d filesystem/BuildKit reference(s)", pinned, filesystem)
	result.Evidence = strings.Join(lines, "\n")
	return result
}

func runSourceDoctorChecks(ctx context.Context, cfg *config.ProjectConfig, projectDir, signal string) []doctor.CheckResult {
	if signal != "" {
		return nil
	}
	checks := []doctor.CheckResult{sourceDoctorCheck(cfg, nil)}
	envs, err := ListEnvs(projectDir)
	if err != nil {
		return append(checks, doctor.CheckResult{Name: "sources: environments", Status: doctor.StatusWarn, Message: err.Error()})
	}
	for _, env := range envs {
		entities, err := renderBuildKCL(ctx, projectDir, env)
		if err != nil {
			checks = append(checks, doctor.CheckResult{Name: "sources: " + env, Status: doctor.StatusWarn, Message: "cannot read source declarations: " + err.Error()})
			continue
		}
		check := sourceDoctorCheck(nil, entities)
		check.Name = "sources: " + env
		checks = append(checks, check)
	}
	return checks
}
