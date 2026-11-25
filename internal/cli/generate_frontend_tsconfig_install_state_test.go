package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// tsconfig.json is generated AND COMMITTED, and consumers re-run `forge
// generate` in CI and fail the build on any diff. What forge writes into it
// must therefore be a function of committed inputs — never of whether, or
// where, somebody ran `npm install`.
//
// It was not. control-plane commits its internal-console pins as
// `../../node_modules/…`. A developer who ran `npm ci` inside the frontend
// got a nested node_modules/@connectrpc/connect, forge's layout probe read
// that as "installed locally", and `forge generate` rewrote all 14 committed
// pins to `./node_modules/…` — a diff on a file nobody had touched, which
// flipped back the next time someone with forge's dev bridge generated. The
// file followed the install layout of whoever ran generate last.
//
// This drives the generate-time reconcile over the same committed tree under
// every install state a checkout actually passes through, and requires the
// same bytes from all of them. (The dev-bridge state is DECLARED — forge's
// workspace root file — so it legitimately retargets; it is excluded from
// the comparison and asserted separately.)
func TestTsconfigPinsIgnoreInstallState(t *testing.T) {
	t.Parallel()

	installStates := []struct {
		name    string
		install func(t *testing.T, projectDir, feDir string)
	}{
		{name: "fresh clone, nothing installed", install: func(*testing.T, string, string) {}},
		{name: "npm ci inside the frontend", install: func(t *testing.T, _, feDir string) {
			mustMkdirAll(t, filepath.Join(feDir, "node_modules", "@connectrpc", "connect"))
		}},
		{name: "both a nested and a hoisted copy", install: func(t *testing.T, projectDir, feDir string) {
			mustMkdirAll(t, filepath.Join(projectDir, "node_modules", "@connectrpc", "connect"))
			mustMkdirAll(t, filepath.Join(feDir, "node_modules", "@connectrpc", "connect"))
		}},
	}

	// Two committed starting points: control-plane's (pins at the root), and
	// a project whose tsconfig predates the peer pins entirely.
	committedStates := []struct {
		name string
		body string
	}{
		{name: "committed root pins", body: committedRootPinTsconfig()},
		{name: "committed without pins", body: legacyTsconfig},
	}

	for _, committed := range committedStates {
		t.Run(committed.name, func(t *testing.T) {
			t.Parallel()

			results := map[string]string{}
			for _, st := range installStates {
				projectDir := t.TempDir()
				feDir := filepath.Join(projectDir, "frontends", "internal-console")
				mustMkdirAll(t, feDir)
				// Exactly control-plane's tracked shape: the registry range,
				// and no tracked root manifest.
				mustWrite(t, filepath.Join(feDir, "package.json"),
					`{"dependencies":{"@reliantlabs/forge-web-runtime":"^0.3.1"}}`)
				tsconfig := filepath.Join(feDir, "tsconfig.json")
				mustWrite(t, tsconfig, committed.body)
				st.install(t, projectDir, feDir)

				cfg := &config.ProjectConfig{
					Name: "p",
					Frontends: []config.FrontendConfig{
						config.FrontendConfig{Name: "internal-console", Type: "nextjs"}.WithDir("frontends/internal-console"),
					},
				}
				reconcileFrontendTsconfigPeers(cfg, projectDir)
				results[st.name] = mustReadTsconfig(t, tsconfig)
			}

			want := results[installStates[0].name]
			for _, st := range installStates[1:] {
				if got := results[st.name]; got != want {
					t.Errorf("forge generate wrote a different tsconfig.json after %q than on a fresh "+
						"clone — the committed file now depends on who last ran npm install:\n%s",
						st.name, firstDiffLine(want, got))
				}
			}
			if committed.body == committedRootPinTsconfig() && want != committed.body {
				t.Errorf("forge generate rewrote control-plane's committed pins on a fresh clone:\n%s",
					firstDiffLine(committed.body, want))
			}
		})
	}
}

// committedRootPinTsconfig is a tsconfig whose peer pins all name the project
// root's node_modules — the value control-plane commits.
func committedRootPinTsconfig() string {
	var b strings.Builder
	b.WriteString("{\n  \"compilerOptions\": {\n    \"paths\": {\n")
	for _, pkg := range tsconfigPeerPins() {
		target := pkg
		if pkg == "react" {
			target = "@types/react"
		}
		b.WriteString(`      "` + pkg + `": ["../../node_modules/` + target + `"],` + "\n")
	}
	b.WriteString("      \"@/*\": [\"./src/*\"]\n    }\n  }\n}\n")
	return b.String()
}

// firstDiffLine names the first line two renderings disagree on, which is
// all a reader needs to see which pin moved.
func firstDiffLine(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < len(w) || i < len(g); i++ {
		var wl, gl string
		if i < len(w) {
			wl = w[i]
		}
		if i < len(g) {
			gl = g[i]
		}
		if wl != gl {
			return "  want: " + strings.TrimSpace(wl) + "\n  got:  " + strings.TrimSpace(gl)
		}
	}
	return "(identical)"
}
