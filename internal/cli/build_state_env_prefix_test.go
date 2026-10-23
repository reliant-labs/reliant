package cli

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/buildtarget"
)

// Per-service build state lives at .forge/state/build-<env>-<service>.json,
// and the digest resolver finds those files by globbing
// "build-<env>-*.json". That pattern is ambiguous whenever one env's name is
// a prefix of another's: globbing for env "dev" also matches every
// "build-dev-k8s-*.json", because the glob cannot tell the "-" that separates
// env from service apart from the "-" inside "dev-k8s".
//
// Both files declare the same image, so they collide on the same key in the
// image→digest map and the last one wins by glob order — which is
// alphabetical, so "build-dev-k8s-daemon-gateway.json" lands AFTER
// "build-dev-daemon-gateway.json" and overwrites the fresher digest with a
// stale one from an env nobody deployed.
//
// Observed in control-plane: `forge env up dev` built and pushed
// reliant-daemon-gateway correctly, recorded the new digest in
// build-dev-daemon-gateway.json, and then deployed the digest from
// build-dev-k8s-daemon-gateway.json — an image built 23 days earlier. The
// rollout reported success, the pod never changed, and nothing in the output
// connected the two. Every `forge env up dev` after that was a silent no-op
// for that service.
func TestResolveDeployImageDigests_IgnoresPrefixSiblingEnv(t *testing.T) {
	dir := t.TempDir()
	freshDigest := "sha256:" + strings.Repeat("a", 64)
	staleDigest := "sha256:" + strings.Repeat("b", 64)

	// The env being deployed.
	if err := buildtarget.WriteState(dir, "dev", buildtarget.State{
		Service: "daemon-gateway", Image: "reliant-daemon-gateway",
		Tag: "dev", Digest: freshDigest,
	}); err != nil {
		t.Fatalf("write dev state: %v", err)
	}
	// A DIFFERENT env whose name begins with "dev-". Alphabetically after the
	// dev file, so an env-prefix glob lets it win.
	if err := buildtarget.WriteState(dir, "dev-k8s", buildtarget.State{
		Service: "daemon-gateway", Image: "reliant-daemon-gateway",
		Tag: "dev-k8s", Digest: staleDigest,
	}); err != nil {
		t.Fatalf("write dev-k8s state: %v", err)
	}

	got, err := resolveDeployImageDigests(dir, "dev", false)
	if err != nil {
		t.Fatalf("resolveDeployImageDigests: %v", err)
	}

	if got["reliant-daemon-gateway"] == staleDigest {
		t.Fatalf("deploying env %q picked up env %q's digest %s — a sibling env whose "+
			"name shares the %q prefix must not contribute digests",
			"dev", "dev-k8s", shortDigest(staleDigest), "dev-")
	}
	if got["reliant-daemon-gateway"] != freshDigest {
		t.Fatalf("reliant-daemon-gateway digest = %q, want the dev build's %q",
			got["reliant-daemon-gateway"], freshDigest)
	}
}

// The reverse direction: deploying the LONGER env name must still find its own
// state. A fix that filters by counting separators, or that refuses any file
// whose service segment contains "-", would break this — service names
// legitimately contain hyphens ("daemon-gateway"), which is precisely why the
// filename is ambiguous in the first place.
func TestResolveDeployImageDigests_PrefixSiblingEnvResolvesItsOwn(t *testing.T) {
	dir := t.TempDir()
	devDigest := "sha256:" + strings.Repeat("c", 64)
	k8sDigest := "sha256:" + strings.Repeat("d", 64)

	if err := buildtarget.WriteState(dir, "dev", buildtarget.State{
		Service: "daemon-gateway", Image: "reliant-daemon-gateway",
		Tag: "dev", Digest: devDigest,
	}); err != nil {
		t.Fatalf("write dev state: %v", err)
	}
	if err := buildtarget.WriteState(dir, "dev-k8s", buildtarget.State{
		Service: "daemon-gateway", Image: "reliant-daemon-gateway",
		Tag: "dev-k8s", Digest: k8sDigest,
	}); err != nil {
		t.Fatalf("write dev-k8s state: %v", err)
	}

	got, err := resolveDeployImageDigests(dir, "dev-k8s", false)
	if err != nil {
		t.Fatalf("resolveDeployImageDigests: %v", err)
	}
	if got["reliant-daemon-gateway"] != k8sDigest {
		t.Fatalf("env %q resolved digest %q, want its own %q",
			"dev-k8s", got["reliant-daemon-gateway"], k8sDigest)
	}
}

// A service whose name itself contains what looks like an env suffix must
// still resolve. "build-dev-k8s-gateway.json" is genuinely ambiguous between
// (env=dev, service=k8s-gateway) and (env=dev-k8s, service=gateway); the
// resolver must not silently attribute it to the wrong env.
func TestResolveDeployImageDigests_ServiceNameLooksLikeEnvSuffix(t *testing.T) {
	dir := t.TempDir()
	digest := "sha256:" + strings.Repeat("e", 64)

	if err := buildtarget.WriteState(dir, "dev", buildtarget.State{
		Service: "k8s-gateway", Image: "k8s-gateway-image",
		Tag: "dev", Digest: digest,
	}); err != nil {
		t.Fatalf("write state: %v", err)
	}

	got, err := resolveDeployImageDigests(dir, "dev", false)
	if err != nil {
		t.Fatalf("resolveDeployImageDigests: %v", err)
	}
	if got["k8s-gateway-image"] != digest {
		t.Fatalf("service %q under env %q resolved %q, want %q — a service name "+
			"that resembles an env suffix must still belong to its own env",
			"k8s-gateway", "dev", got["k8s-gateway-image"], digest)
	}
}
