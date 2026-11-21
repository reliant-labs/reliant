package cli

import (
	"context"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/reliant-labs/forge/internal/buildtarget"
	"github.com/reliant-labs/forge/pkg/release"
)

// A forge.BuildOnly ShellBuild that declares no image (reliant's desktop
// bundle: cwd "electron", a packaging cmd, no image, no outputs) renders no
// artifact name (kcl/tests/positive_build_only_shell_no_image.k). Its build
// must then record no image: no registry lookup, no image in its own state,
// no image a release must carry, no composition slot — and it must leave the
// env's deploy-side build-<env>.json, which another build wrote, alone
// (forge bug 882ce226: it overwrote build-release.json with a placeholder).
func TestBuildExternal_ImagelessBuildOnlyShellRecordsNoImage(t *testing.T) {
	oldBuild, oldDigest, oldSource, oldStorage := externalShellBuild, externalImageDigestResolver, externalSourceCapture, checkBuildStorageFn
	t.Cleanup(func() {
		externalShellBuild, externalImageDigestResolver, externalSourceCapture, checkBuildStorageFn = oldBuild, oldDigest, oldSource, oldStorage
	})
	var builds, lookups atomic.Int32
	checkBuildStorageFn = func(string) error { return nil }
	externalSourceCapture = func(context.Context, string, string, string) *buildtarget.Source { return nil }
	externalShellBuild = func(_ context.Context, spec buildtarget.Spec) buildtarget.BuildResult {
		builds.Add(1)
		return buildtarget.BuildResult{Service: spec.Service, Tag: spec.Tag}
	}
	externalImageDigestResolver = func(context.Context, string) (string, []string, error) {
		lookups.Add(1)
		return "", nil, os.ErrNotExist
	}

	project := t.TempDir()
	if err := os.MkdirAll(project+"/electron", 0o755); err != nil {
		t.Fatal(err)
	}
	// What the env's image build recorded before the desktop build ran.
	prior := BuildState{Image: "ghcr.io/acme/server", Tag: "v1", Digest: "sha256:" + strings.Repeat("a", 64)}
	if err := WriteBuildState(project, "release", prior); err != nil {
		t.Fatal(err)
	}

	desktop := shellSvc("desktop", "", "node scripts/pack.js", "electron", nil)
	desktop.Runtime = RuntimeEntity{Type: RuntimeBuildOnly, BuildOnly: &BuildOnlyDeploy{}}
	results := buildExternalServices(context.Background(), []WorkloadEntity{desktop}, buildOptions{env: "release"}, "latest", project)
	if len(results) != 1 || results[0].err != nil {
		t.Fatalf("results = %+v", results)
	}
	if builds.Load() != 1 {
		t.Fatalf("the command ran %d time(s), want 1", builds.Load())
	}
	if n := lookups.Load(); n != 0 {
		t.Errorf("resolved an image digest %d time(s) for a workload that pushed no image", n)
	}

	got, err := ReadBuildState(project, "release")
	if err != nil || got == nil {
		t.Fatalf("ReadBuildState = %+v, %v", got, err)
	}
	if got.Image != prior.Image || got.Digest != prior.Digest || got.Tag != prior.Tag {
		t.Errorf("build-release.json = {image %q, tag %q, digest %q}, want the image build's {%q, %q, %q} untouched",
			got.Image, got.Tag, got.Digest, prior.Image, prior.Tag, prior.Digest)
	}

	if st, err := buildtarget.ReadState(project, "release", "desktop"); err != nil || st == nil {
		t.Fatalf("per-service state = %+v, %v", st, err)
	} else if st.Image != "" || st.Digest != "" {
		t.Errorf("per-service state records image %q digest %q for a build that pushed none", st.Image, st.Digest)
	}

	artifacts := harvestReleaseArtifacts(project, "release")
	if _, ok := artifacts[prior.Image]; !ok || len(artifacts) != 1 {
		t.Errorf("release artifacts = %v, want only %s", artifacts, prior.Image)
	}
	entities := &KCLEntities{Workloads: []WorkloadEntity{desktop}}
	if gaps := releaseCoverageGaps(entities, map[string]release.Artifact{}, "release"); len(gaps) != 0 {
		t.Errorf("a release requires an image of an image-less workload: %q", gaps)
	}
	consumers, err := compositionConsumers(entities)
	if err != nil {
		t.Fatal(err)
	}
	if len(consumers) != 0 {
		t.Errorf("composition consumers = %+v, want none: the workload has no image slot", consumers)
	}
}
