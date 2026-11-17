package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/webruntimepeers"
)

// installFakePeerForGenerate writes pkg the way npm would, with react-query's
// real resolution fields: `types` names build/legacy, `exports` build/modern.
func installFakePeerForGenerate(t *testing.T, nodeModules, pkg string) {
	t.Helper()
	dir := filepath.Join(nodeModules, filepath.FromSlash(webruntimepeers.TypePinTarget(pkg)))
	mustMkdirAll(t, filepath.Join(dir, "build", "modern"))
	mustMkdirAll(t, filepath.Join(dir, "build", "legacy"))
	mustWrite(t, filepath.Join(dir, "package.json"), `{"name":"`+pkg+`","types":"build/legacy/index.d.ts",
  "exports":{".":{"import":{"types":"./build/modern/index.d.ts","default":"./build/modern/index.js"}}}}`)
	mustWrite(t, filepath.Join(dir, "build", "modern", "index.d.ts"), "export {};\n")
	mustWrite(t, filepath.Join(dir, "build", "legacy", "index.d.ts"), "export {};\n")
}

// TestGenerateHealsResolvingDirectoryPins is the reproduction for EXISTING
// projects. Every forge project scaffolded before this fix committed its peer
// pins as package DIRECTORIES, and in a checkout where they resolve (an
// ordinary `npm ci` in the frontend) Next's webpack resolver follows them for
// app code, skips `exports`, and bundles a second copy of every pinned package
// whose exports disagree with module/main — "No QueryClient set" at prerender.
// `forge generate` must heal those pins into declaration-file pins.
func TestGenerateHealsResolvingDirectoryPins(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	feDir := filepath.Join(projectDir, "frontends", "web")
	mustMkdirAll(t, feDir)
	mustWrite(t, filepath.Join(feDir, "package.json"),
		`{"dependencies":{"@reliantlabs/forge-web-runtime":"^0.3.1"}}`)
	path := filepath.Join(feDir, "tsconfig.json")
	mustWrite(t, path, tsconfigWithPins(false))
	for _, pkg := range tsconfigPeerPins() {
		installFakePeerForGenerate(t, filepath.Join(feDir, "node_modules"), pkg)
	}

	cfg := &config.ProjectConfig{Name: "p", Frontends: []config.FrontendConfig{
		config.FrontendConfig{Name: "web", Type: "nextjs"}.WithDir("frontends/web"),
	}}
	reconcileFrontendTsconfigPeers(cfg, projectDir)

	paths := parseTsconfig(t, mustReadTsconfig(t, path))
	for _, pkg := range tsconfigPeerPins() {
		want := webruntimepeers.TypePinPath(pkg, false, "build/modern/index.d.ts")
		if got := paths[pkg]; len(got) != 1 || got[0] != want {
			t.Errorf("paths[%q] = %v, want exactly [%q] — a resolving directory pin splits the "+
				"webpack bundle; generate must heal it to the package's declaration file", pkg, got, want)
		}
	}
}

// The heal reads the package ONLY at the layout the committed pin names; it
// never changes the layout. That is what keeps control-plane's case — pins
// committed at the project root ("../../node_modules/…"), and a developer who
// ran `npm ci` INSIDE the frontend — generating the same bytes as a fresh
// clone (#248). The nested copy is real and has a real manifest, but it is
// not where the committed pin points, so it is not evidence about that pin.
func TestGenerateHealIgnoresAnInstallAtAnotherLayout(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	feDir := filepath.Join(projectDir, "frontends", "internal-console")
	mustMkdirAll(t, feDir)
	mustWrite(t, filepath.Join(feDir, "package.json"),
		`{"dependencies":{"@reliantlabs/forge-web-runtime":"^0.3.1"}}`)
	path := filepath.Join(feDir, "tsconfig.json")
	committed := tsconfigWithPins(true)
	mustWrite(t, path, committed)
	for _, pkg := range tsconfigPeerPins() {
		installFakePeerForGenerate(t, filepath.Join(feDir, "node_modules"), pkg)
	}

	cfg := &config.ProjectConfig{Name: "p", Frontends: []config.FrontendConfig{
		config.FrontendConfig{Name: "internal-console", Type: "nextjs"}.WithDir("frontends/internal-console"),
	}}
	reconcileFrontendTsconfigPeers(cfg, projectDir)

	if got := mustReadTsconfig(t, path); got != committed {
		t.Errorf("a nested install rewrote committed root pins — the file now depends on where someone "+
			"ran npm ci:\n%s", firstDiffLine(committed, got))
	}
}

// `next build` rewrites tsconfig.json itself whenever it adds its own
// includes ("We detected TypeScript in your project and reconfigured your
// tsconfig.json"), and it writes every array multi-line. That is the shape
// most real Next projects carry, so the heal has to recognise it — and must
// put the surrounding whitespace back exactly.
func TestGenerateHealsNextReformattedPins(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	feDir := filepath.Join(projectDir, "frontends", "web")
	mustMkdirAll(t, feDir)
	mustWrite(t, filepath.Join(feDir, "package.json"),
		`{"dependencies":{"@reliantlabs/forge-web-runtime":"^0.3.1"}}`)
	var b strings.Builder
	b.WriteString("{\n  \"compilerOptions\": {\n    \"paths\": {\n")
	for _, pkg := range tsconfigPeerPins() {
		b.WriteString(`      "` + pkg + "\": [\n        \"" + webruntimepeers.TypePinPath(pkg, false, "") + "\"\n      ],\n")
		installFakePeerForGenerate(t, filepath.Join(feDir, "node_modules"), pkg)
	}
	b.WriteString("      \"@/*\": [\n        \"./src/*\"\n      ]\n    }\n  }\n}\n")
	path := filepath.Join(feDir, "tsconfig.json")
	mustWrite(t, path, b.String())

	cfg := &config.ProjectConfig{Name: "p", Frontends: []config.FrontendConfig{
		config.FrontendConfig{Name: "web", Type: "nextjs"}.WithDir("frontends/web"),
	}}
	reconcileFrontendTsconfigPeers(cfg, projectDir)

	got := mustReadTsconfig(t, path)
	want := "\"@tanstack/react-query\": [\n        \"./node_modules/@tanstack/react-query/build/modern/index.d.ts\"\n      ],"
	if !strings.Contains(got, want) {
		t.Errorf("the multi-line pin next build writes was not healed in place; want\n%s\nin\n%s", want, got)
	}
	if n := strings.Count(got, `"@tanstack/react-query":`); n != 1 {
		t.Errorf("@tanstack/react-query appears %d times — the heal must rewrite, not append", n)
	}
}

// The heal must never become the flip-flop #248 removed. That was two states
// each rewriting the other according to who last ran `npm install`. Here the
// states are: an inert directory pin (nothing installed — left alone), and a
// declaration-file pin (kept verbatim whatever is installed). Once healed,
// generate is a fixed point on a fresh clone, on an installed tree, and after
// the install is deleted again.
func TestGenerateDeclarationPinsAreAFixedPoint(t *testing.T) {
	t.Parallel()

	for _, hoisted := range []bool{false, true} {
		projectDir := t.TempDir()
		feDir := filepath.Join(projectDir, "frontends", "internal-console")
		mustMkdirAll(t, feDir)
		mustWrite(t, filepath.Join(feDir, "package.json"),
			`{"dependencies":{"@reliantlabs/forge-web-runtime":"^0.3.1"}}`)

		// Committed already healed, at this project's layout.
		var committed string
		{
			committed = "{\n  \"compilerOptions\": {\n    \"paths\": {\n"
			for _, pkg := range tsconfigPeerPins() {
				committed += `      "` + pkg + `": ["` + webruntimepeers.TypePinPath(pkg, hoisted, "build/modern/index.d.ts") + `"],` + "\n"
			}
			committed += "      \"@/*\": [\"./src/*\"]\n    }\n  }\n}\n"
		}
		path := filepath.Join(feDir, "tsconfig.json")
		mustWrite(t, path, committed)

		cfg := &config.ProjectConfig{Name: "p", Frontends: []config.FrontendConfig{
			config.FrontendConfig{Name: "internal-console", Type: "nextjs"}.WithDir("frontends/internal-console"),
		}}

		steps := []struct {
			name    string
			prepare func()
		}{
			{"fresh clone, nothing installed", func() {}},
			{"npm ci in the frontend", func() {
				for _, pkg := range tsconfigPeerPins() {
					installFakePeerForGenerate(t, filepath.Join(feDir, "node_modules"), pkg)
				}
			}},
			{"a hoisted copy too", func() {
				for _, pkg := range tsconfigPeerPins() {
					installFakePeerForGenerate(t, filepath.Join(projectDir, "node_modules"), pkg)
				}
			}},
			{"install deleted again", func() {
				_ = os.RemoveAll(filepath.Join(feDir, "node_modules"))
				_ = os.RemoveAll(filepath.Join(projectDir, "node_modules"))
			}},
		}
		for _, st := range steps {
			st.prepare()
			if reconcileFrontendTsconfigPeers(cfg, projectDir); mustReadTsconfig(t, path) != committed {
				t.Errorf("hoisted=%v, after %q: generate rewrote committed declaration pins:\n%s",
					hoisted, st.name, firstDiffLine(committed, mustReadTsconfig(t, path)))
			}
		}
	}
}
