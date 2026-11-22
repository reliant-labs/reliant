package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/reliant-labs/forge/internal/buildtarget"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/pkg/release"
)

// filesOnlyShell is a build-only ShellBuild workload with no image whose
// artifact is its declared outputs.
func filesOnlyShell(name, cwd string, outputs ...BuildOutput) WorkloadEntity {
	return WorkloadEntity{
		Name:    name,
		Kind:    "tool",
		Build:   BuildConfigEntity{Type: "shell", Shell: &ShellBuild{Cmd: "./package.sh", Cwd: cwd}, Outputs: outputs},
		Runtime: RuntimeEntity{Type: RuntimeBuildOnly, BuildOnly: &BuildOnlyDeploy{}},
	}
}

func TestHarvestDeclaredOutputs_HashesEveryMatchedFile(t *testing.T) {
	project := t.TempDir()
	writeArtifactFixture(t, filepath.Join(project, "tools", "cli", "dist", "acme_linux_amd64.tar.gz"), "linux-archive")
	writeArtifactFixture(t, filepath.Join(project, "tools", "cli", "dist", "acme_darwin_arm64.tar.gz"), "darwin-archive!")
	writeArtifactFixture(t, filepath.Join(project, "tools", "cli", "dist", "checksums.txt"), "sums")
	writeArtifactFixture(t, filepath.Join(project, "bin", "acme-agent"), "agent-binary")

	entities := &KCLEntities{Workloads: []WorkloadEntity{
		filesOnlyShell("acme-cli", "tools/cli",
			BuildOutput{Path: "dist/acme_linux_amd64.tar.gz", Platform: "linux/amd64"},
			BuildOutput{Path: "dist/*.txt"},
		),
		{
			// A GoBuild's outputs are relative to the project root.
			Name:  "acme-agent",
			Build: BuildConfigEntity{Type: "go", Go: &GoBuild{Cmd: "./cmd/agent"}, Outputs: []BuildOutput{{Path: "bin/acme-agent", Platform: "linux/arm64"}}},
		},
		// No outputs declared: contributes nothing.
		shellSvc("api", "ghcr.io/acme/api", "true", "", nil),
	}}

	got, missing, err := harvestDeclaredOutputs(context.Background(), project, entities)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 0 {
		t.Fatalf("missing = %q, want none", missing)
	}
	want := map[string]release.Artifact{
		"acme-cli/dist/acme_linux_amd64.tar.gz": {
			Kind: release.KindFile, Mode: release.ModeShared, Version: "dist/acme_linux_amd64.tar.gz",
			Integrity: sha256Hex(t, "linux-archive"), Size: int64(len("linux-archive")), Platforms: []string{"linux/amd64"},
		},
		"acme-cli/dist/checksums.txt": {
			Kind: release.KindFile, Mode: release.ModeShared, Version: "dist/checksums.txt",
			Integrity: sha256Hex(t, "sums"), Size: 4,
		},
		"acme-agent/bin/acme-agent": {
			Kind: release.KindFile, Mode: release.ModeShared, Version: "bin/acme-agent",
			Integrity: sha256Hex(t, "agent-binary"), Size: int64(len("agent-binary")), Platforms: []string{"linux/arm64"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("harvested:\n%+v\nwant:\n%+v", got, want)
	}
	// Every harvested artifact is one a release accepts.
	for name, a := range got {
		if err := a.Validate(name); err != nil {
			t.Errorf("artifact %s does not validate: %v", name, err)
		}
	}
}

func TestHarvestDeclaredOutputs_UsesResolvedShellSourceSubdir(t *testing.T) {
	project, checkout := t.TempDir(), t.TempDir()
	const output = "dist/tool.tar.gz"
	workDir := filepath.Join(checkout, "tools")
	writeArtifactFixture(t, filepath.Join(workDir, output), "built in pinned source")
	writeArtifactFixture(t, filepath.Join(project, output), "stale local output")
	const repo = "github.com/acme/tool"
	writeSourceOverride(t, project, repo, checkout)
	w := filesOnlyShell("tool", "", BuildOutput{Path: output})
	w.Build.Shell.Source = &config.GitSource{Repo: repo, Ref: "v1", Subdir: "tools"}
	entities := &KCLEntities{Workloads: []WorkloadEntity{w}}

	// First resolve for --no-build, then use the same already-resolved path
	// that a normal build leaves behind.
	for range 2 {
		got, missing, err := harvestDeclaredOutputs(context.Background(), project, entities)
		if err != nil || len(missing) != 0 {
			t.Fatalf("harvest: %v; missing %v", err, missing)
		}
		artifact := got["tool/"+output]
		if artifact.Integrity != sha256Hex(t, "built in pinned source") || w.Build.Shell.SourceDir != workDir {
			t.Fatalf("harvest did not use the resolved build directory: %+v, dir %q", artifact, w.Build.Shell.SourceDir)
		}
	}
}

func TestHarvestDeclaredOutputs_ListsOutputsThatMatchNothing(t *testing.T) {
	project := t.TempDir()
	writeArtifactFixture(t, filepath.Join(project, "dist", "acme.zip"), "zip")
	entities := &KCLEntities{Workloads: []WorkloadEntity{
		filesOnlyShell("acme-cli", "", BuildOutput{Path: "dist/acme.zip"}, BuildOutput{Path: "dist/*.dmg"}),
	}}
	got, missing, err := harvestDeclaredOutputs(context.Background(), project, entities)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["acme-cli/dist/acme.zip"]; !ok || len(got) != 1 {
		t.Errorf("harvested %v, want the one file that exists", got)
	}
	if len(missing) != 1 || !strings.Contains(missing[0], "acme-cli: dist/*.dmg") {
		t.Fatalf("missing = %q, want the .dmg output named", missing)
	}

	err = errMissingDeclaredOutputs("v1.2.0", missing)
	for _, want := range []string{"--release v1.2.0", "matched no file", "acme-cli: dist/*.dmg", "only bytes that exist"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("release refusal %q lacks %q", err, want)
		}
	}

	var out bytes.Buffer
	warnMissingDeclaredOutputs(context.Background(), &out, project, entities)
	if !strings.Contains(out.String(), "Warning: declared output matched no file") || !strings.Contains(out.String(), "dist/*.dmg") {
		t.Errorf("a plain build's warning = %q", out.String())
	}
}

func TestHarvestDeclaredOutputs_RefusesAPathOutsideTheWorkingDirectory(t *testing.T) {
	entities := &KCLEntities{Workloads: []WorkloadEntity{filesOnlyShell("acme-cli", "", BuildOutput{Path: "../secrets.txt"})}}
	if _, _, err := harvestDeclaredOutputs(context.Background(), t.TempDir(), entities); err == nil ||
		!strings.Contains(err.Error(), "leaves the build's working directory") {
		t.Fatalf("err = %v, want the escape refused", err)
	}
}

// Images merge first and are never overwritten by a declared output, even
// one whose key collides with an image's.
func TestDeclaredOutputsNeverDisplaceAnImage(t *testing.T) {
	image := release.Artifact{Kind: release.KindOCI, Mode: release.ModeShared, Digests: map[string]string{release.SharedVariant: "sha256:" + strings.Repeat("a", 64)}}
	artifacts := map[string]release.Artifact{"acme-cli/dist/acme.zip": image}
	added := mergeReleaseArtifacts(artifacts, map[string]release.Artifact{
		"acme-cli/dist/acme.zip": {Kind: release.KindFile, Mode: release.ModeShared, Version: "dist/acme.zip"},
	})
	if added != 0 || artifacts["acme-cli/dist/acme.zip"].Kind != release.KindOCI {
		t.Fatalf("a declared output displaced an image: %+v", artifacts)
	}
}

// An env whose only release artifact is a declared output is not "empty".
func TestReleaseCapturesNothing_DeclaredOutputsCount(t *testing.T) {
	entities := &KCLEntities{Workloads: []WorkloadEntity{{
		Name:    "acme-agent",
		Build:   BuildConfigEntity{Type: "go", Go: &GoBuild{Cmd: "./cmd/agent"}, Outputs: []BuildOutput{{Path: "bin/acme-agent"}}},
		Runtime: RuntimeEntity{Type: RuntimeBuildOnly, BuildOnly: &BuildOnlyDeploy{}},
	}}}
	opts := buildOptions{env: "prod", release: "v1"}
	if why, empty := releaseCapturesNothing(t.TempDir(), entities, buildTargetSet{}, opts); empty {
		t.Fatalf("a release of declared outputs was refused as empty: %s", why)
	}
	entities.Workloads[0].Build.Outputs = nil
	if _, empty := releaseCapturesNothing(t.TempDir(), entities, buildTargetSet{}, opts); !empty {
		t.Fatal("control: the same env without outputs should capture nothing")
	}
}

// A files-only ShellBuild pushes no image: forge asks the registry for
// nothing, records no deploy-side image handoff, and a release requires no
// image for it.
func TestBuildExternal_FilesOnlyWorkloadResolvesNoImage(t *testing.T) {
	oldBuild, oldDigest, oldSource, oldStorage := externalShellBuild, externalImageDigestResolver, externalSourceCapture, checkBuildStorageFn
	t.Cleanup(func() {
		externalShellBuild, externalImageDigestResolver, externalSourceCapture, checkBuildStorageFn = oldBuild, oldDigest, oldSource, oldStorage
	})
	var lookups atomic.Int32
	checkBuildStorageFn = func(string) error { return nil }
	externalShellBuild = func(_ context.Context, spec buildtarget.Spec) buildtarget.BuildResult {
		return buildtarget.BuildResult{Service: spec.Service, Tag: spec.Tag}
	}
	externalImageDigestResolver = func(context.Context, string) (string, []string, error) {
		lookups.Add(1)
		return "", nil, os.ErrNotExist
	}
	externalSourceCapture = func(context.Context, string, string, string) *buildtarget.Source { return nil }

	project := t.TempDir()
	svc := filesOnlyShell("acme-cli", "", BuildOutput{Path: "dist/acme.zip"})
	results := buildExternalServices(context.Background(), []WorkloadEntity{svc}, buildOptions{env: "prod"}, "v1", project)
	if len(results) != 1 || results[0].err != nil {
		t.Fatalf("results = %+v", results)
	}
	if n := lookups.Load(); n != 0 {
		t.Errorf("resolved an image digest %d time(s) for a workload that pushed none", n)
	}
	if _, err := os.Stat(buildStatePath(project, "prod")); !os.IsNotExist(err) {
		t.Errorf("an image-less build wrote the env's deploy-side image handoff (%v)", err)
	}
	if gaps := releaseCoverageGaps(&KCLEntities{Workloads: []WorkloadEntity{svc}}, map[string]release.Artifact{}, "prod"); len(gaps) != 0 {
		t.Errorf("a release requires an image of a files-only workload: %q", gaps)
	}
}

// A files-only workload has no image slot for a composition snapshot to
// bind; a build-only workload with an image keeps its slot.
func TestCompositionConsumers_SkipAFilesOnlyWorkload(t *testing.T) {
	withImage := filesOnlyShell("installer", "", BuildOutput{Path: "out/setup.exe"})
	withImage.Image = "ghcr.io/acme/installer"
	e := &KCLEntities{Workloads: []WorkloadEntity{filesOnlyShell("acme-cli", "", BuildOutput{Path: "dist/acme.zip"}), withImage}}
	consumers, err := compositionConsumers(e)
	if err != nil {
		t.Fatal(err)
	}
	if len(consumers) != 1 || consumers[0].Key.Workload != "installer" {
		t.Fatalf("consumers = %+v, want only the image-bearing workload", consumers)
	}
}
