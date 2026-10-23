package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

// The two defects this file pins, both surfaced by adopting the
// registry-bearing image model (#322) in a project with a LIVE release
// binding:
//
//  1. A promotion ledger cut BEFORE that model keys its digests by BARE image
//     name, while every declared image is now a full repository reference. The
//     lookup misses, and each image silently falls back to its mutable env tag.
//  2. Nothing noticed. The render/deploy banner went on announcing "image
//     digests pinned" while emitting none, so a release-bound prod deploy would
//     have shipped an unpushed `git describe` tag.
//
// Both were reproduced against a real prod ledger (release v1.7.0) before the
// fix: all four images dropped their digests with no error and no warning.

// TestExpandLegacyLedgerKeys_BareNamesBecomeRepositories is defect 1. A ledger
// keyed by bare name must resolve against the FULL references the KCL declares,
// using the repository the release recorded in Artifact.URI.
//
// Before the fix this returned the bare keys untouched, so a workload
// declaring `us-central1-docker.pkg.dev/acme/prod/control-plane` found nothing.
func TestExpandLegacyLedgerKeys_BareNamesBecomeRepositories(t *testing.T) {
	const gar = "us-central1-docker.pkg.dev/reliant-labs-475814/reliant-prod"

	// A legacy promotion: bare names, exactly as prod's ledger holds them.
	resolved := map[string]string{
		"control-plane":  sha("a"),
		"reliant":        sha("b"),
		"workspace-base": sha("c"),
	}
	// The release records where each artifact actually lives.
	uris := map[string]string{
		"control-plane":  gar,
		"reliant":        gar,
		"workspace-base": gar,
	}

	got := expandLegacyLedgerKeys(resolved, uris)

	for name, want := range map[string]string{
		gar + "/control-plane":  sha("a"),
		gar + "/reliant":        sha("b"),
		gar + "/workspace-base": sha("c"),
	} {
		if got[name] != want {
			t.Errorf("digest for %q = %q, want %q — a legacy bare-name ledger must resolve against the declared repository", name, got[name], want)
		}
	}

	// The bare key is DROPPED, not kept as a parallel path: one key per
	// artifact. A surviving bare key would answer a future lookup with a
	// digest from whichever registry happened to be cut last.
	for _, bare := range []string{"control-plane", "reliant", "workspace-base"} {
		if _, stale := got[bare]; stale {
			t.Errorf("bare key %q survived expansion; expected exactly one key per artifact", bare)
		}
	}
}

// TestExpandLegacyLedgerKeys_ModernAndUnplaceableKeysSurvive: a ledger cut
// TODAY is already keyed by repository and must pass through untouched, and an
// artifact whose URI the release never recorded must stay visible (rather than
// vanish) so the fail-closed check can report it.
func TestExpandLegacyLedgerKeys_ModernAndUnplaceableKeysSurvive(t *testing.T) {
	const repo = "ghcr.io/acme/api"
	got := expandLegacyLedgerKeys(
		map[string]string{repo: sha("a"), "orphan": sha("b")},
		map[string]string{repo: "ghcr.io/acme"},
	)
	if got[repo] != sha("a") {
		t.Errorf("a repository-keyed entry must pass through verbatim, got %q", got[repo])
	}
	if got["orphan"] != sha("b") {
		t.Errorf("an artifact with no recorded URI must survive so it can be reported, got %q", got["orphan"])
	}
}

// TestResolveDeployDigests_LegacyLedgerResolvesDeclaredRepositories is defect 1
// end to end, through the real resolver and the real file ledger: a release cut
// with bare-name artifacts (URI recorded separately) that an env is promoted to
// must pin the repositories the KCL declares.
func TestResolveDeployDigests_LegacyLedgerResolvesDeclaredRepositories(t *testing.T) {
	dir := t.TempDir()
	const gar = "us-central1-docker.pkg.dev/reliant-labs-475814/reliant-prod"

	// A release in the legacy shape: keyed by bare name, repository in URI.
	if err := testCutRelease(t, dir, release.Release{
		Version: "v1.7.0",
		Artifacts: map[string]release.Artifact{
			"control-plane": {
				Kind: release.KindOCI, Mode: release.ModeShared,
				Digests: map[string]string{release.SharedVariant: sha("a")},
				URI:     gar,
			},
		},
	}); err != nil {
		t.Fatalf("write release: %v", err)
	}
	if _, err := testBindings(t, dir).Append(context.Background(), release.Promotion{
		Env: "prod", Release: "v1.7.0", Kind: release.KindPromote,
		Resolved: map[string]string{"control-plane": sha("a")},
	}, appendGuard{}); err != nil {
		t.Fatalf("write binding: %v", err)
	}

	digests, boundRel, err := resolveDeployDigests(
		context.Background(), dir, "prod", false,
		testBindings(t, dir), testReleases(t, dir),
	)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if boundRel != "v1.7.0" {
		t.Errorf("boundRelease = %q, want v1.7.0", boundRel)
	}
	want := gar + "/control-plane"
	if digests[want] != sha("a") {
		t.Errorf("digest for the DECLARED reference %q = %q, want %q.\n"+
			"A legacy ledger that cannot be read under the declared reference is what silently unpinned prod.",
			want, digests[want], sha("a"))
	}
}

// TestCheckReleasePinned_UnpinnedImageIsRefused is defect 2, the guard that
// turns a silent fall-through into a stop. A release-bound env with a declared,
// forge-built image that resolved no digest must ERROR, naming the image and
// the keys the ledger did hold.
func TestCheckReleasePinned_UnpinnedImageIsRefused(t *testing.T) {
	const gar = "us-central1-docker.pkg.dev/reliant-labs-475814/reliant-prod"
	entities := &KCLEntities{Workloads: []WorkloadEntity{{
		Name:    "admin-server",
		Image:   gar + "/control-plane",
		Build:   BuildConfigEntity{Type: "go"},
		Runtime: RuntimeEntity{Type: RuntimeCluster},
	}}}

	// The ledger holds only the BARE key — the pre-#322 shape that no longer
	// matches the declaration.
	err := checkReleasePinned(entities, map[string]string{"control-plane": sha("a")}, "v1.7.0", "prod")
	if err == nil {
		t.Fatal("a release-bound env with an unpinned declared image must ERROR, not fall through to the mutable tag")
	}
	for _, want := range []string{gar + "/control-plane", "admin-server", "v1.7.0", "control-plane"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must name %q so the fix is obvious; got:\n%s", want, err)
		}
	}
}

// TestCheckReleasePinned_PinnedAndUnboundPass: the guard fires ONLY on a
// release-bound env with a genuinely unpinned image. A fully pinned env, and
// any env with no binding at all, proceed untouched.
func TestCheckReleasePinned_PinnedAndUnboundPass(t *testing.T) {
	const repo = "ghcr.io/acme/api"
	entities := &KCLEntities{Workloads: []WorkloadEntity{{
		Name:    "api",
		Image:   repo,
		Build:   BuildConfigEntity{Type: "go"},
		Runtime: RuntimeEntity{Type: RuntimeCluster},
	}}}

	if err := checkReleasePinned(entities, map[string]string{repo: sha("a")}, "v2.0.0", "prod"); err != nil {
		t.Errorf("a fully pinned release-bound env must pass, got %v", err)
	}
	// No binding: digest pinning is not being claimed, so nothing to enforce.
	if err := checkReleasePinned(entities, nil, "", "dev"); err != nil {
		t.Errorf("an env with no release binding must pass, got %v", err)
	}
}

// TestCheckReleasePinned_BuildOnlyImageOnlyWhenReferenced: a build-only image
// is pulled only through a release_inputs reference (workspace-base, handed to
// daemon pods by a controller), so it must be pinned exactly then. A build-only
// image nothing references (workspace-runtime, the parent workspace-base is
// built FROM) renders into no object and must not make a release cut before it
// existed unrenderable.
func TestCheckReleasePinned_BuildOnlyImageOnlyWhenReferenced(t *testing.T) {
	const gar = "us-central1-docker.pkg.dev/acme/prod"
	entities := &KCLEntities{
		Workloads: []WorkloadEntity{
			{Name: "workspace-runtime", Image: gar + "/workspace-runtime", Build: BuildConfigEntity{Type: "shell"}, Runtime: RuntimeEntity{Type: RuntimeBuildOnly}},
			{Name: "workspace-base", Image: gar + "/workspace-base", Build: BuildConfigEntity{Type: "shell"}, Runtime: RuntimeEntity{Type: RuntimeBuildOnly}},
			{Name: "controller", Image: gar + "/control-plane", Build: BuildConfigEntity{Type: "go"}, Runtime: RuntimeEntity{Type: RuntimeCluster},
				ReleaseInputs: map[string]string{"workspace-base": gar + "/workspace-base:v1"}},
		},
		ReleaseManifestConsumers: []releaseManifestConsumer{{Name: "prepull", ReleaseInputs: map[string]string{"container:prepull": gar + "/prepull@" + sha("c")}}},
	}
	entities.Workloads = append(entities.Workloads, WorkloadEntity{Name: "prepull-image", Image: gar + "/prepull", Build: BuildConfigEntity{Type: "shell"}, Runtime: RuntimeEntity{Type: RuntimeBuildOnly}})
	pinned := map[string]string{gar + "/control-plane": sha("a"), gar + "/workspace-base": sha("b"), gar + "/prepull": sha("c")}
	if err := checkReleasePinned(entities, pinned, "v1.0.0", "prod"); err != nil {
		t.Fatalf("an unreferenced build-only image tripped the release pin check: %v", err)
	}
	for _, missing := range []string{gar + "/workspace-base", gar + "/prepull"} {
		partial := map[string]string{}
		for k, v := range pinned {
			if k != missing {
				partial[k] = v
			}
		}
		err := checkReleasePinned(entities, partial, "v1.0.0", "prod")
		if err == nil || !strings.Contains(err.Error(), missing) {
			t.Errorf("a referenced build-only image %s without a digest must be refused, got %v", missing, err)
		}
	}
}

// TestCheckReleasePinned_IgnoresImagesForgeDoesNotBuild: third-party images
// (declared with their own pinned tag, built by nobody here) and host/compose
// workloads pull nothing from a release, so they must not trip the guard.
func TestCheckReleasePinned_IgnoresImagesForgeDoesNotBuild(t *testing.T) {
	entities := &KCLEntities{Workloads: []WorkloadEntity{
		// Third party: no build.
		{Name: "nats", Image: "docker.io/library/nats:2.10-alpine", Runtime: RuntimeEntity{Type: RuntimeCluster}},
		// Built, but runs on the host — nothing pulls an image.
		{Name: "api-dev", Image: "ghcr.io/acme/api", Build: BuildConfigEntity{Type: "go"}, Runtime: RuntimeEntity{Type: RuntimeHost}},
	}}
	if err := checkReleasePinned(entities, map[string]string{}, "v2.0.0", "prod"); err != nil {
		t.Errorf("only images forge builds for a pulling runtime are checked, got %v", err)
	}
}
