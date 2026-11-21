package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEnsureWebRuntimeDependency_DevBuildLeavesTrackedManifestAtPublishedRange
// is the reproduction for the defect this file exists to prevent.
//
// frontends/<name>/package.json is a COMMITTED file. A dev forge build used to
// rewrite the runtime specifier there to "file:../../../forge/web-runtime",
// which made every maintainer's `git status` permanently dirty and — the real
// damage — made it trivial to commit a path that resolves only on a machine
// with a sibling forge checkout at exactly that depth. Committing it breaks CI
// and every other developer.
//
// The bridge is still written; it just lives in a gitignored layer now (see
// TestEnsureDevWebRuntimeLink_*). The tracked manifest keeps the published
// range on every build flavour.
func TestEnsureWebRuntimeDependency_DevBuildLeavesTrackedManifestAtPublishedRange(t *testing.T) {
	base := t.TempDir()
	forgeRoot := fakeForgeCheckout(t, filepath.Join(base, "forge"))
	pinDevBuild(t, true, forgeRoot)

	projectDir := filepath.Join(base, "app")
	manifest := writeFrontendManifest(t, projectDir, "web", "\n    \"react\": \"^19.1.0\"")

	EnsureWebRuntimeDependency(projectDir, filepath.Join("frontends", "web"), "web")

	if got := readDeps(t, manifest)[WebRuntimePackage]; got != webRuntimePublishedRange {
		t.Errorf("dev build wrote %q into the TRACKED manifest, want the published range %q\n"+
			"a machine-specific path in a committed file dirties git status and breaks CI when committed",
			got, webRuntimePublishedRange)
	}

	raw, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if strings.Contains(string(raw), "file:") {
		t.Errorf("tracked manifest carries a file: specifier:\n%s", raw)
	}
}

// TestEnsureWebRuntimeDependency_DevBuildNormalisesAnAlreadyBridgedManifest
// covers the repos that are ALREADY dirty from the old behaviour: running a
// fixed forge over them must restore the committed specifier rather than
// leaving the local path in place.
func TestEnsureWebRuntimeDependency_DevBuildNormalisesAnAlreadyBridgedManifest(t *testing.T) {
	base := t.TempDir()
	forgeRoot := fakeForgeCheckout(t, filepath.Join(base, "forge"))
	pinDevBuild(t, true, forgeRoot)

	projectDir := filepath.Join(base, "app")
	manifest := writeFrontendManifest(t, projectDir, "web",
		"\n    \""+WebRuntimePackage+"\": \"file:../../../forge/web-runtime\"")

	EnsureWebRuntimeDependency(projectDir, filepath.Join("frontends", "web"), "web")

	if got := readDeps(t, manifest)[WebRuntimePackage]; got != webRuntimePublishedRange {
		t.Errorf("stale bridge not normalised: specifier = %q, want %q", got, webRuntimePublishedRange)
	}
}
