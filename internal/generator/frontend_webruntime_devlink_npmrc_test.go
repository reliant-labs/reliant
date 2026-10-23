package generator

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureDevWebRuntimeLink_WritesAnIgnoredNpmrc(t *testing.T) {
	base := t.TempDir()
	forgeRoot := fakeForgeCheckout(t, filepath.Join(base, "forge"))
	pinDevBuild(t, true, forgeRoot)
	projectDir := filepath.Join(base, "app")
	bridgeProject(t, projectDir, forgeRoot)
	writeFrontendManifest(t, projectDir, "web", "")

	EnsureDevWebRuntimeLink(projectDir)

	raw, err := os.ReadFile(filepath.Join(projectDir, devNpmrcName))
	if err != nil {
		t.Fatalf("bridge wrote no %s: %v", devNpmrcName, err)
	}
	if !strings.Contains(string(raw), "legacy-peer-deps=true") {
		t.Errorf("%s lacks legacy-peer-deps=true:\n%s", devNpmrcName, raw)
	}
	ignore, _ := os.ReadFile(filepath.Join(projectDir, ".gitignore"))
	if !gitignoreHasEntry(string(ignore), "/"+devNpmrcName) {
		t.Errorf(".gitignore does not ignore /%s — the bridge would be committable:\n%s", devNpmrcName, ignore)
	}

	before := readAll(t, projectDir)
	EnsureDevWebRuntimeLink(projectDir)
	if readAll(t, projectDir) != before {
		t.Errorf("second run changed the bridge's files")
	}
}

func TestEnsureDevWebRuntimeLink_NeverTouchesAUserNpmrc(t *testing.T) {
	base := t.TempDir()
	forgeRoot := fakeForgeCheckout(t, filepath.Join(base, "forge"))
	pinDevBuild(t, true, forgeRoot)
	projectDir := filepath.Join(base, "app")
	bridgeProject(t, projectDir, forgeRoot)
	writeFrontendManifest(t, projectDir, "web", "")
	const mine = "registry=https://registry.example.com/\n"
	if err := os.WriteFile(filepath.Join(projectDir, devNpmrcName), []byte(mine), 0o644); err != nil {
		t.Fatal(err)
	}

	EnsureDevWebRuntimeLink(projectDir)

	if got, _ := os.ReadFile(filepath.Join(projectDir, devNpmrcName)); string(got) != mine {
		t.Errorf("a user's own %s was modified:\n%s", devNpmrcName, got)
	}
}

func TestEnsureDevWebRuntimeLink_HealingTheModuleCacheBridgeRemovesItsNpmrc(t *testing.T) {
	base := t.TempDir()
	projectDir, _ := modCacheBridgeFixture(t, base)
	npmrc := filepath.Join(projectDir, devNpmrcName)
	if err := os.WriteFile(npmrc, []byte(devNpmrcBody), 0o644); err != nil {
		t.Fatal(err)
	}
	pinDevBuild(t, true, "")

	EnsureDevWebRuntimeLink(projectDir)

	if _, err := os.Stat(npmrc); !os.IsNotExist(err) {
		t.Errorf("the bridge's %s outlived the bridge (%v)", devNpmrcName, err)
	}
}

// TestDevBridgeInstallSurvivesVitest4OnTheBundledNpm reproduces the crash
// `forge project new --link-forge` hit: npm 10.x dies with "Cannot read
// properties of null (reading 'edgesOut')" resolving a tree that holds
// vitest >= 4.1, which web-runtime's devDependencies bring in once it is a
// workspace member. It runs the real npm against the registry, so it only
// runs in full mode.
func TestDevBridgeInstallSurvivesVitest4OnTheBundledNpm(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: runs a real `npm install --package-lock-only` against the registry; runs in task test")
	}
	npm, err := exec.LookPath("npm")
	if err != nil {
		t.Skip("npm not on PATH")
	}
	base := t.TempDir()
	forgeRoot := fakeForgeCheckout(t, filepath.Join(base, "forge"))
	pinDevBuild(t, true, forgeRoot)
	// The checkout's web-runtime stands in for the real one with exactly the
	// devDependency that triggers the crash.
	wr := `{"name":"@reliantlabs/forge-web-runtime","version":"0.4.1","devDependencies":{"vitest":"^4.1.11"}}`
	if err := os.WriteFile(filepath.Join(forgeRoot, "web-runtime", "package.json"), []byte(wr), 0o644); err != nil {
		t.Fatal(err)
	}
	projectDir := filepath.Join(base, "app")
	bridgeProject(t, projectDir, forgeRoot)
	writeFrontendManifest(t, projectDir, "web", `"@reliantlabs/forge-web-runtime": "^0.4.0"`)

	EnsureDevWebRuntimeLink(projectDir)

	cmd := exec.Command(npm, "install", "--package-lock-only", "--ignore-scripts", "--no-audit", "--no-fund")
	cmd.Dir = projectDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		if s := string(out); strings.Contains(s, "ENOTFOUND") || strings.Contains(s, "EAI_AGAIN") || strings.Contains(s, "ETIMEDOUT") || strings.Contains(s, "ECONNREFUSED") {
			t.Skipf("npm registry unreachable: %s", s)
		}
		t.Fatalf("npm install in the bridged project failed: %v\n%s", err, out)
	}
}
