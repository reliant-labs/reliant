package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/frontenddeps"
)

func writePnpmBridgeFrontend(t *testing.T, projectDir, name string) {
	t.Helper()
	dir := filepath.Join(projectDir, "frontends", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for f, body := range map[string]string{
		"package.json":        `{"name":"` + name + `","packageManager":"pnpm@12.15.0","dependencies":{"@reliantlabs/forge-web-runtime":"^0.12.0"}}`,
		"pnpm-workspace.yaml": "overrides:\n  'parent>child': ^1.2.3\nallowBuilds:\n  esbuild: true\n  protobufjs: false\n",
		"pnpm-lock.yaml":      "lockfileVersion: '9.0'\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEnsureDevWebRuntimeLinkPnpm(t *testing.T) {
	base := t.TempDir()
	forgeRoot := fakeForgeCheckout(t, filepath.Join(base, "forge"))
	pinDevBuild(t, true, forgeRoot)
	projectDir := filepath.Join(base, "app")
	bridgeProject(t, projectDir, forgeRoot)
	writePnpmBridgeFrontend(t, projectDir, "web")
	feDir := filepath.Join(projectDir, "frontends", "web")
	before, _ := os.ReadFile(filepath.Join(feDir, "pnpm-lock.yaml"))

	EnsureDevWebRuntimeLink(projectDir)
	body, err := os.ReadFile(filepath.Join(projectDir, devPnpmWorkspaceName))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{frontenddeps.PnpmBridgeMarker, "frontends/web", "parent>child", "esbuild: true", "protobufjs: false", "verifyDepsBeforeRun: false", "link:./.forge-link/web-runtime"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("pnpm bridge lacks %q:\n%s", want, body)
		}
	}
	if _, err := os.Stat(filepath.Join(projectDir, "package.json")); !os.IsNotExist(err) {
		t.Errorf("pnpm-only project got an npm root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, devLinkDir, "web-runtime", "package.json")); err != nil {
		t.Fatalf("runtime symlink does not resolve: %v", err)
	}
	ignore, _ := os.ReadFile(filepath.Join(projectDir, ".gitignore"))
	for _, entry := range devBridgePnpmIgnoreEntries {
		if !gitignoreHasEntry(string(ignore), entry) {
			t.Errorf("bridge could be committed: missing ignore %s", entry)
		}
	}
	if got := frontenddeps.PnpmInstallRoot(feDir); got != projectDir {
		t.Errorf("pnpm install root = %s, want bridge root %s", got, projectDir)
	}
	EnsureDevWebRuntimeLink(projectDir)
	again, _ := os.ReadFile(filepath.Join(projectDir, devPnpmWorkspaceName))
	lockAfter, _ := os.ReadFile(filepath.Join(feDir, "pnpm-lock.yaml"))
	if string(again) != string(body) || string(lockAfter) != string(before) {
		t.Fatal("reconcile changed the bridge or tracked frontend inputs")
	}
}

func TestPnpmBridgePreservesUserRoot(t *testing.T) {
	projectDir := t.TempDir()
	writePnpmBridgeFrontend(t, projectDir, "web")
	path := filepath.Join(projectDir, devPnpmWorkspaceName)
	want := "packages:\n  - packages/*\n  - frontends/*\n"
	if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}
	if changed, err := reconcilePnpmBridge(projectDir, []string{"frontends/web"}); err != nil || changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != want {
		t.Fatalf("rewrote user's root: %s", got)
	}
}

func TestBridgeSeparatesNpmAndPnpmMembers(t *testing.T) {
	base := t.TempDir()
	forgeRoot := fakeForgeCheckout(t, filepath.Join(base, "forge"))
	pinDevBuild(t, true, forgeRoot)
	projectDir := filepath.Join(base, "app")
	bridgeProject(t, projectDir, forgeRoot)
	writeFrontendManifest(t, projectDir, "legacy", "")
	writePnpmBridgeFrontend(t, projectDir, "web")
	EnsureDevWebRuntimeLink(projectDir)
	npm, _ := os.ReadFile(filepath.Join(projectDir, "package.json"))
	pnpm, _ := os.ReadFile(filepath.Join(projectDir, devPnpmWorkspaceName))
	if !strings.Contains(string(npm), "frontends/legacy") || strings.Contains(string(npm), "frontends/web") ||
		!strings.Contains(string(pnpm), "frontends/web") || strings.Contains(string(pnpm), "frontends/legacy") {
		t.Fatalf("mixed bridge members wrong:\nnpm: %s\npnpm: %s", npm, pnpm)
	}
	// Migrating the last npm frontend removes only forge's npm half.
	writePnpmBridgeFrontend(t, projectDir, "legacy")
	EnsureDevWebRuntimeLink(projectDir)
	for _, p := range []string{"package.json", devNpmrcName} {
		if _, err := os.Stat(filepath.Join(projectDir, p)); !os.IsNotExist(err) {
			t.Errorf("left npm artifact %s after migration: %v", p, err)
		}
	}
}
