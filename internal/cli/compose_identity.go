package cli

import (
	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/internal/devstack"
)

// composeSpecFor is shared by launch and discovery. An env's project name is
// an exact checkout identity, not a directory basename (nested worktrees all
// have the same basename). Explicit project names remain the author's choice.
func composeSpecFor(projectDir, env string, cm *ComposeRuntime) *deploytarget.ComposeSpec {
	spec := &deploytarget.ComposeSpec{ComposeFile: cm.File, Service: cm.Service, EnvFile: cm.EnvFile, Wait: cm.Wait, WaitTimeoutSeconds: cm.WaitTimeout, Shared: cm.Shared, ProjectDirectory: projectDir}
	spec.Env = make(map[string]string, len(cm.Env)+1)
	for k, v := range cm.Env {
		spec.Env[k] = v
	}
	if cm.Shared {
		spec.ProjectDirectory = devstack.SharedProjectDir(projectDir)
	} else if spec.Env["COMPOSE_PROJECT_NAME"] == "" {
		spec.Env["COMPOSE_PROJECT_NAME"] = "forge-" + projectIDForDir(projectDir) + "-" + env
	}
	return spec
}

// withComposeFileEnv adds to spec.Env what every OTHER workload on the same
// compose file declares, under the workload's own entries (its own value
// wins a collision). `docker compose up <svc>` interpolates the WHOLE file
// and starts, or recreates, whatever <svc> depends_on: otel-profiles depends
// on clickstack, so bringing it up with only its own variables recreated
// clickstack on the file's default ports, which on a machine running a
// second stack belong to that stack, and the bind failed.
func withComposeFileEnv(spec *deploytarget.ComposeSpec, cm *ComposeRuntime, e *KCLEntities) {
	if spec == nil || cm == nil || e == nil {
		return
	}
	for _, w := range e.WorkloadsOn(RuntimeCompose) {
		other := w.Runtime.Compose
		if other == nil || other == cm || other.File != cm.File || other.Shared != cm.Shared {
			continue
		}
		for k, v := range other.Env {
			if _, own := spec.Env[k]; !own {
				spec.Env[k] = v
			}
		}
	}
}
