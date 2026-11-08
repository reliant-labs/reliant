package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/generator"
	"github.com/reliant-labs/forge/internal/webruntimepeers"
)

// tsconfig.json is GENERATED AND COMMITTED, and consumers re-run `forge
// generate` in CI and fail the build on any diff. So the layout decision must
// never depend on state that differs between a developer's tree and a fresh
// clone — node_modules is a build artifact, and forge's own dev workspace root
// is gitignored by construction.
//
// The guarantee under test: for one fixed set of tracked files, installing or
// removing node_modules must not change what forge writes.
func TestFrontendPinLayoutIgnoresInstallState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// declareWorkspaceRoot writes an npm workspace root manifest.
		declareWorkspaceRoot bool
		// linkRuntime marks the frontend a workspace member in its own
		// package.json.
		linkRuntime bool
		wantHoisted bool
		wantKnown   bool
	}{
		{name: "workspace root declared", declareWorkspaceRoot: true, wantHoisted: true, wantKnown: true},
		{name: "frontend links the runtime", linkRuntime: true, wantHoisted: true, wantKnown: true},
		// The case that broke control-plane: tracked files say nothing,
		// because the thing that WOULD say something is gitignored.
		{name: "plain frontend, nothing declared", wantKnown: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// The install variants model what npm ACTUALLY does for each
			// shape, which is the comparison that matters: a CI runner has
			// installed nothing, a developer's tree has. Those two must agree,
			// and disagreeing is exactly what turned control-plane red.
			//
			// A workspace install hoists the peer to the root and leaves none
			// nested, so the nested variant is not offered here — a genuinely
			// nested copy is a DIFFERENT layout (the resolver lands there),
			// not the same layout differently installed.
			for _, installed := range []bool{false, true} {
				projectDir := t.TempDir()
				feDir := filepath.Join(projectDir, "frontends", "console")
				mustMkdirAll(t, feDir)

				if tt.declareWorkspaceRoot {
					mustWrite(t, filepath.Join(projectDir, "package.json"),
						`{"workspaces":["frontends/*"]}`)
				}
				fe := `{"dependencies":{"@reliantlabs/forge-web-runtime":"^0.3.1"}}`
				if tt.linkRuntime {
					fe = `{"dependencies":{"@reliantlabs/forge-web-runtime":"workspace:*"}}`
				}
				mustWrite(t, filepath.Join(feDir, "package.json"), fe)

				wantKnown, wantHoisted := tt.wantKnown, tt.wantHoisted
				if installed {
					if tt.wantKnown {
						// Declared a workspace: npm hoists to the root.
						mustMkdirAll(t, filepath.Join(projectDir, "node_modules", "@connectrpc", "connect"))
					} else {
						// Declared nothing: a standalone `npm ci` in the
						// frontend. This is the case that flipped
						// control-plane's committed pins: the nested copy
						// made the layout "known" (local), and generate
						// rewrote 14 committed "../../" pins to "./". An
						// install is not a declaration — the answer must
						// stay "unknown, leave the committed pins alone".
						mustMkdirAll(t, filepath.Join(feDir, "node_modules", "@connectrpc", "connect"))
					}
				}

				got := generator.DetectFrontendPinLayout(projectDir, feDir)
				if got.Known != wantKnown || (got.Known && got.Hoisted != wantHoisted) {
					t.Errorf("DetectFrontendPinLayout(installed=%v) = %+v, want {Hoisted:%v Known:%v} — "+
						"a declared layout must not be re-decided by whether anyone has run "+
						"npm install, or the same commit generates a different tsconfig.json "+
						"on CI than on a developer machine",
						installed, got, wantHoisted, wantKnown)
				}
			}
		})
	}
}

// A genuinely nested copy of the peer outranks a workspace declaration: node
// resolution prefers the NEAREST node_modules, so whatever the root declares,
// that is the copy the resolver binds. That is evidence forge may act on only
// at scaffold time, right after its own install (ObserveInstalledPinLayout) —
// never at generate time, which must not read node_modules at all.
func TestNestedInstallOutranksWorkspaceDeclaration(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	feDir := filepath.Join(projectDir, "frontends", "console")
	mustMkdirAll(t, feDir)
	mustWrite(t, filepath.Join(projectDir, "package.json"), `{"workspaces":["frontends/*"]}`)
	mustWrite(t, filepath.Join(feDir, "package.json"),
		`{"dependencies":{"@reliantlabs/forge-web-runtime":"workspace:*"}}`)
	// Both copies exist; the nested one is nearest.
	mustMkdirAll(t, filepath.Join(projectDir, "node_modules", "@connectrpc", "connect"))
	mustMkdirAll(t, filepath.Join(feDir, "node_modules", "@connectrpc", "connect"))

	if got := generator.ObserveInstalledPinLayout(projectDir, feDir); !got.Known || got.Hoisted {
		t.Errorf("ObserveInstalledPinLayout = %+v, want {Hoisted:false Known:true} — a real "+
			"nested copy is the one node resolution binds", got)
	}
	// The generate-time decision reads declarations only: the root workspace
	// declares hoisting, and no install may overrule a declaration there.
	if got := generator.DetectFrontendPinLayout(projectDir, feDir); !got.Known || !got.Hoisted {
		t.Errorf("DetectFrontendPinLayout = %+v, want {Hoisted:true Known:true} from the workspace declaration", got)
	}
}

// TestReconcileLeavesCommittedPinsAloneWhenLayoutUnknown is the REPRODUCTION
// of the control-plane "Verify Generated Code" failure.
//
// control-plane commits `../../node_modules/…` pins because its real layout is
// forge's dev workspace bridge — but that bridge's root package.json is
// GITIGNORED, and node_modules is absent from a fresh checkout. So on a CI
// clone forge sees a per-frontend package.json naming the registry range and a
// per-frontend lockfile: evidence identical to a plain standalone project.
//
// Forge used to resolve that silence to "local" and rewrite all 14 pins to
// `./node_modules/…`, turning the job red on a file nobody had touched. The
// committed value is itself a reviewed declaration of the layout, and is
// better evidence than anything inferable from an uninstalled tree, so an
// unidentifiable layout must leave it exactly as committed.
func TestReconcileLeavesCommittedPinsAloneWhenLayoutUnknown(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	feDir := filepath.Join(projectDir, "frontends", "internal-console")
	mustMkdirAll(t, feDir)
	// Exactly control-plane's tracked shape: registry range, no root manifest,
	// nothing installed.
	mustWrite(t, filepath.Join(feDir, "package.json"),
		`{"dependencies":{"@reliantlabs/forge-web-runtime":"^0.3.1"}}`)

	path := filepath.Join(feDir, "tsconfig.json")
	committed := tsconfigWithPins(true)
	mustWrite(t, path, committed)

	layout := generator.DetectFrontendPinLayout(projectDir, feDir)
	if layout.Known {
		t.Fatalf("layout reported Known=%v from a fresh clone's tracked files; a bridged "+
			"project is indistinguishable from a standalone one here", layout)
	}

	if addPeerPinsToTsconfig(path, layout) {
		t.Error("rewrote a committed tsconfig on a fresh clone — this is the diff that " +
			"turns Verify Generated Code red on a file nobody touched")
	}
	if got := mustReadTsconfig(t, path); got != committed {
		t.Errorf("committed pins were rewritten\n--- committed ---\n%s\n--- after ---\n%s", committed, got)
	}
}

// The other half of the contract: a project whose layout IS identifiable still
// gets a stale pin healed. Deferring to the file when forge cannot tell must
// not become deferring to it when forge can.
func TestReconcileStillHealsStalePinsWhenLayoutKnown(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		declareRoot bool
		wantHoisted bool
	}{
		{name: "workspace root heals local pins to hoisted", declareRoot: true, wantHoisted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			projectDir := t.TempDir()
			feDir := filepath.Join(projectDir, "frontends", "console")
			mustMkdirAll(t, feDir)
			mustWrite(t, filepath.Join(feDir, "package.json"),
				`{"dependencies":{"@reliantlabs/forge-web-runtime":"^0.3.1"}}`)

			if tc.declareRoot {
				mustWrite(t, filepath.Join(projectDir, "package.json"), `{"workspaces":["frontends/*"]}`)
			} else {
				mustMkdirAll(t, filepath.Join(feDir, "node_modules", "@connectrpc", "connect"))
			}

			// Committed at the WRONG layout — the stale-pin case.
			path := filepath.Join(feDir, "tsconfig.json")
			mustWrite(t, path, tsconfigWithPins(!tc.wantHoisted))

			layout := generator.DetectFrontendPinLayout(projectDir, feDir)
			if !layout.Known || layout.Hoisted != tc.wantHoisted {
				t.Fatalf("DetectFrontendPinLayout = %+v, want {Hoisted:%v Known:true}", layout, tc.wantHoisted)
			}
			if !addPeerPinsToTsconfig(path, layout) {
				t.Fatal("reported no change though every pin named the wrong layout — a pin " +
					"that resolves to nothing is not an error tsc reports, it silently binds " +
					"the linked runtime's copy instead (TS2322)")
			}

			paths := parseTsconfig(t, mustReadTsconfig(t, path))
			for _, pkg := range tsconfigPeerPins() {
				want := webruntimepeers.TypePinPath(pkg, tc.wantHoisted, "")
				if got := paths[pkg]; len(got) != 1 || got[0] != want {
					t.Errorf("paths[%q] = %v, want exactly [%q]", pkg, got, want)
				}
			}

			// And a second pass at the same layout is a no-op.
			if addPeerPinsToTsconfig(path, layout) {
				t.Error("second pass reported a change; the reconcile must be idempotent")
			}
		})
	}
}

// tsconfigWithPins renders a tsconfig whose peer pins all name one layout.
func tsconfigWithPins(hoisted bool) string {
	out := "{\n  \"compilerOptions\": {\n    \"paths\": {\n"
	for _, pkg := range webruntimepeers.TypePins() {
		out += `      "` + pkg + `": ["` + webruntimepeers.TypePinPath(pkg, hoisted, "") + `"],` + "\n"
	}
	return out + "      \"@/*\": [\"./src/*\"]\n    }\n  }\n}\n"
}

func mustMkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", dir, err)
	}
}
