package cli

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/pkg/release"
)

func withHostArch(t *testing.T, arch string) {
	t.Helper()
	prev := hostArch
	hostArch = arch
	t.Cleanup(func() { hostArch = prev })
}

func hostedGoWL(name string) WorkloadEntity {
	w := hostedWL(name, func(w *WorkloadEntity) {
		w.Image = name
		w.Build = BuildConfigEntity{Type: "go", Go: &GoBuild{Cmd: "./cmd/" + name, OutputName: name}}
	})
	return w
}

func imageArchFor(t *testing.T, e *KCLEntities, opts buildOptions) string {
	t.Helper()
	targets, err := resolveBuildTargetSet(&config.ProjectConfig{Name: "p"}, e, opts)
	if err != nil {
		t.Fatalf("resolveBuildTargetSet: %v", err)
	}
	return resolveBuildArchForImage(targets.cfgArchForDocker, opts.targetArch)
}

// A hosted-only env has no cluster platform; its image arch must come from the
// platform, not from the machine that happens to run the build.
func TestHostedOnlyEnvBuildsForPlatformArchNotHostArch(t *testing.T) {
	withHostArch(t, "arm64")
	e := &KCLEntities{Workloads: []WorkloadEntity{hostedGoWL("api")}}
	if got := imageArchFor(t, e, buildOptions{env: "prod", push: true, buildTarget: "all"}); got != "amd64" {
		t.Fatalf("hosted image arch on an arm64 host = %q, want amd64 (the platform's)", got)
	}
}

func TestHostedPlatformDeclaredArm64IsHonoured(t *testing.T) {
	withHostArch(t, "amd64")
	w := hostedGoWL("api")
	w.Runtime.Hosted = &HostedRuntime{Platform: "arm64"}
	e := &KCLEntities{Workloads: []WorkloadEntity{w}}
	if got := imageArchFor(t, e, buildOptions{env: "prod", push: true, buildTarget: "all"}); got != "arm64" {
		t.Fatalf("got %q, want the declared arm64", got)
	}
}

func TestClusterEnvWithExplicitPlatformUnchanged(t *testing.T) {
	withHostArch(t, "arm64")
	e := &KCLEntities{
		ClusterTarget: &ClusterTargetEntity{Platform: "amd64"},
		Workloads:     []WorkloadEntity{clusterWL("api", "gke_prod", "ns")},
	}
	if got := imageArchFor(t, e, buildOptions{env: "prod", push: true, buildTarget: "all"}); got != "amd64" {
		t.Fatalf("got %q, want the declared cluster platform amd64", got)
	}
}

func TestPushedImageForRemoteClusterWithoutArchErrors(t *testing.T) {
	withHostArch(t, "arm64")
	e := &KCLEntities{Workloads: []WorkloadEntity{clusterWL("api", "gke_prod_us", "ns")}}
	_, err := resolveBuildTargetSet(&config.ProjectConfig{Name: "p"}, e, buildOptions{env: "prod", push: true, buildTarget: "all"})
	if err == nil {
		t.Fatal("a pushed image for a remote cluster with no declared arch must fail, not use the host arch")
	}
	for _, want := range []string{"expected:", "found:", "platform", "deploy.target_arch", "--target-arch"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q:\n%v", want, err)
		}
	}
}

func TestLocalClusterWithoutArchStillTracksHost(t *testing.T) {
	withHostArch(t, "arm64")
	e := &KCLEntities{Workloads: []WorkloadEntity{clusterWL("api", "k3d-dev", "ns")}}
	if got := imageArchFor(t, e, buildOptions{env: "dev", push: true, buildTarget: "all"}); got != "arm64" {
		t.Fatalf("local k3d host arch fallback = %q, want arm64", got)
	}
}

func fakePlatformResolver(t *testing.T, platforms []string, err error) *int {
	t.Helper()
	calls := 0
	prev := hostedPlatformResolver
	t.Cleanup(func() { hostedPlatformResolver = prev })
	hostedPlatformResolver = func(_ context.Context, ref string) ([]string, error) {
		calls++
		if !strings.Contains(ref, "@sha256:") {
			t.Errorf("registry read must be by digest, got %q", ref)
		}
		return platforms, err
	}
	return &calls
}

func ociArtifact(e *KCLEntities) map[string]release.Artifact {
	key := hostedArtifactKey(e, e.Workloads[0])
	return map[string]release.Artifact{key: {Kind: release.KindOCI,
		Digests: map[string]string{release.SharedVariant: "sha256:" + strings.Repeat("a", 64)}}}
}

func TestHostedNilPlatformsResolvedFromRegistryArm64Refused(t *testing.T) {
	e := &KCLEntities{Workloads: []WorkloadEntity{hostedGoWL("api")}}
	fakePlatformResolver(t, []string{"linux/arm64"}, nil)
	err := checkHostedArtifactPlatforms(context.Background(), io.Discard, e, ociArtifact(e), "prod", "v1")
	if err == nil || !strings.Contains(err.Error(), "expected linux/amd64, found linux/arm64") {
		t.Fatalf("want refusal naming linux/arm64, got %v", err)
	}
}

func TestHostedNilPlatformsRegistryAmd64Passes(t *testing.T) {
	e := &KCLEntities{Workloads: []WorkloadEntity{hostedGoWL("api")}}
	fakePlatformResolver(t, []string{"linux/amd64"}, nil)
	var out strings.Builder
	if err := checkHostedArtifactPlatforms(context.Background(), &out, e, ociArtifact(e), "prod", "v1"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "linux/amd64") {
		t.Errorf("plan output must show the verified platform: %q", out.String())
	}
}

func TestHostedUnreadableManifestFailsClosed(t *testing.T) {
	e := &KCLEntities{Workloads: []WorkloadEntity{hostedGoWL("api")}}
	fakePlatformResolver(t, nil, errors.New("registry unreachable"))
	err := checkHostedArtifactPlatforms(context.Background(), io.Discard, e, ociArtifact(e), "prod", "v1")
	if err == nil || !strings.Contains(err.Error(), "could not be read") || !strings.Contains(err.Error(), "forge env build prod --push --release") {
		t.Fatalf("want fail-closed runbook error, got %v", err)
	}
}

func TestHostedStaticSiteArtifactWithoutPlatformsIsExempt(t *testing.T) {
	e := &KCLEntities{Workloads: []WorkloadEntity{hostedGoWL("api")}}
	calls := fakePlatformResolver(t, nil, errors.New("must not be read"))
	arts := map[string]release.Artifact{"reg/org/proj/web/static.v1": {Kind: release.KindOCI,
		Digests: map[string]string{release.SharedVariant: "sha256:" + strings.Repeat("b", 64)}}}
	if err := checkHostedArtifactPlatforms(context.Background(), io.Discard, e, arts, "prod", "v1"); err != nil {
		t.Fatalf("static site artifact must be exempt: %v", err)
	}
	if *calls != 0 {
		t.Errorf("registry was read %d time(s) for a static site", *calls)
	}
}

// A plain `docker build` push on macOS is a single manifest, not an index; the
// index template errors on it. Capture must still record the platform.
func TestImageRegistryPlatformsSingleManifest(t *testing.T) {
	prev := imagetoolsInspect
	t.Cleanup(func() { imagetoolsInspect = prev })
	imagetoolsInspect = func(_ context.Context, _, format string) ([]byte, error) {
		if strings.Contains(format, "Manifest.Manifests") {
			return nil, errors.New("exit status 1")
		}
		return []byte("linux/arm64\n"), nil
	}
	got, err := imageRegistryPlatforms(context.Background(), "r/x@sha256:"+strings.Repeat("c", 64))
	if err != nil || len(got) != 1 || got[0] != "linux/arm64" {
		t.Fatalf("got %v, %v; want [linux/arm64]", got, err)
	}
	if p := imageToolsPlatforms(context.Background(), "r/x:tag"); len(p) != 1 {
		t.Errorf("capture-time read (imageToolsPlatforms) recorded %v, want one platform", p)
	}
}
