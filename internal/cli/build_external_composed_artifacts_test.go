package cli

import (
	"context"
	"testing"

	"github.com/reliant-labs/forge/internal/buildtarget"
	"github.com/reliant-labs/forge/pkg/release"
)

func TestBuildExternalSharedArtifactCompositionReceipt(t *testing.T) {
	builds, _, _ := mockExternalArtifactBuild(t, false)
	build := externalShellBuild
	receipt := &release.BuildReceipt{InputDigest: "attested-inputs"}
	externalShellBuild = func(ctx context.Context, spec buildtarget.Spec) buildtarget.BuildResult {
		if !spec.RequireReceipt {
			t.Error("composition lost receipt requirement")
		}
		res := build(ctx, spec)
		res.Receipt = receipt
		return res
	}
	services := sharedExternalConsumers()
	for i := range services {
		services[i].Build.Unit = "shared-recipe"
		services[i].Build.Shell.Cwd = "."
	}
	project := t.TempDir()
	opts := buildOptions{env: "prod", parallel: true, composition: &compositionRun{}}
	results := buildExternalServices(context.Background(), services, opts, "v1", project)
	if len(results) != 3 || builds.Load() != 1 {
		t.Fatalf("results=%d builds=%d", len(results), builds.Load())
	}
	for _, result := range results {
		if result.err != nil || result.receipt != receipt || result.digest != "sha256:shared" || result.workload == "" {
			t.Fatalf("consumer lost attested result: %+v", result)
		}
	}
	steps := planExternalBuilds(planInputs{entities: &KCLEntities{Workloads: services}, opts: opts, resolvedTag: "v1", projectDir: project})
	if len(steps) != 1 || steps[0].problem != "" {
		t.Fatalf("plan=%+v", steps)
	}
}
