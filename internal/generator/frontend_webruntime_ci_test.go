package generator

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/buildinfo"
)

// The web-runtime dev link is a MAINTAINER's artifact. CI is a dev build (the
// workflows install the binary under test from the working tree) but never a
// dev loop, and bridging there added web-runtime's own node_modules as a
// second resolution root — which failed the scaffold's typecheck with two
// copies of @connectrpc/connect (TS2322). These tests pin the gate that
// closed it.
//
// They are deliberately behavioural: each drives EnsureDevWebRuntimeLink and
// asserts on whether the bridge appeared on disk, so the contract survives a
// refactor of how the decision is reached.

// TestEnsureDevWebRuntimeLink_CIWritesNothing is the regression test. Every
// other precondition is satisfied — a Go bridge to a real web-runtime
// checkout, a frontend to bridge — so CI is the only reason the bridge must
// not appear.
func TestEnsureDevWebRuntimeLink_CIWritesNothing(t *testing.T) {
	base := t.TempDir()
	forgeRoot := fakeForgeCheckout(t, filepath.Join(base, "forge"))
	pinDevBuild(t, true, forgeRoot)
	buildinfo.SetCI(true) // after pinDevBuild, whose cleanup clears it

	projectDir := filepath.Join(base, "app")
	bridgeProject(t, projectDir, forgeRoot)
	writeFrontendManifest(t, projectDir, "web", "")

	EnsureDevWebRuntimeLink(projectDir)

	// No workspace member symlink...
	if _, err := os.Lstat(filepath.Join(projectDir, devLinkDir)); !os.IsNotExist(err) {
		t.Errorf("%s/ was created under CI (err=%v) — the bridge must not activate there", devLinkDir, err)
	}
	// ...and no workspace ROOT manifest, which is the half that actually
	// reshapes npm resolution and broke the typecheck.
	if _, err := os.Stat(filepath.Join(projectDir, "package.json")); !os.IsNotExist(err) {
		t.Errorf("workspace root package.json was created under CI (err=%v)", err)
	}
}

// TestEnsureDevWebRuntimeLink_LocalDevLoopStillBridges is the other half of
// the contract, and the one that keeps the fix from being a blunt kill switch:
// a maintainer on their own machine must be unaffected.
func TestEnsureDevWebRuntimeLink_LocalDevLoopStillBridges(t *testing.T) {
	base := t.TempDir()
	forgeRoot := fakeForgeCheckout(t, filepath.Join(base, "forge"))
	pinDevBuild(t, true, forgeRoot) // pins CI off

	projectDir := filepath.Join(base, "app")
	bridgeProject(t, projectDir, forgeRoot)
	writeFrontendManifest(t, projectDir, "web", "")

	EnsureDevWebRuntimeLink(projectDir)

	link := filepath.Join(projectDir, devLinkDir, "web-runtime")
	if _, err := os.Stat(filepath.Join(link, "package.json")); err != nil {
		t.Fatalf("local dev loop lost its bridge: %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, "package.json")); err != nil {
		t.Fatalf("local dev loop lost its workspace root: %v", err)
	}
}

// TestEnsureDevWebRuntimeLink_ForcedOnCI covers the escape hatch: forge's
// "the 20% are never disempowered" rule means the CI suppression is a default,
// not a wall.
func TestEnsureDevWebRuntimeLink_ForcedOnCI(t *testing.T) {
	base := t.TempDir()
	forgeRoot := fakeForgeCheckout(t, filepath.Join(base, "forge"))
	pinDevBuild(t, true, forgeRoot)
	buildinfo.SetCI(true)
	t.Setenv("FORGE_DEV_WEBRUNTIME_LINK", "1")

	projectDir := filepath.Join(base, "app")
	bridgeProject(t, projectDir, forgeRoot)
	writeFrontendManifest(t, projectDir, "web", "")

	EnsureDevWebRuntimeLink(projectDir)

	link := filepath.Join(projectDir, devLinkDir, "web-runtime")
	if _, err := os.Stat(filepath.Join(link, "package.json")); err != nil {
		t.Fatalf("FORGE_DEV_WEBRUNTIME_LINK did not override the CI suppression: %v", err)
	}
}
