// File: internal/cli/release_artifacts_test.go
//
// Covers the non-OCI half of release harvesting: npm packages, Go modules, and
// published files. The invariant every test here defends is the one the model
// rests on — the ledger is a PROJECTION of build state — so each test asserts
// both that a real coordinate is derived AND that nothing is derived when the
// underlying build state does not exist.
package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

// writeArtifactFixture writes content at path, creating parents. Fails the
// test on error.
func writeArtifactFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// requireNPM skips when npm is unavailable. The npm-backed tests shell out to
// the real tool because npm owns the tarball format that produces the
// integrity hash — a hand-rolled packer would be a second source of truth.
func requireNPM(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("npm not on PATH")
	}
}

// TestHarvestNPMArtifacts_RecordsNameVersionAndIntegrity is the core npm case,
// and the direct regression test for the v0.1.12 web-runtime gap: a release
// must be able to NAME the npm package it ships, at a version, with the
// registry integrity hash that makes it verifiable.
//
// The integrity assertion is the part with teeth. Asserting only name+version
// would pass against a manifest read alone and would NOT prove the pack ran —
// it is the "sha512-" hash, which exists nowhere in package.json and can only
// come from npm packing the real bytes, that pins the derivation.
func TestHarvestNPMArtifacts_RecordsNameVersionAndIntegrity(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns package-manager / frontend toolchain subprocesses; runs in task test")
	}
	requireNPM(t)
	dir := t.TempDir()
	pkgDir := filepath.Join(dir, "web-runtime")
	writeArtifactFixture(t, filepath.Join(pkgDir, "package.json"),
		`{"name":"@acme/runtime","version":"0.3.1","files":["dist"],"main":"./dist/index.js"}`)
	writeArtifactFixture(t, filepath.Join(pkgDir, "dist", "index.js"), "export const installDevLogging = () => {};\n")

	got := harvestNPMArtifacts(context.Background(), dir)

	art, ok := got["@acme/runtime"]
	if !ok {
		t.Fatalf("no artifact for @acme/runtime; got %+v", got)
	}
	if art.Kind != release.KindNPM {
		t.Errorf("kind = %q, want %q", art.Kind, release.KindNPM)
	}
	if art.Version != "0.3.1" {
		t.Errorf("version = %q, want 0.3.1", art.Version)
	}
	// The registry's own integrity format. Anything else means we recorded a
	// hash a consumer cannot compare against what npm serves.
	if !strings.HasPrefix(art.Integrity, "sha512-") {
		t.Errorf("integrity = %q, want a sha512- registry integrity string", art.Integrity)
	}
}

// TestHarvestNPMArtifacts_IntegrityTracksContent proves the recorded hash
// describes THE BYTES ON DISK rather than the manifest's metadata. Two packages
// identical except for one file's contents must hash differently.
//
// Without this, a harvest that returned any constant "sha512-…" string would
// satisfy the shape assertion above while recording an unverifiable lie.
func TestHarvestNPMArtifacts_IntegrityTracksContent(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns package-manager / frontend toolchain subprocesses; runs in task test")
	}
	requireNPM(t)

	pack := func(t *testing.T, body string) string {
		t.Helper()
		dir := t.TempDir()
		pkgDir := filepath.Join(dir, "pkg")
		writeArtifactFixture(t, filepath.Join(pkgDir, "package.json"),
			`{"name":"@acme/runtime","version":"0.3.1","files":["dist"],"main":"./dist/index.js"}`)
		writeArtifactFixture(t, filepath.Join(pkgDir, "dist", "index.js"), body)
		art, ok := harvestNPMArtifacts(context.Background(), dir)["@acme/runtime"]
		if !ok {
			t.Fatal("no artifact harvested")
		}
		return art.Integrity
	}

	if a, b := pack(t, "export const a = 1;\n"), pack(t, "export const a = 2;\n"); a == b {
		t.Errorf("integrity did not change with content: both %q — the hash is not derived from the packed bytes", a)
	}
}

// TestHarvestNPMArtifacts_SkipsPrivateAndUnnamed pins the two exclusions that
// keep the ledger honest about what is actually released.
//
//   - `private: true` is npm's OWN publish refusal, so a private package is by
//     definition not a released artifact. Every scaffolded forge frontend sets
//     it, and recording those would fill a release with app bundles.
//   - A manifest with no name/version is not a package. forge's real
//     web-runtime/interceptors/package.json is exactly this — a resolver shim
//     shipped INSIDE the parent's tarball — and naming it would assert an
//     artifact that does not independently exist.
func TestHarvestNPMArtifacts_SkipsPrivateAndUnnamed(t *testing.T) {
	requireNPM(t)
	dir := t.TempDir()
	writeArtifactFixture(t, filepath.Join(dir, "frontend", "package.json"),
		`{"name":"@acme/web","version":"1.0.0","private":true}`)
	writeArtifactFixture(t, filepath.Join(dir, "shim", "package.json"),
		`{"main":"../dist/interceptors.js","types":"../dist/interceptors.d.ts"}`)

	if got := harvestNPMArtifacts(context.Background(), dir); len(got) != 0 {
		t.Errorf("want no artifacts (one private, one unnamed), got %+v", got)
	}
}

// TestHarvestNPMArtifacts_IgnoresDependencyTrees keeps the scan off
// node_modules. A dependency's package.json is a package this project
// CONSUMES; recording it would claim forge releases its own dependencies.
func TestHarvestNPMArtifacts_IgnoresDependencyTrees(t *testing.T) {
	requireNPM(t)
	dir := t.TempDir()
	writeArtifactFixture(t, filepath.Join(dir, "node_modules", "react", "package.json"),
		`{"name":"react","version":"19.0.0","main":"index.js"}`)
	writeArtifactFixture(t, filepath.Join(dir, "node_modules", "react", "index.js"), "module.exports={};\n")

	if got := harvestNPMArtifacts(context.Background(), dir); len(got) != 0 {
		t.Errorf("want no artifacts from node_modules, got %+v", got)
	}
}

// TestHarvestNPMArtifacts_AbsenceIsNotFailure pins constraint 3: a project with
// no npm package harvests nothing and reports no error. The function returns no
// error at all by design, so this asserts the empty-but-usable map.
func TestHarvestNPMArtifacts_AbsenceIsNotFailure(t *testing.T) {
	got := harvestNPMArtifacts(context.Background(), t.TempDir())
	if got == nil {
		t.Fatal("returned a nil map; callers merge into/from it unguarded")
	}
	if len(got) != 0 {
		t.Errorf("want empty, got %+v", got)
	}
}

// TestHarvestGoModuleArtifacts_RecordsVersionAndSum is the gomod case: a nested
// module's version comes from the ROOT go.mod's require (nothing in a module
// declares its own version) and its hash from go.sum's h1: line.
//
// The h1: assertion is what gives this teeth — the hash appears only in go.sum,
// so matching it proves both files were actually read and correlated.
func TestHarvestGoModuleArtifacts_RecordsVersionAndSum(t *testing.T) {
	dir := t.TempDir()
	const h1 = "h1:oEi68/DnGrk7r/zJf5IKTC02p6cISJJhsGKjhCULOuw="
	writeArtifactFixture(t, filepath.Join(dir, "go.mod"), "module github.com/acme/forge\n\ngo 1.22\n\nrequire github.com/acme/forge/pkg v0.1.14\n")
	writeArtifactFixture(t, filepath.Join(dir, "go.sum"),
		"github.com/acme/forge/pkg v0.1.14 "+h1+"\n"+
			"github.com/acme/forge/pkg v0.1.14/go.mod h1:sfx6mK8xPMq8RqvfpkNa3mj4mBp7CDOKaNyTZLmX1QE=\n")
	writeArtifactFixture(t, filepath.Join(dir, "pkg", "go.mod"), "module github.com/acme/forge/pkg\n\ngo 1.22\n")

	art, ok := harvestGoModuleArtifacts(dir)["github.com/acme/forge/pkg"]
	if !ok {
		t.Fatalf("no artifact for the nested module; got %+v", harvestGoModuleArtifacts(dir))
	}
	if art.Kind != release.KindGoModule {
		t.Errorf("kind = %q, want %q", art.Kind, release.KindGoModule)
	}
	if art.Version != "v0.1.14" {
		t.Errorf("version = %q, want v0.1.14", art.Version)
	}
	// Must be the MODULE ZIP hash, not the "/go.mod" hash on the next line —
	// they verify different bytes, and recording the wrong one is a hash that
	// silently fails to match the artifact it claims to describe.
	if art.Integrity != h1 {
		t.Errorf("integrity = %q, want the module zip hash %q", art.Integrity, h1)
	}
}

// TestHarvestGoModuleArtifacts_SkipsUnsummedModule pins the go.sum trap that
// scripts/release-forge.sh exists to catch: the root requires the submodule,
// but go.sum carries no hash for that version (exactly what an in-workspace
// build leaves behind, since go.work resolves the module from disk).
//
// Recording a version with no hash would put an UNVERIFIABLE artifact in the
// ledger — the precise failure mode kinds were added to eliminate — so the
// artifact must be omitted entirely.
func TestHarvestGoModuleArtifacts_SkipsUnsummedModule(t *testing.T) {
	dir := t.TempDir()
	writeArtifactFixture(t, filepath.Join(dir, "go.mod"), "module github.com/acme/forge\n\ngo 1.22\n\nrequire github.com/acme/forge/pkg v0.1.14\n")
	// Only the /go.mod hash is present — the module zip hash is missing.
	writeArtifactFixture(t, filepath.Join(dir, "go.sum"),
		"github.com/acme/forge/pkg v0.1.14/go.mod h1:sfx6mK8xPMq8RqvfpkNa3mj4mBp7CDOKaNyTZLmX1QE=\n")
	writeArtifactFixture(t, filepath.Join(dir, "pkg", "go.mod"), "module github.com/acme/forge/pkg\n\ngo 1.22\n")

	if got := harvestGoModuleArtifacts(dir); len(got) != 0 {
		t.Errorf("want no artifacts (no h1: zip hash to verify against), got %+v", got)
	}
}

// TestHarvestGoModuleArtifacts_SkipsRootModule keeps the root out of the
// ledger. Its version IS the release label, and it has no self-referential
// go.sum entry, so recording it would name an artifact nothing can verify.
func TestHarvestGoModuleArtifacts_SkipsRootModule(t *testing.T) {
	dir := t.TempDir()
	writeArtifactFixture(t, filepath.Join(dir, "go.mod"), "module github.com/acme/forge\n\ngo 1.22\n")
	writeArtifactFixture(t, filepath.Join(dir, "go.sum"), "")

	if got := harvestGoModuleArtifacts(dir); len(got) != 0 {
		t.Errorf("want no artifacts, got %+v", got)
	}
}

// TestHarvestFileArtifacts_HashesDeclaredBinaries covers the file kind: each
// build-only variant the KCL declares, hashed from the binary the build
// emitted into outputDir.
func TestHarvestFileArtifacts_HashesDeclaredBinaries(t *testing.T) {
	dir := t.TempDir()
	writeArtifactFixture(t, filepath.Join(dir, "bin", "reliant-daemon-prod"), "prod-binary-bytes")
	writeArtifactFixture(t, filepath.Join(dir, "bin", "custom-name"), "dev-binary-bytes")

	entities := &KCLEntities{Workloads: []WorkloadEntity{{
		Name: "reliant-daemon", Kind: "tool",
		Runtime: RuntimeEntity{Type: RuntimeBuildOnly, BuildOnly: &BuildOnlyDeploy{BuildVariants: []BuildVariant{
			{Name: "prod"},
			{Name: "dev", OutputName: "custom-name"},
		}}},
	}}}

	got := harvestFileArtifacts(dir, "bin", entities)
	if len(got) != 2 {
		t.Fatalf("want 2 file artifacts, got %d: %+v", len(got), got)
	}
	// Default naming is <service>-<variant>; OutputName overrides it.
	art, ok := got["reliant-daemon-prod"]
	if !ok {
		t.Fatalf("no artifact for reliant-daemon-prod; got %+v", got)
	}
	if art.Kind != release.KindFile {
		t.Errorf("kind = %q, want %q", art.Kind, release.KindFile)
	}
	// The canonical sha256 of "prod-binary-bytes". Pinning the literal hash
	// (rather than merely "some sha256:-shaped string") is what proves the
	// bytes were actually read and hashed; TestHarvestFileArtifacts_Integrity-
	// TracksContent covers the same property from the differential side.
	want := sha256Hex(t, "prod-binary-bytes")
	if art.Integrity != want {
		t.Errorf("integrity = %q, want %q", art.Integrity, want)
	}
	if _, ok := got["custom-name"]; !ok {
		t.Errorf("OutputName override not honored; got %+v", got)
	}
}

// sha256Hex returns the canonical "sha256:<hex>" digest of a string, computed
// independently of the production helper so the assertion is a real check
// rather than a restatement of the implementation.
func sha256Hex(t *testing.T, s string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// TestHarvestFileArtifacts_IntegrityTracksContent proves the file hash is
// derived from the bytes, not fabricated: the same declaration over different
// binary contents must produce different integrity values.
func TestHarvestFileArtifacts_IntegrityTracksContent(t *testing.T) {
	entities := &KCLEntities{Workloads: []WorkloadEntity{{
		Name: "daemon", Kind: "tool",
		Runtime: RuntimeEntity{Type: RuntimeBuildOnly, BuildOnly: &BuildOnlyDeploy{BuildVariants: []BuildVariant{{Name: "prod"}}}},
	}}}

	hashOf := func(t *testing.T, body string) string {
		t.Helper()
		dir := t.TempDir()
		writeArtifactFixture(t, filepath.Join(dir, "bin", "daemon-prod"), body)
		art, ok := harvestFileArtifacts(dir, "bin", entities)["daemon-prod"]
		if !ok {
			t.Fatal("no artifact harvested")
		}
		return art.Integrity
	}

	if a, b := hashOf(t, "bytes-one"), hashOf(t, "bytes-two"); a == b {
		t.Errorf("integrity did not change with content: both %q", a)
	}
}

// TestHarvestFileArtifacts_SkipsUnbuiltVariant pins that a DECLARED variant
// whose binary this build did not produce records nothing. A `--target` run
// skips variants by design, and a release must record only bytes that exist —
// the file-kind twin of the OCI path skipping a digestless image.
func TestHarvestFileArtifacts_SkipsUnbuiltVariant(t *testing.T) {
	dir := t.TempDir()
	entities := &KCLEntities{Workloads: []WorkloadEntity{{
		Name: "daemon", Kind: "tool",
		Runtime: RuntimeEntity{Type: RuntimeBuildOnly, BuildOnly: &BuildOnlyDeploy{BuildVariants: []BuildVariant{{Name: "prod"}}}},
	}}}

	if got := harvestFileArtifacts(dir, "bin", entities); len(got) != 0 {
		t.Errorf("want no artifacts (binary was never built), got %+v", got)
	}
}

// TestHarvestFileArtifacts_IgnoresNonBuildOnlyServices keeps a cluster service's
// binary out of the file kind. Those ship INSIDE an OCI image, which the image
// digest already addresses; recording them again would double-count the same
// bytes under two kinds.
func TestHarvestFileArtifacts_IgnoresNonBuildOnlyServices(t *testing.T) {
	dir := t.TempDir()
	writeArtifactFixture(t, filepath.Join(dir, "bin", "api-server"), "binary")
	entities := &KCLEntities{Workloads: []WorkloadEntity{{Name: "api-server", Runtime: RuntimeEntity{Type: RuntimeCluster, Cluster: &ClusterRuntime{}}}}}

	if got := harvestFileArtifacts(dir, "bin", entities); len(got) != 0 {
		t.Errorf("want no artifacts for a non-build-only service, got %+v", got)
	}
}

// TestHarvestFileArtifacts_NilEntitiesIsEmpty covers the env-less build: a
// plain `forge env build --release` with no --env renders no KCL, so entities is
// nil. That must harvest nothing rather than panic.
func TestHarvestFileArtifacts_NilEntitiesIsEmpty(t *testing.T) {
	got := harvestFileArtifacts(t.TempDir(), "bin", nil)
	if got == nil {
		t.Fatal("returned a nil map; callers merge into/from it unguarded")
	}
	if len(got) != 0 {
		t.Errorf("want empty, got %+v", got)
	}
}

// TestMergeReleaseArtifacts_NeverDisplacesImages is the guard that protects
// constraint 4. Images are merged first; a package or file sharing an image's
// name must NOT overwrite the digest an env deploys.
//
// This is the structural complement to SharedDigest's kind check: that stops a
// non-OCI artifact from being READ as a digest, this stops one from evicting a
// real digest in the first place.
func TestMergeReleaseArtifacts_NeverDisplacesImages(t *testing.T) {
	dst := map[string]release.Artifact{
		"reliant": {Kind: release.KindOCI, Mode: release.ModeShared, Digests: map[string]string{"*": sha("a")}},
	}
	added := mergeReleaseArtifacts(dst, map[string]release.Artifact{
		"reliant":  {Kind: release.KindNPM, Mode: release.ModeShared, Version: "1.0.0", Integrity: "sha512-x"},
		"@acme/ui": {Kind: release.KindNPM, Mode: release.ModeShared, Version: "2.0.0", Integrity: "sha512-y"},
	})
	if added != 1 {
		t.Errorf("added = %d, want 1 (the colliding name must not count)", added)
	}
	art := dst["reliant"]
	if art.Kind != release.KindOCI {
		t.Fatalf("image artifact was displaced: kind = %q", art.Kind)
	}
	if d, ok := art.SharedDigest(); !ok || d != sha("a") {
		t.Errorf("image digest lost: got (%q, %v), want (%q, true)", d, ok, sha("a"))
	}
	if _, ok := dst["@acme/ui"]; !ok {
		t.Error("non-colliding package was not merged")
	}
}

// TestHarvestReleaseArtifacts_UnaffectedByNonOCIKinds pins constraint 4 from
// the other side: the OCI harvest is byte-identical regardless of what npm or
// Go-module state sits beside it in the project. A package manifest in the tree
// must not perturb the image path at all.
func TestHarvestReleaseArtifacts_UnaffectedByNonOCIKinds(t *testing.T) {
	dir := t.TempDir()
	if err := WriteBuildState(dir, "default", BuildState{
		Image: "control-plane", Tag: "v1.4.0", Pushed: true, PushedAt: nowRFC3339(),
		Digest: sha("a"), Platforms: []string{"linux/amd64"},
	}); err != nil {
		t.Fatalf("write build state: %v", err)
	}
	before := harvestReleaseArtifacts(dir, "")

	writeArtifactFixture(t, filepath.Join(dir, "web-runtime", "package.json"), `{"name":"@acme/runtime","version":"0.3.1"}`)
	writeArtifactFixture(t, filepath.Join(dir, "go.mod"), "module github.com/acme/forge\n\ngo 1.22\n\nrequire github.com/acme/forge/pkg v0.1.14\n")

	after := harvestReleaseArtifacts(dir, "")
	if len(after) != len(before) {
		t.Fatalf("OCI harvest changed: %d artifacts before, %d after", len(before), len(after))
	}
	art, ok := after["control-plane"]
	if !ok {
		t.Fatalf("image missing after adding package state: %+v", after)
	}
	if art.Kind != release.KindOCI {
		t.Errorf("kind = %q, want %q", art.Kind, release.KindOCI)
	}
	if d, _ := art.SharedDigest(); d != sha("a") {
		t.Errorf("digest = %q, want %q", d, sha("a"))
	}
}
