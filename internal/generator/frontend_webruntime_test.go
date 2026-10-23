package generator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/webruntimepeers"
)

// fakeForgeCheckout writes the minimum a forge source tree needs for the
// web-runtime bridge to consider it usable: a web-runtime/ directory carrying
// a package.json.
func fakeForgeCheckout(t *testing.T, root string) string {
	t.Helper()
	pkgDir := filepath.Join(root, "web-runtime")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("mkdir web-runtime: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte(`{"name":"`+WebRuntimePackage+`"}`), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	return root
}

// pinDevBuild makes IsDevBuild / DiscoverDevForgeRootFromSource deterministic
// for the duration of a test. Under `go test` the ambient binary always looks
// like a dev build resolving to the LIVE forge checkout, so the release path
// is otherwise unreachable.
func pinDevBuild(t *testing.T, dev bool, root string) {
	t.Helper()
	stamped := buildinfo.DevForgeRoot
	buildinfo.DevForgeRoot = ""
	buildinfo.SetDevBuild(dev)
	buildinfo.SetDiscoveredForgeRoot(root)
	// Pin CI off too. These tests assert the MAINTAINER's dev loop, and this
	// suite itself runs on CI — without this the ambient CI=true would
	// suppress the very bridge they exist to check, so they would pass
	// locally and fail on a runner for a reason unrelated to their subject.
	buildinfo.SetCI(false)
	t.Cleanup(func() {
		buildinfo.DevForgeRoot = stamped
		buildinfo.ClearDevBuild()
		buildinfo.ClearDiscoveredForgeRoot()
		buildinfo.ClearCI()
	})
}

// bridgeProject records the opt-in the npm twin follows: projectDir's go.work
// `use`s forgeRoot, which is made a module declaring forge's path — exactly
// what `forge project new --link-forge` or `go work use` leaves behind.
func bridgeProject(t *testing.T, projectDir, forgeRoot string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(forgeRoot, "go.mod"), []byte("module github.com/reliant-labs/forge\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatalf("write forge go.mod: %v", err)
	}
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	work := "go 1.26\n\nuse (\n\t.\n\t" + forgeRoot + "\n)\n"
	if err := os.WriteFile(filepath.Join(projectDir, "go.work"), []byte(work), 0o644); err != nil {
		t.Fatalf("write go.work: %v", err)
	}
}

// writeFrontendManifest lays down frontends/<name>/package.json with the given
// dependencies body and returns the project dir.
func writeFrontendManifest(t *testing.T, projectDir, feName, deps string) string {
	t.Helper()
	feDir := filepath.Join(projectDir, "frontends", feName)
	if err := os.MkdirAll(feDir, 0o755); err != nil {
		t.Fatalf("mkdir frontend: %v", err)
	}
	body := `{
  "name": "` + feName + `",
  "version": "0.1.0",
  "dependencies": {` + deps + `
  }
}
`
	if err := os.WriteFile(filepath.Join(feDir, "package.json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	return filepath.Join(feDir, "package.json")
}

// readDeps returns the parsed "dependencies" map of a manifest.
func readDeps(t *testing.T, path string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var doc struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("manifest is not valid JSON after reconcile: %v\n%s", err, raw)
	}
	return doc.Dependencies
}

// TestEnsureWebRuntimeDependency_ManifestCarriesNoHomePath is the same gate at
// the file level: whatever this machine's layout is, the manifest forge writes
// never names the user.
func TestEnsureWebRuntimeDependency_ManifestCarriesNoHomePath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory on this machine")
	}
	base := t.TempDir()
	forgeRoot := fakeForgeCheckout(t, filepath.Join(base, "forge"))
	pinDevBuild(t, true, forgeRoot)

	projectDir := filepath.Join(base, "app")
	manifest := writeFrontendManifest(t, projectDir, "web", "")

	EnsureWebRuntimeDependency(projectDir, filepath.Join("frontends", "web"), "web")

	raw, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if strings.Contains(string(raw), home) {
		t.Errorf("manifest embeds the home directory %q:\n%s", home, raw)
	}
	if user := filepath.Base(home); strings.Contains(string(raw), user) {
		t.Errorf("manifest embeds the username %q:\n%s", user, raw)
	}
}

func TestEnsureWebRuntimeDependency_ReleaseBuildWritesAVersionRange(t *testing.T) {
	forgeRoot := fakeForgeCheckout(t, t.TempDir())
	pinDevBuild(t, false, forgeRoot)

	projectDir := filepath.Join(t.TempDir(), "app")
	manifest := writeFrontendManifest(t, projectDir, "web", "\n    \""+WebRuntimePackage+"\": \"file:../../../forge/web-runtime\"")

	EnsureWebRuntimeDependency(projectDir, filepath.Join("frontends", "web"), "web")

	// A release forge normalises a bridge away — a machine-local path must
	// never survive into a project a released binary regenerates.
	if got := readDeps(t, manifest)[WebRuntimePackage]; got != webRuntimePublishedRange {
		t.Errorf("specifier = %q, want %q", got, webRuntimePublishedRange)
	}
}

func TestEnsureWebRuntimeDependency_IdempotentAndFormatPreserving(t *testing.T) {
	base := t.TempDir()
	forgeRoot := fakeForgeCheckout(t, filepath.Join(base, "forge"))
	pinDevBuild(t, true, forgeRoot)

	projectDir := filepath.Join(base, "app")
	feRel := filepath.Join("frontends", "web")
	manifest := writeFrontendManifest(t, projectDir, "web", "\n    \"react\": \"^19.1.0\"")

	EnsureWebRuntimeDependency(projectDir, feRel, "web")
	first, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	for i := 0; i < 3; i++ {
		EnsureWebRuntimeDependency(projectDir, feRel, "web")
	}
	again, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if string(again) != string(first) {
		t.Errorf("re-running changed the manifest:\n--- first ---\n%s\n--- again ---\n%s", first, again)
	}
	if n := strings.Count(string(again), WebRuntimePackage); n != 1 {
		t.Errorf("package declared %d times, want 1:\n%s", n, again)
	}
	// The neighbouring entry and its formatting survive.
	if !strings.Contains(string(again), `"react": "^19.1.0"`) {
		t.Errorf("reconcile disturbed the surrounding manifest:\n%s", again)
	}
}

// TestEnsureWebRuntimeDependency_CorrectsAStalePath — a dev build normalises a
// manifest an older forge bridged in place, back to the committed range.
func TestEnsureWebRuntimeDependency_CorrectsAStalePath(t *testing.T) {
	base := t.TempDir()
	forgeRoot := fakeForgeCheckout(t, filepath.Join(base, "forge"))
	pinDevBuild(t, true, forgeRoot)

	projectDir := filepath.Join(base, "app")
	manifest := writeFrontendManifest(t, projectDir, "web",
		"\n    \""+WebRuntimePackage+"\": \"file:../../../../somewhere/else/web-runtime\"")

	EnsureWebRuntimeDependency(projectDir, filepath.Join("frontends", "web"), "web")

	if got := readDeps(t, manifest)[WebRuntimePackage]; got != webRuntimePublishedRange {
		t.Errorf("stale path not corrected: %q", got)
	}
}

func TestEnsureWebRuntimeDependency_EmptyDependenciesObject(t *testing.T) {
	base := t.TempDir()
	forgeRoot := fakeForgeCheckout(t, filepath.Join(base, "forge"))
	pinDevBuild(t, true, forgeRoot)

	projectDir := filepath.Join(base, "app")
	manifest := writeFrontendManifest(t, projectDir, "web", "")

	EnsureWebRuntimeDependency(projectDir, filepath.Join("frontends", "web"), "web")

	if got := readDeps(t, manifest)[WebRuntimePackage]; got != webRuntimePublishedRange {
		t.Errorf("specifier = %q", got)
	}
}

// TestWebRuntimePublishedRangeTracksPackage keeps the release specifier from
// silently drifting away from the version web-runtime actually declares.
func TestWebRuntimePublishedRangeTracksPackage(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "web-runtime", "package.json"))
	if err != nil {
		t.Skipf("web-runtime package.json not readable: %v", err)
	}
	var doc struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse web-runtime package.json: %v", err)
	}
	if doc.Name != WebRuntimePackage {
		t.Errorf("web-runtime package name = %q, want %q", doc.Name, WebRuntimePackage)
	}
	majorMinor := func(v string) string {
		parts := strings.SplitN(strings.TrimPrefix(v, "^"), ".", 3)
		if len(parts) < 2 {
			return v
		}
		return parts[0] + "." + parts[1]
	}
	if got, want := majorMinor(webRuntimePublishedRange), majorMinor(doc.Version); got != want {
		t.Errorf("webRuntimePublishedRange %q does not track web-runtime version %q",
			webRuntimePublishedRange, doc.Version)
	}
}

// A project scaffolded before web-runtime 0.4.0 does not declare @hyperdx/browser,
// and `forge generate` rewrites src/lib/otel_gen.ts to import it. Without this
// sync the first build after upgrading fails to resolve the package.
func TestEnsureBrowserTelemetryDependencies(t *testing.T) {
	feRel := filepath.Join("frontends", "web")

	t.Run("adds the SDK at the runtime's own pin", func(t *testing.T) {
		projectDir := filepath.Join(t.TempDir(), "app")
		manifest := writeFrontendManifest(t, projectDir, "web", "\n    \"react\": \"^19.1.0\"")

		EnsureBrowserTelemetryDependencies(projectDir, feRel, "web")

		want, ok := webruntimepeers.PeerSpec("@hyperdx/browser")
		if !ok || want == "" {
			t.Fatal("web-runtime declares no @hyperdx/browser peer")
		}
		deps := readDeps(t, manifest)
		if deps["@hyperdx/browser"] != want {
			t.Errorf("@hyperdx/browser = %q, want %q", deps["@hyperdx/browser"], want)
		}
		if deps["react"] != "^19.1.0" {
			t.Errorf("a neighbouring entry was disturbed: %v", deps)
		}
	})

	t.Run("an existing declaration is the project's own and is kept", func(t *testing.T) {
		projectDir := filepath.Join(t.TempDir(), "app")
		manifest := writeFrontendManifest(t, projectDir, "web", "\n    \"@hyperdx/browser\": \"0.25.0\"")
		before, _ := os.ReadFile(manifest)

		EnsureBrowserTelemetryDependencies(projectDir, feRel, "web")

		after, _ := os.ReadFile(manifest)
		if string(before) != string(after) {
			t.Errorf("manifest rewritten:\n%s", after)
		}
	})

	t.Run("idempotent", func(t *testing.T) {
		projectDir := filepath.Join(t.TempDir(), "app")
		manifest := writeFrontendManifest(t, projectDir, "web", "")
		EnsureBrowserTelemetryDependencies(projectDir, feRel, "web")
		first, _ := os.ReadFile(manifest)
		EnsureBrowserTelemetryDependencies(projectDir, feRel, "web")
		again, _ := os.ReadFile(manifest)
		if string(first) != string(again) || strings.Count(string(again), "@hyperdx/browser") != 1 {
			t.Errorf("not idempotent:\n%s", again)
		}
	})

	t.Run("no manifest is a no-op", func(t *testing.T) {
		EnsureBrowserTelemetryDependencies(t.TempDir(), feRel, "web") // must not panic or create files
	})
}
