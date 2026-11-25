package cli

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/reliant-labs/forge/internal/buildtarget"
)

// Test seams for dispatch tests; production uses the existing runner and capture.
var externalShellBuild = buildtarget.NewRunner().Build
var externalSourceCapture = captureShellSource

// externalBuildArtifact is invocation-local, not a cache. Its first consumer
// (lexicographically by name) owns execution and source capture; every consumer
// gets that exact outcome. Group BEFORE spawning workers or writing any state.
type externalBuildArtifact struct {
	services []WorkloadEntity
	spec     buildtarget.Spec
}

func externalBuildSpec(svc WorkloadEntity, opts buildOptions, tag, projectDir string) buildtarget.Spec {
	return buildtarget.Spec{
		Service: svc.Name, Image: svc.Image, Tag: externalBuildTag(svc, tag, opts),
		ProjectDir: projectDir, BuildCmd: svc.EffectiveBuildCmd(),
		BuildCwd: svc.EffectiveBuildCwd(), BuildEnv: svc.EffectiveBuildEnv(),
	}
}

// externalBuildArtifacts is shared by --plan and execution. Identical commands
// alone are insufficient: they must name the same target and have the same cwd,
// declared source, environment and platform. Different recipes targeting one
// immutable tag are a configuration error, not two builds racing to overwrite it.
func externalBuildArtifacts(services []WorkloadEntity, opts buildOptions, tag, projectDir string) ([]externalBuildArtifact, error) {
	ordered := append([]WorkloadEntity(nil), services...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Name < ordered[j].Name })
	var artifacts []externalBuildArtifact
	byTarget := map[string]int{}
	identities := map[string]string{}
	for _, svc := range ordered {
		spec := externalBuildSpec(svc, opts, tag, projectDir)
		cwd, err := buildtarget.ResolveCwd(spec)
		if err != nil {
			return nil, err
		}
		cwd, err = filepath.Abs(cwd)
		if err != nil {
			return nil, err
		}
		// Canonicalize aliases so ./source and a symlink to it agree.
		cwd, err = filepath.EvalSymlinks(cwd)
		if err != nil {
			return nil, fmt.Errorf("ShellBuild %s: resolve cwd: %w", svc.Name, err)
		}
		spec.BuildCwd = cwd
		identity, err := json.Marshal(externalBuildIdentity(svc, cwd, opts))
		if err != nil {
			return nil, err
		}
		target := externalPushedRef(svc, spec.Tag, opts.pushPlan.pushBase)
		// An image-less command declares no shared artifact; never coalesce it.
		if svc.Image == "" {
			artifacts = append(artifacts, externalBuildArtifact{services: []WorkloadEntity{svc}, spec: spec})
			continue
		}
		if idx, ok := byTarget[target]; ok {
			if identities[target] != string(identity) {
				return nil, fmt.Errorf("conflicting ShellBuild inputs for image %s: services %q and %q differ in recipe, cwd, environment, platform or source; declare distinct image tags or identical build inputs", target, artifacts[idx].services[0].Name, svc.Name)
			}
			artifacts[idx].services = append(artifacts[idx].services, svc)
			continue
		}
		byTarget[target] = len(artifacts)
		identities[target] = string(identity)
		artifacts = append(artifacts, externalBuildArtifact{services: []WorkloadEntity{svc}, spec: spec})
	}
	return artifacts, nil
}

// externalArtifactIdentity is everything DECLARED that makes two ShellBuild
// artifacts one recipe: the coalescing key of externalBuildArtifacts, and
// embedded verbatim in the skip fingerprint (shellBuildFingerprint), so a
// field added here is hashed by both — the fingerprint can never know less
// about a build than the artifact identity does.
//
// Its JSON is the identity: field names and order are load-bearing.
type externalArtifactIdentity struct {
	Cmd, Cwd, Platform, InputsFile string
	Env                            map[string]string
	BuiltImages                    map[string]string
	Source                         any
	Inputs                         []string `json:",omitempty"`
}

// externalBuildIdentity is svc's artifact identity. cwd must already be the
// canonical (absolute, symlink-resolved) working directory.
func externalBuildIdentity(svc WorkloadEntity, cwd string, opts buildOptions) externalArtifactIdentity {
	// JSON sorts map keys and preserves recipe bytes. Nil and empty env
	// overlays have the same execution semantics.
	env := svc.EffectiveBuildEnv()
	if env == nil {
		env = map[string]string{}
	}
	// built_images is part of the recipe: the same command built FROM a
	// different parent is a different image. Nil and empty are one recipe.
	builtImages := svc.EffectiveBuiltImages()
	if len(builtImages) == 0 {
		builtImages = nil
	}
	return externalArtifactIdentity{
		Cmd: svc.EffectiveBuildCmd(), Cwd: cwd, Platform: externalBuildPlatform(svc, opts),
		InputsFile: svc.Build.InputsFile, Env: env, BuiltImages: builtImages,
		Source: externalDeclaredSource(svc), Inputs: externalBuildInputs(svc),
	}
}

// externalBuildPlatform is the platform a ShellBuild builds for: --target-arch,
// else the cluster runtime's platform, else the hosted runtime's.
func externalBuildPlatform(svc WorkloadEntity, opts buildOptions) string {
	platform := opts.targetArch
	if platform == "" && svc.Runtime.Cluster != nil {
		platform = svc.Runtime.Cluster.Platform
	}
	if platform == "" && svc.Runtime.Type == RuntimeHosted {
		platform = svc.Runtime.HostedPlatform()
	}
	return platform
}

func externalDeclaredSource(svc WorkloadEntity) any {
	if svc.Build.Shell != nil && svc.Build.Shell.Source != nil {
		return svc.Build.Shell.Source
	}
	return nil
}
