package generator

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/webruntimepeers"
)

// installFakePeer writes pkg into nodeModules the way npm would: a manifest
// carrying the real resolution fields of @tanstack/react-query (whose `types`
// names the legacy build while `exports` names the modern one) and the
// declaration file those fields point at. The manifest is what the reconcile
// reads, so it is the part that has to be faithful.
func installFakePeer(t *testing.T, nodeModules, pkg string) {
	t.Helper()
	dir := filepath.Join(nodeModules, filepath.FromSlash(webruntimepeers.TypePinTarget(pkg)))
	if err := os.MkdirAll(filepath.Join(dir, "build", "modern"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"` + pkg + `","types":"build/legacy/index.d.ts","module":"build/legacy/index.js",
  "exports":{".":{"import":{"types":"./build/modern/index.d.ts","default":"./build/modern/index.js"}}}}`
	for rel, body := range map[string]string{
		"package.json":            manifest,
		"build/modern/index.d.ts": "export {};\n",
		"build/modern/index.js":   "export {};\n",
		"build/legacy/index.d.ts": "export {};\n",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestReconcileFrontendTsconfigPeers_PinsDeclarationFiles is the reproduction
// for defect "tsconfig paths pins split packages in the bundle".
//
// The template writes each pin as a package DIRECTORY, because it renders
// before any install exists. Next's webpack resolver applies tsconfig `paths`
// to app code, and a directory request skips the package's `exports` map — so
// once `npm install` makes the pin resolve, app code bundles react-query's
// build/legacy while @reliantlabs/forge-web-runtime bundles build/modern. Two
// module instances are two React contexts, and prerender fails with "No
// QueryClient set, use QueryClientProvider to set one" (reproduced with
// `next build` on a freshly scaffolded frontend).
//
// The scaffold-time reconcile runs right after forge's own install, so it is
// the pass that must turn every directory pin into the package's declaration
// file — which Next's resolver skips, leaving the bundle on `exports`.
func TestReconcileFrontendTsconfigPeers_PinsDeclarationFiles(t *testing.T) {
	t.Parallel()

	root := writeLayoutFixture(t, false)
	feModules := filepath.Join(root, "frontends", "web", "node_modules")
	for _, pkg := range webruntimepeers.TypePins() {
		installFakePeer(t, feModules, pkg)
	}

	ReconcileFrontendTsconfigPeers(root)

	paths := readPinPaths(t, root)
	for _, pkg := range webruntimepeers.TypePins() {
		want := webruntimepeers.TypePinPath(pkg, false, "build/modern/index.d.ts")
		if got := paths[pkg]; len(got) != 1 || got[0] != want {
			t.Errorf("paths[%q] = %v, want exactly [%q] — a directory pin that resolves makes "+
				"webpack bypass the package's exports map and bundle a second copy", pkg, got, want)
		}
	}

	// Idempotent: the same install reconciled again changes nothing.
	path := filepath.Join(root, "frontends", "web", "tsconfig.json")
	before, _ := os.ReadFile(path)
	ReconcileFrontendTsconfigPeers(root)
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Errorf("second reconcile rewrote the file\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}
}

// The entry follows the layout: a workspace install hoists the peers, so the
// manifest to read — and the file to pin — is under the ROOT node_modules.
func TestReconcileFrontendTsconfigPeers_DeclarationFilesFollowTheHoist(t *testing.T) {
	t.Parallel()

	root := writeLayoutFixture(t, true)
	for _, pkg := range webruntimepeers.TypePins() {
		installFakePeer(t, filepath.Join(root, "node_modules"), pkg)
	}

	ReconcileFrontendTsconfigPeers(root)

	want := webruntimepeers.TypePinPath("@tanstack/react-query", true, "build/modern/index.d.ts")
	if got := readPinPaths(t, root)["@tanstack/react-query"]; len(got) != 1 || got[0] != want {
		t.Errorf("paths[@tanstack/react-query] = %v, want exactly [%q]", got, want)
	}
}
