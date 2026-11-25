package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/pkg/release"
)

// A third-party image on a hosted workload forge does not build is MIRRORED
// into the push base at release time (report 2af7cf85): the cut copies it,
// records the copy's digest under the mirror's address, and that address is
// the artifact key the deploy pins by. A bare image CI pushed is read under
// the push base rather than on Docker Hub, which is where `whoami` alone used
// to resolve ("imagetools inspect whoami: exit status 1").
func TestHarvestHostedBackendArtifacts_MirrorsThirdPartyAndResolvesBare(t *testing.T) {
	prevName := hostedProjectName
	hostedProjectName = func() string { return "shop" }
	t.Cleanup(func() { hostedProjectName = prevName })
	const org = "org-7"
	base := "registry.reliant.dev/" + org + "/shop"
	ents := &KCLEntities{
		ControlPlane: testControlPlane(org, "registry.reliant.dev"),
		Workloads: []WorkloadEntity{
			hostedWL("whoami", func(w *WorkloadEntity) { w.Spec.Image = "docker.io/traefik/whoami:v1.10.3" }),
			hostedWL("ci", func(w *WorkloadEntity) { w.Spec.Image = "ci:v2" }),
		},
	}

	var mirrored []string
	prevMirror := hostedImageMirror
	hostedImageMirror = func(_ context.Context, source, destination string) (deploytarget.MirroredImage, error) {
		mirrored = append(mirrored, source+" -> "+destination)
		return deploytarget.MirroredImage{Digest: "sha256:aaa", Platforms: []string{"linux/amd64"}}, nil
	}
	t.Cleanup(func() { hostedImageMirror = prevMirror })
	prevResolve := hostedBackendDigestResolver
	hostedBackendDigestResolver = func(_ context.Context, ref string) (string, []string, error) {
		if ref == base+"/ci:v2" {
			return "sha256:bbb", nil, nil
		}
		return "", nil, fmt.Errorf("unexpected lookup of %s", ref)
	}
	t.Cleanup(func() { hostedBackendDigestResolver = prevResolve })

	if got := hostedArtifactKey(ents, ents.Workloads[0]); got != base+"/traefik/whoami" {
		t.Fatalf("third-party artifact key = %q, want the mirror address %s/traefik/whoami", got, base)
	}
	out := map[string]release.Artifact{}
	if err := harvestHostedBackendArtifacts(context.Background(), "staging", ents, out); err != nil {
		t.Fatalf("harvest: %v", err)
	}
	if len(mirrored) != 1 || mirrored[0] != "docker.io/traefik/whoami:v1.10.3 -> "+base+"/traefik/whoami" {
		t.Errorf("mirrored = %v, want exactly the third-party image into %s/traefik/whoami", mirrored, base)
	}
	if a := out[base+"/traefik/whoami"]; a.Digests[release.SharedVariant] != "sha256:aaa" {
		t.Errorf("mirror artifact = %+v, want digest sha256:aaa", a)
	}
	if a := out[base+"/ci"]; a.Digests[release.SharedVariant] != "sha256:bbb" {
		t.Errorf("bare CI-pushed artifact = %+v (all: %v), want digest sha256:bbb keyed by %s/ci", a, out, base)
	}

	// The deploy pins the mirror, which the platform admits.
	spec := ents.Workloads[0].Spec
	records, err := deploytarget.HostedRecords(deploytarget.ServiceGroup{
		Env: "staging",
		Hosted: &deploytarget.HostedTarget{Release: "v1", PushBase: base,
			Digests: map[string]string{base + "/traefik/whoami": "sha256:aaa"}},
		Services: []deploytarget.ResolvedService{{Name: "whoami", Hosted: &deploytarget.HostedWorkload{
			Tier: deploytarget.HostedTierWorkload, Workload: &spec,
			Artifact: hostedArtifactKey(ents, ents.Workloads[0]),
		}}},
	})
	if err != nil {
		t.Fatalf("hosted records: %v", err)
	}
	want := base + "/traefik/whoami@sha256:aaa"
	found := false
	for _, r := range records {
		found = found || strings.Contains(string(r.YAML), want)
	}
	if !found {
		t.Errorf("the published workload does not pin the mirror %s:\n%v", want, records)
	}
}

// A mirrored third-party image is held to the platform guard like a built one:
// a single-arch image for the wrong platform would crash-loop with "exec format
// error". An unbuilt image keyed by name alone (under the base, CI-pushed) is
// left to the deploy, as before.
func TestHostedPlatformGuard_ChecksMirroredImages(t *testing.T) {
	prevName := hostedProjectName
	hostedProjectName = func() string { return "shop" }
	t.Cleanup(func() { hostedProjectName = prevName })
	e := &KCLEntities{
		ControlPlane: testControlPlane("org-7", "registry.reliant.dev"),
		Workloads: []WorkloadEntity{hostedWL("whoami", func(w *WorkloadEntity) {
			w.Spec.Image = "docker.io/acme/armonly:v1"
		})},
	}
	key := hostedArtifactKey(e, e.Workloads[0])
	arts := map[string]release.Artifact{key: {Kind: release.KindOCI, Platforms: []string{"linux/arm64"},
		Digests: map[string]string{release.SharedVariant: "sha256:" + strings.Repeat("a", 64)}}}
	err := checkHostedArtifactPlatforms(context.Background(), io.Discard, e, arts, "staging", "v1")
	if err == nil || !strings.Contains(err.Error(), "expected linux/amd64, found linux/arm64") {
		t.Fatalf("a mirrored arm64-only image must be refused, got %v", err)
	}
}

// The cut refuses, before copying anything, a mirror destination the registry
// cannot hold and two different sources that would share one destination
// (review B1 on #2016). Two spellings of one Docker Hub reference are one
// source and mirror fine.
func TestHarvestHostedBackendArtifacts_RefusesUnrepresentableMirrors(t *testing.T) {
	prevName := hostedProjectName
	hostedProjectName = func() string { return "shop" }
	t.Cleanup(func() { hostedProjectName = prevName })
	var mirrored []string
	prevMirror := hostedImageMirror
	hostedImageMirror = func(_ context.Context, source, destination string) (deploytarget.MirroredImage, error) {
		mirrored = append(mirrored, source+" -> "+destination)
		return deploytarget.MirroredImage{Digest: "sha256:aaa"}, nil
	}
	t.Cleanup(func() { hostedImageMirror = prevMirror })
	harvest := func(images ...string) error {
		t.Helper()
		mirrored = nil
		ents := &KCLEntities{ControlPlane: testControlPlane("org-7", "registry.reliant.dev")}
		for i, img := range images {
			img := img
			ents.Workloads = append(ents.Workloads, hostedWL(fmt.Sprintf("w%d", i), func(w *WorkloadEntity) { w.Spec.Image = img }))
		}
		return harvestHostedBackendArtifacts(context.Background(), "staging", ents, map[string]release.Artifact{})
	}

	for _, tc := range []struct {
		name   string
		images []string
		want   string
	}{
		{"two registries, one path", []string{"ghcr.io/acme/api:v1", "docker.io/acme/api:v1"}, "would both be mirrored to registry.reliant.dev/org-7/shop/acme/api"},
		{"one repository, two tags", []string{"docker.io/library/redis:6", "docker.io/redis:7"}, "would both be mirrored"},
		{"a destination past the length limit", []string{"ghcr.io/acme/" + strings.Repeat("a", 250)}, "over the 255"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := harvest(tc.images...)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "nothing was copied") {
				t.Fatalf("harvest(%v) = %v, want a refusal containing %q", tc.images, err, tc.want)
			}
			if len(mirrored) != 0 {
				t.Errorf("a refused cut copied %v", mirrored)
			}
		})
	}

	if err := harvest("docker.io/nginx:1.27", "index.docker.io/library/nginx:1.27"); err != nil {
		t.Fatalf("two spellings of one Docker Hub reference must mirror: %v", err)
	}
	if len(mirrored) != 1 || !strings.HasSuffix(mirrored[0], " -> registry.reliant.dev/org-7/shop/library/nginx") {
		t.Errorf("mirrored = %v, want one copy into .../library/nginx", mirrored)
	}
}
