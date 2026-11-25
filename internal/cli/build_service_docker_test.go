package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// argsHave reports whether the flag/value pair appears adjacently in args.
func argsHave(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

// countFlag counts occurrences of an exact arg token.
func countFlag(args []string, tok string) int {
	n := 0
	for _, a := range args {
		if a == tok {
			n++
		}
	}
	return n
}

// TestServiceDockerBuildArgs_TagsTheWorkloadsDeclaredRepository: a per-service
// docker build is tagged under the repository the WORKLOAD declared, verbatim.
//
// This replaces the old per-build-vs-per-env registry precedence test.
// Both of those fields are gone, and the precedence they encoded was the defect:
// a build could be aimed at one registry while the spec the deploy reads named
// another, which is an ErrImagePull that only surfaces at rollout. With one
// declaration there is nothing to reconcile.
func TestServiceDockerBuildArgs_TagsTheWorkloadsDeclaredRepository(t *testing.T) {
	cfg := &config.ProjectConfig{Name: "control-plane"}
	// serviceDockerBuildArgs takes the REPOSITORY, already resolved by
	// serviceDockerImage from the workload's own image.
	args, _ := serviceDockerBuildArgs(cfg, "us-docker.pkg.dev/svc-specific/workspace-base",
		"Dockerfile", &DockerBuild{}, buildOptions{}, "", "v1.2.3")

	for _, want := range []string{
		"us-docker.pkg.dev/svc-specific/workspace-base:latest",
		"us-docker.pkg.dev/svc-specific/workspace-base:v1.2.3",
	} {
		if !argsHave(args, "-t", want) {
			t.Errorf("missing tag %q; args=%v", want, args)
		}
	}
	// Nothing is composed from the project name: it is not a registry.
	for _, a := range args {
		if strings.HasPrefix(a, "control-plane/") {
			t.Errorf("the project name was used as a registry; got tag %q", a)
		}
	}
}

// TestServiceDockerBuildArgs_UndeclaredImageIsTaggedBare: an artifact no
// workload declares a reference for is tagged by its bare name. That is not a
// silent mis-push — a bare name has no registry host, so nothing pushes it, and
// the render is what refuses a bare image on a runtime that must pull one.
func TestServiceDockerBuildArgs_UndeclaredImageIsTaggedBare(t *testing.T) {
	cfg := &config.ProjectConfig{Name: "control-plane"}
	args, pushTags := serviceDockerBuildArgs(cfg, "svc", "Dockerfile", &DockerBuild{}, buildOptions{}, "", "")
	if !argsHave(args, "-t", "svc:latest") {
		t.Errorf("expected a bare local tag; args=%v", args)
	}
	if len(pushTags) != 0 {
		t.Errorf("a bare image has no push destination, got %v", pushTags)
	}
}

// TestServiceDockerBuildArgs_PerServiceBuildContextsWin asserts a DockerBuild's
// own build_contexts are used (and the project-level docker.build_contexts are
// NOT also appended) — a service declares ONLY the contexts its Dockerfile
// actually COPY --from=s.
func TestServiceDockerBuildArgs_PerServiceBuildContextsWin(t *testing.T) {
	cfg := &config.ProjectConfig{
		Name: "control-plane",
		Docker: config.DockerConfig{
			BuildContexts: map[string]config.BuildContext{"projectwide": {Path: "../should-not-appear"}},
		},
	}
	d := &DockerBuild{
		BuildContexts: map[string]config.BuildContext{
			"forge":   {Path: "../forge"},
			"reliant": {Path: "../reliant"},
		},
	}

	args, _ := serviceDockerBuildArgs(cfg, "svc", "Dockerfile", d, buildOptions{}, "", "")

	wantForge := "forge=" + filepath.Join(".", "../forge")
	wantReliant := "reliant=" + filepath.Join(".", "../reliant")
	if !argsHave(args, "--build-context", wantForge) {
		t.Errorf("per-service forge context missing; args=%v", args)
	}
	if !argsHave(args, "--build-context", wantReliant) {
		t.Errorf("per-service reliant context missing; args=%v", args)
	}
	// The project-wide context must NOT be appended when the service overrides.
	for _, a := range args {
		if strings.Contains(a, "projectwide") || strings.Contains(a, "should-not-appear") {
			t.Errorf("project-wide build_contexts leaked despite per-service override: %q", a)
		}
	}
}

// TestServiceDockerBuildArgs_BuildContextsFallToProject asserts a DockerBuild
// with NO build_contexts inherits the project-level forge.yaml ones — the
// single-image project keeps declaring contexts once at the top level.
func TestServiceDockerBuildArgs_BuildContextsFallToProject(t *testing.T) {
	cfg := &config.ProjectConfig{
		Name: "control-plane",
		Docker: config.DockerConfig{
			BuildContexts: map[string]config.BuildContext{"forge": {Path: "../forge"}},
		},
	}
	args, _ := serviceDockerBuildArgs(cfg, "svc", "Dockerfile", &DockerBuild{}, buildOptions{}, "", "")
	want := "forge=" + filepath.Join(".", "../forge")
	if !argsHave(args, "--build-context", want) {
		t.Errorf("expected project-level build_contexts fallback; args=%v", args)
	}
}

// TestServiceDockerBuildArgs_NoBaseImageInjection is the load-bearing
// regression for the base-agnostic break: a vanilla DockerBuild (no build_args,
// no base config anywhere) must emit ZERO base/mirror `--build-arg` injection.
// forge no longer discovers, mirrors, pins, or injects base images — the
// Dockerfile's `FROM` is the whole story. The only `--build-arg`s ever present
// are the service's explicit DockerBuild.build_args.
func TestServiceDockerBuildArgs_NoBaseImageInjection(t *testing.T) {
	cfg := &config.ProjectConfig{
		Name: "control-plane",
	}

	t.Run("vanilla service: no --build-arg at all", func(t *testing.T) {
		args, _ := serviceDockerBuildArgs(cfg, "svc", "Dockerfile", &DockerBuild{}, buildOptions{}, "amd64", "tag")
		if n := countFlag(args, "--build-arg"); n != 0 {
			t.Errorf("vanilla service must inject zero --build-arg, got %d: %v", n, args)
		}
		for _, a := range args {
			if strings.HasPrefix(a, "BASE_") {
				t.Errorf("base-image build-arg leaked into a base-agnostic build: %q", a)
			}
		}
	})

	t.Run("only explicit build_args appear", func(t *testing.T) {
		d := &DockerBuild{BuildArgs: map[string]string{"FORGE_VERSION": "v1"}}
		args, _ := serviceDockerBuildArgs(cfg, "svc", "Dockerfile", d, buildOptions{}, "amd64", "tag")
		if n := countFlag(args, "--build-arg"); n != 1 {
			t.Errorf("expected exactly the 1 explicit build_arg, got %d: %v", n, args)
		}
		if !argsHave(args, "--build-arg", "FORGE_VERSION=v1") {
			t.Errorf("explicit build_arg missing; args=%v", args)
		}
	})
}
