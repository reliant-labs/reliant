package cli

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/reliant-labs/forge/internal/buildtarget"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/kclrender"
	"github.com/reliant-labs/forge/pkg/release"
)

// coverRel is a release whose shop image carries a cover variant beside its
// shared digest.
func coverRel() release.Release {
	r := rel("v1.4.0", "2026-01-01T00:00:00Z", "cccccccccccc", false, map[string]string{
		"ghcr.io/acme/shop": sha("shared"),
		"ghcr.io/acme/edge": sha("edge"),
	})
	r.Artifacts[release.VariantArtifactName("ghcr.io/acme/shop", release.CoverVariant)] = release.Artifact{
		Kind: release.KindOCI, Mode: release.ModeVariant, Digests: map[string]string{release.CoverVariant: sha("cover")},
	}
	return r
}

// A preview instance (its directory carries forge's marker) runs the cover
// variant; every other env runs the shared digests.
func markPreview(t *testing.T, projectDir, env string) {
	t.Helper()
	dir := filepath.Join(projectDir, "deploy", "kcl", env)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, kclrender.PreviewMarkerFile), []byte(`{"of":"preview","expires_at":"2030-01-01T00:00:00Z"}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

// THE RULE: a release that carries a cover variant promotes to prod, and the
// promotion prod gets pins the shared digests — the bytes staging ran.
func TestPromotePlan_ReleaseWithCoverVariantPromotesToProdOnShared(t *testing.T) {
	plan, err := computePromotePlan(context.Background(), promotePlanOptions{
		Env: "prod", Version: "v1.4.0", ProjectDir: t.TempDir(),
		Bindings: newMemBindingStore(nil), Releases: newMemReleaseLedger(coverRel()),
		Git: allCommitsPresent("cccccccccccc"),
	})
	if err != nil {
		t.Fatalf("a release carrying a cover variant must promote to prod: %v", err)
	}
	want := map[string]string{"ghcr.io/acme/shop": sha("shared"), "ghcr.io/acme/edge": sha("edge")}
	if !maps.Equal(plan.targetResolved, want) {
		t.Errorf("prod pins = %v, want the shared digests %v", plan.targetResolved, want)
	}
}

// A preview of the same release runs its cover build.
func TestPromotePlan_PreviewRunsTheCoverVariant(t *testing.T) {
	dir := t.TempDir()
	markPreview(t, dir, "preview-pr-7")
	plan, err := computePromotePlan(context.Background(), promotePlanOptions{
		Env: "preview-pr-7", Version: "v1.4.0", ProjectDir: dir,
		Bindings: newMemBindingStore(nil), Releases: newMemReleaseLedger(coverRel()),
		Git: allCommitsPresent("cccccccccccc"),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"ghcr.io/acme/shop": sha("cover"), "ghcr.io/acme/edge": sha("edge")}
	if !maps.Equal(plan.targetResolved, want) {
		t.Errorf("preview pins = %v, want the cover build for shop %v", plan.targetResolved, want)
	}
}

// What is refused: a cover digest on a persistent env. A release cut from a
// cover build that recorded it as SHARED (a cut that predates variants, or a
// hand-edited ledger) would otherwise put it on prod.
func TestPromotePlan_ForcedCoverDigestOnProdIsRefused(t *testing.T) {
	forced := coverRel()
	forced.Artifacts["ghcr.io/acme/shop"] = release.Artifact{Kind: release.KindOCI, Mode: release.ModeShared, Digests: map[string]string{release.SharedVariant: sha("cover")}}
	_, err := computePromotePlan(context.Background(), promotePlanOptions{
		Env: "prod", Version: "v1.4.0", ProjectDir: t.TempDir(),
		Bindings: newMemBindingStore(nil), Releases: newMemReleaseLedger(forced),
		Git: allCommitsPresent("cccccccccccc"),
	})
	if !errors.Is(err, release.ErrVariantOnPersistentEnv) {
		t.Fatalf("err = %v, want ErrVariantOnPersistentEnv", err)
	}
}

// The cut records a cover build beside the shared image, never in its place:
// a release build's state carries the cover digest it pushed beside the
// shared one, and a cover-built image (a preview's own build) records its
// digest as the variant only.
func TestHarvestReleaseArtifacts_CoverVariant(t *testing.T) {
	dir := t.TempDir()
	for _, st := range []buildtarget.State{
		{Service: "shop", Image: "ghcr.io/acme/shop", Tag: "v1.4.0", PushedAt: nowRFC3339(), Digest: sha("shared"), CoverDigest: sha("cover")},
		{Service: "edge", Image: "ghcr.io/acme/edge", Tag: "v1.4.0", PushedAt: nowRFC3339(), Digest: sha("edgecover"), Variant: release.CoverVariant},
	} {
		if err := buildtarget.WriteState(dir, "default", st); err != nil {
			t.Fatal(err)
		}
	}
	got := harvestReleaseArtifacts(dir, "")
	variant := func(d string) release.Artifact {
		return release.Artifact{Kind: release.KindOCI, Mode: release.ModeVariant, Digests: map[string]string{release.CoverVariant: d}}
	}
	want := map[string]release.Artifact{
		"ghcr.io/acme/shop":       {Kind: release.KindOCI, Mode: release.ModeShared, Digests: map[string]string{release.SharedVariant: sha("shared")}},
		"ghcr.io/acme/shop#cover": variant(sha("cover")),
		"ghcr.io/acme/edge#cover": variant(sha("edgecover")),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("harvest = %+v\nwant %+v", got, want)
	}
	r := release.Release{Version: "v1.4.0", Artifacts: got}
	if err := r.Validate(); err != nil {
		t.Errorf("the harvested release is invalid: %v", err)
	}
}

// A release build of an opted-in project's image marks it for a cover
// variant, unless the env or the build turned coverage off; an image the env
// already instruments is itself the cover build.
func TestApplyCover_ReleaseVariantEligibility(t *testing.T) {
	img := func(name string, cov map[string]bool) WorkloadEntity {
		return clusterWL(name, "k3d-x", "ns", pgoDocker(DockerBuild{Dockerfile: "Dockerfile", Coverage: cov}))
	}
	for _, tc := range []struct {
		name    string
		e       *KCLEntities
		optIn   bool
		variant bool
	}{
		{"persistent, opted in", &KCLEntities{Env: "staging", Project: "shop", Workloads: []WorkloadEntity{img("api", nil)}}, true, true},
		{"persistent, not opted in", &KCLEntities{Env: "staging", Project: "shop", Workloads: []WorkloadEntity{img("api", nil)}}, false, false},
		{"env says no", &KCLEntities{Env: "staging", Project: "shop", Tests: &TestsEntity{Coverage: boolp(false)}, Workloads: []WorkloadEntity{img("api", nil)}}, true, false},
		{"build says no", &KCLEntities{Env: "staging", Project: "shop", Workloads: []WorkloadEntity{img("api", map[string]bool{"./cmd/shop": false})}}, true, false},
		{"already the cover build", &KCLEntities{Env: "dev", Lifecycle: "local", Project: "shop", Workloads: []WorkloadEntity{img("api", nil)}}, true, false},
		// The binary's coverage is per binary, not per image: another
		// workload that runs ./cmd/shop and declares it off turns the
		// variant off for this image too, as a preview's own build would.
		{"another workload turns the binary off", &KCLEntities{Env: "staging", Project: "shop", Workloads: []WorkloadEntity{
			img("api", nil), img("api-2", map[string]bool{"cmd/shop": false}),
		}}, true, false},
	} {
		bins, err := ResolveCoverBinaries(tc.e, tc.optIn)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		tc.e.applyCover(bins, tc.optIn)
		if got := tc.e.Workloads[0].Build.Docker.coverVariant != nil; got != tc.variant {
			t.Errorf("%s: cover variant = %v, want %v", tc.name, got, tc.variant)
		}
	}
}

// The variant's argv is the shared build's, targeting the cover stage, with
// every tag suffixed -cover.
func TestCoverVariantArgs(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile("forge.yaml", []byte("name: shop\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("Dockerfile", []byte("FROM golang AS builder\nFROM alpine AS cover\nFROM alpine AS production\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := &DockerBuild{Dockerfile: "Dockerfile", coverVariant: &workloadCover{packages: []string{"./internal/..."}}}
	shared := []string{"build", "--platform=linux/amd64", "-t", "ghcr.io/acme/shop:v1.4.0", "-t", "localhost:5050/shop:v1.4.0", "-f", "Dockerfile", "."}
	got, err := coverVariantArgs(context.Background(), "shop", d, shared)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"build", "--target", "cover", "--build-arg", "FORGE_COVERPKG=./internal/...", "--platform=linux/amd64",
		"-t", "ghcr.io/acme/shop:v1.4.0-cover", "-t", "localhost:5050/shop:v1.4.0-cover", "-f", "Dockerfile", "."}
	if !slices.Equal(got, want) {
		t.Errorf("args = %v\nwant %v", got, want)
	}
	// Every spelling of a tag docker accepts is suffixed; an argv with no
	// tag at all builds no variant rather than push over the shared tags.
	long := []string{"build", "--tag", "ghcr.io/acme/shop:v1.4.0", "--tag=localhost:5050/shop:v1.4.0", "-t=a/b:v1", "-tc/d:v1", "-f", "Dockerfile", "."}
	got, err = coverVariantArgs(context.Background(), "shop", d, long)
	if err != nil {
		t.Fatal(err)
	}
	want = []string{"build", "--target", "cover", "--build-arg", "FORGE_COVERPKG=./internal/...",
		"--tag", "ghcr.io/acme/shop:v1.4.0-cover", "--tag=localhost:5050/shop:v1.4.0-cover", "-t=a/b:v1-cover", "-tc/d:v1-cover", "-f", "Dockerfile", "."}
	if !slices.Equal(got, want) {
		t.Errorf("long-form args = %v\nwant %v", got, want)
	}
	if _, err := coverVariantArgs(context.Background(), "shop", d, []string{"build", "-f", "Dockerfile", "."}); !errors.Is(err, errCoverVariantUntagged) {
		t.Errorf("an untagged argv: err %v, want errCoverVariantUntagged", err)
	}
	if dg, err := buildReleaseCoverVariant(context.Background(), "shop", d, []string{"build", "-f", "Dockerfile", "."}, []string{"ghcr.io/acme/shop:v1.4.0"}, false); dg != "" || err != nil {
		t.Errorf("an untagged argv built a variant (%q, %v)", dg, err)
	}
	for ref, want := range map[string]string{
		"ghcr.io/acme/shop:v1.4.0": "ghcr.io/acme/shop:v1.4.0-cover",
		"localhost:5050/shop":      "localhost:5050/shop:cover",
	} {
		if got := coverTag(ref); got != want {
			t.Errorf("coverTag(%q) = %q, want %q", ref, got, want)
		}
	}
	// No variant is built where there is nothing to push or no stage.
	for name, d := range map[string]*DockerBuild{"not marked": {Dockerfile: "Dockerfile"}, "own target": {Target: "debug", coverVariant: d.coverVariant}} {
		if dg, err := buildReleaseCoverVariant(context.Background(), "shop", d, shared, []string{"ghcr.io/acme/shop:v1.4.0"}, false); dg != "" || err != nil {
			t.Errorf("%s: built a variant (%q, %v)", name, dg, err)
		}
	}
	if dg, err := buildReleaseCoverVariant(context.Background(), "shop", d, shared, nil, false); dg != "" || err != nil {
		t.Errorf("a release that pushes nowhere built a variant (%q, %v)", dg, err)
	}
}

// A release image built from a GitSource Dockerfile gets its cover variant
// argv from the pinned file's cover stage, not a local Dockerfile; a pinned
// Dockerfile without the stage gets none (and builds nothing).
func TestCoverVariantArgs_GitSourceDockerfile(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("forge.yaml", []byte("name: shop\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	checkout := t.TempDir()
	withStage := filepath.Join(checkout, "with.Dockerfile")
	without := filepath.Join(checkout, "without.Dockerfile")
	for p, body := range map[string]string{withStage: "FROM alpine AS cover\nFROM alpine AS production\n", without: "FROM alpine AS production\n"} {
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	src := &config.GitSource{Repo: "github.com/acme/other", Ref: "v1"}
	shared := []string{"build", "-t", "ghcr.io/acme/shop:v1.4.0", "-f", withStage, "."}
	d := &DockerBuild{DockerfileSource: src, DockerfilePath: withStage, coverVariant: &workloadCover{}}
	got, err := coverVariantArgs(context.Background(), "shop", d, shared)
	if want := []string{"build", "--target", "cover", "-t", "ghcr.io/acme/shop:v1.4.0-cover", "-f", withStage, "."}; err != nil || !slices.Equal(got, want) {
		t.Errorf("args = %v (err %v), want %v", got, err, want)
	}
	noStage := &DockerBuild{DockerfileSource: src, DockerfilePath: without, coverVariant: &workloadCover{}}
	if dg, err := buildReleaseCoverVariant(context.Background(), "shop", noStage, shared, []string{"ghcr.io/acme/shop:v1.4.0"}, false); dg != "" || err != nil {
		t.Errorf("a pinned Dockerfile without the cover stage built a variant (%q, %v)", dg, err)
	}
}
