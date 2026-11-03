package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/generator"
	"github.com/reliant-labs/forge/internal/webruntimepeers"
)

// jsonCommentLine strips `// …` line comments so a tsconfig (JSONC) can be
// parsed by encoding/json. tsc tolerates them; encoding/json does not.
var jsonCommentLine = regexp.MustCompile(`(?m)^\s*//.*$`)

func parseTsconfig(t *testing.T, raw string) map[string][]string {
	t.Helper()
	var cfg struct {
		CompilerOptions struct {
			Paths map[string][]string `json:"paths"`
		} `json:"compilerOptions"`
	}
	stripped := jsonCommentLine.ReplaceAllString(raw, "")
	if err := json.Unmarshal([]byte(stripped), &cfg); err != nil {
		t.Fatalf("reconciled tsconfig is not valid JSON after comment-stripping: %v\n%s", err, raw)
	}
	return cfg.CompilerOptions.Paths
}

// A tsconfig as scaffolded BEFORE the peer-pin template change — the shape
// every existing forge project carries, because tsconfig.json is written once
// at birth and never rewritten.
const legacyTsconfig = `{
  "compilerOptions": {
    "target": "ES2022",
    "strict": true,
    "moduleResolution": "bundler",
    "paths": {
      "@/*": ["./src/*"]
    }
  },
  "include": ["**/*.ts", "**/*.tsx"],
  "exclude": ["node_modules"]
}
`

// TestAddPeerPinsToTsconfig_LegacyProject is the reproduction.
//
// Without the reconcile pass this file keeps only "@/*", the linked
// web-runtime's copy of @connectrpc/connect resolves out of the forge
// checkout, and `tsc --noEmit` fails mock-transport_gen.ts with TS2322
// "Type Transport is not assignable to type Transport".
func TestAddPeerPinsToTsconfig_LegacyProject(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "tsconfig.json")
	if err := os.WriteFile(path, []byte(legacyTsconfig), 0o644); err != nil {
		t.Fatal(err)
	}

	if !addPeerPinsToTsconfig(path, knownLayout(false)) {
		t.Fatal("addPeerPinsToTsconfig reported no change on a tsconfig with no peer pins")
	}

	raw := mustReadTsconfig(t, path)
	paths := parseTsconfig(t, raw)

	for _, pkg := range tsconfigPeerPins() {
		// The key is the import specifier; the value is where its TYPES
		// live. Those differ for a package typed by a separate @types/
		// package — pinning react at ./node_modules/react gives tsc a
		// directory with no .d.ts and fails every .tsx with TS7016.
		// EXACTLY ONE element, aimed at this project's layout. Not a
		// candidate list: SWC asserts the single-element rule for a
		// non-wildcard key and panics `next build` on anything longer.
		want := webruntimepeers.TypePinPath(pkg, false, "")
		got, ok := paths[pkg]
		if !ok {
			t.Errorf("paths has no entry for %q — tsc resolves it from the linked runtime "+
				"and fails mock-transport_gen.ts with TS2322. paths=%v", pkg, paths)
			continue
		}
		if len(got) != 1 || got[0] != want {
			t.Errorf("paths[%q] = %v, want exactly [%q] so it resolves to THIS app's copy", pkg, got, want)
		}
	}

	// The pre-existing mapping must survive — every scaffolded source file
	// imports through it.
	if got := paths["@/*"]; len(got) != 1 || got[0] != "./src/*" {
		t.Errorf(`paths must still map "@/*" to ["./src/*"], got %v`, got)
	}
}

// TestAddPeerPinsToTsconfig_Idempotent pins the property that keeps
// `forge generate` twice in a row reporting no changes: a second pass must
// leave the file byte-identical.
func TestAddPeerPinsToTsconfig_Idempotent(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "tsconfig.json")
	if err := os.WriteFile(path, []byte(legacyTsconfig), 0o644); err != nil {
		t.Fatal(err)
	}

	addPeerPinsToTsconfig(path, knownLayout(false))
	first := mustReadTsconfig(t, path)

	if addPeerPinsToTsconfig(path, knownLayout(false)) {
		t.Error("second pass reported a change; the reconcile must be idempotent")
	}
	if second := mustReadTsconfig(t, path); second != first {
		t.Errorf("second pass rewrote the file\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
}

// TestAddPeerPinsToTsconfig_NoPathsBlock — forge does not invent a
// compilerOptions key in a file it does not own. A `paths` block can require
// `baseUrl` depending on the config, and guessing there breaks more than it
// fixes, so this is a documented skip rather than a best-effort insertion.
func TestAddPeerPinsToTsconfig_NoPathsBlock(t *testing.T) {
	t.Parallel()

	const noPaths = `{"compilerOptions":{"strict":true}}`
	path := filepath.Join(t.TempDir(), "tsconfig.json")
	if err := os.WriteFile(path, []byte(noPaths), 0o644); err != nil {
		t.Fatal(err)
	}

	if addPeerPinsToTsconfig(path, knownLayout(false)) {
		t.Error("changed a tsconfig with no paths block; forge must leave it alone")
	}
	if got := mustReadTsconfig(t, path); got != noPaths {
		t.Errorf("file was modified: %s", got)
	}
}

// TestAddPeerPinsToTsconfig_PartialPins covers the project that already
// carries SOME pins (an older run of this pass, or a hand-edit): only the
// genuinely missing keys are added, and no key is duplicated.
func TestAddPeerPinsToTsconfig_PartialPins(t *testing.T) {
	t.Parallel()

	partial := strings.Replace(legacyTsconfig,
		`"@/*": ["./src/*"]`,
		`"@connectrpc/connect": ["./node_modules/@connectrpc/connect"],
      "@/*": ["./src/*"]`, 1)

	path := filepath.Join(t.TempDir(), "tsconfig.json")
	if err := os.WriteFile(path, []byte(partial), 0o644); err != nil {
		t.Fatal(err)
	}

	if !addPeerPinsToTsconfig(path, knownLayout(false)) {
		t.Fatal("reported no change though several pins were missing")
	}

	raw := mustReadTsconfig(t, path)
	if n := strings.Count(raw, `"@connectrpc/connect":`); n != 1 {
		t.Errorf("@connectrpc/connect appears %d times, want 1 — the existing pin must not be duplicated", n)
	}
	paths := parseTsconfig(t, raw)
	for _, pkg := range tsconfigPeerPins() {
		if _, ok := paths[pkg]; !ok {
			t.Errorf("paths has no entry for %q after reconcile; paths=%v", pkg, paths)
		}
	}
}

// TestAddPeerPinsToTsconfig_RetargetsStaleLayout pins the healing half.
//
// tsconfig.json is scaffold-once, so a project whose node_modules layout later
// changed — most commonly because a dev forge build wrote the workspace bridge
// and npm hoisted every frontend's dependencies to the project root — keeps
// pins aimed at a frontends/<name>/node_modules that no longer exists. tsc
// reports nothing for a pin that resolves to nothing: it silently falls back
// to the ordinary walk, finds the linked runtime's own copy, and fails
// mock-transport_gen.ts with TS2322. Adding the missing pins is not enough;
// the present-but-wrong ones have to be retargeted.
func TestAddPeerPinsToTsconfig_RetargetsStaleLayout(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "tsconfig.json")
	if err := os.WriteFile(path, []byte(legacyTsconfig), 0o644); err != nil {
		t.Fatal(err)
	}

	// Born standalone…
	if !addPeerPinsToTsconfig(path, knownLayout(false)) {
		t.Fatal("first pass reported no change")
	}
	// …then the tree became a workspace and npm hoisted.
	if !addPeerPinsToTsconfig(path, knownLayout(true)) {
		t.Fatal("layout changed to hoisted but the reconcile reported no change — " +
			"the pins still name a directory that does not exist, and tsc silently " +
			"resolves the linked runtime's copy instead (TS2322)")
	}

	paths := parseTsconfig(t, mustReadTsconfig(t, path))
	for _, pkg := range tsconfigPeerPins() {
		want := webruntimepeers.TypePinPath(pkg, true, "")
		got, ok := paths[pkg]
		if !ok {
			t.Errorf("paths lost its entry for %q; paths=%v", pkg, paths)
			continue
		}
		if len(got) != 1 || got[0] != want {
			t.Errorf("paths[%q] = %v, want exactly [%q] for the hoisted layout", pkg, got, want)
		}
	}

	// Retargeting must not duplicate the key, and must leave "@/*" alone.
	raw := mustReadTsconfig(t, path)
	if n := strings.Count(raw, `"@connectrpc/connect":`); n != 1 {
		t.Errorf("@connectrpc/connect appears %d times after retarget, want 1", n)
	}
	if got := paths["@/*"]; len(got) != 1 || got[0] != "./src/*" {
		t.Errorf(`paths must still map "@/*" to ["./src/*"], got %v`, got)
	}

	// Re-running against the SAME layout is a no-op, so `forge generate`
	// twice in a row still reports no changes.
	if addPeerPinsToTsconfig(path, knownLayout(true)) {
		t.Error("second pass at the same layout reported a change; the reconcile must be idempotent")
	}
}

// A pin must carry exactly one element. TypeScript tolerates more, but Next.js
// resolves through SWC, which asserts the single-element rule for a key with
// no `*` and takes the whole build down with a Rust panic:
//
//	assertion `left == right` failed: value of `paths.@bufbuild/protobuf`
//	should be an array with one element because the src path does not
//	contains * (wildcard)
//
// That is why the fix resolves the layout instead of listing both.
func TestAddPeerPinsToTsconfig_EmitsExactlyOneCandidate(t *testing.T) {
	t.Parallel()

	for _, hoisted := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "tsconfig.json")
		if err := os.WriteFile(path, []byte(legacyTsconfig), 0o644); err != nil {
			t.Fatal(err)
		}
		addPeerPinsToTsconfig(path, knownLayout(hoisted))

		for pkg, got := range parseTsconfig(t, mustReadTsconfig(t, path)) {
			if pkg == "@/*" {
				continue // a wildcard key, exempt from the SWC rule
			}
			if len(got) != 1 {
				t.Errorf("paths[%q] (hoisted=%v) has %d elements, want 1 — SWC panics `next build` "+
					"on a multi-element value for a non-wildcard key", pkg, hoisted, len(got))
			}
		}
	}
}

func mustReadTsconfig(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// knownLayout is the test spelling of "forge identified this layout". These
// tests exercise the RETARGET behaviour, which only applies to an identified
// layout — the unknown case is covered in the determinism test file.
func knownLayout(hoisted bool) generator.PinLayout {
	return generator.PinLayout{Hoisted: hoisted, Known: true}
}
