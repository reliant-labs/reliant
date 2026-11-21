package generator

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// TestFrontendTsconfigDedupesRuntimePeers pins the tsconfig half of the
// duplicate-peer fix.
//
// @reliantlabs/forge-web-runtime declares @connectrpc/connect,
// @bufbuild/protobuf and @opentelemetry/api as peerDependencies — "the
// consuming app supplies this copy". npm honours that for a registry
// install but CANNOT honour it for the `file:` specifier a dev build of
// forge writes to bridge a local checkout: that specifier becomes a
// SYMLINK into a working tree carrying its own node_modules, so the
// runtime binds its copy while the app binds its own.
//
// Both bundler configs already force the single copy — `resolve.dedupe`
// in vite.config.ts, a scoped resolver rule in next.config.ts. Neither
// helps `tsc --noEmit`, which runs no bundler. Typecheck therefore saw
// two nominally-distinct copies of the same package and failed the
// scaffold's own lint with, in a project forge had just generated:
//
//	src/lib/mock-transport_gen.ts(54,3): error TS2322: Type
//	'…/web-runtime/node_modules/@connectrpc/connect/…'.Transport is not
//	assignable to type '…/frontends/admin/node_modules/…'.Transport
//
// mock-transport_gen.ts imports Transport from the app's copy and is
// handed one built from the runtime's, so the two spellings meet exactly
// where forge's own generated code joins the runtime.
//
// A scaffold that does not pass the lint forge runs on it is the whole
// bug, so this asserts the mapping is present for every web kind rather
// than trusting the bundler configs to speak for typecheck.
func TestFrontendTsconfigDedupesRuntimePeers(t *testing.T) {
	t.Parallel()

	// Kept in step with vite.config.ts / next.config.ts. These are the
	// runtime's peerDependencies — the packages that must resolve to one
	// copy, the app's.
	peers := []string{
		"@connectrpc/connect",
		"@bufbuild/protobuf",
		"@opentelemetry/api",
	}

	// "" is the Next.js default; vite-spa is the SPA kind. Both emit
	// mock-transport_gen.ts and both link the runtime, so both need it.
	for _, kind := range []string{"", "vite-spa"} {
		label := kind
		if label == "" {
			label = "web(default)"
		}
		t.Run(label, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			if err := GenerateFrontendFilesWithOptions(
				root, "example.com/app", "app", "fe", 8080, kind, FrontendGenOptions{},
			); err != nil {
				t.Fatalf("GenerateFrontendFilesWithOptions(kind=%q): %v", kind, err)
			}

			raw := mustRead(t, filepath.Join(root, "frontends", "fe", "tsconfig.json"))

			// Parse rather than string-match: a mapping that does not
			// survive into valid JSON is not a mapping tsc will read,
			// and the template carries Workspaces conditionals around
			// this block.
			var cfg struct {
				CompilerOptions map[string]json.RawMessage `json:"compilerOptions"`
			}
			if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
				t.Fatalf("tsconfig.json is not valid JSON: %v\n%s", err, raw)
			}

			// tsc VALIDATES compilerOptions and rejects any key it does not
			// know with `TS5023: Unknown compiler option`. A "// note"-style
			// comment key is tolerated at the top level of tsconfig.json but
			// NOT here, so an explanatory comment placed inside this object
			// fails typecheck on every scaffolded project. Caught in CI the
			// first time, hence the guard.
			for key := range cfg.CompilerOptions {
				if strings.HasPrefix(key, "//") {
					t.Errorf("compilerOptions contains comment key %q — tsc rejects unknown keys "+
						"in this object with TS5023 and the scaffold fails its own typecheck. "+
						"Move the note to the TOP LEVEL of tsconfig.json, where tsc ignores it.", key)
				}
			}

			var paths map[string][]string
			if rawPaths, ok := cfg.CompilerOptions["paths"]; ok {
				if err := json.Unmarshal(rawPaths, &paths); err != nil {
					t.Fatalf("compilerOptions.paths is not a string->[]string map: %v", err)
				}
			}

			for _, peer := range peers {
				target, ok := paths[peer]
				if !ok {
					t.Errorf("tsconfig.json paths has no entry for %q — tsc will resolve it "+
						"from the linked runtime's node_modules and fail mock-transport_gen.ts "+
						"with TS2322 on two distinct Transport types. paths=%v",
						peer, paths)
					continue
				}
				// EXACTLY ONE element. Not belt-and-braces pedantry: Next.js
				// resolves through SWC, which asserts the single-element rule
				// for a key with no `*` and panics the whole build on a
				// longer value. The scaffold therefore names the one layout
				// the project has; the generate-time reconcile retargets it
				// if that layout later changes.
				want := "./node_modules/" + peer
				if len(target) != 1 || target[0] != want {
					t.Errorf("tsconfig.json maps %q to %v, want exactly [%q] so it resolves to "+
						"THIS app's copy (and so SWC does not panic `next build`)", peer, target, want)
				}
			}

			// react is pinned like the others, but its TARGET must be the
			// @types package. A paths entry overrides resolution outright,
			// so pointing at ./node_modules/react — which ships no .d.ts —
			// stops tsc consulting @types/react and fails EVERY .tsx in the
			// scaffold with TS7016. That shipped once; this pins it.
			if target, ok := paths["react"]; !ok {
				t.Errorf("tsconfig.json has no paths entry for react; paths=%v", paths)
			} else if want := "./node_modules/@types/react"; len(target) != 1 || target[0] != want {
				t.Errorf("tsconfig.json maps react to %v, want exactly [%q] — the implementation "+
					"directory has no typings and tsc reports TS7016 for every .tsx", target, want)
			}

			// The pre-existing "@/*" mapping must survive the addition —
			// every scaffolded source file imports through it.
			if got := paths["@/*"]; len(got) != 1 || got[0] != "./src/*" {
				t.Errorf(`tsconfig.json must still map "@/*" to ["./src/*"], got %v`, got)
			}
		})
	}
}
