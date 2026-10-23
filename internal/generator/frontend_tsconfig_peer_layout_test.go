package generator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/webruntimepeers"
)

// writeLayoutFixture builds a project with one frontend whose tsconfig pins
// every peer at the frontend-local layout — what the scaffold writes before an
// install has decided anything.
func writeLayoutFixture(t *testing.T, workspaceRoot bool) string {
	t.Helper()

	root := t.TempDir()
	feDir := filepath.Join(root, "frontends", "web")
	if err := os.MkdirAll(feDir, 0o755); err != nil {
		t.Fatal(err)
	}

	var pins strings.Builder
	for _, pkg := range webruntimepeers.TypePins() {
		pins.WriteString(`      "` + pkg + `": ["` + webruntimepeers.TypePinPath(pkg, false, "") + `"],` + "\n")
	}
	tsconfig := "{\n  \"compilerOptions\": {\n    \"paths\": {\n" + pins.String() +
		"      \"@/*\": [\"./src/*\"]\n    }\n  }\n}\n"
	if err := os.WriteFile(filepath.Join(feDir, "tsconfig.json"), []byte(tsconfig), 0o644); err != nil {
		t.Fatal(err)
	}

	if workspaceRoot {
		manifest := `{"name":"root","private":true,"workspaces":["frontends/*",".forge-link/*"]}`
		if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(manifest), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func readPinPaths(t *testing.T, root string) map[string][]string {
	t.Helper()

	body, err := os.ReadFile(filepath.Join(root, "frontends", "web", "tsconfig.json"))
	if err != nil {
		t.Fatal(err)
	}
	stripped := regexp.MustCompile(`(?m)^\s*//.*$`).ReplaceAll(body, nil)
	var cfg struct {
		CompilerOptions struct {
			Paths map[string][]string `json:"paths"`
		} `json:"compilerOptions"`
	}
	if err := json.Unmarshal(stripped, &cfg); err != nil {
		t.Fatalf("reconciled tsconfig is not valid JSON: %v\n%s", err, body)
	}
	return cfg.CompilerOptions.Paths
}

// TestReconcileFrontendTsconfigPeers_RetargetsOnWorkspaceHoist is the
// reproduction for the `forge scaffold frontend` path.
//
// That verb writes the tsconfig and THEN runs `npm install`, and the install is
// what decides the layout. In a workspace project npm hoists to the project
// root and creates no frontends/<name>/node_modules, so the pins just written
// name a directory that does not exist. tsc reports nothing for a pin that
// resolves to nothing — it falls back to the ordinary walk, finds the linked
// runtime's own copy, and the freshly added frontend fails its first
// `tsc --noEmit` with TS2322 "Transport is not assignable to Transport".
func TestReconcileFrontendTsconfigPeers_RetargetsOnWorkspaceHoist(t *testing.T) {
	t.Parallel()

	root := writeLayoutFixture(t, true)
	ReconcileFrontendTsconfigPeers(root)

	paths := readPinPaths(t, root)
	for _, pkg := range webruntimepeers.TypePins() {
		want := webruntimepeers.TypePinPath(pkg, true, "")
		got, ok := paths[pkg]
		if !ok {
			t.Errorf("paths lost its entry for %q", pkg)
			continue
		}
		if len(got) != 1 || got[0] != want {
			t.Errorf("paths[%q] = %v, want exactly [%q] — the workspace root hoists, so a "+
				"frontend-local pin resolves to nothing and tsc silently binds the linked "+
				"runtime's copy instead", pkg, got, want)
		}
	}
	if got := paths["@/*"]; len(got) != 1 || got[0] != "./src/*" {
		t.Errorf(`paths must still map "@/*" to ["./src/*"], got %v`, got)
	}
}

// A frontend that really has the PACKAGE locally is not hoisted, whatever the
// root says: node resolution prefers the nearest copy, so the pin must too.
func TestReconcileFrontendTsconfigPeers_NestedInstallWins(t *testing.T) {
	t.Parallel()

	root := writeLayoutFixture(t, true)
	// The package itself, not merely a node_modules directory — see
	// frontendPinsAreHoisted for why the difference decides the answer.
	if err := os.MkdirAll(filepath.Join(root, "frontends", "web", "node_modules", "@connectrpc", "connect"), 0o755); err != nil {
		t.Fatal(err)
	}

	ReconcileFrontendTsconfigPeers(root)

	paths := readPinPaths(t, root)
	want := webruntimepeers.TypePinPath("@connectrpc/connect", false, "")
	if got := paths["@connectrpc/connect"]; len(got) != 1 || got[0] != want {
		t.Errorf("paths[@connectrpc/connect] = %v, want exactly [%q] — a real nested install "+
			"is the nearest copy and must not be retargeted at the root", got, want)
	}
}

// TestReconcileFrontendTsconfigPeers_PartialNestedInstall is the case that a
// directory-existence check gets wrong.
//
// npm hoists what it can and leaves behind only what it cannot: a vite-spa
// frontend ends up with esbuild and vite in its OWN node_modules while
// @connectrpc/connect and @bufbuild/protobuf sit at the workspace root. So
// "frontends/<name>/node_modules exists" is true and "the peers are there" is
// false. Reading the directory rather than the package pinned the peers at a
// directory that does not contain them; the pin resolved to nothing, tsc fell
// back to the ordinary walk, and it bound the linked runtime's copy — TS2322
// on a frontend that had just been scaffolded.
func TestReconcileFrontendTsconfigPeers_PartialNestedInstall(t *testing.T) {
	t.Parallel()

	root := writeLayoutFixture(t, true)
	// Present locally, but NOT the peers — exactly what npm leaves behind.
	for _, pkg := range []string{"vite", "esbuild"} {
		if err := os.MkdirAll(filepath.Join(root, "frontends", "web", "node_modules", pkg), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// The peers really live at the root.
	if err := os.MkdirAll(filepath.Join(root, "node_modules", "@connectrpc", "connect"), 0o755); err != nil {
		t.Fatal(err)
	}

	ReconcileFrontendTsconfigPeers(root)

	want := webruntimepeers.TypePinPath("@connectrpc/connect", true, "")
	if got := readPinPaths(t, root)["@connectrpc/connect"]; len(got) != 1 || got[0] != want {
		t.Errorf("paths[@connectrpc/connect] = %v, want exactly [%q] — the frontend's own "+
			"node_modules exists but does NOT hold the peer, so the pin must point at the root",
			got, want)
	}
}

// An ordinary standalone project must be left exactly as scaffolded, and a
// second pass must never rewrite the file — `forge generate` twice in a row
// still reports no changes.
func TestReconcileFrontendTsconfigPeers_Idempotent(t *testing.T) {
	t.Parallel()

	root := writeLayoutFixture(t, false)
	path := filepath.Join(root, "frontends", "web", "tsconfig.json")

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ReconcileFrontendTsconfigPeers(root)
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("rewrote a standalone project's tsconfig\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}
}

// Every emitted pin must hold exactly one element. Next.js resolves through
// SWC, which asserts that rule for a key with no `*` and panics the whole
// build otherwise — which is why the layout is resolved rather than listed.
func TestReconcileFrontendTsconfigPeers_OneCandidatePerPin(t *testing.T) {
	t.Parallel()

	root := writeLayoutFixture(t, true)
	ReconcileFrontendTsconfigPeers(root)

	for pkg, got := range readPinPaths(t, root) {
		if pkg == "@/*" {
			continue // wildcard keys are exempt from the SWC rule
		}
		if len(got) != 1 {
			t.Errorf("paths[%q] has %d elements, want 1 — SWC panics `next build` on a "+
				"multi-element value for a non-wildcard key", pkg, len(got))
		}
	}
}
