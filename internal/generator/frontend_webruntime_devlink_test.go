package generator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEnsureDevWebRuntimeLink_WritesAGitignoredBridge is the other half of the
// contract: the link must still EXIST, just not in a tracked file.
func TestEnsureDevWebRuntimeLink_WritesAGitignoredBridge(t *testing.T) {
	base := t.TempDir()
	forgeRoot := fakeForgeCheckout(t, filepath.Join(base, "forge"))
	pinDevBuild(t, true, forgeRoot)

	projectDir := filepath.Join(base, "app")
	bridgeProject(t, projectDir, forgeRoot)
	writeFrontendManifest(t, projectDir, "web", "")

	EnsureDevWebRuntimeLink(projectDir)

	// The workspace member symlink resolves to the real package directory.
	link := filepath.Join(projectDir, devLinkDir, "web-runtime")
	if _, err := os.Stat(filepath.Join(link, "package.json")); err != nil {
		t.Fatalf("bridge does not resolve to the web-runtime package: %v", err)
	}

	// The gitignored workspace root declares the link glob AND the frontends,
	// which is what makes npm hoist the member over the registry copy.
	rootManifest := filepath.Join(projectDir, "package.json")
	raw, err := os.ReadFile(rootManifest)
	if err != nil {
		t.Fatalf("read workspace root manifest: %v", err)
	}
	for _, want := range []string{devLinkDir + "/*", `"frontends/web"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("workspace root does not declare %q:\n%s", want, raw)
		}
	}

	// ...and forge ensures the ignore entries exist rather than assuming it.
	ignore, err := os.ReadFile(filepath.Join(projectDir, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	for _, want := range []string{"/package.json", "/package-lock.json", "/" + devLinkDir + "/"} {
		if !gitignoreHasEntry(string(ignore), want) {
			t.Errorf(".gitignore is missing %q — the bridge would be committable:\n%s", want, ignore)
		}
	}
}

// TestEnsureDevWebRuntimeLink_UnbridgedProjectGetsNothing is the opt-in,
// pinned: a dev build that knows its own checkout — the build that used to
// lay .forge-link/ into every project it generated — writes nothing into a
// project whose Go build is not bridged. The twin follows the go.work record;
// it never decides on its own.
func TestEnsureDevWebRuntimeLink_UnbridgedProjectGetsNothing(t *testing.T) {
	base := t.TempDir()
	forgeRoot := fakeForgeCheckout(t, filepath.Join(base, "forge"))
	pinDevBuild(t, true, forgeRoot)

	projectDir := filepath.Join(base, "app")
	writeFrontendManifest(t, projectDir, "web", "")

	EnsureDevWebRuntimeLink(projectDir)

	for _, p := range []string{"package.json", devLinkDir} {
		if _, err := os.Stat(filepath.Join(projectDir, p)); !os.IsNotExist(err) {
			t.Errorf("an unbridged project got %s at its root", p)
		}
	}
}

// TestEnsureDevWebRuntimeLink_FollowsTheBridgedCheckout: the twin links the
// checkout the project's go.work names, not the one this binary was built
// from, so the Go library and the web runtime always come from one tree.
func TestEnsureDevWebRuntimeLink_FollowsTheBridgedCheckout(t *testing.T) {
	base := t.TempDir()
	binaryCheckout := fakeForgeCheckout(t, filepath.Join(base, "forge-binary"))
	bridged := fakeForgeCheckout(t, filepath.Join(base, "forge-bridged"))
	pinDevBuild(t, false, binaryCheckout) // a release binary, even

	projectDir := filepath.Join(base, "app")
	bridgeProject(t, projectDir, bridged)
	writeFrontendManifest(t, projectDir, "web", "")

	EnsureDevWebRuntimeLink(projectDir)

	got, err := filepath.EvalSymlinks(filepath.Join(projectDir, devLinkDir, "web-runtime"))
	if err != nil {
		t.Fatalf("no twin for a bridged project: %v", err)
	}
	want, _ := filepath.EvalSymlinks(filepath.Join(bridged, "web-runtime"))
	if got != want {
		t.Errorf("twin resolves to %s, want the bridged checkout's %s", got, want)
	}
}

// TestEnsureDevWebRuntimeLink_Idempotent — `forge generate` runs constantly;
// a second run must not append a duplicate ignore entry or rewrite the root.
func TestEnsureDevWebRuntimeLink_Idempotent(t *testing.T) {
	base := t.TempDir()
	forgeRoot := fakeForgeCheckout(t, filepath.Join(base, "forge"))
	pinDevBuild(t, true, forgeRoot)

	projectDir := filepath.Join(base, "app")
	bridgeProject(t, projectDir, forgeRoot)
	writeFrontendManifest(t, projectDir, "web", "")

	EnsureDevWebRuntimeLink(projectDir)
	first := readAll(t, projectDir)
	for i := 0; i < 3; i++ {
		EnsureDevWebRuntimeLink(projectDir)
	}
	if again := readAll(t, projectDir); again != first {
		t.Errorf("re-running changed the bridge:\n--- first ---\n%s\n--- again ---\n%s", first, again)
	}
}

// workspaceMembers decodes the root manifest's workspaces array.
func workspaceMembers(t *testing.T, projectDir string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(projectDir, "package.json"))
	if err != nil {
		t.Fatalf("read workspace root manifest: %v", err)
	}
	var manifest struct {
		Workspaces []string `json:"workspaces"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("workspace root is not valid JSON: %v\n%s", err, raw)
	}
	return manifest.Workspaces
}

// TestEnsureDevWebRuntimeLink_LeavesNativeAppsStandalone is the reproduction
// for TestE2EAddFrontendKindsProduceABuildableTree failing on every dev build:
//
//	[BABEL]: Cannot find module 'expo/config'
//
// A `frontends/*` glob made the Expo app a member of the same hoisted tree as
// the React-19 web kinds, so npm split expo (nested) from babel-preset-expo
// (hoisted) whenever web installed first. The Expo app must not be a member —
// neither by name nor by a glob that happens to match it.
func TestEnsureDevWebRuntimeLink_LeavesNativeAppsStandalone(t *testing.T) {
	base := t.TempDir()
	forgeRoot := fakeForgeCheckout(t, filepath.Join(base, "forge"))
	pinDevBuild(t, true, forgeRoot)

	projectDir := filepath.Join(base, "app")
	bridgeProject(t, projectDir, forgeRoot)
	writeFrontendManifest(t, projectDir, "web", `"react": "^19.1.0"`)
	writeFrontendManifest(t, projectDir, "spa", `"react": "^19.1.0"`)
	writeFrontendManifest(t, projectDir, "mobile", `"expo": "~52.0.0", "react": "^18.3.0"`)
	writeFrontendManifest(t, projectDir, "bare", `"react-native": "0.76.9"`)

	EnsureDevWebRuntimeLink(projectDir)

	got := strings.Join(workspaceMembers(t, projectDir), ",")
	want := "frontends/spa,frontends/web," + devLinkDir + "/*"
	if got != want {
		t.Fatalf("workspace members = %s, want %s — a React Native app must install standalone", got, want)
	}
	for _, fe := range []string{"mobile", "bare"} {
		if rootWorkspaceCovers(projectDir, filepath.Join(projectDir, "frontends", fe)) {
			t.Errorf("the pin-layout probe reads frontends/%s as hoisted, but it is not a member", fe)
		}
	}
	if !rootWorkspaceCovers(projectDir, filepath.Join(projectDir, "frontends", "web")) {
		t.Error("the pin-layout probe no longer sees frontends/web as a workspace member")
	}
}

// TestEnsureDevWebRuntimeLink_ReconcilesItsOwnRoot: a frontend added after the
// root was written must join it, and a root written by an older forge (the
// `frontends/*` glob) must be healed. A user's own root must never be touched.
func TestEnsureDevWebRuntimeLink_ReconcilesItsOwnRoot(t *testing.T) {
	base := t.TempDir()
	forgeRoot := fakeForgeCheckout(t, filepath.Join(base, "forge"))
	pinDevBuild(t, true, forgeRoot)

	projectDir := filepath.Join(base, "app")
	bridgeProject(t, projectDir, forgeRoot)
	writeFrontendManifest(t, projectDir, "web", "")
	writeFrontendManifest(t, projectDir, "mobile", `"expo": "~52.0.0"`)
	legacy := `{"name":"` + devWorkspaceRootName + `","private":true,"workspaces":["frontends/*",".forge-link/*"]}`
	if err := os.WriteFile(filepath.Join(projectDir, "package.json"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	EnsureDevWebRuntimeLink(projectDir)
	if got := strings.Join(workspaceMembers(t, projectDir), ","); got != "frontends/web,"+devLinkDir+"/*" {
		t.Fatalf("legacy glob root not healed: members = %s", got)
	}

	writeFrontendManifest(t, projectDir, "admin", "")
	EnsureDevWebRuntimeLink(projectDir)
	if got := strings.Join(workspaceMembers(t, projectDir), ","); got != "frontends/admin,frontends/web,"+devLinkDir+"/*" {
		t.Fatalf("a newly added frontend did not join the root: members = %s", got)
	}

	userRoot := `{"name":"my-monorepo","private":true,"workspaces":["frontends/*"]}`
	if err := os.WriteFile(filepath.Join(projectDir, "package.json"), []byte(userRoot), 0o644); err != nil {
		t.Fatal(err)
	}
	EnsureDevWebRuntimeLink(projectDir)
	if raw, _ := os.ReadFile(filepath.Join(projectDir, "package.json")); string(raw) != userRoot {
		t.Fatalf("forge rewrote a user-owned root package.json:\n%s", raw)
	}
}

// readAll concatenates the two files the bridge owns, for change detection.
func readAll(t *testing.T, projectDir string) string {
	t.Helper()
	var b strings.Builder
	for _, p := range []string{"package.json", ".gitignore"} {
		raw, err := os.ReadFile(filepath.Join(projectDir, p))
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		b.WriteString(p + ":\n" + string(raw) + "\n")
	}
	return b.String()
}

// modCacheBridgeFixture lays down a project carrying the bridge an earlier
// forge wrote into the READ-ONLY module cache: forge's own root manifest and
// .forge-link/web-runtime pointing at $GOMODCACHE/…/forge@<version>/web-runtime.
func modCacheBridgeFixture(t *testing.T, base string) (projectDir, link string) {
	t.Helper()
	cached := fakeForgeCheckout(t, filepath.Join(base, "gomod", "github.com", "reliant-labs",
		"forge@v0.1.18-0.20260926053138-05d5d6999d39"))
	projectDir = filepath.Join(base, "app")
	writeFrontendManifest(t, projectDir, "web", `"react": "^19.1.0"`)
	if err := os.WriteFile(filepath.Join(projectDir, "package.json"),
		[]byte(devWorkspaceRootManifest([]string{"frontends/web"})), 0o644); err != nil {
		t.Fatal(err)
	}
	link = filepath.Join(projectDir, devLinkDir, "web-runtime")
	if _, err := ensureRelativeSymlink(link, filepath.Join(cached, "web-runtime")); err != nil {
		t.Fatal(err)
	}
	return projectDir, link
}

// TestEnsureDevWebRuntimeLink_HealsABridgeIntoTheModuleCache: a project that
// an earlier forge bridged into the module cache must not stay broken after
// the discovery fix. The stale link made every `npm install` fail with EACCES
// creating node_modules inside the read-only cache, so the frontend lane
// could not run until someone found and deleted it by hand.
func TestEnsureDevWebRuntimeLink_HealsABridgeIntoTheModuleCache(t *testing.T) {
	base := t.TempDir()
	projectDir, link := modCacheBridgeFixture(t, base)
	// The pinned pseudo-version forge: a "dev build" whose discovery (now)
	// finds no checkout, because the only candidate was the module cache.
	pinDevBuild(t, true, "")

	EnsureDevWebRuntimeLink(projectDir)

	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Errorf("the link into the module cache is still there (%v) — npm install keeps failing with EACCES", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, "package.json")); !os.IsNotExist(err) {
		t.Errorf("forge's own workspace root for the stale bridge was left behind (%v)", err)
	}
}

// TestEnsureDevWebRuntimeLink_LeavesARealCheckoutBridgeAlone: a released
// forge running in a maintainer's tree must not dismantle their bridge to a
// real sibling checkout, and a user's own root manifest is never touched.
func TestEnsureDevWebRuntimeLink_LeavesARealCheckoutBridgeAlone(t *testing.T) {
	base := t.TempDir()
	checkout := fakeForgeCheckout(t, filepath.Join(base, "forge"))
	projectDir := filepath.Join(base, "app")
	writeFrontendManifest(t, projectDir, "web", `"react": "^19.1.0"`)
	const userRoot = `{"name":"my-monorepo","private":true,"workspaces":["frontends/*"]}`
	if err := os.WriteFile(filepath.Join(projectDir, "package.json"), []byte(userRoot), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(projectDir, devLinkDir, "web-runtime")
	if _, err := ensureRelativeSymlink(link, filepath.Join(checkout, "web-runtime")); err != nil {
		t.Fatal(err)
	}
	pinDevBuild(t, false, "")

	EnsureDevWebRuntimeLink(projectDir)

	if _, err := os.Lstat(link); err != nil {
		t.Errorf("a bridge to a real checkout was removed: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(projectDir, "package.json")); string(got) != userRoot {
		t.Errorf("a user's own root package.json was modified: %s", got)
	}
}
