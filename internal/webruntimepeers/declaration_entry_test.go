package webruntimepeers_test

import (
	"testing"

	"github.com/reliant-labs/forge/internal/webruntimepeers"
)

// The manifests below are the real package.json shapes (trimmed to the
// resolution fields) of the pinned packages at versions inside the ranges
// forge declares. Each one is a case the entry derivation has to get right,
// because a pin that names a file the installed version does not ship
// resolves to nothing — tsc silently falls back to its ordinary walk, and the
// dedupe the pin exists for is lost.
func TestDeclarationEntry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		manifest string
		want     string
	}{
		{
			// The package that failed prerender. `types` names the LEGACY
			// build and exports names the modern one; tsc under
			// `moduleResolution: bundler` binds exports, so the pin must too
			// or it would dedupe onto a different file than every importer
			// already resolves.
			name: "@tanstack/react-query 5.104.0: exports import-types beats legacy types",
			manifest: `{"types":"build/legacy/index.d.ts","module":"build/legacy/index.js",
				"exports":{".":{"@tanstack/custom-condition":"./src/index.ts",
				"import":{"types":"./build/modern/index.d.ts","default":"./build/modern/index.js"},
				"require":{"types":"./build/modern/index.d.cts","default":"./build/modern/index.cjs"}},
				"./package.json":"./package.json"}}`,
			want: "build/modern/index.d.ts",
		},
		{
			// No `types` field at all; the declaration is only reachable
			// as the sibling of the exports import target — which is what
			// tsc reads, and connect's `main` (CJS) disagrees with that
			// exports target (ESM), so leaving it a directory pin would
			// split it in the bundle exactly like react-query.
			name:     "@connectrpc/connect 2.1.2: no types field, sibling of the exports import target",
			manifest: `{"main":"./dist/cjs/index.js","exports":{".":{"import":"./dist/esm/index.js","require":"./dist/cjs/index.js"}}}`,
			want:     "dist/esm/index.d.ts",
		},
		{
			name: "@bufbuild/protobuf 2.14: import branch types",
			manifest: `{"types":"./dist/commonjs/index.d.ts","exports":{".":{
				"import":{"types":"./dist/esm/index.d.ts","default":"./dist/esm/index.js"},
				"require":{"types":"./dist/commonjs/index.d.ts","default":"./dist/commonjs/index.js"}}}}`,
			want: "dist/esm/index.d.ts",
		},
		{
			// The entry MOVED inside the declared ^2.0.0 range: 2.0.0 ships
			// index.d.ts, 2.11.0 only index-shim.d.ts. This is why the entry
			// is derived from the installed manifest and never hardcoded.
			name:     "@opentelemetry/sdk-trace-base 2.0.0",
			manifest: `{"main":"build/src/index.js","module":"build/esm/index.js","types":"build/src/index.d.ts"}`,
			want:     "build/src/index.d.ts",
		},
		{
			name:     "@opentelemetry/sdk-trace-base 2.11.0",
			manifest: `{"main":"build/src/index-shim.js","module":"build/esm/index-shim.js","types":"build/src/index-shim.d.ts"}`,
			want:     "build/src/index-shim.d.ts",
		},
		{
			name: "@opentelemetry/api 1.9: flat condition map with types",
			manifest: `{"types":"build/src/index.d.ts","exports":{".":{"module":"./build/esm/index.js",
				"esnext":"./build/esnext/index.js","types":"./build/src/index.d.ts","default":"./build/src/index.js"}}}`,
			want: "build/src/index.d.ts",
		},
		{
			// react's pin targets @types/react, whose exports gate the
			// declaration behind a typesVersions-style condition.
			name:     "@types/react 19: exports types.default",
			manifest: `{"types":"index.d.ts","exports":{".":{"types@<=5.0":{"default":"./ts5.0/index.d.ts"},"types":{"default":"./index.d.ts"}}}}`,
			want:     "index.d.ts",
		},
		{name: "unparseable manifest", manifest: `{`, want: ""},
		{name: "types pointing outside the package is refused", manifest: `{"types":"../evil.d.ts"}`, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := webruntimepeers.DeclarationEntry([]byte(tt.manifest), nil); got != tt.want {
				t.Errorf("DeclarationEntry = %q, want %q", got, tt.want)
			}
		})
	}
}

// A manifest can name a declaration the tarball does not ship. Pinning it
// would dangle — tsc says nothing and resolves the linked runtime's copy — so
// the derivation walks on to the next candidate that is really on disk.
func TestDeclarationEntry_SkipsFilesThePackageDoesNotShip(t *testing.T) {
	t.Parallel()

	manifest := `{"types":"build/legacy/index.d.ts",
		"exports":{".":{"import":{"types":"./build/modern/index.d.ts","default":"./build/modern/index.js"}}}}`
	shipped := map[string]bool{"build/legacy/index.d.ts": true}
	got := webruntimepeers.DeclarationEntry([]byte(manifest), func(rel string) bool { return shipped[rel] })
	if got != "build/legacy/index.d.ts" {
		t.Errorf("DeclarationEntry = %q, want the next candidate that exists (build/legacy/index.d.ts)", got)
	}
	if got := webruntimepeers.DeclarationEntry([]byte(manifest), func(string) bool { return false }); got != "" {
		t.Errorf("DeclarationEntry = %q with nothing on disk, want \"\" so the caller keeps its current pin", got)
	}
}

// A pin with an entry must end in a declaration file — that suffix is the
// entire mechanism: Next's JsConfigPathsPlugin skips any candidate ending in
// `.d.ts` ("Ensure .d.ts is not matched"), so webpack resolves the import
// through the package's `exports` exactly as it does for the runtime.
func TestTypePinPath_EntryPinIsInvisibleToTheBundler(t *testing.T) {
	t.Parallel()

	got := webruntimepeers.TypePinPath("@tanstack/react-query", false, "build/modern/index.d.ts")
	if want := "./node_modules/@tanstack/react-query/build/modern/index.d.ts"; got != want {
		t.Errorf("TypePinPath = %q, want %q", got, want)
	}
	if got := webruntimepeers.TypePinPath("react", true, "index.d.ts"); got != "../../node_modules/@types/react/index.d.ts" {
		t.Errorf("react pin = %q, want the @types/react declaration file at the hoisted root", got)
	}
}

// SplitPinPath is the inverse the reconcilers rely on to change a pin's
// layout without losing its entry (and vice versa).
func TestSplitPinPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		pkg, value  string
		hoisted, ok bool
		entry       string
	}{
		{pkg: "@connectrpc/connect", value: "./node_modules/@connectrpc/connect", ok: true},
		{pkg: "@connectrpc/connect", value: "../../node_modules/@connectrpc/connect", hoisted: true, ok: true},
		{pkg: "@tanstack/react-query", value: "../../node_modules/@tanstack/react-query/build/modern/index.d.ts",
			hoisted: true, entry: "build/modern/index.d.ts", ok: true},
		{pkg: "react", value: "./node_modules/@types/react/index.d.ts", entry: "index.d.ts", ok: true},
		// forge's own stale TS7016 value — recognised so it can be healed.
		{pkg: "react", value: "./node_modules/react", ok: true},
		// A shape a human wrote: left alone.
		{pkg: "@connectrpc/connect", value: "./vendor/connect", ok: false},
		// A prefix match on a DIFFERENT package must not be claimed.
		{pkg: "@tanstack/react-query", value: "./node_modules/@tanstack/react-query-devtools", ok: false},
	}
	for _, tt := range tests {
		hoisted, entry, ok := webruntimepeers.SplitPinPath(tt.pkg, tt.value)
		if ok != tt.ok || (ok && (hoisted != tt.hoisted || entry != tt.entry)) {
			t.Errorf("SplitPinPath(%q, %q) = (%v, %q, %v), want (%v, %q, %v)",
				tt.pkg, tt.value, hoisted, entry, ok, tt.hoisted, tt.entry, tt.ok)
		}
	}
}
