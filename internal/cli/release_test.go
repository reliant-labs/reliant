package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/buildtarget"

	"github.com/reliant-labs/forge/pkg/release"
)

// TestReleaseFileStem_PreservesDots pins Fix 3: a semver version's literal
// label IS its filename stem (dots are filesystem-safe), so "v1.0.0" maps
// to "v1.0.0", NOT the surprising "v1_0_0" the old statefile.SafeSegment
// produced. Path separators and other unsafe bytes still flatten to '_',
// and a dot-only traversal token is neutralized.
func TestReleaseFileStem_PreservesDots(t *testing.T) {
	cases := []struct{ in, want string }{
		{"v1.0.0", "v1.0.0"}, // the Fix 3 case: dots preserved, least surprise
		{"v1.4.0", "v1.4.0"}, //
		{"1.2.3-rc.1", "1.2.3-rc.1"},
		{"v2", "v2"},   // no dots, unchanged
		{"a/b", "a_b"}, // path separator flattened — never escapes the dir
		{`a\b`, "a_b"}, // backslash too
		{"a b", "a_b"}, // space flattened
		{".", "_"},     // bare dot is a dir ref, neutralized
		{"..", "__"},   // parent-dir traversal neutralized
		{"...", "___"}, //
		{"", "_"},      // empty never yields an empty stem
	}
	for _, c := range cases {
		if got := releaseFileStem(c.in); got != c.want {
			t.Errorf("releaseFileStem(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestReleaseVersionRoundTrips is the cross-command consistency guard: build
// (Cut) and promote/deploy (Get) resolve a version label to the SAME record.
//
// It no longer asserts a FILENAME. The retired backend stored one
// .forge/releases/<stem>.json per version, which needed a lossy
// version→filename mapping and a test that the mapping round-tripped
// ("v1.0.0" had to land at v1.0.0.json, not the surprising v1_0_0.json). The
// machine ledger is an append-only log keyed by the version INSIDE each
// record, so there is no filename to get wrong and nothing to round-trip
// through. What still matters — and is what this now pins — is that a
// release written under a label reads back under that same label.
func TestReleaseVersionRoundTrips(t *testing.T) {
	dir := newLedgerTestProject(t, "roundtrip-project")
	const version = "v1.0.0"

	rel := release.Release{
		Version: version,
		Artifacts: map[string]release.Artifact{
			"control-plane": {Kind: release.KindOCI, Mode: release.ModeShared, Digests: map[string]string{"*": sha("a")}},
		},
	}
	if err := testCutRelease(t, dir, rel); err != nil {
		t.Fatalf("cut: %v", err)
	}
	got, err := testGetRelease(t, dir, version)
	if err != nil || got == nil {
		t.Fatalf("testGetRelease(%q) = (%v, %v), want a record", version, got, err)
	}
	if got.Version != version {
		t.Errorf("round-tripped version = %q, want %q", got.Version, version)
	}
	// Nothing was written into the checkout.
	if _, err := os.Stat(filepath.Join(dir, ".forge", "releases")); !os.IsNotExist(err) {
		t.Errorf("releases must not land in the checkout (stat err: %v)", err)
	}
}

// sha is a canonical fixture digest. A single hex character repeats (sha("a")
// is sha256:aaaa…); any other label is hashed, so sha("same") and sha("new")
// are distinct, stable and — which release.Validate now requires — canonical.
func sha(c string) string {
	if len(c) == 1 && strings.Contains("0123456789abcdef", c) {
		return "sha256:" + strings.Repeat(c, 64)
	}
	sum := sha256.Sum256([]byte(c))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// TestRelease_LedgerRoundTrip locks the on-disk contract: a Release written by
// `forge env build --release` reads back intact, including the per-image shared
// digest map and platforms.
func TestRelease_LedgerRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := release.Release{
		Version: "v1.4.0",
		Git:     release.Git{Commit: "8a7be2b", Tag: "v1.4.0", Dirty: false},
		Artifacts: map[string]release.Artifact{
			"control-plane": {Kind: release.KindOCI, Mode: release.ModeShared, Digests: map[string]string{"*": sha("a")}, Platforms: []string{"linux/amd64"}},
			"reliant":       {Kind: release.KindOCI, Mode: release.ModeShared, Digests: map[string]string{"*": sha("b")}},
		},
	}
	if err := testCutRelease(t, dir, want); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := testGetRelease(t, dir, "v1.4.0")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got == nil {
		t.Fatal("read returned nil")
	}
	if !reflect.DeepEqual(*got, want) {
		t.Errorf("round-trip mismatch:\n  want %+v\n  got  %+v", want, *got)
	}
}

// TestReadRelease_MissingIsNilNil keeps "no such release" distinct from an
// error so promote can produce a friendly not-found message.
func TestReadRelease_MissingIsNilNil(t *testing.T) {
	got, err := testGetRelease(t, t.TempDir(), "v9.9.9")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != nil {
		t.Fatalf("want nil, got %+v", got)
	}
}

// TestHarvestReleaseArtifacts proves the release ledger is a projection of the
// EXISTING digest capture: the aggregate project build state AND the
// per-service external-build states both feed the harvested artifact map, each
// image carrying its OWN captured digest.
func TestHarvestReleaseArtifacts(t *testing.T) {
	dir := t.TempDir()
	// Aggregate project image (what dockerBuildProject captures), written under
	// the env-agnostic "default" key a plain `forge env build --release` produces.
	if err := WriteBuildState(dir, "default", BuildState{
		Image: "control-plane", Tag: "v1.4.0", Pushed: true, PushedAt: nowRFC3339(),
		Digest: sha("a"), Platforms: []string{"linux/amd64"},
	}); err != nil {
		t.Fatalf("write aggregate: %v", err)
	}
	// Per-service external-build states (reliant, workspace-base) keyed to the
	// SAME env the harvest runs against (empty env → "default").
	for img, c := range map[string]string{"reliant": "b", "workspace-base": "d"} {
		if err := buildtarget.WriteState(dir, "default", buildtarget.State{
			Service: img, Image: img, Tag: "v1.4.0", PushedAt: nowRFC3339(), Digest: sha(c),
		}); err != nil {
			t.Fatalf("write per-service %s: %v", img, err)
		}
	}

	// Empty env harvests the "default" records (buildStateLookupEnvs("") == ["default"]).
	got := harvestReleaseArtifacts(dir, "")
	// Kind is stamped explicitly on every harvested artifact: a build captures
	// container images, and a ledger cut today says so rather than relying on
	// the empty-means-OCI default that exists only for pre-kind files.
	want := map[string]release.Artifact{
		"control-plane":  {Kind: release.KindOCI, Mode: release.ModeShared, Digests: map[string]string{"*": sha("a")}, Platforms: []string{"linux/amd64"}},
		"reliant":        {Kind: release.KindOCI, Mode: release.ModeShared, Digests: map[string]string{"*": sha("b")}},
		"workspace-base": {Kind: release.KindOCI, Mode: release.ModeShared, Digests: map[string]string{"*": sha("d")}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("harvest mismatch:\n  want %+v\n  got  %+v", want, got)
	}
}

// TestHarvestReleaseArtifacts_KeyIsTheRepository pins the coordinate that makes
// an OCI artifact VERIFIABLE rather than merely named.
//
// A digest says what the bytes are; it does not say which host serves them. A
// ledger that recorded `sha256:…` against a BARE image name therefore named an
// image nobody could look up, and `forge release verify` could only report it
// UNVERIFIABLE — the same "claim nothing checks" shape that let v0.1.12 ship a
// package that was never published.
//
// The fix is the KEY: an artifact is keyed by the repository its workload
// declared, host included, so the key IS the address. There is no URI field to
// drift from it, and two envs building the same app to two registries stay two
// entries rather than collapsing onto one — which is what would let a prod
// promotion read a digest that only exists in the dev registry.
//
// A build that pushed NOWHERE keeps its hostless key: inventing a host would
// turn an honest "cannot check this" into a spurious failure against a registry
// that was never involved.
func TestHarvestReleaseArtifacts_KeyIsTheRepository(t *testing.T) {
	dir := t.TempDir()
	if err := WriteBuildState(dir, "default", BuildState{
		Image: "ghcr.io/reliant-labs/control-plane", Tag: "v1.4.0",
		Pushed: true, PushedAt: nowRFC3339(), Digest: sha("a"),
	}); err != nil {
		t.Fatalf("write aggregate: %v", err)
	}
	// An external build pushed to a DIFFERENT registry — each artifact keeps
	// its own host, not one borrowed from a sibling.
	if err := buildtarget.WriteState(dir, "default", buildtarget.State{
		Service: "reliant", Image: "us-central1-docker.pkg.dev/proj/repo/reliant",
		Tag: "v1.4.0", PushedAt: nowRFC3339(), Digest: sha("b"),
	}); err != nil {
		t.Fatalf("write per-service: %v", err)
	}
	// A local build that pushed nowhere: its key names no host.
	if err := buildtarget.WriteState(dir, "default", buildtarget.State{
		Service: "local-only", Image: "local-only", Tag: "dev",
		PushedAt: nowRFC3339(), Digest: sha("c"),
	}); err != nil {
		t.Fatalf("write local-only: %v", err)
	}

	got := harvestReleaseArtifacts(dir, "")

	for _, key := range []string{
		"ghcr.io/reliant-labs/control-plane",
		"us-central1-docker.pkg.dev/proj/repo/reliant",
		"local-only",
	} {
		art, ok := got[key]
		if !ok {
			t.Fatalf("%s missing from harvest — the key must be the declared repository", key)
		}
		// No URI: the key is the address, so a second copy could only disagree.
		if art.URI != "" {
			t.Errorf("%s recorded a URI %q; the key is the address", key, art.URI)
		}
	}
	// The two pushed images resolve to their own hosts; the local one to none.
	for key, wantHost := range map[string]string{
		"ghcr.io/reliant-labs/control-plane":           "ghcr.io",
		"us-central1-docker.pkg.dev/proj/repo/reliant": "us-central1-docker.pkg.dev",
		"local-only": "",
	} {
		if host, _ := ociManifestCoordinates(key); host != wantHost {
			t.Errorf("%s resolves to host %q, want %q", key, host, wantHost)
		}
	}
}

// TestHarvestReleaseArtifacts_SkipsDigestless confirms an image with no
// captured digest is omitted — a release records only content-addressed bytes.
func TestHarvestReleaseArtifacts_SkipsDigestless(t *testing.T) {
	dir := t.TempDir()
	if err := WriteBuildState(dir, "default", BuildState{
		Image: "control-plane", Tag: "v1.4.0", PushedAt: nowRFC3339(), // no Digest
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := harvestReleaseArtifacts(dir, ""); len(got) != 0 {
		t.Errorf("want empty (no digests captured), got %+v", got)
	}
}

// TestResolveReleaseDigests flattens shared artifacts to the image→digest map
// the deploy/promote paths consume, and errors when a release pins nothing.
func TestResolveReleaseDigests(t *testing.T) {
	rel := release.Release{
		Version: "v1.4.0",
		Artifacts: map[string]release.Artifact{
			"control-plane": {Kind: release.KindOCI, Mode: release.ModeShared, Digests: map[string]string{"*": sha("a")}},
			"reliant":       {Kind: release.KindOCI, Mode: release.ModeShared, Digests: map[string]string{"*": sha("b")}},
		},
	}
	got, err := resolveReleaseDigests(rel)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	want := map[string]string{"control-plane": sha("a"), "reliant": sha("b")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("resolve mismatch:\n  want %+v\n  got  %+v", want, got)
	}

	if _, err := resolveReleaseDigests(release.Release{Version: "v0", Artifacts: map[string]release.Artifact{}}); err == nil {
		t.Error("want error for a release with no shared digests, got nil")
	}
}

// TestResolveReleaseDigests_EmptyArtifactsActionableError pins the friction
// fix: a release cut with an EMPTY artifact map (forgot --push, no services
// built, all external builds skipped) must fail with an ACTIONABLE message that
// names the version, the likely causes, and `forge project audit` — not the old vague
// "carries no shared image digests to pin" that read like an internal invariant.
func TestResolveReleaseDigests_EmptyArtifactsActionableError(t *testing.T) {
	_, err := resolveReleaseDigests(release.Release{Version: "v1.4.0", Artifacts: map[string]release.Artifact{}})
	if err == nil {
		t.Fatal("want error for a release with no artifacts, got nil")
	}
	msg := err.Error()
	for _, want := range []string{
		`"v1.4.0"`, // names the offending release
		"no image digests",
		"--push",              // the most common cause/remedy
		"build_cwd",           // external-build-skip cause
		"forge project audit", // the inspect next-step
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("empty-artifacts error missing %q\n  got: %s", want, msg)
		}
	}
}

// TestResolveReleaseDigests_VariantOnlyError confirms a release that carries
// artifacts but only variant-mode ones (no shared digest the MVP can pin)
// fails with a message distinct from the empty-artifacts case, so the user
// isn't told to re-run --push when the real situation is "variant promotion
// isn't supported yet".
func TestResolveReleaseDigests_VariantOnlyError(t *testing.T) {
	_, err := resolveReleaseDigests(release.Release{
		Version: "v2.0.0",
		Artifacts: map[string]release.Artifact{
			// variant-mode: a digest keyed by an env variant, NOT release.SharedVariant,
			// so SharedDigest() returns ("", false).
			"control-plane": {Kind: release.KindOCI, Mode: release.ModeVariant, Digests: map[string]string{"prod": sha("a")}},
		},
	})
	if err == nil {
		t.Fatal("want error for a variant-only release, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "variant") {
		t.Errorf("variant-only error should mention variant artifacts\n  got: %s", msg)
	}
	if strings.Contains(msg, "--push") {
		t.Errorf("variant-only error should NOT suggest --push (wrong remedy)\n  got: %s", msg)
	}
}

// TestRunPromote_NoImagesReleaseSurfacesActionableError proves a release that
// pins no container image fails at promote (no binding written) with guidance
// that names what the release actually holds. An EMPTY release can no longer
// be cut at all — release.Validate refuses it — so the reachable shape is a
// release of only packages.
func TestRunPromote_NoImagesReleaseSurfacesActionableError(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := testCutRelease(t, dir, release.Release{
		Version: "v3.1.0",
		Artifacts: map[string]release.Artifact{
			"@acme/ui": {Kind: release.KindNPM, Mode: release.ModeShared, Version: "1.0.0"},
		},
	}); err != nil {
		t.Fatalf("write release: %v", err)
	}

	err := runPromote(context.Background(), "v3.1.0", "staging", promoteOptions{Ledger: declaredLedger(t, dir, "staging")})
	if err == nil {
		t.Fatal("want error promoting a release with no images, got nil")
	}
	if !strings.Contains(err.Error(), "no container images") {
		t.Errorf("promote error should say the release has no images\n  got: %s", err.Error())
	}
	if _, bound, berr := testBindings(t, dir).Current(context.Background(), "staging"); berr != nil {
		t.Fatalf("read binding: %v", berr)
	} else if bound {
		t.Error("staging must NOT be bound when the release pins no digests")
	}
}

// TestWriteRelease_EmptyIsRefused: a release naming nothing cannot be cut.
func TestWriteRelease_EmptyIsRefused(t *testing.T) {
	err := testCutRelease(t, t.TempDir(), release.Release{Version: "v3.1.0", Artifacts: map[string]release.Artifact{}})
	if !errors.Is(err, release.ErrInvalid) {
		t.Fatalf("an empty release must be refused as invalid, got %v", err)
	}
}

// TestResolveDeployDigests_BoundEnvUsesRelease proves the load-bearing
// precedence: when an env is bound to a release, the deploy resolves the
// digests from the RELEASE binding — NOT the per-env build state — so a
// promoted env ships the release's bytes.
func TestResolveDeployDigests_BoundEnvUsesRelease(t *testing.T) {
	dir := t.TempDir()

	// Per-env build state holds a DIFFERENT (older) digest for control-plane.
	if err := WriteBuildState(dir, "prod", BuildState{
		Image: "control-plane", Tag: "old", Pushed: true, PushedAt: nowRFC3339(), Digest: sha("0"),
	}); err != nil {
		t.Fatalf("write build state: %v", err)
	}
	// prod is promoted to v1.4.0, whose control-plane digest is sha(a).
	if _, err := testBindings(t, dir).Append(context.Background(), release.Promotion{
		Env: "prod", Release: "v1.4.0", Kind: release.KindPromote,
		Resolved: map[string]string{"control-plane": sha("a"), "reliant": sha("b")},
	}, appendGuard{}); err != nil {
		t.Fatalf("write bindings: %v", err)
	}

	digests, boundRel, err := resolveDeployDigests(context.Background(), dir, "prod", false, testBindings(t, dir), testReleases(t, dir))
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if boundRel != "v1.4.0" {
		t.Errorf("boundRelease = %q, want v1.4.0", boundRel)
	}
	// The RELEASE digest wins over the per-env build-state digest.
	if digests["control-plane"] != sha("a") {
		t.Errorf("control-plane = %q, want release digest %q (release must override per-env build state)", digests["control-plane"], sha("a"))
	}
	if digests["reliant"] != sha("b") {
		t.Errorf("reliant = %q, want %q", digests["reliant"], sha("b"))
	}
}

// TestResolveDeployDigests_ReleaseOverridesTaggedKeys proves a release binding
// wins for a TAG-PINNED workload as well as an unpinned one.
//
// A release ledger records a digest per image NAME and carries no tag, while a
// workload declaring `reliant:e2e` is resolved by the tag-qualified key. So an
// overlay that wrote only the bare key would leave the tag-pinned workloads
// resolving to the local build state's digest — the env would report the
// release deployed while shipping something else entirely. Every tag-qualified
// key of an image the release pins must follow the release.
func TestResolveDeployDigests_ReleaseOverridesTaggedKeys(t *testing.T) {
	dir := t.TempDir()
	if err := buildtarget.WriteState(dir, "prod", buildtarget.State{
		Service: "reliant-api-server", Image: "reliant", Tag: "e2e",
		PushedAt: nowRFC3339(), Digest: sha("0"),
	}); err != nil {
		t.Fatalf("write build state: %v", err)
	}
	if _, err := testBindings(t, dir).Append(context.Background(), release.Promotion{
		Env: "prod", Release: "v1.4.0", Kind: release.KindPromote,
		Resolved: map[string]string{"reliant": sha("a")},
	}, appendGuard{}); err != nil {
		t.Fatalf("write bindings: %v", err)
	}

	digests, boundRel, err := resolveDeployDigests(context.Background(), dir, "prod", false, testBindings(t, dir), testReleases(t, dir))
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if boundRel != "v1.4.0" {
		t.Errorf("boundRelease = %q, want v1.4.0", boundRel)
	}
	for _, key := range []string{"reliant", "reliant:e2e"} {
		if digests[key] != sha("a") {
			t.Errorf("%s = %q, want the release digest %q", key, digests[key], sha("a"))
		}
	}
}

// TestResolveDeployDigests_UnboundEnvFallsBack proves full backward compat: an
// env with NO release binding resolves exactly the per-env build-state digests,
// with an empty bound release — byte-identical to the pre-release flow.
func TestResolveDeployDigests_UnboundEnvFallsBack(t *testing.T) {
	dir := t.TempDir()
	if err := WriteBuildState(dir, "staging", BuildState{
		Image: "control-plane", Tag: "v1.4.0", Pushed: true, PushedAt: nowRFC3339(), Digest: sha("c"),
	}); err != nil {
		t.Fatalf("write build state: %v", err)
	}

	digests, boundRel, err := resolveDeployDigests(context.Background(), dir, "staging", false, testBindings(t, dir), testReleases(t, dir))
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if boundRel != "" {
		t.Errorf("boundRelease = %q, want empty (no binding)", boundRel)
	}
	if digests["control-plane"] != sha("c") {
		t.Errorf("control-plane = %q, want per-env build-state digest %q", digests["control-plane"], sha("c"))
	}
}

// TestResolveDeployDigests_NoDigestSkipsRelease confirms --no-digest disables
// the release lookup too (the tag-only escape hatch overrides everything).
func TestResolveDeployDigests_NoDigestSkipsRelease(t *testing.T) {
	dir := t.TempDir()
	if _, err := testBindings(t, dir).Append(context.Background(), release.Promotion{
		Env: "prod", Release: "v1.4.0", Kind: release.KindPromote, Resolved: map[string]string{"control-plane": sha("a")},
	}, appendGuard{}); err != nil {
		t.Fatalf("write bindings: %v", err)
	}

	digests, boundRel, err := resolveDeployDigests(context.Background(), dir, "prod", true /* noDigest */, testBindings(t, dir), testReleases(t, dir))
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if boundRel != "" || len(digests) != 0 {
		t.Errorf("--no-digest should yield no digests and no bound release, got digests=%v release=%q", digests, boundRel)
	}
}

// TestRunPromote_WritesBinding exercises the promote command end-to-end against
// a release ledger, proving it records the env→release binding with the
// resolved digests snapshotted from the release.
func TestRunPromote_WritesBinding(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	// A release ledger exists (as `forge env build --release` would have written).
	if err := testCutRelease(t, dir, release.Release{
		Version: "v1.4.0",
		Artifacts: map[string]release.Artifact{
			"control-plane": {Kind: release.KindOCI, Mode: release.ModeShared, Digests: map[string]string{"*": sha("a")}},
			"reliant":       {Kind: release.KindOCI, Mode: release.ModeShared, Digests: map[string]string{"*": sha("b")}},
		},
	}); err != nil {
		t.Fatalf("write release: %v", err)
	}

	if err := runPromote(context.Background(), "v1.4.0", "staging", promoteOptions{Ledger: declaredLedger(t, dir, "staging")}); err != nil {
		t.Fatalf("promote: %v", err)
	}

	binding, bound, err := testBindings(t, dir).Current(context.Background(), "staging")
	if err != nil {
		t.Fatalf("read binding: %v", err)
	}
	if !bound {
		t.Fatal("staging should be bound after promote")
	}
	if binding.Release != "v1.4.0" {
		t.Errorf("binding.Release = %q, want v1.4.0", binding.Release)
	}
	want := map[string]string{"control-plane": sha("a"), "reliant": sha("b")}
	if !reflect.DeepEqual(binding.Resolved, want) {
		t.Errorf("binding.Resolved = %+v, want %+v", binding.Resolved, want)
	}
}

// TestRunPromote_UnknownReleaseErrors confirms promoting a release that was
// never cut fails with a clear not-found rather than writing a bad binding.
func TestRunPromote_UnknownReleaseErrors(t *testing.T) {
	dir := useTestLedger(t, t.TempDir())
	t.Chdir(dir)
	if err := runPromote(context.Background(), "v9.9.9", "staging", promoteOptions{Ledger: declaredLedger(t, dir, "staging")}); err == nil {
		t.Fatal("want error promoting a non-existent release, got nil")
	}
}

// TestReleaseLedger_KindlessArtifactIsRefused: an artifact with no kind is
// not read as an image. The retired "empty kind means OCI" default is how a
// record nobody can classify could have been deployed.
//
// The malformed record is planted as a LINE in the machine ledger, which is
// where releases live now; the rule being tested (a record that does not
// satisfy release.Validate fails to READ, rather than being silently
// skipped) is unchanged.
func TestReleaseLedger_KindlessArtifactIsRefused(t *testing.T) {
	dir := newLedgerTestProject(t, "kindless-project")
	path := filepath.Join(testStore(t, dir).Dir(), "releases.jsonl")
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	line := `{"release":"v1.0.0","git":{},"created_at":"2026-01-01T00:00:00Z",` +
		`"artifacts":{"control-plane":{"mode":"shared","digests":{"*":"` + sha("a") + `"}}}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatalf("plant the record: %v", err)
	}
	if _, err := testGetRelease(t, dir, "v1.0.0"); !errors.Is(err, release.ErrInvalid) {
		t.Fatalf("a kindless artifact must fail to read with ErrInvalid, got %v", err)
	}
}

// TestReleaseArtifact_NonOCIDoesNotPinImages is the guard that keeps the new
// kinds from corrupting the deploy path. An npm package has no container
// digest, so asking one for a digest must answer "no" rather than an empty
// string a caller might write into a pod spec.
//
// This is why the Kind check lives in SharedDigest rather than at each call
// site: the image-pinning paths get the exclusion for free and cannot forget it.
func TestReleaseArtifact_NonOCIDoesNotPinImages(t *testing.T) {
	// The Digests map is populated ON PURPOSE, and it is what gives this test
	// teeth. A non-OCI artifact with no digest would be excluded by the map
	// lookup alone, so the test would pass with or without the Kind guard and
	// prove nothing. A published file legitimately carries a sha256, so this
	// shape is real — and only the Kind check keeps it out of the image path.
	npm := release.Artifact{
		Kind:      release.KindNPM,
		Mode:      release.ModeShared,
		Version:   "0.3.1",
		Integrity: "sha512-abc",
		Digests:   map[string]string{"*": sha("f")},
	}
	if d, ok := npm.SharedDigest(); ok || d != "" {
		t.Errorf("SharedDigest() on an npm artifact = (%q, %v), want (\"\", false)", d, ok)
	}

	// A mixed release pins ONLY its images, and the npm entry rides along in
	// the ledger without ever reaching a container spec.
	rel := release.Release{
		Version: "v1.0.0",
		Artifacts: map[string]release.Artifact{
			"control-plane":            {Kind: release.KindOCI, Mode: release.ModeShared, Digests: map[string]string{"*": sha("a")}},
			"@reliantlabs/web-runtime": npm,
		},
	}
	resolved, err := resolveReleaseDigests(rel)
	if err != nil {
		t.Fatalf("a mixed release must resolve its images: %v", err)
	}
	if len(resolved) != 1 || resolved["control-plane"] != sha("a") {
		t.Errorf("resolved = %+v, want only control-plane pinned", resolved)
	}
}

// TestResolveReleaseDigests_PackageOnlyReleaseSaysSo covers the release that
// motivated multi-kind artifacts: forge's own v0.1.12 shipped an npm package
// and no images. "carries only variant-mode artifacts" would send a reader
// hunting for a feature flag, when the truth is there is simply nothing for an
// environment to run.
func TestResolveReleaseDigests_PackageOnlyReleaseSaysSo(t *testing.T) {
	rel := release.Release{
		Version: "v0.1.12",
		Artifacts: map[string]release.Artifact{
			"@reliantlabs/forge-web-runtime": {Kind: release.KindNPM, Mode: release.ModeShared, Version: "0.3.1", Integrity: "sha512-abc"},
		},
	}
	_, err := resolveReleaseDigests(rel)
	if err == nil {
		t.Fatal("want an error for a package-only release, got nil")
	}
	if !strings.Contains(err.Error(), "no container images") {
		t.Errorf("error should say the release has no images, got: %v", err)
	}
	if strings.Contains(err.Error(), "variant-mode") {
		t.Errorf("error must not blame variant mode for a package-only release, got: %v", err)
	}
}

// TestResolveDeployDigests_NamedReleaseWinsOverThePromotedOne: `forge env deploy
// <env> <version>` records the bundle BEFORE it moves the binding. The bundle's
// images must come from the release NAMED on the command line, not from the
// release the env is currently promoted to — otherwise a release that moves
// images yields a bundle of the previous images, the plan says "3 changed"
// while the bundle it planned carries the old digests, and applying it binds the
// ledger to a release whose code never runs.
func TestResolveDeployDigests_NamedReleaseWinsOverThePromotedOne(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// prod is promoted to v1.4.0 (control-plane = sha(a)).
	if err := testCutRelease(t, dir, rel("v1.4.0", "2026-01-01T00:00:00Z", "", false, map[string]string{"control-plane": sha("a")})); err != nil {
		t.Fatalf("cut v1.4.0: %v", err)
	}
	if _, err := testBindings(t, dir).Append(ctx, release.Promotion{
		Env: "prod", Release: "v1.4.0", Kind: release.KindPromote,
		Resolved: map[string]string{"control-plane": sha("a")},
	}, appendGuard{}); err != nil {
		t.Fatalf("promote v1.4.0: %v", err)
	}
	// v1.5.0 is cut but NOT promoted, and moves the image to sha(c).
	if err := testCutRelease(t, dir, rel("v1.5.0", "2026-01-02T00:00:00Z", "", false, map[string]string{"control-plane": sha("c")})); err != nil {
		t.Fatalf("cut v1.5.0: %v", err)
	}

	// Without a named release: the promoted one, unchanged behaviour.
	got, bound, err := resolveDeployDigests(ctx, dir, "prod", false, testBindings(t, dir), testReleases(t, dir))
	if err != nil || bound != "v1.4.0" || got["control-plane"] != sha("a") {
		t.Fatalf("no named release: bound=%q control-plane=%q err=%v, want v1.4.0/%s", bound, got["control-plane"], err, sha("a"))
	}

	// With v1.5.0 named: ITS pins.
	named := withHostedPinRelease(ctx, "v1.5.0")
	got, bound, err = resolveDeployDigests(named, dir, "prod", false, testBindings(t, dir), testReleases(t, dir))
	if err != nil {
		t.Fatalf("resolve with v1.5.0 named: %v", err)
	}
	if bound != "v1.5.0" {
		t.Errorf("bound release = %q, want the NAMED v1.5.0", bound)
	}
	if got["control-plane"] != sha("c") {
		t.Errorf("control-plane = %q, want v1.5.0's %q: the bundle would ship the promoted-from release's image", got["control-plane"], sha("c"))
	}

	// A named release the ledger does not hold is an error, never a silent
	// fall-through to the promoted release's pins.
	if _, _, err := resolveDeployDigests(withHostedPinRelease(ctx, "v9.9.9"), dir, "prod", false, testBindings(t, dir), testReleases(t, dir)); err == nil {
		t.Error("an unknown named release fell through to the promoted release's pins")
	}
}
