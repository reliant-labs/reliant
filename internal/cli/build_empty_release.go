package cli

import (
	"fmt"
	"path/filepath"
)

// The empty-release refusal, raised BEFORE anything is built.
//
// An env that declares nothing a release can pin (manifests and components
// only: an operator, a store, policies) used to be refused by the cut with
// "no image digest was captured", but only after the whole build had run. The
// SC7 rehearsal paid four minutes of an unrelated frontend build to learn it.
//
// The check is deliberately one-sided. It refuses only when NO source the cut
// harvests (cutReleaseFromBuildState) could yield an artifact, so it can never
// refuse an env the cut would have accepted: every doubt (a publishable npm
// package, a nested Go module, a build state left by an earlier build, a
// hosted workload, a ShellBuild) means "capturable" and the build proceeds to
// the cut's own, exact check.

// releaseCapturesNothing reports whether a release cut for this env can only
// come out empty, and why.
func releaseCapturesNothing(projectDir string, entities *KCLEntities, targets buildTargetSet, opts buildOptions) (string, bool) {
	if entities == nil || opts.release == "" || opts.plan {
		return "", false
	}
	// Artifacts an earlier build left in .forge/state: the cut harvests them.
	if len(harvestReleaseArtifacts(projectDir, opts.env)) > 0 {
		return "", false
	}
	for _, w := range entities.Workloads {
		switch {
		case w.Image != "" && w.Build.Type != "" && !w.OnRuntime(RuntimeHost) && !w.OnRuntime(RuntimeCompose):
			return "", false
		case w.EffectiveBuildCmd() != "":
			return "", false
		case w.OnRuntime(RuntimeHosted):
			return "", false
		case w.OnRuntime(RuntimeBuildOnly) && w.Runtime.BuildOnly != nil && len(w.Runtime.BuildOnly.BuildVariants) > 0:
			return "", false
		}
	}
	for _, fe := range entities.Frontends {
		if frontendIsHosted(fe) || (fe.Source != nil && fe.Source.Repo != "") {
			return "", false
		}
	}
	if opts.buildDocker {
		if len(targets.goTargets) > 0 && !targets.skipProjectDocker && fileExists(filepath.Join(projectDir, "Dockerfile")) {
			return "", false
		}
		for _, fe := range targets.dockerFrontends {
			if fileExists(filepath.Join(fe.DeclaredDir(), "Dockerfile")) {
				return "", false
			}
		}
	}
	for _, dir := range scanForManifests(projectDir, "package.json") {
		if _, ok := readNPMManifest(filepath.Join(dir, "package.json")); ok {
			return "", false
		}
	}
	for _, dir := range scanForManifests(projectDir, "go.mod") {
		if dir != projectDir {
			return "", false
		}
	}
	return fmt.Sprintf("env %q declares nothing a release can pin: no workload with an image forge builds, no ShellBuild, no hosted workload or static site, no source-pinned frontend, no build-only binary, and no publishable package in the project", opts.env), true
}

// errEmptyRelease is the refusal. It names the cause and the way out, and says
// what was NOT done, so the operator knows no build time was spent.
func errEmptyRelease(why string, opts buildOptions) error {
	return fmt.Errorf("--release %s: %s, so the release would name no artifact (a release that names nothing cannot be promoted).\n"+
		"  Nothing was built. `forge env deploy %s` with no version builds, cuts and promotes a release, so it cannot ship an env that pins nothing yet.\n"+
		"  fix: declare something to pin (a workload `image` with a build, a ShellBuild, a hosted static site),\n"+
		"       or preview the manifests with `forge env deploy %s --dry-run` and apply them yourself",
		opts.release, why, opts.env, opts.env)
}
